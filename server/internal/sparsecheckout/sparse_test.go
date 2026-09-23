package sparsecheckout

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	scope, err := Parse(" apps/web, packages/core\napps/web ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(scope.Paths, ",") != "apps/web,packages/core" {
		t.Fatalf("paths = %#v", scope.Paths)
	}
	empty, err := Parse("  ")
	if err != nil || empty.Active() {
		t.Fatalf("empty declaration = %+v, %v; want a full checkout", empty, err)
	}
	root, err := Parse(".")
	if err != nil || root.Active() {
		t.Fatalf(". = %+v, %v; want a full checkout", root, err)
	}
	for _, bad := range []string{"../secret", "/etc/passwd", `..\windows`, "apps/web, ."} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded; want an error", bad)
		}
	}
}

func TestMetadataString(t *testing.T) {
	t.Parallel()
	got, err := MetadataString([]byte(`{"checkout_paths":"apps/web","other":1}`))
	if err != nil || got != "apps/web" {
		t.Fatalf("metadata = %q, %v", got, err)
	}
	got, err = MetadataString([]byte(`{}`))
	if err != nil || got != "" {
		t.Fatalf("missing key = %q, %v", got, err)
	}
	if _, err := MetadataString([]byte(`{"checkout_paths":true}`)); err == nil {
		t.Fatal("non-string checkout_paths was accepted")
	}
}

