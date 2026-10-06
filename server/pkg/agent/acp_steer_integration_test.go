//go:build agentintegration

package agent

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestACPHandoffSteerRealSmoke runs a real ACP CLI, steers it mid-turn, and
// checks the session id is unchanged and the model acted on the supplement
// while finishing the original task (DENE-1347).
//
//	MULTICA_RUN_REAL_AGENT_SMOKE=1 MULTICA_STEER_PROVIDER=kimi \
//	  go test -tags agentintegration -run TestACPHandoffSteerRealSmoke -v ./pkg/agent
func TestACPHandoffSteerRealSmoke(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to steer an authenticated ACP CLI")
	}
	provider := os.Getenv("MULTICA_STEER_PROVIDER")
	if provider == "" {
		provider = "kimi"
	}
	backend, err := ResolveBackend(provider, Config{
		ExecutablePath: os.Getenv("MULTICA_STEER_PATH"),
		Logger:         slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	dir := t.TempDir()
	session, err := backend.Execute(ctx,
		"Run this exact shell command and wait for it to finish: "+
			"for i in $(seq 1 20); do echo tick $i; sleep 1; done; "+
			"then write the last tick number into a file named done.txt and reply with that number.",
		ExecOptions{Cwd: dir, Timeout: 5 * time.Minute, EnableTaskSupplement: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.Supplement == nil || session.SupplementReady == nil {
		t.Fatal("session does not expose a supplement hook")
	}

	var pinned []string
	toolSeen := make(chan struct{}, 1)
	go func() {
		for msg := range session.Messages {
			t.Logf("msg %s tool=%s status=%s text=%.80q", msg.Type, msg.Tool, msg.Status, msg.Content)
			if msg.SessionID != "" {
				pinned = append(pinned, msg.SessionID)
			}
			if msg.Type == MessageToolUse {
				select {
				case toolSeen <- struct{}{}:
				default:
				}
			}
		}
	}()

	select {
	case <-toolSeen:
	case <-time.After(3 * time.Minute):
		t.Fatal("the agent never started a tool call")
	}
	time.Sleep(3 * time.Second)
	if !session.SupplementReady() {
		t.Fatal("supplement not ready while the turn is running")
	}
	steerAt := time.Now()
	supplement := formatSteerSmoke("Also create a file named banana.txt containing the word BANANA, " +
		"and end your final reply with the word BANANA.")
	if err := session.Supplement(ctx, supplement); err != nil {
		t.Fatalf("supplement: %v", err)
	}
	t.Logf("supplement delivered after %s", time.Since(steerAt).Round(time.Millisecond))

	result := <-session.Result
	t.Logf("status=%s session=%s pinned=%v\noutput=%s", result.Status, result.SessionID, pinned, result.Output)
	if result.Status != "completed" {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	for _, id := range pinned {
		if id != result.SessionID {
			t.Fatalf("session id changed: pinned %v, result %s", pinned, result.SessionID)
		}
	}
	if banana, err := os.ReadFile(dir + "/banana.txt"); err != nil || !strings.Contains(string(banana), "BANANA") {
		t.Errorf("supplement not applied: banana.txt = %q, err = %v", banana, err)
	}
	if done, err := os.ReadFile(dir + "/done.txt"); err != nil {
		t.Errorf("original task not finished: done.txt err = %v", err)
	} else {
		t.Logf("done.txt = %q", strings.TrimSpace(string(done)))
	}
}

// formatSteerSmoke mirrors the daemon's supplement wording closely enough for
// a smoke test without importing the daemon package.
func formatSteerSmoke(content string) string {
	return "[ADDITIONAL GUIDANCE] Human \"tester\" added guidance while you were working.\n\n" +
		"Treat this as additional guidance for the same active task, not as a replacement:\n" +
		"- Preserve and complete the original objective.\n\nHuman message:\n" + content
}

// TestACPHandoffSteerRawProtocol drives acpHandoffSteer directly against any
// ACP CLI command line, so the stop-and-continue path can be checked on a CLI
// that is installed and logged in even when its own backend steers natively.
//
//	MULTICA_RUN_REAL_AGENT_SMOKE=1 \
//	MULTICA_STEER_RAW_CMD="grok --no-auto-update agent --always-approve stdio" \
//	MULTICA_STEER_RAW_AUTH=cached_token \
//	  go test -tags agentintegration -run TestACPHandoffSteerRawProtocol -v ./pkg/agent
func TestACPHandoffSteerRawProtocol(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" || os.Getenv("MULTICA_STEER_RAW_CMD") == "" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 and MULTICA_STEER_RAW_CMD to steer a raw ACP CLI")
	}
	argv := strings.Fields(os.Getenv("MULTICA_STEER_RAW_CMD"))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	dir := t.TempDir()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	toolSeen := make(chan struct{}, 1)
	var stopReasons []string
	var mu sync.Mutex
	c := &hermesClient{
		cfg:          Config{Logger: slog.Default()},
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		onMessage: func(msg Message) {
			if msg.Type == MessageText || msg.Type == MessageToolResult || msg.Type == MessageError {
				t.Logf("%s %.400q", msg.Type, msg.Content+msg.Output)
			}
			if msg.Type == MessageToolUse {
				t.Logf("tool %s %v", msg.Tool, msg.Input)
				select {
				case toolSeen <- struct{}{}:
				default:
				}
			}
		},
		onPromptDone: func(r hermesPromptResult) {
			mu.Lock()
			stopReasons = append(stopReasons, r.stopReason)
			mu.Unlock()
		},
	}
	steer := newACPHandoffSteer(c)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, `"session/cancel"`) || strings.Contains(line, `"stopReason"`) {
				t.Logf("<- %.300s", line)
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("agent exited"))
	}()

	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}},
	}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if method := os.Getenv("MULTICA_STEER_RAW_AUTH"); method != "" {
		if _, err := c.request(ctx, "authenticate", map[string]any{"methodId": method, "_meta": map[string]any{"headless": true}}); err != nil {
			t.Fatalf("authenticate: %v", err)
		}
	}
	newResult, err := c.request(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	sessionID := extractACPSessionID(newResult)
	c.sessionID = sessionID
	t.Logf("session %s", sessionID)

	promptDone := make(chan error, 1)
	go func() {
		_, err := steer.prompt(ctx, sessionID,
			"Run this exact shell command and wait for it to finish: "+
				"for i in $(seq 1 20); do echo tick $i; sleep 1; done; "+
				"then write the last tick number into a file named done.txt and reply with that number.")
		promptDone <- err
	}()
	select {
	case <-toolSeen:
	case err := <-promptDone:
		t.Fatalf("turn ended before any tool call: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("the agent never started a tool call")
	}
	time.Sleep(3 * time.Second)
	steerAt := time.Now()
	if err := steer.supplement(ctx, formatSteerSmoke("Also create a file named banana.txt containing the word BANANA, "+
		"and end your final reply with the word BANANA.")); err != nil {
		t.Fatalf("supplement: %v", err)
	}
	t.Logf("follow-up prompt sent %s after the steer", time.Since(steerAt).Round(time.Millisecond))
	if err := <-promptDone; err != nil {
		t.Fatalf("prompt: %v", err)
	}
	mu.Lock()
	t.Logf("stop reasons reported to the backend: %v", stopReasons)
	mu.Unlock()
	if banana, err := os.ReadFile(dir + "/banana.txt"); err != nil || !strings.Contains(string(banana), "BANANA") {
		t.Errorf("supplement not applied: banana.txt = %q, err = %v", banana, err)
	}
	if done, err := os.ReadFile(dir + "/done.txt"); err != nil {
		t.Errorf("original task not finished: done.txt err = %v", err)
	} else {
		t.Logf("done.txt = %q", strings.TrimSpace(string(done)))
	}
}
