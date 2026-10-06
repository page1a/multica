package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpFrameWriter stands in for the agent's stdin: every JSON-RPC frame the
// client writes arrives on frames.
type acpFrameWriter struct {
	frames chan map[string]any
}

func (w *acpFrameWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			return 0, err
		}
		w.frames <- frame
	}
	return len(p), nil
}

type acpSteerHarness struct {
	t      *testing.T
	client *hermesClient
	steer  *acpHandoffSteer
	frames chan map[string]any

	mu   sync.Mutex
	done []string // stop reasons the backend saw through onPromptDone
}

func newACPSteerHarness(t *testing.T) *acpSteerHarness {
	t.Helper()
	h := &acpSteerHarness{t: t, frames: make(chan map[string]any, 16)}
	h.client = &hermesClient{
		stdin:        &acpFrameWriter{frames: h.frames},
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		onPromptDone: func(r hermesPromptResult) {
			h.mu.Lock()
			h.done = append(h.done, r.stopReason)
			h.mu.Unlock()
		},
	}
	h.steer = newACPHandoffSteer(h.client)
	return h
}

func (h *acpSteerHarness) next(method string) map[string]any {
	h.t.Helper()
	select {
	case frame := <-h.frames:
		if frame["method"] != method {
			h.t.Fatalf("frame method = %v, want %s (frame %v)", frame["method"], method, frame)
		}
		return frame
	case <-time.After(2 * time.Second):
		h.t.Fatalf("timed out waiting for %s", method)
	}
	return nil
}

func (h *acpSteerHarness) reply(frame map[string]any, body string) {
	h.client.handleLine(fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,%s}`, frame["id"], body))
}

func (h *acpSteerHarness) stopReasons() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.done...)
}

func promptText(t *testing.T, frame map[string]any) string {
	t.Helper()
	params := frame["params"].(map[string]any)
	blocks := params["prompt"].([]any)
	return blocks[0].(map[string]any)["text"].(string)
}

func TestACPHandoffSteerCancelsStepAndContinuesSameSession(t *testing.T) {
	h := newACPSteerHarness(t)
	type promptOut struct {
		result json.RawMessage
		err    error
	}
	out := make(chan promptOut, 1)
	go func() {
		result, err := h.steer.prompt(context.Background(), "sess-1", "original task")
		out <- promptOut{result, err}
	}()

	first := h.next("session/prompt")
	if got := promptText(t, first); got != "original task" {
		t.Fatalf("first prompt = %q", got)
	}
	waitFor(t, h.steer.ready)

	delivered := make(chan error, 1)
	go func() { delivered <- h.steer.supplement(context.Background(), "use tabs") }()

	cancel := h.next("session/cancel")
	if _, hasID := cancel["id"]; hasID {
		t.Fatalf("session/cancel must be a notification, got id: %v", cancel)
	}
	if got := cancel["params"].(map[string]any)["sessionId"]; got != "sess-1" {
		t.Fatalf("cancel sessionId = %v", got)
	}
	h.reply(first, `"result":{"stopReason":"cancelled"}`)

	second := h.next("session/prompt")
	if got := second["params"].(map[string]any)["sessionId"]; got != "sess-1" {
		t.Fatalf("follow-up sessionId = %v, want the same session", got)
	}
	text := promptText(t, second)
	if !strings.HasPrefix(text, acpHandoffSteerPreamble) || !strings.HasSuffix(text, "use tabs") {
		t.Fatalf("follow-up prompt = %q", text)
	}
	if err := <-delivered; err != nil {
		t.Fatalf("supplement: %v", err)
	}
	h.reply(second, `"result":{"stopReason":"end_turn"}`)

	res := <-out
	if res.err != nil {
		t.Fatalf("prompt: %v", res.err)
	}
	// The deliberate cancel is absorbed: the backend only sees the final turn.
	if got := h.stopReasons(); len(got) != 1 || got[0] != "end_turn" {
		t.Fatalf("stop reasons seen by backend = %v, want [end_turn]", got)
	}
	if h.steer.ready() {
		t.Fatal("steer still ready after the turn ended")
	}
	if err := h.steer.supplement(context.Background(), "late"); err == nil {
		t.Fatal("supplement after the turn ended should fail")
	}
}

func TestACPHandoffSteerContinuesWhenCancelIsAnRPCError(t *testing.T) {
	h := newACPSteerHarness(t)
	out := make(chan error, 1)
	go func() {
		_, err := h.steer.prompt(context.Background(), "sess-2", "task")
		out <- err
	}()
	first := h.next("session/prompt")
	waitFor(t, h.steer.ready)

	delivered := make(chan error, 1)
	go func() { delivered <- h.steer.supplement(context.Background(), "more") }()
	h.next("session/cancel")
	h.reply(first, `"error":{"code":-32800,"message":"Request cancelled"}`)

	second := h.next("session/prompt")
	if err := <-delivered; err != nil {
		t.Fatalf("supplement: %v", err)
	}
	h.reply(second, `"result":{"stopReason":"end_turn"}`)
	if err := <-out; err != nil {
		t.Fatalf("prompt: %v", err)
	}
}

func TestACPHandoffSteerFailsSupplementWhenTurnEndsWithAnError(t *testing.T) {
	h := newACPSteerHarness(t)
	out := make(chan error, 1)
	go func() {
		_, err := h.steer.prompt(context.Background(), "sess-3", "task")
		out <- err
	}()
	first := h.next("session/prompt")
	waitFor(t, h.steer.ready)
	h.reply(first, `"error":{"code":-32603,"message":"boom"}`)
	if err := <-out; err == nil {
		t.Fatal("prompt error was swallowed")
	}
	if err := h.steer.supplement(context.Background(), "x"); err == nil {
		t.Fatal("supplement after a failed turn should fail")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
