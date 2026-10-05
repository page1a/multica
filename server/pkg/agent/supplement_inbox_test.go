package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeExtension plays the provider-side half of the inbox protocol: it claims
// the first published input and settles it with the given suffix and detail.
func fakeExtension(t *testing.T, inbox *supplementInbox, suffix, detail string) <-chan string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		for {
			names, _ := filepath.Glob(filepath.Join(inbox.dir, "*.txt"))
			if len(names) == 0 {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			base := strings.TrimSuffix(names[0], ".txt")
			if os.Rename(names[0], base+".claimed") != nil {
				continue
			}
			text, _ := os.ReadFile(base + ".claimed")
			got <- string(text)
			if suffix != "" {
				_ = os.WriteFile(base+suffix, []byte(detail), 0o600)
			}
			return
		}
	}()
	return got
}

func newTestInbox(t *testing.T) *supplementInbox {
	t.Helper()
	inbox, err := newSupplementInbox("test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inbox.close)
	return inbox
}

func TestSupplementInboxRefusesBeforeTurnStarts(t *testing.T) {
	t.Parallel()
	inbox := newTestInbox(t)
	if inbox.ready() {
		t.Fatal("ready before the provider confirmed a turn")
	}
	err := inbox.deliver(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "turn has not started") {
		t.Fatalf("deliver before start = %v, want turn-not-started", err)
	}
}

func TestSupplementInboxDeliversOnAck(t *testing.T) {
	t.Parallel()
	inbox := newTestInbox(t)
	inbox.start()
	claimed := fakeExtension(t, inbox, ".ok", "")
	if err := inbox.deliver(context.Background(), "keep the tests"); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if text := <-claimed; text != "keep the tests" {
		t.Fatalf("extension read %q", text)
	}
}

func TestSupplementInboxMapsExtensionFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		detail string
		want   func(error) bool
	}{
		{"turn ended before delivery", func(err error) bool { return errors.Is(err, context.Canceled) }},
		{"session busy", func(err error) bool { return err != nil && strings.Contains(err.Error(), "session busy") }},
	} {
		inbox := newTestInbox(t)
		inbox.start()
		fakeExtension(t, inbox, ".err", tc.detail)
		if err := inbox.deliver(context.Background(), "x"); !tc.want(err) {
			t.Fatalf("%q: deliver = %v", tc.detail, err)
		}
	}
}

func TestSupplementInboxWithdrawsUnclaimedInputOnCancel(t *testing.T) {
	t.Parallel()
	inbox := newTestInbox(t)
	inbox.start()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := inbox.deliver(ctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deliver = %v, want deadline", err)
	}
	if names, _ := filepath.Glob(filepath.Join(inbox.dir, "*.txt")); len(names) != 0 {
		t.Fatalf("withdrawn input still claimable: %v", names)
	}
}

func TestSupplementInboxClaimedInputOutlivesCancel(t *testing.T) {
	t.Parallel()
	inbox := newTestInbox(t)
	inbox.start()
	ctx, cancel := context.WithCancel(context.Background())
	claimed := fakeExtension(t, inbox, "", "")
	done := make(chan error, 1)
	go func() { done <- inbox.deliver(ctx, "x") }()
	<-claimed
	cancel()
	select {
	case err := <-done:
		t.Fatalf("deliver returned %v while the extension still owned the input", err)
	case <-time.After(3 * supplementInboxPollInterval):
	}
	matches, _ := filepath.Glob(filepath.Join(inbox.dir, "*.claimed"))
	_ = os.WriteFile(strings.TrimSuffix(matches[0], ".claimed")+".ok", nil, 0o600)
	if err := <-done; err != nil {
		t.Fatalf("deliver = %v, want the extension's delivery", err)
	}
}

func TestSupplementInboxCloseSettlesPendingAndRemovesDir(t *testing.T) {
	t.Parallel()
	inbox, err := newSupplementInbox("test")
	if err != nil {
		t.Fatal(err)
	}
	inbox.start()
	done := make(chan error, 1)
	go func() { done <- inbox.deliver(context.Background(), "x") }()
	time.Sleep(2 * supplementInboxPollInterval)
	inbox.close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("pending deliver after close = %v, want canceled", err)
	}
	if inbox.ready() {
		t.Fatal("ready after close")
	}
	if _, err := os.Stat(inbox.dir); !os.IsNotExist(err) {
		t.Fatalf("inbox dir survived close: %v", err)
	}
	if err := inbox.deliver(context.Background(), "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("deliver after close = %v, want canceled", err)
	}
}

