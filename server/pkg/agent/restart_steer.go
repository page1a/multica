package agent

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SteersByRestart reports whether provider takes a mid-run message by
// restarting its CLI on the same session (DENE-1349). These CLIs run one
// command and exit, with no input channel while they work, so the only way in
// is to stop the process and resume the session with the message as the next
// prompt. The cost is one extra process start; the session is kept.
func SteersByRestart(provider string) bool {
	switch provider {
	case "cursor", "copilot", "codearts", "deveco", "antigravity", "openclaw":
		return true
	}
	return false
}

// WithRestartSteer gives a restart-tier backend a Supplement: stop the running
// CLI, then start it again on the same session with the message as the
// prompt. Other providers are returned unchanged, and so is any run that did
// not negotiate run-scoped input (ExecOptions.EnableTaskSupplement).
func WithRestartSteer(provider string, backend Backend) Backend {
	if !SteersByRestart(provider) {
		return backend
	}
	return &restartSteerBackend{inner: backend}
}

type restartSteerBackend struct {
	inner Backend
}

var errRestartSteerTurnEnded = errors.New("turn ended before the message could be delivered")

// restartSteerNotice heads the resumed prompt. The model wrote nothing that
// says its process was stopped, so the prompt has to: whatever step was in
// flight may be half done.
const restartSteerNotice = `[SESSION RESUMED] Your CLI process was stopped and restarted on this same session so the message below could reach you. The step you were in may be unfinished: check its state, then continue the original task with the message taken into account.

`

func (b *restartSteerBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	if !opts.EnableTaskSupplement {
		return b.inner.Execute(ctx, prompt, opts)
	}
	r := &restartSteerRun{
		backend:   b.inner,
		ctx:       ctx,
		opts:      opts,
		sessionID: opts.ResumeSessionID,
		started:   time.Now(),
		msgs:      make(chan Message, 256),
		result:    make(chan Result, 1),
	}
	if err := r.launch(prompt, opts); err != nil {
		return nil, err
	}
	first := r.current
	go r.forward()
	session := &Session{
		Supplement:      r.supplement,
		SupplementReady: r.ready,
		Messages:        r.msgs,
		Result:          r.result,
	}
	// A nil hook means something to the daemon (message-based accounting, no
	// terminal boundary), so the run offers a hook only where the backend
	// does. Every process of the run comes from the same backend.
	if first.ToolActivity != nil {
		session.ToolActivity = r.toolActivity
	}
	if first.InterruptBackgroundTools != nil {
		session.InterruptBackgroundTools = r.interruptBackgroundTools
	}
	if first.TerminalObserved != nil {
		session.TerminalObserved = r.terminalObserved
	}
	return session, nil
}

// restartSteerRun is one task run that may span several CLI processes, all on
// one session. Only the last process's result ends the run.
type restartSteerRun struct {
	backend Backend
	ctx     context.Context
	opts    ExecOptions
	started time.Time
	msgs    chan Message
	result  chan Result

	mu        sync.Mutex
	current   *Session
	cancel    context.CancelFunc
	sessionID string
	// live is set once the current process's model has produced something. A
	// message is only taken after that, so a second message cannot kill a
	// resumed process before the CLI has recorded the first in the session.
	live     bool
	pending  *restartSteerRequest
	finished bool
	usage    map[string]TokenUsage
	turns    int
}

type restartSteerRequest struct {
	prompt string
	done   chan error
}

func (r *restartSteerRun) launch(prompt string, opts ExecOptions) error {
	procCtx, cancel := context.WithCancel(r.ctx)
	session, err := r.backend.Execute(procCtx, prompt, opts)
	if err != nil {
		cancel()
		return err
	}
	r.mu.Lock()
	r.current, r.cancel, r.live = session, cancel, false
	r.mu.Unlock()
	return nil
}

