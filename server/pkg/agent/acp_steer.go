package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// SteersByHandoff reports whether provider takes a mid-run message by stopping
// the current ACP step and prompting the same session again (DENE-1347).
func SteersByHandoff(provider string) bool {
	switch provider {
	case "hermes", "kimi", "kiro", "qoder", "qoderclicn", "qwenpaw",
		"reasonix", "traecli", "zeroclaw", "devin", "dim", "mcode":
		return true
	}
	return false
}

// acpHandoffSteerPreamble opens the follow-up prompt sent after a step was
// stopped to make room for a supplement. The model sees its own interrupted
// step in the session history; this line tells it the stop was deliberate.
const acpHandoffSteerPreamble = "[STEP INTERRUPTED BY USER] Your current step was stopped on purpose so the guidance below could reach you. This is the same task and the same session: continue the original task from where you left off, applying this guidance."

// acpHandoffSteer delivers a mid-turn supplement to ACP agents that have no
// in-turn interjection method (DENE-1347): it sends `session/cancel` for the
// running prompt, waits for that prompt to settle, then sends the supplement
// as the next `session/prompt` in the same process and session. The run keeps
// going until a prompt ends with nothing waiting behind it.
//
// The prompt response that the cancel produced is absorbed here (its usage is
// still billed) so the backend only ever sees the last prompt's result and does
// not mistake the deliberate stop for an aborted run.
type acpHandoffSteer struct {
	client *hermesClient
	// params builds a session/prompt request; Kiro sends its blocks under two
	// keys, everyone else uses acpTextPrompt.
	params func(sessionID, text string) map[string]any

	mu         sync.Mutex
	sessionID  string
	inTurn     bool // a session/prompt is outstanding
	cancelSent bool // session/cancel already sent for the outstanding prompt
	closed     bool // the turn is over; no more supplements are taken
	pending    []*acpSteerInput
}

type acpSteerInput struct {
	text string
	done chan error
}

func newACPHandoffSteer(c *hermesClient) *acpHandoffSteer {
	s := &acpHandoffSteer{client: c, params: acpTextPrompt}
	c.absorbPromptResult = s.absorbPromptResult
	return s
}

// ready reports whether a supplement sent now would reach the running turn.
func (s *acpHandoffSteer) ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.inTurn && s.sessionID != ""
}

// supplement queues text for the running turn and stops its current step. It
// returns once the follow-up prompt carrying the text has been written to the
// agent, or with an error when the turn ended first.
func (s *acpHandoffSteer) supplement(ctx context.Context, text string) error {
	input := &acpSteerInput{text: text, done: make(chan error, 1)}
	s.mu.Lock()
	if s.closed || s.sessionID == "" {
		s.mu.Unlock()
		return fmt.Errorf("acp turn is no longer active")
	}
	s.pending = append(s.pending, input)
	sendCancel := s.inTurn && !s.cancelSent
	if sendCancel {
		s.cancelSent = true
	}
	sessionID := s.sessionID
	s.mu.Unlock()

	if sendCancel {
		if err := s.client.notify("session/cancel", map[string]any{"sessionId": sessionID}); err != nil {
			s.drop(input)
			return fmt.Errorf("acp session/cancel failed: %w", err)
		}
	}
	select {
	case err := <-input.done:
		return err
	case <-ctx.Done():
		s.drop(input)
		return ctx.Err()
	}
}