func TestOpenCodeConfigWithPluginKeepsExistingConfig(t *testing.T) {
	t.Parallel()
	got, err := opencodeConfigWithPlugin(`{"mcp":{"a":{"type":"remote"}},"plugin":["user-plugin"]}`, "/tmp/x/multica-supplement.js")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		MCP    map[string]any `json:"mcp"`
		Plugin []string       `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MCP["a"] == nil || len(doc.Plugin) != 2 || doc.Plugin[0] != "user-plugin" || doc.Plugin[1] != "file:///tmp/x/multica-supplement.js" {
		t.Fatalf("merged config = %s", got)
	}
	if _, err := opencodeConfigWithPlugin(`[1]`, "/x.js"); err == nil {
		t.Fatal("non-object config accepted")
	}
	if _, err := opencodeConfigWithPlugin(`{"plugin":"one"}`, "/x.js"); err == nil {
		t.Fatal("non-list plugin field accepted")
	}
}

// fakeSupplementProviderScript emits startEvent, then settles the first
// supplement exactly as a provider extension would, then emits endEvents.
func fakeSupplementProviderScript(startEvent string, endEvents []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncat > /dev/null\n")
	b.WriteString(`printf '%s\n' "$*" > "$CAPTURE_DIR/args"` + "\n")
	b.WriteString(`printf '%s' "${OPENCODE_CONFIG_CONTENT-}" > "$CAPTURE_DIR/config"` + "\n")
	b.WriteString("printf '%s\\n' '" + startEvent + "'\n")
	b.WriteString(`while ! ls "$MULTICA_SUPPLEMENT_DIR"/*.txt >/dev/null 2>&1; do sleep 0.02; done
for f in "$MULTICA_SUPPLEMENT_DIR"/*.txt; do
  id=$(basename "$f" .txt)
  mv "$f" "$MULTICA_SUPPLEMENT_DIR/$id.claimed"
  cp "$MULTICA_SUPPLEMENT_DIR/$id.claimed" "$CAPTURE_DIR/supplement"
  : > "$MULTICA_SUPPLEMENT_DIR/$id.ok"
done
`)
	for _, e := range endEvents {
		b.WriteString("printf '%s\\n' '" + e + "'\n")
	}
	return b.String()
}

func runSupplementFixture(t *testing.T, provider, script string) (captureDir string, result Result) {
	t.Helper()
	captureDir = t.TempDir()
	fakePath := filepath.Join(t.TempDir(), provider)
	writeTestExecutable(t, fakePath, []byte(script))
	backend, err := New(provider, Config{
		ExecutablePath: fakePath,
		Logger:         slog.Default(),
		Env:            map[string]string{"CAPTURE_DIR": captureDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "task", ExecOptions{
		Cwd:                  t.TempDir(),
		ResumeSessionID:      filepath.Join(t.TempDir(), "session.jsonl"),
		EnableTaskSupplement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Supplement == nil || session.SupplementReady == nil {
		t.Fatal("session does not expose task supplements")
	}
	go func() {
		for range session.Messages {
		}
	}()
	for !session.SupplementReady() {
		select {
		case <-ctx.Done():
			t.Fatal("provider turn never became ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := session.Supplement(ctx, "also keep the tests"); err != nil {
		t.Fatalf("supplement: %v", err)
	}
	result = <-session.Result
	if session.SupplementReady() {
		t.Fatal("still ready after the provider exited")
	}
	return captureDir, result
}

func TestOpenCodeExecuteDeliversSupplementThroughPlugin(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	captureDir, result := runSupplementFixture(t, "opencode", fakeSupplementProviderScript(
		`{"type":"step_start","timestamp":1,"sessionID":"ses_fake","part":{"type":"step-start"}}`,
		[]string{
			`{"type":"text","timestamp":2,"sessionID":"ses_fake","part":{"type":"text","text":"ok","time":{"end":3}}}`,
			`{"type":"step_finish","timestamp":3,"sessionID":"ses_fake","part":{"type":"step-finish","reason":"stop"}}`,
		}))
	if result.Status != "completed" || result.SessionID != "ses_fake" {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(captureDir, "supplement")); string(got) != "also keep the tests" {
		t.Fatalf("plugin read %q", got)
	}
	config, _ := os.ReadFile(filepath.Join(captureDir, "config"))
	var doc struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(config, &doc); err != nil || len(doc.Plugin) != 1 || !strings.HasPrefix(doc.Plugin[0], "file://") {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %s", config)
	}
	if _, err := os.Stat(strings.TrimPrefix(doc.Plugin[0], "file://")); !os.IsNotExist(err) {
		t.Fatalf("plugin inbox survived the run: %v", err)
	}
}

func TestPiExecuteDeliversSupplementThroughExtension(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	captureDir, result := runSupplementFixture(t, "pi", fakeSupplementProviderScript(
		`{"type":"agent_start"}`,
		[]string{
			`{"type":"turn_start"}`,
			`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"ok"}}`,
			`{"type":"turn_end","message":{"role":"assistant","model":"test","stopReason":"stop"}}`,
			`{"type":"agent_end"}`,
		}))
	if result.Status != "completed" {
		t.Fatalf("result = %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(captureDir, "supplement")); string(got) != "also keep the tests" {
		t.Fatalf("extension read %q", got)
	}
	args, _ := os.ReadFile(filepath.Join(captureDir, "args"))
	if !strings.Contains(string(args), "--extension ") || !strings.Contains(string(args), "multica-supplement.js") {
		t.Fatalf("pi args = %s", args)
	}
}

func TestSupplementExtensionsOnlyWhenNegotiated(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	for _, provider := range []string{"opencode", "pi"} {
		captureDir := t.TempDir()
		fakePath := filepath.Join(t.TempDir(), provider)
		writeTestExecutable(t, fakePath, []byte("#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' \"$*\" > \"$CAPTURE_DIR/args\"\nprintf '%s' \"${OPENCODE_CONFIG_CONTENT-}${MULTICA_SUPPLEMENT_DIR-}\" > \"$CAPTURE_DIR/env\"\n"))
		backend, err := New(provider, Config{ExecutablePath: fakePath, Logger: slog.Default(), Env: map[string]string{"CAPTURE_DIR": captureDir}})
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(context.Background(), "task", ExecOptions{ResumeSessionID: filepath.Join(t.TempDir(), "s.jsonl")})
		if err != nil {
			t.Fatal(err)
		}
		if session.Supplement != nil {
			t.Fatalf("%s exposes supplements without negotiation", provider)
		}
		go func() {
			for range session.Messages {
			}
		}()
		<-session.Result
		args, _ := os.ReadFile(filepath.Join(captureDir, "args"))
		env, _ := os.ReadFile(filepath.Join(captureDir, "env"))
		if strings.Contains(string(args), "--extension") || len(env) != 0 {
			t.Fatalf("%s: args=%q env=%q", provider, args, env)
		}
	}
}
