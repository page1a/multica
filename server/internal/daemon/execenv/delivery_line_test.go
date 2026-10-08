package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testDeliveryBranch = "agent/delivery/dene-1"
	testParentIssue    = "11112222-3333-4444-5555-00000000aaaa"
)

// prepareSubIssue prepares a sub-issue's worktree on the parent's line.
func prepareSubIssue(t *testing.T, repo, key, issueID, taskID string) *LocalWorktree {
	t.Helper()
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "J",
		TaskID:          taskID,
		ConversationKey: key,
		WorkspaceID:     testBranchOwner.WorkspaceID,
		AgentID:         testBranchOwner.AgentID,
		ConversationID:  issueID,
		DeliveryBranch:  testDeliveryBranch,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(%s): %v", key, err)
	}
	return wt
}

func commitIn(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, file), content)
	gitRun(t, dir, "add", file)
	gitRun(t, dir, "commit", "-m", msg)
}

func mergeBack(t *testing.T, wt *LocalWorktree) DeliveryMergeResult {
	t.Helper()
	res, err := MergeIntoDeliveryLine(DeliveryMergeParams{
		Dir: wt.WorkDir, Branch: testDeliveryBranch, OwnerIssueID: testParentIssue, WorkspaceID: testBranchOwner.WorkspaceID,
		IssueID: wt.owner.ConversationID,
	})
	if err != nil {
		t.Fatalf("MergeIntoDeliveryLine(%s): %v", wt.Branch, err)
	}
	return res
}

func subjects(commits []DeliveryMergeCommit) []string {
	out := make([]string, 0, len(commits))
	for _, c := range commits {
		out = append(out, c.Subject)
	}
	return out
}

// The DENE-1537 acceptance chain on real git: stage 1 has one sub-issue,
// stage 2 has two parallel ones. Each later sub-issue stands on the earlier
// stage's work, the parallel pair merges back without a PR each, the parent
// continues the one delivery branch, and the user's main is never touched.
func TestDeliveryLineChainsSequentialAndParallelSubIssues(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	mainBefore := gitRun(t, repo, "rev-parse", "main")

	// Stage 1: the delivery branch does not exist yet; the first merge-back
	// creates it.
	a := prepareSubIssue(t, repo, "DENE-2", "11112222-3333-4444-5555-00000000000a", "11112222-3333-4444-5555-0000000000a1")
	commitIn(t, a.WorkDir, "a.txt", "stage one\n", "feat: stage one")
	resA := mergeBack(t, a)
	if resA.Status != DeliveryMerged || resA.SourceBranch != a.Branch {
		t.Fatalf("stage one merge = %+v", resA)
	}
	if got := subjects(resA.Commits); !containsString(got, "feat: stage one") {
		t.Fatalf("stage one commits = %v, want feat: stage one", got)
	}
	if tip := gitRun(t, repo, "rev-parse", testDeliveryBranch); tip != resA.Tip {
		t.Fatalf("delivery tip = %s, report says %s", tip, resA.Tip)
	}
	finalizeOK(t, a)

	// Stage 2: two siblings prepared side by side, both forked from the
	// delivery tip, so both see stage one's work.
	b := prepareSubIssue(t, repo, "DENE-3", "11112222-3333-4444-5555-00000000000b", "11112222-3333-4444-5555-0000000000b1")
	c := prepareSubIssue(t, repo, "DENE-4", "11112222-3333-4444-5555-00000000000c", "11112222-3333-4444-5555-0000000000c1")
	for _, wt := range []*LocalWorktree{b, c} {
		if got := readFile(t, filepath.Join(wt.WorkDir, "a.txt")); got != "stage one\n" {
			t.Fatalf("%s does not stand on stage one: a.txt = %q", wt.Branch, got)
		}
		if wt.BaseCommit == "" || !isAncestor(repo, resA.Tip, wt.BaseCommit) {
			t.Fatalf("%s base %s does not descend from the delivery tip %s", wt.Branch, wt.BaseCommit, resA.Tip)
		}
	}
	if b.Branch == c.Branch {
		t.Fatalf("parallel siblings share branch %s", b.Branch)
	}
	commitIn(t, b.WorkDir, "b.txt", "stage two, b\n", "feat: stage two b")
	commitIn(t, c.WorkDir, "c.txt", "stage two, c\n", "feat: stage two c")

	resB := mergeBack(t, b)
	if got := subjects(resB.Commits); len(got) != 1 || got[0] != "feat: stage two b" {
		t.Fatalf("b's merge lists %v, want only its own commit", got)
	}
	resC := mergeBack(t, c)
	if resC.Status != DeliveryMerged {
		t.Fatalf("c's merge = %+v", resC)
	}
	if got := subjects(resC.Commits); len(got) != 1 || got[0] != "feat: stage two c" {
		t.Fatalf("c's merge lists %v, want only its own commit", got)
	}
	finalizeOK(t, b)
	finalizeOK(t, c)

	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := gitTry(t, repo, "cat-file", "-e", testDeliveryBranch+":"+f); err != nil {
			t.Errorf("delivery branch is missing %s", f)
		}
	}
	if got := gitRun(t, repo, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("main moved from %s to %s before any PR merged", mainBefore, got)
	}

	// The parent continues the delivery branch as its canonical line and
	// opens the one PR from it.
	parent, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "K",
		TaskID:          "11112222-3333-4444-5555-0000000000f1",
		ConversationKey: "DENE-1",
		WorkspaceID:     testBranchOwner.WorkspaceID,
		AgentID:         "11112222-3333-4444-5555-0000000000ff",
		ConversationID:  testParentIssue,
		CanonicalBranch: testDeliveryBranch,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(parent): %v", err)
	}
	if parent.Branch != testDeliveryBranch || !parent.Continued {
		t.Fatalf("parent branch=%q continued=%v, want it to continue %s", parent.Branch, parent.Continued, testDeliveryBranch)
	}
	finalizeOK(t, parent)
}

