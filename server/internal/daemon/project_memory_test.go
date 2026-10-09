package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/projectmemory"
)

func memoryGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The local directory sits on an old commit; origin's default branch already
// has the docs index. Only that slot is marked behind; a slot nobody wrote
// stays plainly missing.
func TestMarkMainlineMemoryFlagsSlotsTheRemoteAlreadyHas(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	memoryGit(t, dir, "init", "-q", "-b", "dev")
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("map"), 0o644)
	memoryGit(t, dir, "add", ".")
	memoryGit(t, dir, "commit", "-qm", "old")
	os.MkdirAll(filepath.Join(dir, "docs", "adr"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "README.md"), []byte("index"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "adr", "0001.md"), []byte("adr"), 0o644)
	memoryGit(t, dir, "add", ".")
	memoryGit(t, dir, "commit", "-qm", "new")
	memoryGit(t, dir, "update-ref", "refs/remotes/origin/dev", "HEAD")
	memoryGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/dev")
	memoryGit(t, dir, "checkout", "-q", "--detach", "HEAD~1")

	results := projectmemory.Check(dir)
	markMainlineMemory(context.Background(), dir, results)
	byKey := map[string]projectmemory.LocationResult{}
	for _, result := range results {
		byKey[result.Key] = result
	}
	if got := byKey[projectmemory.LocationDocs].MainlineRef; got != "origin/dev" {
		t.Fatalf("docs_index mainline = %q, want origin/dev", got)
	}
	if got := byKey[projectmemory.LocationADR].MainlineRef; got != "origin/dev" {
		t.Fatalf("adr mainline = %q, want origin/dev", got)
	}
	if got := byKey[projectmemory.LocationEvidence].MainlineRef; got != "" {
		t.Fatalf("evidence_index was never written but marked behind %q", got)
	}
	if got := byKey[projectmemory.LocationAgents].MainlineRef; got != "" {
		t.Fatalf("present slot marked behind %q", got)
	}
}

func TestMarkMainlineMemoryIgnoresNonGitDirectory(t *testing.T) {
	dir := t.TempDir()
	results := projectmemory.Check(dir)
	markMainlineMemory(context.Background(), dir, results)
	for _, result := range results {
		if result.MainlineRef != "" {
			t.Fatalf("%s marked behind in a non-git directory", result.Key)
		}
	}
}