func TestSparseWorktreeKeepsTheSourceFullAndShrinksTheCopy(t *testing.T) {
	top := newRepo(t)
	writeTree(t, top, map[string]string{
		"package.json":        `{"name":"root"}`,
		"pnpm-lock.yaml":      "lock\n",
		"apps/web/index.ts":   "export const web = 1\n",
		"apps/other/index.ts": "export const other = 1\n",
		"server/main.go":      "package main\n",
	})
	// Big enough that "significantly smaller" is not a rounding error, small
	// enough that the test stays cheap. The worktree file is this many bytes;
	// the sparse copy must not contain it.
	const heavy = 4 << 20
	if err := os.WriteFile(filepath.Join(top, "server", "big.bin"), bytes.Repeat([]byte("x"), heavy), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, top, "add", "-A")
	runGit(t, top, "commit", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, top, "worktree", "add", "--no-checkout", "-b", "agent/t", wt, "HEAD")
	ctx := context.Background()
	scope, err := Parse("apps/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable(ctx, wt, scope); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutCurrent(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if err := Finish(ctx, wt); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(wt, "apps", "web", "index.ts")); err != nil {
		t.Fatalf("declared module missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "package.json")); err != nil {
		t.Fatalf("root project file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "pnpm-lock.yaml")); err != nil {
		t.Fatalf("root lockfile missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "server", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("excluded blob is on disk: %v", err)
	}
	marker := filepath.Join(wt, "server", MarkerName)
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("excluded directory has no marker: %v", err)
	}
	if !strings.Contains(string(body), "multica repo sparse-add") {
		t.Fatalf("marker does not say how to materialize the path:\n%s", body)
	}
	if status := runGit(t, wt, "status", "--porcelain"); status != "" {
		t.Fatalf("marker showed up as uncommitted work:\n%s", status)
	}

	sparseBytes := treeBytes(t, wt)
	fullBytes := treeBytes(t, top)
	t.Logf("measurement: full checkout %d bytes, sparse checkout of apps/web %d bytes", fullBytes, sparseBytes)
	if sparseBytes*10 >= fullBytes {
		t.Fatalf("sparse checkout is not significantly smaller: sparse %d, full %d", sparseBytes, fullBytes)
	}

	// The source checkout this worktree was added from stays complete, and it
	// is not itself sparse. extensions.worktreeConfig may flip on; the files
	// and the sparse flag of the source must not.
	if _, err := os.Stat(filepath.Join(top, "server", "big.bin")); err != nil {
		t.Fatalf("source lost its files: %v", err)
	}
	if out, err := exec.Command("git", "-C", top, "config", "--worktree", "--get", "core.sparseCheckout").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "true" {
		t.Fatalf("source worktree became sparse: %s", out)
	}

	msg, err := Materialize(ctx, wt, "server/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "Checked out server/main.go") {
		t.Fatalf("materialize message = %q", msg)
	}
	if _, err := os.Stat(filepath.Join(wt, "server", "main.go")); err != nil {
		t.Fatalf("materialize left the file off disk: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker survived materialize of the directory: %v", err)
	}

	_, err = Materialize(ctx, wt, "no/such/file.go")
	var missing *PathNotInRepositoryError
	if !errors.As(err, &missing) {
		t.Fatalf("missing path error = %v, want PathNotInRepositoryError", err)
	}

	_, err = Materialize(ctx, top, "apps/web")
	var full *NotSparseError
	if !errors.As(err, &full) {
		t.Fatalf("full checkout materialize error = %v, want NotSparseError", err)
	}
}

func TestDeclaredPathMissingIsAnError(t *testing.T) {
	top := newRepo(t)
	writeTree(t, top, map[string]string{"package.json": "{}\n", "apps/web/index.ts": "x\n"})
	runGit(t, top, "add", "-A")
	runGit(t, top, "commit", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, top, "worktree", "add", "--no-checkout", "-b", "agent/t", wt, "HEAD")
	scope, err := Parse("apps/typo")
	if err != nil {
		t.Fatal(err)
	}
	err = Enable(context.Background(), wt, scope)
	var declared *DeclaredPathError
	if !errors.As(err, &declared) || declared.Path != "apps/typo" {
		t.Fatalf("Enable error = %v, want DeclaredPathError apps/typo", err)
	}
}

func TestWidenBeforeReplayKeepsAnOutsideEditOnDisk(t *testing.T) {
	top := newRepo(t)
	writeTree(t, top, map[string]string{
		"package.json":        "{}\n",
		"apps/web/index.ts":   "web\n",
		"apps/other/index.ts": "other\n",
	})
	runGit(t, top, "add", "-A")
	runGit(t, top, "commit", "-m", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, top, "worktree", "add", "--no-checkout", "-b", "agent/t", wt, "HEAD")
	ctx := context.Background()
	scope, err := Parse("apps/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable(ctx, wt, scope); err != nil || CheckoutCurrent(ctx, wt) != nil || Finish(ctx, wt) != nil {
		t.Fatal(err)
	}

	// A commit that touches a file outside the cone. Cherry-picking it without
	// widening stages the edit and leaves the file off disk.
	runGit(t, top, "commit", "--allow-empty", "-m", "hold")
	if err := os.WriteFile(filepath.Join(top, "apps", "other", "index.ts"), []byte("other\ndirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, top, "add", "apps/other/index.ts")
	runGit(t, top, "commit", "-m", "dirty")
	dirty := strings.TrimSpace(runGit(t, top, "rev-parse", "HEAD"))
	runGit(t, top, "reset", "--hard", "HEAD~2")

	if err := WidenToCover(ctx, wt, []string{"apps/other/index.ts"}); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "cherry-pick", "--no-commit", dirty)
	body, err := os.ReadFile(filepath.Join(wt, "apps", "other", "index.ts"))
	if err != nil {
		t.Fatalf("widened edit is not on disk: %v", err)
	}
	if string(body) != "other\ndirty\n" {
		t.Fatalf("widened file = %q", body)
	}
	// The source the worktree was cut from is back at the original commit and
	// still has its own file. The widen must not have edited it.
	if _, err := os.Stat(filepath.Join(top, "apps", "other", "index.ts")); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedCloneChecksOutOnlyTheCone(t *testing.T) {
	top := newRepo(t)
	writeTree(t, top, map[string]string{
		"package.json":      "{}\n",
		"apps/web/index.ts": "web\n",
		"server/main.go":    "package main\n",
	})
	heavy := bytes.Repeat([]byte("y"), 1<<20)
	if err := os.WriteFile(filepath.Join(top, "server", "big.bin"), heavy, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, top, "add", "-A")
	runGit(t, top, "commit", "-m", "init")

	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, top, "clone", "--local", "--no-checkout", top, clone)
	ctx := context.Background()
	scope, err := Parse("apps/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable(ctx, clone, scope); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, "checkout", "--detach", "HEAD")
	if err := Finish(ctx, clone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, "apps", "web", "index.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clone, "server", "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("isolated clone wrote the excluded blob: %v", err)
	}
	sparseBytes := treeBytes(t, clone)
	if sparseBytes >= int64(len(heavy)) {
		t.Fatalf("isolated sparse checkout is %d bytes, want well under the %d byte blob", sparseBytes, len(heavy))
	}
}

func TestReconcileRestoresAFullTreeAndWidensACone(t *testing.T) {
	top := newRepo(t)
	writeTree(t, top, map[string]string{
		"package.json":        "{}\n",
		"apps/web/index.ts":   "web\n",
		"apps/other/index.ts": "other\n",
		"server/main.go":      "package main\n",
	})
	runGit(t, top, "add", "-A")
	runGit(t, top, "commit", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, top, "worktree", "add", "--no-checkout", "-b", "agent/t", wt, "HEAD")
	ctx := context.Background()
	scope, err := Parse("apps/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := Enable(ctx, wt, scope); err != nil || CheckoutCurrent(ctx, wt) != nil || Finish(ctx, wt) != nil {
		t.Fatal(err)
	}

	// Kept checkout, empty declaration: the rest of the tree comes back.
	action, err := Reconcile(ctx, wt, Scope{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if action != SkippedRestored {
		t.Fatalf("restore action = %q", action)
	}
	if _, err := os.Stat(filepath.Join(wt, "server", "main.go")); err != nil {
		t.Fatalf("restore left server/main.go off disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "apps", "other", "index.ts")); err != nil {
		t.Fatalf("restore left apps/other off disk: %v", err)
	}
	if isSparse(ctx, wt) {
		t.Fatal("empty declaration left the checkout sparse")
	}

	// Put the cone back, then widen it without being allowed to narrow.
	web, err := Parse("apps/web")
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, wt, web); err != nil {
		t.Fatal(err)
	}
	wider, err := Parse("apps/web,server")
	if err != nil {
		t.Fatal(err)
	}
	action, err = Reconcile(ctx, wt, wider, false)
	if err != nil {
		t.Fatal(err)
	}
	if action != SkippedWidened {
		t.Fatalf("widen action = %q", action)
	}
	if _, err := os.Stat(filepath.Join(wt, "server", "main.go")); err != nil {
		t.Fatalf("widen left server/main.go off disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "apps", "other", "index.ts")); !os.IsNotExist(err) {
		t.Fatalf("widen checked out a directory outside the declaration: %v", err)
	}
	if !isSparse(ctx, wt) {
		t.Fatal("wider declaration turned sparse checkout off")
	}

	// A narrower declaration on a kept checkout must not delete files.
	action, err = Reconcile(ctx, wt, web, false)
	if err != nil {
		t.Fatal(err)
	}
	if action != SkippedKept {
		t.Fatalf("narrow action = %q", action)
	}
	if _, err := os.Stat(filepath.Join(wt, "server", "main.go")); err != nil {
		t.Fatalf("narrowing a kept checkout removed server/main.go: %v", err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@t")
	runGit(t, dir, "config", "user.name", "t")
	return dir
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}

func treeBytes(t *testing.T, root string) int64 {
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