// A real conflict is reported with its files, and the delivery branch stays
// where it was: the platform never resolves it.
func TestDeliveryLineConflictNamesFilesAndLeavesTheBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	d := prepareSubIssue(t, repo, "DENE-5", "11112222-3333-4444-5555-00000000000d", "11112222-3333-4444-5555-0000000000d1")
	e := prepareSubIssue(t, repo, "DENE-6", "11112222-3333-4444-5555-00000000000e", "11112222-3333-4444-5555-0000000000e1")
	commitIn(t, d.WorkDir, "tracked.txt", "d's version\n", "feat: d")
	commitIn(t, e.WorkDir, "tracked.txt", "e's version\n", "feat: e")
	commitIn(t, e.WorkDir, "e-only.txt", "fine\n", "feat: e extra")

	resD := mergeBack(t, d)
	if resD.Status != DeliveryMerged {
		t.Fatalf("d = %+v", resD)
	}
	resE := mergeBack(t, e)
	if resE.Status != DeliveryConflict {
		t.Fatalf("e = %+v, want a conflict", resE)
	}
	if len(resE.ConflictFiles) != 1 || resE.ConflictFiles[0] != "tracked.txt" {
		t.Fatalf("conflict files = %v, want [tracked.txt]", resE.ConflictFiles)
	}
	if tip := gitRun(t, repo, "rev-parse", testDeliveryBranch); tip != resD.Tip {
		t.Fatalf("a conflict moved the delivery branch to %s", tip)
	}

	// The executor resolves on its own branch and closes again.
	if _, err := gitTry(t, e.WorkDir, "merge", testDeliveryBranch); err == nil {
		t.Fatal("expected the merge to stop on the conflict")
	}
	writeFile(t, filepath.Join(e.WorkDir, "tracked.txt"), "both\n")
	gitRun(t, e.WorkDir, "add", "tracked.txt")
	gitRun(t, e.WorkDir, "commit", "--no-edit")
	resE = mergeBack(t, e)
	if resE.Status != DeliveryMerged || resE.Tip == resD.Tip {
		t.Fatalf("after resolving, e = %+v", resE)
	}
	if got := gitRun(t, repo, "show", testDeliveryBranch+":tracked.txt"); got != "both" {
		t.Fatalf("delivery tracked.txt = %q", got)
	}
}

func TestDeliveryLineRefusesUncommittedWork(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	f := prepareSubIssue(t, repo, "DENE-7", "11112222-3333-4444-5555-00000000000f", "11112222-3333-4444-5555-0000000000f7")
	writeFile(t, filepath.Join(f.WorkDir, "loose.txt"), "not committed\n")
	// Injected instruction files and Multica sidecars never block the merge.
	writeFile(t, filepath.Join(f.WorkDir, "CLAUDE.md"), "runtime brief\n")

	_, err := MergeIntoDeliveryLine(DeliveryMergeParams{Dir: f.WorkDir, Branch: testDeliveryBranch, OwnerIssueID: testParentIssue, WorkspaceID: testBranchOwner.WorkspaceID, IssueID: f.owner.ConversationID})
	if !errors.Is(err, ErrDeliveryMergeRefused) || !strings.Contains(err.Error(), "loose.txt") {
		t.Fatalf("err = %v, want a refusal naming loose.txt", err)
	}
	if strings.Contains(err.Error(), "CLAUDE.md") {
		t.Fatalf("injected CLAUDE.md counted as work: %v", err)
	}
	if _, err := gitTry(t, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+testDeliveryBranch); err == nil {
		t.Fatal("a refused merge created the delivery branch")
	}
	if err := os.Remove(filepath.Join(f.WorkDir, "loose.txt")); err != nil {
		t.Fatal(err)
	}
}

// A close run from a checkout that is not the sub-issue's own never merges
// that checkout's branch into the parent's line.
func TestDeliveryLineRefusesAnotherIssuesBranch(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	g := prepareSubIssue(t, repo, "DENE-8", "11112222-3333-4444-5555-000000000008", "11112222-3333-4444-5555-0000000000a8")
	commitIn(t, g.WorkDir, "g.txt", "g\n", "feat: g")
	_, err := MergeIntoDeliveryLine(DeliveryMergeParams{Dir: g.WorkDir, Branch: testDeliveryBranch, OwnerIssueID: testParentIssue,
		WorkspaceID: testBranchOwner.WorkspaceID, IssueID: "11112222-3333-4444-5555-000000000009"})
	if !errors.Is(err, ErrDeliveryMergeRefused) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, err := MergeIntoDeliveryLine(DeliveryMergeParams{Dir: repo, Branch: testDeliveryBranch, OwnerIssueID: testParentIssue,
		WorkspaceID: testBranchOwner.WorkspaceID, IssueID: "11112222-3333-4444-5555-000000000008"}); !errors.Is(err, ErrDeliveryMergeRefused) {
		t.Fatalf("merging the user's own checkout: err = %v, want a refusal", err)
	}
}

func containsString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}
