package agent

import (
	"io"
	"log/slog"
	"slices"
	"testing"
)

func TestValidateCustomArgsForProviderRejectsCodexConfigSyntax(t *testing.T) {
	for _, provider := range []string{"claude", "codebuddy", "antigravity", "grok", "qwen", "opencode", "deveco", "codearts", "pi"} {
		t.Run(provider, func(t *testing.T) {
			if err := ValidateCustomArgsForProvider(provider, []string{"-c", "model_reasoning_effort=high"}); err == nil {
				t.Fatalf("expected Codex -c syntax to be rejected for %s", provider)
			}
			if err := ValidateCustomArgsForProvider(provider, []string{"-c=model_reasoning_effort=high"}); err == nil {
				t.Fatalf("expected inline Codex -c syntax to be rejected for %s", provider)
			}
		})
	}
}

func TestValidateCustomArgsForProviderAllowsNativeOrCodexArgs(t *testing.T) {
	if err := ValidateCustomArgsForProvider("claude", []string{"-c", "--max-turns", "7"}); err != nil {
		t.Fatalf("native Claude continuation form should not be rejected: %v", err)
	}
	if err := ValidateCustomArgsForProvider("codex", []string{"-c", "model_reasoning_effort=high"}); err != nil {
		t.Fatalf("Codex config syntax should remain valid for Codex: %v", err)
	}
}

// TestSessionContinuationArgsAreBlocked guards DENE-1160's class of bug: a
// "continue the latest session" flag in custom_args makes a cold start attach
// to whatever session last ran in the same cwd, and in shared mode that is
// another chat's conversation.
func TestSessionContinuationArgsAreBlocked(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name    string
		blocked map[string]blockedArgMode
		args    []string
	}{
		{"opencode", opencodeBlockedArgs, []string{"-c", "--continue", "-s", "ses_1", "--fork"}},
		{"deveco", devecoBlockedArgs, []string{"-c", "--continue", "-s", "ses_1", "--fork"}},
		{"codearts", codeartsBlockedArgs, []string{"-c", "--continue", "-s", "ses_1", "--fork"}},
		{"cursor", cursorBlockedArgs, []string{"--continue", "--resume"}},
		{"copilot", copilotBlockedArgs, []string{"--continue"}},
		{"pi", piBlockedArgs, []string{"-c", "--continue", "-r", "--resume", "--no-session", "--session-id", "x", "--session-dir", "/tmp/s", "--fork", "/tmp/f.jsonl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--keep")
			got := filterCustomArgs(args, tc.blocked, logger)
			if !slices.Equal(got, []string{"--keep"}) {
				t.Fatalf("filterCustomArgs(%v) = %v, want [--keep]", args, got)
			}
			got = filterCustomArgs([]string{"-c", "model_reasoning_effort=high", "--keep"}, tc.blocked, logger)
			if tc.blocked["-c"] == blockedOptionalValue && !slices.Equal(got, []string{"--keep"}) {
				t.Fatalf("stray Codex -c value leaked through: %v", got)
			}
		})
	}
}
