package repocache

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/sparsecheckout"
)

func TestCreateWorktreeSparsePathsLeavesTheRestOffDisk(t *testing.T) {
	t.Parallel()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":      "{}\n",
		"apps/web/index.ts": "web\n",
		"server/main.go":    "package main\n",
	}
	for name, body := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "server", "big.bin"), bytes.Repeat([]byte("q"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main", source},
		{"-C", source, "config", "user.email", "t@t"},
		{"-C", source, "config", "user.name", "t"},
		{"-C", source, "add", "-A"},
		{"-C", source, "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-sparse", []RepoInfo{{URL: source}}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-sparse",
		RepoURL:     source,
		WorkDir:     t.TempDir(),
		AgentName:   "sparse",
		TaskID:      "task-sparse",
		SparsePaths: "apps/web",
	})
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if result.SparsePaths != "apps/web" || result.SparseSkipped != "" {
		t.Fatalf("result sparse = %q skipped %q", result.SparsePaths, result.SparseSkipped)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "apps", "web", "index.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "server", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("cache worktree wrote the excluded blob: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "server", "MULTICA_SPARSE_EXCLUDED.txt")); err != nil {
		t.Fatalf("excluded directory has no marker: %v", err)
	}
}

// A later checkout of the same workdir is the normal reuse path: the task
// branch is already checked out, so CreateWorktree keeps it. An empty
// declaration (or --full, which arrives as an empty declaration) has to
// bring the rest of the repository back, and a wider declaration has to
// bring the newly named directories back. Neither may report success while
// those files stay off disk.
func TestCreateWorktreeReuseRestoresAndWidensSparseCheckout(t *testing.T) {
	t.Parallel()
	for _, isolated := range []bool{false, true} {
		name := "linked"
		if isolated {
			name = "isolated"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := sparseFixtureRepo(t)
			cache := New(t.TempDir(), testLogger())
			if err := cache.Sync("ws-sparse", []RepoInfo{{URL: source}}); err != nil {
				t.Fatalf("Sync: %v", err)
			}

			restoreDir := t.TempDir()
			restore := checkoutSparse(t, cache, source, restoreDir, "task-restore", "apps/web", isolated)
			if _, err := os.Stat(filepath.Join(restore.Path, "server", "main.go")); !os.IsNotExist(err) {
				t.Fatalf("setup left server/main.go on disk: %v", err)
			}
			again := checkoutSparse(t, cache, source, restoreDir, "task-restore", "", isolated)
			if again.Path != restore.Path {
				t.Fatalf("reuse path = %s, want %s", again.Path, restore.Path)
			}
			if again.Kept != KeptTaskBranch {
				t.Fatalf("Kept = %q, want %s", again.Kept, KeptTaskBranch)
			}
			if again.SparseSkipped != sparsecheckout.SkippedRestored || again.SparsePaths != "" {
				t.Fatalf("restore result skipped=%q paths=%q", again.SparseSkipped, again.SparsePaths)
			}
			assertOnDisk(t, again.Path, "server/main.go", "apps/other/index.ts", "apps/web/index.ts")
			if sparseFlag(t, again.Path) == "true" {
				t.Fatal("empty declaration left core.sparseCheckout=true")
			}
			if _, err := os.Stat(filepath.Join(again.Path, "server", "MULTICA_SPARSE_EXCLUDED.txt")); !os.IsNotExist(err) {
				t.Fatalf("marker survived restore: %v", err)
			}
			if status := gitStatus(t, again.Path); status != "" {
				t.Fatalf("restored checkout is dirty:\n%s", status)
			}

			// Already full: saying nothing again must not pretend to restore.
			third := checkoutSparse(t, cache, source, restoreDir, "task-restore", "", isolated)
			if third.SparseSkipped != "" || third.Kept != KeptTaskBranch {
				t.Fatalf("second empty declaration skipped=%q kept=%q", third.SparseSkipped, third.Kept)
			}

			// A kept full tree is not narrowed. Removing files waits for --fresh.
			narrow := checkoutSparse(t, cache, source, restoreDir, "task-restore", "apps/web", isolated)
			if narrow.SparseSkipped != sparsecheckout.SkippedKept {
				t.Fatalf("narrow skipped=%q, want kept", narrow.SparseSkipped)
			}
			assertOnDisk(t, narrow.Path, "server/main.go")

			widenDir := t.TempDir()
			wide := checkoutSparse(t, cache, source, widenDir, "task-widen", "apps/web", isolated)
			if _, err := os.Stat(filepath.Join(wide.Path, "server", "main.go")); !os.IsNotExist(err) {
				t.Fatalf("widen setup left server/main.go on disk: %v", err)
			}
			widened := checkoutSparse(t, cache, source, widenDir, "task-widen", "apps/web,server", isolated)
			if widened.Path != wide.Path {
				t.Fatalf("widen path = %s, want %s", widened.Path, wide.Path)
			}
			if widened.Kept != KeptTaskBranch {
				t.Fatalf("widen Kept = %q, want %s", widened.Kept, KeptTaskBranch)
			}
			if widened.SparseSkipped != sparsecheckout.SkippedWidened || widened.SparsePaths != "apps/web,server" {
				t.Fatalf("widen result skipped=%q paths=%q", widened.SparseSkipped, widened.SparsePaths)
			}
			if sparseFlag(t, widened.Path) != "true" {
				t.Fatalf("wider declaration left core.sparseCheckout=%q", sparseFlag(t, widened.Path))
			}
			assertOnDisk(t, widened.Path, "apps/web/index.ts", "server/main.go")
			if _, err := os.Stat(filepath.Join(widened.Path, "apps", "other", "index.ts")); !os.IsNotExist(err) {
				t.Fatalf("widen brought a directory the declaration did not name: %v", err)
			}
		})
	}
}

func sparseFixtureRepo(t *testing.T) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	files := map[string]string{
		"package.json":        "{}\n",
		"apps/web/index.ts":   "web\n",
		"apps/other/index.ts": "other\n",
		"server/main.go":      "package main\n",
	}
	for name, body := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-b", "main", source},
		{"-C", source, "config", "user.email", "t@t"},
		{"-C", source, "config", "user.name", "t"},
		{"-C", source, "add", "-A"},
		{"-C", source, "commit", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	return source
}

func checkoutSparse(t *testing.T, cache *Cache, source, workDir, taskID, paths string, isolated bool) *WorktreeResult {
	t.Helper()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-sparse",
		RepoURL:             source,
		WorkDir:             workDir,
		AgentName:           "sparse",
		TaskID:              taskID,
		SparsePaths:         paths,
		IsolatedGitMetadata: isolated,
	})
	if err != nil {
		t.Fatalf("CreateWorktree(%q) task %s: %v", paths, taskID, err)
	}
	return result
}

func assertOnDisk(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s is not on disk: %v", rel, err)
		}
	}
}

func sparseFlag(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", path, "config", "--worktree", "--get", "core.sparseCheckout").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitStatus(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", path, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return strings.TrimSpace(string(out))
}
