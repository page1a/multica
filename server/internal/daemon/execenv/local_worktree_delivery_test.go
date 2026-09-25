package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// A rerun by another seat lands on the issue's canonical delivery line, not
// on a branch named after the new seat (DENE-820).
func TestPrepareLocalWorktreeContinuesTheCanonicalBranchOfAnotherSeat(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "seat one's work\n")
	firstOutcome := finalizeOK(t, first)
	if firstOutcome.Branch != "agent/j/mul-6881" {
		t.Fatalf("first outcome branch = %q", firstOutcome.Branch)
	}
	firstTip := gitRun(t, repo, "rev-parse", "agent/j/mul-6881")

	otherSeat := testBranchOwner
	otherSeat.AgentID = "11112222-3333-4444-5555-000000000099"
	second, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "K",
		TaskID:          turnTwoTask,
		ConversationKey: "MUL-6881",
		WorkspaceID:     otherSeat.WorkspaceID,
		AgentID:         otherSeat.AgentID,
		ConversationID:  otherSeat.ConversationID,
		CanonicalBranch: "agent/j/mul-6881",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(other seat): %v", err)
	}
	if second.Branch != "agent/j/mul-6881" {
		t.Fatalf("other seat branch = %q, want the canonical line agent/j/mul-6881", second.Branch)
	}
	if !second.Continued || second.BaseCommit != firstTip {
		t.Fatalf("other seat did not continue the canonical tip: continued=%v base=%s want=%s", second.Continued, second.BaseCommit, firstTip)
	}
	if got := readFile(t, filepath.Join(second.WorkDir, "turn-one.txt")); got != "seat one's work\n" {
		t.Errorf("seat one's work missing from the rerun's tree: %q", got)
	}
	writeFile(t, filepath.Join(second.WorkDir, "turn-two.txt"), "seat two's work\n")
	if outcome := finalizeOK(t, second); outcome.Branch != "agent/j/mul-6881" {
		t.Fatalf("other seat delivered onto %q, want the canonical line", outcome.Branch)
	}

	// The original seat comes back: the record now names the other seat, and
	// the canonical hint is what lets it continue its own line.
	third, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "J",
		TaskID:          turnThreeTask,
		ConversationKey: "MUL-6881",
		WorkspaceID:     testBranchOwner.WorkspaceID,
		AgentID:         testBranchOwner.AgentID,
		ConversationID:  testBranchOwner.ConversationID,
		CanonicalBranch: "agent/j/mul-6881",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(seat one again): %v", err)
	}
	if third.Branch != "agent/j/mul-6881" || !third.Continued {
		t.Fatalf("seat one returning: branch=%q continued=%v", third.Branch, third.Continued)
	}
	for _, name := range []string{"turn-one.txt", "turn-two.txt"} {
		if _, err := os.Stat(filepath.Join(third.WorkDir, name)); err != nil {
			t.Errorf("seat one's third turn is missing %s: %v", name, err)
		}
	}
	finalizeOK(t, third)
}

// The canonical hint is not a skeleton key: a branch recorded for ANOTHER
// conversation, or one with no record at all, is still refused.
func TestPrepareLocalWorktreeRefusesACanonicalBranchOfAnotherConversation(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)

	first := prepareTurn(t, repo, "MUL-6881", turnOneTask)
	writeFile(t, filepath.Join(first.WorkDir, "turn-one.txt"), "issue A\n")
	finalizeOK(t, first)

	otherIssue := testBranchOwner
	otherIssue.ConversationID = "11112222-3333-4444-5555-000000000077"
	wt, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "J",
		TaskID:          turnTwoTask,
		ConversationKey: "MUL-7000",
		WorkspaceID:     otherIssue.WorkspaceID,
		AgentID:         otherIssue.AgentID,
		ConversationID:  otherIssue.ConversationID,
		CanonicalBranch: "agent/j/mul-6881",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree: %v", err)
	}
	if wt.Branch == "agent/j/mul-6881" {
		t.Fatal("a different conversation adopted another issue's canonical branch")
	}
	if _, err := os.Stat(filepath.Join(wt.WorkDir, "turn-one.txt")); err == nil {
		t.Error("issue A's work leaked into issue B's tree")
	}
	finalizeOK(t, wt)

	// A canonical name the server knows but this machine never made: no
	// record, so nothing to continue — and nothing invented under that name.
	gitRun(t, repo, "branch", "agent/z/mul-9000")
	ghost, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:       repo,
		EnvRoot:         t.TempDir(),
		AgentName:       "J",
		TaskID:          turnFourTask,
		ConversationKey: "MUL-9000",
		WorkspaceID:     testBranchOwner.WorkspaceID,
		AgentID:         testBranchOwner.AgentID,
		ConversationID:  "11112222-3333-4444-5555-000000000090",
		CanonicalBranch: "agent/z/mul-9000",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("PrepareLocalWorktree(ghost): %v", err)
	}
	if ghost.Branch == "agent/z/mul-9000" {
		t.Fatal("adopted a branch with no ownership record")
	}
	finalizeOK(t, ghost)
}
