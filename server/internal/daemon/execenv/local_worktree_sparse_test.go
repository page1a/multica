package execenv

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareLocalWorktreeHonorsCheckoutPaths(t *testing.T) {
	t.Parallel()
	requireGit(t)
	repo := sparseFixtureRepo(t)

	// An edit outside the declared module has to survive into the worktree.
	// Leaving it staged-but-absent is the silent miss sparse checkout causes.
	other := filepath.Join(repo, "apps", "other", "index.ts")
	if err := os.WriteFile(other, []byte("other\ndirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:     repo,
		EnvRoot:       t.TempDir(),
		AgentName:     "J",
		TaskID:        "11112222-3333-4444-5555-dddddddddddd",
		CheckoutPaths: "apps/web",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wt.Discard(worktreeTestLogger()) })

	if _, err := os.Stat(filepath.Join(wt.Path, "apps", "web", "index.ts")); err != nil {
		t.Fatalf("declared module missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "package.json")); err != nil {
		t.Fatalf("root project file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "server", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("excluded blob is on disk: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(wt.Path, "apps", "other", "index.ts"))
	if err != nil {
		t.Fatalf("user edit outside the cone is not on disk: %v", err)
	}
	if string(body) != "other\ndirty\n" {
		t.Fatalf("replayed edit = %q", body)
	}
	if _, err := os.Stat(filepath.Join(repo, "server", "big.bin")); err != nil {
		t.Fatalf("the user's checkout lost a file: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "config", "--worktree", "--get", "core.sparseCheckout").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "true" {
		t.Fatalf("the user's checkout became sparse: %s", out)
	}

	sparseBytes := dirBytes(t, wt.Path)
	fullBytes := dirBytes(t, repo)
	t.Logf("local worktree measurement: full %d bytes, sparse %d bytes", fullBytes, sparseBytes)
	if sparseBytes*5 >= fullBytes {
		t.Fatalf("sparse worktree is not significantly smaller: sparse %d, full %d", sparseBytes, fullBytes)
	}
}

func sparseFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	files := map[string]string{
		"package.json":        "{}\n",
		"apps/web/index.ts":   "web\n",
		"apps/other/index.ts": "other\n",
		"server/main.go":      "package main\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "big.bin"), bytes.Repeat([]byte("z"), 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@test.com"},
		{"add", "-A"},
		{"commit", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	return dir
}

func dirBytes(t *testing.T, root string) int64 {
	t.Helper()
	var n int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
