package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decision table on its own: every class, and the cases that sit on the
// boundary between two of them.
func TestClassifyDelivery(t *testing.T) {
	t.Parallel()
	const base, head, branch = "base", "head", "branch"
	cases := []struct {
		name  string
		facts deliveryFacts
		class deliveryClass
		point string
	}{
		{"on its own branch", deliveryFacts{Head: head, Base: base, BranchTip: head, HeadContainsBase: true, HeadHasOwnCommits: true}, deliveryOnBranch, head},
		{"on its own branch, nothing done", deliveryFacts{Head: base, Base: base, BranchTip: base, HeadContainsBase: true}, deliveryOnBranch, base},
		{"committed on the branch, then wandered off", deliveryFacts{Head: head, Base: base, BranchTip: branch, BranchContainsBase: true, BranchStrandsWork: true}, deliveryOnBranch, branch},
		{"read-only: switched to origin, no commit", deliveryFacts{Head: head, Base: base, BranchTip: base, BranchContainsBase: true}, deliveryReadOnly, ""},
		{"read-only: task branch reset to the user's HEAD", deliveryFacts{Head: head, Base: base, BranchTip: head}, deliveryReadOnly, ""},
		{"read-only: task branch deleted", deliveryFacts{Head: head, Base: base}, deliveryReadOnly, ""},
		{"renamed: own branch from the start point", deliveryFacts{Head: head, Base: base, BranchTip: base, HeadContainsBase: true, BranchContainsBase: true, HeadHasOwnCommits: true}, deliveryRenamed, head},
		{"renamed: task branch deleted", deliveryFacts{Head: head, Base: base, HeadContainsBase: true, HeadHasOwnCommits: true}, deliveryRenamed, head},
		{"rebased: new branch on origin with commits", deliveryFacts{Head: head, Base: base, BranchTip: base, BranchContainsBase: true, HeadHasOwnCommits: true}, deliveryRebased, head},
		{"rebased: task branch itself reset to origin and committed", deliveryFacts{Head: head, Base: base, BranchTip: head, HeadHasOwnCommits: true}, deliveryRebased, head},
		{"rebased: on the reset branch, then wandered off", deliveryFacts{Head: head, Base: base, BranchTip: branch, BranchStrandsWork: true}, deliveryRebased, branch},
		{"stuck: no HEAD", deliveryFacts{Base: base, BranchTip: base}, deliveryStuck, ""},
		{"stuck: branch and HEAD diverged", deliveryFacts{Head: head, Base: base, BranchTip: branch, HeadContainsBase: true, BranchContainsBase: true, HeadHasOwnCommits: true, BranchStrandsWork: true}, deliveryStuck, ""},
	}
	for _, tc := range cases {
		got := classifyDelivery(tc.facts)
		if got.class != tc.class || got.point != tc.point {
			t.Errorf("%s: got %s at %q, want %s at %q", tc.name, got.class, got.point, tc.class, tc.point)
		}
		if got.class == deliveryStuck && got.reason == "" {
			t.Errorf("%s: stuck without a reason", tc.name)
		}
	}
}

// newStaleDirtyRepo is the DENE-871 setup: a local checkout that tracks
// origin/main, is one commit behind it, and has an uncommitted edit, so the
// baseline refresh cannot fast-forward it.
func newStaleDirtyRepo(t *testing.T) (repo, remoteHead string) {
	t.Helper()
	repo = newTestRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, remote, "init", "--bare", "-b", "main")
	gitRun(t, repo, "remote", "add", "origin", remote)
	gitRun(t, repo, "push", "-u", "origin", "main")
	other := filepath.Join(t.TempDir(), "other")
	gitRun(t, t.TempDir(), "clone", remote, other)
	gitRun(t, other, "config", "user.name", "Other")
	gitRun(t, other, "config", "user.email", "other@test.com")
	writeFile(t, filepath.Join(other, "remote.txt"), "remote\n")
	gitRun(t, other, "add", ".")
	gitRun(t, other, "commit", "-m", "remote update")
	gitRun(t, other, "push", "origin", "main")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "local edit\n")
	return repo, gitRun(t, other, "rev-parse", "HEAD")
}