func (r *restartSteerRun) forward() {
	for {
		r.mu.Lock()
		session, cancel := r.current, r.cancel
		r.mu.Unlock()

		for msg := range session.Messages {
			r.mu.Lock()
			if msg.Type != MessageStatus && msg.Type != MessageLog {
				r.live = true
			}
			if msg.SessionID != "" {
				r.sessionID = msg.SessionID
			}
			r.mu.Unlock()
			r.send(msg)
		}
		res := <-session.Result
		cancel()

		r.mu.Lock()
		if res.SessionID != "" {
			r.sessionID = res.SessionID
		}
		r.addUsage(res)
		req := r.pending
		r.pending = nil
		if req == nil || r.ctx.Err() != nil {
			r.finished = true
			r.mu.Unlock()
			if req != nil {
				req.done <- errRestartSteerTurnEnded
			}
			r.finish(res)
			return
		}
		sessionID := r.sessionID
		r.mu.Unlock()

		opts := r.opts
		opts.ResumeSessionID = sessionID
		opts.ResumeExpected = true
		opts.ResumeContinuityNotice = ""
		// The resumed session already holds the brief.
		opts.SystemPrompt = ""
		if err := r.launch(req.prompt, opts); err != nil {
			r.mu.Lock()
			r.finished = true
			r.mu.Unlock()
			req.done <- err
			res.Status = "failed"
			res.Error = "restart for the added message failed: " + ExplainExecError(err).Error()
			r.finish(res)
			return
		}
		r.send(Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
		req.done <- nil
	}
}

func (r *restartSteerRun) send(msg Message) {
	select {
	case r.msgs <- msg:
	case <-r.ctx.Done():
	}
}

func (r *restartSteerRun) addUsage(res Result) {
	r.turns += res.NumTurns
	if len(res.Usage) == 0 {
		return
	}
	if r.usage == nil {
		r.usage = make(map[string]TokenUsage, len(res.Usage))
	}
	for model, u := range res.Usage {
		sum := r.usage[model]
		sum.InputTokens += u.InputTokens
		sum.OutputTokens += u.OutputTokens
		sum.CacheReadTokens += u.CacheReadTokens
		sum.CacheWriteTokens += u.CacheWriteTokens
		sum.CostUSDTicks += u.CostUSDTicks
		r.usage[model] = sum
	}
}

// finish reports the last process's result with the whole run's totals. The
// caller holds no lock.
func (r *restartSteerRun) finish(res Result) {
	r.mu.Lock()
	res.Usage = r.usage
	res.NumTurns = r.turns
	if res.SessionID == "" {
		res.SessionID = r.sessionID
	}
	r.mu.Unlock()
	res.DurationMs = time.Since(r.started).Milliseconds()
	close(r.msgs)
	r.result <- res
	close(r.result)
}

func (r *restartSteerRun) ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.finished && r.pending == nil && r.live && r.sessionID != ""
}

// supplement stops the running CLI and resumes its session with prompt. It
// returns once the new process has started with the message.
func (r *restartSteerRun) supplement(ctx context.Context, prompt string) error {
	r.mu.Lock()
	if r.finished || r.pending != nil || r.sessionID == "" {
		r.mu.Unlock()
		return errRestartSteerTurnEnded
	}
	req := &restartSteerRequest{prompt: restartSteerNotice + prompt, done: make(chan error, 1)}
	r.pending = req
	r.cancel()
	r.mu.Unlock()
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *restartSteerRun) session() *Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

func (r *restartSteerRun) toolActivity() (int32, time.Time) {
	if s := r.session(); s.ToolActivity != nil {
		return s.ToolActivity()
	}
	return 0, time.Time{}
}

func (r *restartSteerRun) interruptBackgroundTools() bool {
	if s := r.session(); s.InterruptBackgroundTools != nil {
		return s.InterruptBackgroundTools()
	}
	return false
}

func (r *restartSteerRun) terminalObserved() bool {
	if s := r.session(); s.TerminalObserved != nil {
		return s.TerminalObserved()
	}
	return false
}
