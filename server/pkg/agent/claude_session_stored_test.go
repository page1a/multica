package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeSessionStoredFindsTranscriptInAnyProject(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	projectDir := filepath.Join(configDir, "projects", "-Users-me-repo-multica-worktrees-dene-1-aaaa")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "s1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CLAUDE_CONFIG_DIR=" + configDir}
	otherCwd := t.TempDir()
	if !ClaudeSessionStored(env, otherCwd, "s1") {
		t.Fatal("transcript under another project folder was not found")
	}
	if ClaudeSessionStored(env, otherCwd, "s2") {
		t.Fatal("missing transcript reported stored")
	}
	if ClaudeSessionStored(env, otherCwd, "../s1") {
		t.Fatal("path-like session id accepted")
	}
	if ClaudeSessionStored([]string{"CLAUDE_CONFIG_DIR=" + filepath.Join(configDir, "absent")}, otherCwd, "s1") {
		t.Fatal("absent config dir reported stored")
	}
}