// The opening message says how stale the base is, why, and which branch the
// run delivers on — so the agent has no reason to start a branch of its own.
func TestStaleDirtyBaselineIsExplainedWithTheLegalAction(t *testing.T) {
	t.Parallel()
	repo, _ := newStaleDirtyRepo(t)
	wt := prepareTurn(t, repo, "DENE-871", turnOneTask)
	for _, want := range []string{"1 commit behind \"origin/main\"", "tracked.txt", "uncommitted changes"} {
		if !strings.Contains(wt.StaleBaselineNotice, want) {
			t.Errorf("notice %q is missing %q", wt.StaleBaselineNotice, want)
		}
	}
	if wt.Upstream != "origin/main" {
		t.Errorf("Upstream = %q, want origin/main", wt.Upstream)
	}
	finalizeOK(t, wt)
}

// DENE-871: stale dirty base, the agent switches to a new branch on the
// latest origin and commits nothing. That is a read-only run — success, no
// error, the task branch dropped, and the agent's branch cleaned up.
func TestFinalizeReadOnlyRunThatSwitchedToOrigin(t *testing.T) {
	t.Parallel()
	repo, remoteHead := newStaleDirtyRepo(t)
	wt := prepareTurn(t, repo, "DENE-871", turnOneTask)
	wt.PreparedAt-- // the agent's branch is created after prepare
	gitRun(t, wt.Path, "checkout", "--quiet", "-b", "agent-fresh", "origin/main")

	outcome := finalizeOK(t, wt)
	if outcome.Branch != "" || outcome.PreservedPath != "" {
		t.Errorf("outcome = %+v, want a read-only run", outcome)
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "refs/heads/agent/j/dene-871"); err == nil {
		t.Error("the task branch survived a read-only run")
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "refs/heads/agent-fresh"); err == nil {
		t.Error("the agent's own branch was left behind")
	}
	if got := gitRun(t, repo, "rev-parse", "origin/main"); got != remoteHead {
		t.Errorf("origin/main moved to %s", got)
	}
	if got := readFile(t, filepath.Join(repo, "tracked.txt")); got != "local edit\n" {
		t.Errorf("the user's edit was touched: %q", got)
	}
}

// The agent restarts from the latest origin and commits there. The run
// succeeds, the task branch is the delivery line, and it says it does not
// carry the local edits — which the next turn then replays onto it.
func TestFinalizeDeliversARunRebasedOntoOrigin(t *testing.T) {
	t.Parallel()
	repo, remoteHead := newStaleDirtyRepo(t)
	wt := prepareTurn(t, repo, "DENE-871", turnOneTask)
	wt.PreparedAt--
	gitRun(t, wt.Path, "checkout", "--quiet", "-b", "agent-fresh", "origin/main")
	writeFile(t, filepath.Join(wt.Path, "fix.txt"), "the fix\n")
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-m", "fix on the latest origin")
	delivered := gitRun(t, wt.Path, "rev-parse", "HEAD")

	outcome := finalizeOK(t, wt)
	if outcome.Branch != "agent/j/dene-871" {
		t.Fatalf("outcome branch = %q, want the task branch", outcome.Branch)
	}
	if got := gitRun(t, repo, "rev-parse", "agent/j/dene-871"); got != delivered {
		t.Errorf("task branch = %s, want the rebased commit %s", got, delivered)
	}
	if parent := gitRun(t, repo, "rev-parse", "agent/j/dene-871^"); parent != remoteHead {
		t.Errorf("delivery parent = %s, want origin's head %s", parent, remoteHead)
	}
	if !strings.Contains(outcome.Notice, "does not carry the local-directory snapshot") {
		t.Errorf("notice = %q, want the missing-snapshot remark", outcome.Notice)
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "refs/heads/agent-fresh"); err == nil {
		t.Error("the agent's own branch was left behind")
	}

	// The next turn continues the delivery line and gets the local edit back.
	next := prepareTurn(t, repo, "DENE-871", turnTwoTask)
	if !next.Continued || next.Branch != "agent/j/dene-871" {
		t.Fatalf("next turn: branch=%q continued=%v", next.Branch, next.Continued)
	}
	if got := readFile(t, filepath.Join(next.WorkDir, "tracked.txt")); got != "local edit\n" {
		t.Errorf("next turn tracked.txt = %q, want the user's edit replayed", got)
	}
	if got := readFile(t, filepath.Join(next.WorkDir, "fix.txt")); got != "the fix\n" {
		t.Errorf("next turn lost the delivered fix: %q", got)
	}
	finalizeOK(t, next)
}

