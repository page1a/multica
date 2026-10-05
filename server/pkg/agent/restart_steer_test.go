package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOneShot stands in for a one-shot CLI: each Execute is one process that
// reports its session, says something, and runs until it is cancelled or
// told to end.
type fakeOneShot struct {
	mu    sync.Mutex
	calls []fakeOneShotCall
	ends  []chan struct{}
}

type fakeOneShotCall struct {
	prompt string
	opts   ExecOptions
}

func (f *fakeOneShot) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeOneShotCall{prompt: prompt, opts: opts})
	end := make(chan struct{})
	f.ends = append(f.ends, end)
	f.mu.Unlock()

	sid := opts.ResumeSessionID
	if sid == "" {
		sid = "sess-1"
	}
	msgs := make(chan Message, 4)
	result := make(chan Result, 1)
	go func() {
		msgs <- Message{Type: MessageStatus, Status: "running", SessionID: sid}
		msgs <- Message{Type: MessageText, Content: "working"}
		status := "completed"
		select {
		case <-ctx.Done():
			status = "aborted"
		case <-end:
		}
		close(msgs)
		result <- Result{Status: status, SessionID: sid, NumTurns: 1, Usage: map[string]TokenUsage{"m": {InputTokens: 10, OutputTokens: 1}}}
		close(result)
	}()
	return &Session{Messages: msgs, Result: result}, nil
}

func (f *fakeOneShot) endProcess(i int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.ends[i])
}

func (f *fakeOneShot) snapshot() []fakeOneShotCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeOneShotCall(nil), f.calls...)
}

func drain(t *testing.T, s *Session) {
	t.Helper()
	go func() {
		for range s.Messages {
		}
	}()
}

func waitReady(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !s.SupplementReady() {
		if time.Now().After(deadline) {
			t.Fatal("run never became ready for a message")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWithRestartSteerLeavesOtherProvidersAlone(t *testing.T) {
	f := &fakeOneShot{}
	if WithRestartSteer("claude", f) != Backend(f) {
		t.Fatal("claude reads messages in-process; it must not be wrapped")
	}
	for _, p := range []string{"cursor", "copilot", "codearts", "deveco", "antigravity", "openclaw"} {
		if !SteersByRestart(p) || !SupportsTaskSupplement(p, "") {
			t.Fatalf("%s should steer by restart", p)
		}
	}
}

func TestRestartSteerWithoutNegotiationIsPlain(t *testing.T) {
	f := &fakeOneShot{}
	s, err := WithRestartSteer("cursor", f).Execute(context.Background(), "task", ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Supplement != nil {
		t.Fatal("a run that did not negotiate input must not take messages")
	}
	f.endProcess(0)
	drain(t, s)
	<-s.Result
}

func TestRestartSteerResumesTheSameSession(t *testing.T) {
	f := &fakeOneShot{}
	s, err := WithRestartSteer("cursor", f).Execute(context.Background(), "task", ExecOptions{EnableTaskSupplement: true, SystemPrompt: "brief"})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	waitReady(t, s)

	if err := s.Supplement(context.Background(), "only fix web"); err != nil {
		t.Fatalf("supplement: %v", err)
	}
	calls := f.snapshot()
	if len(calls) != 2 {
		t.Fatalf("processes = %d, want the CLI restarted once", len(calls))
	}
	second := calls[1]
	if second.opts.ResumeSessionID != "sess-1" || !second.opts.ResumeExpected {
		t.Fatalf("restart opts = %+v, want resume of sess-1", second.opts)
	}
	if second.opts.SystemPrompt != "" {
		t.Fatal("the resumed session already holds the brief")
	}
	if !strings.HasPrefix(second.prompt, "[SESSION RESUMED]") || !strings.HasSuffix(second.prompt, "only fix web") {
		t.Fatalf("restart prompt = %q", second.prompt)
	}

	waitReady(t, s)
	f.endProcess(1)
	res := <-s.Result
	if res.Status != "completed" || res.SessionID != "sess-1" {
		t.Fatalf("result = %+v, want the second process's completion on sess-1", res)
	}
	if res.NumTurns != 2 || res.Usage["m"].InputTokens != 20 {
		t.Fatalf("result totals = %d turns, %+v; want both processes counted", res.NumTurns, res.Usage)
	}
	if s.SupplementReady() {
		t.Fatal("a finished run must not take messages")
	}
	if err := s.Supplement(context.Background(), "late"); err == nil {
		t.Fatal("a message after the run ended must fail so it stays queued")
	}
}

func TestRestartSteerNotReadyBeforeTheModelSpeaks(t *testing.T) {
	msgs := make(chan Message, 1)
	result := make(chan Result, 1)
	msgs <- Message{Type: MessageStatus, Status: "running", SessionID: "sess-9"}
	backend := backendFunc(func(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
		go func() {
			<-ctx.Done()
			close(msgs)
			result <- Result{Status: "aborted"}
			close(result)
		}()
		return &Session{Messages: msgs, Result: result}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	s, err := WithRestartSteer("copilot", backend).Execute(ctx, "task", ExecOptions{EnableTaskSupplement: true})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, s)
	time.Sleep(50 * time.Millisecond)
	if s.SupplementReady() {
		t.Fatal("a process that has only reported its session may not have recorded the prompt yet")
	}
	cancel()
	if res := <-s.Result; res.SessionID != "sess-9" {
		t.Fatalf("result session = %q, want the pinned sess-9", res.SessionID)
	}
}

type backendFunc func(ctx context.Context, prompt string, opts ExecOptions) (*Session, error)

func (f backendFunc) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return f(ctx, prompt, opts)
}
