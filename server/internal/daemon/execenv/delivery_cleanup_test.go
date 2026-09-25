package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merged rescue line is removed whole: worktree, branch and state refs.
// An unmerged one is kept with a reason unless forced (DENE-820).
func TestCleanupDeliveryBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	trunk := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD")

	wt := filepath.Join(t.TempDir(), "rescue")
	gitRun(t, repo, "worktree", "add", "-b", "agent/r/mul-7000", wt, "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "rescue.txt"), []byte("fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", "rescue.txt")
	gitRun(t, wt, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "rescue")
	gitRun(t, repo, "update-ref", userStateRef("agent/r/mul-7000"), "HEAD")

	res, err := CleanupDeliveryBranch(repo, "agent/r/mul-7000", trunk, false, worktreeTestLogger())
	if err != nil {
		t.Fatalf("unmerged cleanup: %v", err)
	}
	if res.Cleaned() || !strings.Contains(res.Kept, "not merged") {
		t.Fatalf("unmerged branch should be kept, got %+v", res)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree must survive a kept verdict: %v", err)
	}

	// Merge it (fast-forward) and clean again.
	gitRun(t, repo, "merge", "-q", "--ff-only", "agent/r/mul-7000")
	res, err = CleanupDeliveryBranch(repo, "agent/r/mul-7000", trunk, false, worktreeTestLogger())
	if err != nil {
		t.Fatalf("merged cleanup: %v", err)
	}
	if !res.Cleaned() || res.Evidence != MergeEvidenceAncestor || len(res.Removed) != 1 {
		t.Fatalf("merged branch should be cleaned, got %+v", res)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present after cleanup: %v", err)
	}
	if list := gitRun(t, repo, "branch", "--list", "agent/r/mul-7000"); strings.TrimSpace(list) != "" {
		t.Fatalf("branch still present: %q", list)
	}
	if refs := gitRun(t, repo, "for-each-ref", localStateRefPrefix); strings.TrimSpace(refs) != "" {
		t.Fatalf("state ref still present: %q", refs)
	}

	// Cleaning an already-gone branch is a no-op success.
	res, err = CleanupDeliveryBranch(repo, "agent/r/mul-7000", trunk, false, worktreeTestLogger())
	if err != nil || !res.Cleaned() {
		t.Fatalf("second cleanup: res=%+v err=%v", res, err)
	}
}

func TestCleanupDeliveryBranchForceRemovesUnmergedWork(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	trunk := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	wt := filepath.Join(t.TempDir(), "experiment")
	gitRun(t, repo, "worktree", "add", "-b", "agent/x/mul-7001", wt, "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", "x.txt")
	gitRun(t, wt, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "experiment")

	res, err := CleanupDeliveryBranch(repo, "agent/x/mul-7001", trunk, true, worktreeTestLogger())
	if err != nil {
		t.Fatalf("forced cleanup: %v", err)
	}
	if !res.Cleaned() || len(res.Removed) != 1 {
		t.Fatalf("forced cleanup should remove, got %+v", res)
	}
	if list := gitRun(t, repo, "branch", "--list", "agent/x/mul-7001"); strings.TrimSpace(list) != "" {
		t.Fatalf("branch still present: %q", list)
	}
}