// Work committed on a branch of the agent's own naming, from the turn's start,
// is fast-forwarded onto the task branch.
func TestFinalizeDeliversARenamedBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	wt := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	wt.PreparedAt--
	gitRun(t, wt.Path, "checkout", "--quiet", "-b", "my-feature")
	writeFile(t, filepath.Join(wt.WorkDir, "agent.txt"), "work\n")
	// Left uncommitted: Finalize commits it onto my-feature first.

	outcome := finalizeOK(t, wt)
	if outcome.Branch != "agent/j/mul-6881" || !outcome.AutoCommitted {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := gitRun(t, repo, "show", "agent/j/mul-6881:agent.txt"); got != "work" {
		t.Errorf("task branch agent.txt = %q", got)
	}
	if !isAncestor(repo, wt.BaseCommit, gitRun(t, repo, "rev-parse", "agent/j/mul-6881")) {
		t.Error("renamed delivery lost the turn's start")
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "refs/heads/my-feature"); err == nil {
		t.Error("the agent's own branch was left behind")
	}
	next := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if !next.Continued {
		t.Error("the renamed delivery is not continuable")
	}
	finalizeOK(t, next)
}

// A branch the agent pushed is still its work: the remote copy must not turn
// the run into a read-only one.
func TestFinalizeDeliversAPushedBranch(t *testing.T) {
	t.Parallel()
	repo, _ := newStaleDirtyRepo(t)
	wt := prepareTurn(t, repo, "DENE-871", turnOneTask)
	gitRun(t, wt.Path, "checkout", "--quiet", "-b", "pushed", "origin/main")
	writeFile(t, filepath.Join(wt.Path, "fix.txt"), "the fix\n")
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-m", "pushed fix")
	gitRun(t, wt.Path, "push", "--quiet", "-u", "origin", "pushed")
	delivered := gitRun(t, wt.Path, "rev-parse", "HEAD")

	outcome := finalizeOK(t, wt)
	if outcome.Branch != "agent/j/dene-871" {
		t.Fatalf("outcome branch = %q, want the task branch", outcome.Branch)
	}
	if got := gitRun(t, repo, "rev-parse", "agent/j/dene-871"); got != delivered {
		t.Errorf("task branch = %s, want %s", got, delivered)
	}
}

// A branch the user already had is never deleted, even when the agent ended
// on it and it holds nothing new.
func TestFinalizeKeepsAUserBranchTheRunEndedOn(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	gitRun(t, repo, "branch", "users-own")
	wt := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	gitRun(t, wt.Path, "checkout", "--quiet", "users-own")

	if outcome := finalizeOK(t, wt); outcome.Branch != "" {
		t.Errorf("outcome branch = %q, want a read-only run", outcome.Branch)
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "refs/heads/users-own"); err != nil {
		t.Error("the user's branch was deleted")
	}
}

// The one case left to a person: the task branch and HEAD each carry work.
// The error says where the work is and how to keep it.
func TestFinalizeStuckDeliveryNamesTheWorkAndTheRecovery(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	wt := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(wt.WorkDir, "on-branch.txt"), "a\n")
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-m", "on the task branch")
	onBranch := gitRun(t, repo, "rev-parse", "agent/j/mul-6881")
	gitRun(t, wt.Path, "checkout", "--quiet", "-b", "elsewhere", wt.BaseCommit)
	writeFile(t, filepath.Join(wt.WorkDir, "elsewhere.txt"), "b\n")
	gitRun(t, wt.Path, "add", "-A")
	gitRun(t, wt.Path, "commit", "-m", "elsewhere")
	head := gitRun(t, wt.Path, "rev-parse", "HEAD")

	outcome, err := wt.Finalize(worktreeTestLogger())
	if err == nil {
		t.Fatal("Finalize picked one of two diverged lines")
	}
	for _, want := range []string{"branch elsewhere", wt.Path, "nothing is left uncommitted", "branch agent/j/mul-6881-recovered " + head} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
	if outcome.PreservedPath != wt.Path || outcome.Branch != "" {
		t.Errorf("outcome = %+v, want the worktree preserved and no branch", outcome)
	}
	if got := gitRun(t, repo, "rev-parse", "agent/j/mul-6881"); got != onBranch {
		t.Errorf("task branch moved to %s", got)
	}
	_ = removeLocalWorktreeDir(repo, wt.Path, worktreeTestLogger())
}
