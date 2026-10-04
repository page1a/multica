package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A branch forked beside a busy conversation branch records its owner, so
// once the server makes it the issue's canonical line the next turn continues
// it instead of forking again (DENE-1286 ended up with four branches).
func TestPrepareLocalWorktreeContinuesAForkedBranchMadeCanonical(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	holder := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	t.Cleanup(func() { holder.Discard(worktreeTestLogger()) })

	forked := prepareTurn(t, repo, "MUL-6881", turnTwoTask)
	if forked.Branch == holder.Branch {
		t.Fatalf("fork took the busy branch %s", holder.Branch)
	}
	writeFile(t, filepath.Join(forked.WorkDir, "forked.txt"), "work on the fork\n")
	out := finalizeOK(t, forked)

	next, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "J",
		TaskID:          turnThreeTask,
		ConversationKey: "MUL-6881",
		WorkspaceID:     testBranchOwner.WorkspaceID,
		AgentID:         testBranchOwner.AgentID,
		ConversationID:  testBranchOwner.ConversationID,
		CanonicalBranch: out.Branch,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("next turn: %v", err)
	}
	t.Cleanup(func() { next.Discard(worktreeTestLogger()) })
	if next.Branch != out.Branch || !next.Continued {
		t.Fatalf("next turn branch = %q continued=%v, want to continue %q", next.Branch, next.Continued, out.Branch)
	}
	if _, err := os.Stat(filepath.Join(next.WorkDir, "forked.txt")); err != nil {
		t.Fatalf("next turn does not carry the fork's work: %v", err)
	}
}

// A run killed before Finalize leaves its copy on the conversation branch.
// The same-seat retry reclaims it: same path, same branch, committed work
// carried, uncommitted work saved under a ref and named in the notice.
func TestPrepareLocalWorktreeRetryReclaimsTheInterruptedCopy(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	killed := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(killed.WorkDir, "committed.txt"), "committed\n")
	gitRun(t, killed.Path, "add", "-A")
	gitRun(t, killed.Path, "commit", "-m", "committed before the kill")
	writeFile(t, filepath.Join(killed.WorkDir, "half-done.txt"), "not committed\n")

	if _, ok := ReclaimableWorktreeDir(killed.WorktreeRoot, killed.GitRoot, repo, killed.WorkDir); !ok {
		t.Fatal("the interrupted copy is not reclaimable")
	}

	retry, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:        repo,
		EnvRoot:          t.TempDir(),
		AgentName:        "J",
		TaskID:           turnTwoTask,
		ConversationKey:  "MUL-6881",
		WorkspaceID:      testBranchOwner.WorkspaceID,
		AgentID:          testBranchOwner.AgentID,
		ConversationID:   testBranchOwner.ConversationID,
		ResumeWorkDir:    killed.WorkDir,
		ReclaimPriorCopy: true,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	t.Cleanup(func() { retry.Discard(worktreeTestLogger()) })

	if !SameCanonicalPath(retry.Path, killed.Path) {
		t.Fatalf("retry copy = %q, want the reclaimed %q", retry.Path, killed.Path)
	}
	if retry.Branch != killed.Branch || !retry.Continued {
		t.Fatalf("retry branch = %q continued=%v, want to continue %q", retry.Branch, retry.Continued, killed.Branch)
	}
	if _, err := os.Stat(filepath.Join(retry.WorkDir, "committed.txt")); err != nil {
		t.Fatalf("retry lost the committed work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(retry.WorkDir, "half-done.txt")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted work was applied to the retry: %v", err)
	}
	ref := localInterruptedRefPrefix + killed.Branch
	if got := gitRun(t, repo, "show", ref+":half-done.txt"); strings.TrimSpace(got) != "not committed" {
		t.Fatalf("saved uncommitted work = %q", got)
	}
	if !strings.Contains(retry.InterruptedWorkNotice, ref) {
		t.Fatalf("notice does not name the saved ref: %q", retry.InterruptedWorkNotice)
	}
}

// A directory with no Multica record is not ours to remove, even on disk
// under the worktree root.
func TestReclaimableWorktreeDirRequiresARecordedRegisteredCopy(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	root := DefaultWorktreeRoot(repo)
	stray := filepath.Join(root, "dene-1286-stray")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if _, ok := ReclaimableWorktreeDir(root, repo, repo, stray); ok {
		t.Fatal("accepted a directory Multica never recorded")
	}
	missing := filepath.Join(root, "dene-1286-gone")
	if _, ok := ReclaimableWorktreeDir(root, repo, repo, missing); ok {
		t.Fatal("accepted a directory that is not on disk")
	}
}