func (s *acpHandoffSteer) drop(input *acpSteerInput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pending {
		if p == input {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}

// absorbPromptResult runs on the reader goroutine for every successful
// session/prompt response, before the backend's onPromptDone. True means a
// follow-up prompt is about to go out, so this result is not the turn's last.
func (s *acpHandoffSteer) absorbPromptResult() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inTurn = false
	if len(s.pending) == 0 {
		s.closed = true
		return false
	}
	return true
}

// prompt sends the first prompt and every follow-up a supplement causes. It
// returns the last prompt's result, or the first error that ends the turn.
func (s *acpHandoffSteer) prompt(ctx context.Context, sessionID, text string) (json.RawMessage, error) {
	s.mu.Lock()
	s.sessionID = sessionID
	s.inTurn = false
	s.cancelSent = false
	s.closed = false
	s.mu.Unlock()

	// inTurn flips only once the prompt is on the wire, so a session/cancel
	// can never overtake the prompt it is meant to stop.
	result, err := s.client.requestAndNotifySent(ctx, "session/prompt", s.params(sessionID, text), s.markInTurn)
	for {
		s.mu.Lock()
		s.inTurn = false
		var next []*acpSteerInput
		switch {
		case s.closed:
		case err == nil:
			next = s.pending
		case s.cancelSent && len(s.pending) > 0 && isACPRPCError(err) && ctx.Err() == nil:
			// Some agents answer a cancelled prompt with an RPC error instead
			// of stopReason "cancelled". The stop was ours, so carry on.
			next = s.pending
		}
		if len(next) == 0 {
			failed := s.pending
			s.pending = nil
			s.closed = true
			s.mu.Unlock()
			for _, input := range failed {
				input.done <- fmt.Errorf("acp turn ended before the supplement was sent")
			}
			return result, err
		}
		s.pending = nil
		s.cancelSent = false
		s.mu.Unlock()

		texts := make([]string, len(next))
		for i, input := range next {
			texts[i] = input.text
		}
		followUp := acpHandoffSteerPreamble + "\n\n" + strings.Join(texts, "\n\n")
		result, err = s.client.requestAndNotifySent(ctx, "session/prompt", s.params(sessionID, followUp), func() {
			s.markInTurn()
			for _, input := range next {
				input.done <- nil
			}
		})
		if err != nil {
			// A write failure never reached afterWrite; release the waiters.
			for _, input := range next {
				select {
				case input.done <- err:
				default:
				}
			}
		}
	}
}

func (s *acpHandoffSteer) markInTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.inTurn = true
	// A supplement that arrived while the prompt was being written is waiting
	// for this prompt; stop it right away.
	if len(s.pending) > 0 && !s.cancelSent {
		s.cancelSent = true
		sessionID := s.sessionID
		go func() { _ = s.client.notify("session/cancel", map[string]any{"sessionId": sessionID}) }()
	}
}

func acpTextPrompt(sessionID, text string) map[string]any {
	return map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": text},
		},
	}
}

func isACPRPCError(err error) bool {
	var rpcErr *acpRPCError
	return errors.As(err, &rpcErr)
}

// sendACPPrompt is the session/prompt call every ACP backend without a native
// interjection method makes. With steer non-nil the prompt can be stopped and
// continued by a supplement; nil keeps the plain single request.
func sendACPPrompt(ctx context.Context, c *hermesClient, steer *acpHandoffSteer, sessionID, text string) (json.RawMessage, error) {
	if steer == nil {
		return c.request(ctx, "session/prompt", acpTextPrompt(sessionID, text))
	}
	return steer.prompt(ctx, sessionID, text)
}

// sendACPPromptWith is sendACPPrompt for a dialect with its own request shape.
func sendACPPromptWith(ctx context.Context, c *hermesClient, steer *acpHandoffSteer, params func(sessionID, text string) map[string]any, sessionID, text string) (json.RawMessage, error) {
	if steer == nil {
		return c.request(ctx, "session/prompt", params(sessionID, text))
	}
	steer.params = params
	return steer.prompt(ctx, sessionID, text)
}

// attachACPHandoffSteer exposes steer on the session when the run negotiated
// supplements.
func attachACPHandoffSteer(session *Session, steer *acpHandoffSteer) *Session {
	if steer != nil {
		session.Supplement = steer.supplement
		session.SupplementReady = steer.ready
	}
	return session
}
