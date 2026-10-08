package execenv

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sub-issues deliver onto the parent's line (DENE-1537).
//
// A sub-issue's run works on its own branch, forked from the parent's
// delivery branch (forkFromDeliveryLine). `multica issue close --outcome
// done` calls MergeIntoDeliveryLine from inside that worktree: the
// sub-issue's commits land on the delivery branch, and the parent's one PR
// carries them. The merge never touches a checkout — the delivery branch is
// moved with a compare-and-swap update-ref — so a sibling running in its own
// worktree is never disturbed, and two siblings closing at once cannot both
// win the same tip.

// Delivery merge outcomes.
const (
	DeliveryMerged   = "merged"
	DeliveryConflict = "conflict"
)

const maxDeliveryMergeCommits = 200

// DeliveryMergeParams says what to merge where.
type DeliveryMergeParams struct {
	// Dir is the sub-issue's working directory (its worktree).
	Dir string
	// Branch is the parent's delivery branch.
	Branch string
	// OwnerIssueID is the parent issue: the delivery branch is recorded as
	// its conversation's, so the parent's own run continues it.
	OwnerIssueID string
	// WorkspaceID is recorded with the branch when the sub-issue's branch
	// carries no record to copy it from.
	WorkspaceID string
	// IssueID is the sub-issue. The checkout's branch must be recorded as
	// its conversation's, so a close run from some other checkout never
	// merges unrelated work into the parent's line.
	IssueID string
}

// DeliveryMergeCommit is one commit the merge added to the delivery branch.
type DeliveryMergeCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// DeliveryMergeResult is what happened.
type DeliveryMergeResult struct {
	Status       string                `json:"status"`
	Branch       string                `json:"branch"`
	SourceBranch string                `json:"source_branch,omitempty"`
	Tip          string                `json:"tip,omitempty"`
	Commits      []DeliveryMergeCommit `json:"commits"`
	// ConflictFiles is set for a conflict: the files git could not merge.
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

// ErrDeliveryMergeRefused is returned for a merge the caller must fix before
// retrying: uncommitted work, a detached checkout, the delivery branch
// checked out somewhere. Nothing was written.
var ErrDeliveryMergeRefused = errors.New("delivery merge refused")

// MergeIntoDeliveryLine merges the commits on Dir's branch into the parent's
// delivery branch. A conflict is a result, not an error: nothing is written
// and the conflicting files are returned.
func MergeIntoDeliveryLine(p DeliveryMergeParams) (DeliveryMergeResult, error) {
	branch := strings.TrimSpace(p.Branch)
	res := DeliveryMergeResult{Branch: branch, Commits: []DeliveryMergeCommit{}}
	if branch == "" {
		return res, fmt.Errorf("%w: no delivery branch to merge into", ErrDeliveryMergeRefused)
	}
	if _, err := runGitTrimmed(p.Dir, "check-ref-format", "--branch", branch); err != nil {
		return res, fmt.Errorf("%w: %q is not a branch name", ErrDeliveryMergeRefused, branch)
	}
	gitRoot, err := runGitTrimmed(p.Dir, "rev-parse", "--show-toplevel")
	if err != nil || gitRoot == "" {
		return res, fmt.Errorf("%w: %s is not inside a git checkout; run issue close from the sub-issue's working directory", ErrDeliveryMergeRefused, p.Dir)
	}
	source := worktreeBranch(gitRoot)
	if source == "" {
		return res, fmt.Errorf("%w: the checkout is not on a branch; switch back to the task branch first", ErrDeliveryMergeRefused)
	}
	res.SourceBranch = source
	if source == branch {
		return res, fmt.Errorf("%w: this checkout is on the delivery branch %s itself; sub-issues commit on their own branch", ErrDeliveryMergeRefused, branch)
	}
	if p.IssueID != "" {
		ref, _ := readUserStateRef(gitRoot, source)
		rec, err := readBranchRecord(gitRoot, ref)
		if ref == "" || err != nil || rec.owner.ConversationID != p.IssueID {
			return res, fmt.Errorf("%w: branch %s is not this sub-issue's task branch; run issue close from the worktree Multica prepared for it", ErrDeliveryMergeRefused, source)
		}
	}
	if dirty, err := uncommittedWork(gitRoot); err != nil {
		return res, err
	} else if len(dirty) > 0 {
		return res, fmt.Errorf("%w: uncommitted changes (%s); commit them on %s first — only commits are merged", ErrDeliveryMergeRefused, strings.Join(clip(dirty, 5), ", "), source)
	}
	if holder := worktreeHolding(gitRoot, branch); holder != "" {
		return res, fmt.Errorf("%w: %s is checked out in %s; the merge would leave that checkout behind its branch. Switch it off the branch and close again", ErrDeliveryMergeRefused, branch, holder)
	}
	head, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return res, fmt.Errorf("resolve HEAD: %w", err)
	}

	ref := "refs/heads/" + branch
	tip, _ := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref)
	var next string
	switch {
	case tip == "":
		// The first sub-issue to finish creates the line at its own tip.
		res.Commits, err = listCommits(gitRoot, head, "--not", "--remotes")
		if err != nil {
			return res, err
		}
		if out, err := runGit(gitRoot, "update-ref", ref, head, strings.Repeat("0", len(head))); err != nil {
			return res, fmt.Errorf("create %s: %s: %w", branch, strings.TrimSpace(out), err)
		}
		next = head
	case isAncestor(gitRoot, head, tip):
		// Already on the line: a re-close, or a run that made no commits.
		res.Status = DeliveryMerged
		res.Tip = tip
		return res, nil
	default:
		res.Commits, err = listCommits(gitRoot, head, "^"+tip)
		if err != nil {
			return res, err
		}
		if isAncestor(gitRoot, tip, head) {
			next = head
		} else {
			tree, files, mergeErr := mergeTree(gitRoot, tip, head)
			if mergeErr != nil {
				return res, mergeErr
			}
			if len(files) > 0 {
				res.Status = DeliveryConflict
				res.Commits = []DeliveryMergeCommit{}
				res.ConflictFiles = files
				return res, nil
			}
			msg := fmt.Sprintf("Merge %s into %s", source, branch)
			args := append(commitIdentityArgs(gitRoot), "commit-tree", tree, "-p", tip, "-p", head, "-m", msg)
			if next, err = runGitTrimmed(gitRoot, args...); err != nil {
				return res, fmt.Errorf("git commit-tree: %w", err)
			}
		}
		// Compare-and-swap: a sibling that merged in between makes this fail,
		// and the caller retries against the new tip.
		if out, err := runGit(gitRoot, "update-ref", ref, next, tip); err != nil {
			return res, fmt.Errorf("move %s: %s: %w (another sub-issue may have merged at the same moment; close again)", branch, strings.TrimSpace(out), err)
		}
	}
	res.Status = DeliveryMerged
	res.Tip = next
	recordDeliveryLine(gitRoot, source, branch, next, p)
	return res, nil
}

// recordDeliveryLine writes the branch record that lets the parent's run
// continue the delivery branch (DENE-820's sameDeliveryLine: workspace and
// conversation). The user snapshot is the one the sub-issue's branch carries,
// so the next fork replays only what the user changed since. Best-effort: a
// missing record only means the parent's run forks a branch of its own.
func recordDeliveryLine(gitRoot, source, branch, tip string, p DeliveryMergeParams) {
	owner := branchOwner{WorkspaceID: p.WorkspaceID, AgentID: "multica-delivery", ConversationID: p.OwnerIssueID}
	state, userHead := "", ""
	if ref, err := readUserStateRef(gitRoot, source); err == nil && ref != "" {
		if rec, err := readBranchRecord(gitRoot, ref); err == nil {
			state, userHead = rec.state, rec.userHead
			if rec.owner.WorkspaceID != "" {
				owner.WorkspaceID = rec.owner.WorkspaceID
			}
			if rec.owner.AgentID != "" {
				owner.AgentID = rec.owner.AgentID
			}
		}
	}
	if !owner.valid() {
		return
	}
	if state == "" {
		// No snapshot to carry: record the user's clean HEAD, so the next
		// fork replays their uncommitted edits in full.
		head, err := runGitTrimmed(mainWorktree(gitRoot), "rev-parse", "--verify", "HEAD")
		if err != nil {
			return
		}
		state, userHead = head, head
	}
	_, _ = writeBranchRecord(gitRoot, branch, state, tip, owner, userHead, "")
}

// mainWorktree is the repository's own checkout — the user's directory —
// rather than the task worktree dir belongs to.
func mainWorktree(dir string) string {
	out, err := runGitStdout(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return dir
	}
	first, _, _ := strings.Cut(out, "\n")
	if path := strings.TrimPrefix(first, "worktree "); path != first && path != "" {
		return path
	}
	return dir
}

// mergeTree merges two commits without a checkout. A conflict returns the
// conflicting files and no tree.
func mergeTree(dir, ours, theirs string) (string, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", ours, theirs)
	raw, err := cmd.Output()
	out := string(raw)
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return "", nil, fmt.Errorf("git merge-tree: %w", withGitStderr(err))
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	if err == nil {
		return strings.TrimSpace(fields[0]), nil, nil
	}
	// Exit status 1 is a conflict; the tree id comes first, then the files.
	if out == "" || len(fields) < 2 {
		return "", nil, fmt.Errorf("git merge-tree: %w", err)
	}
	seen := map[string]bool{}
	files := []string{}
	for _, f := range fields[1:] {
		if f == "" {
			// --name-only -z ends the file list with an empty field; what
			// follows is informational messages, suppressed above.
			break
		}
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return "", nil, fmt.Errorf("git merge-tree: %w", err)
	}
	return "", files, nil
}

func listCommits(dir, head string, exclude ...string) ([]DeliveryMergeCommit, error) {
	args := append([]string{"log", "--reverse", "--no-merges", "--format=%H%x1f%s", head}, exclude...)
	out, err := runGitStdout(dir, args...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	commits := []DeliveryMergeCommit{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, subject, ok := strings.Cut(line, "\x1f")
		if !ok {
			continue
		}
		commits = append(commits, DeliveryMergeCommit{SHA: sha, Subject: subject})
	}
	if len(commits) > maxDeliveryMergeCommits {
		commits = commits[len(commits)-maxDeliveryMergeCommits:]
	}
	return commits, nil
}

// uncommittedWork lists changed paths, leaving out Multica's own sidecars and
// the instruction files the runtime injects into every checkout.
func uncommittedWork(dir string) ([]string, error) {
	out, err := runGitStdout(dir, "status", "--porcelain", "-z", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	var paths []string
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		path := e[3:]
		if e[0] == 'R' || e[0] == 'C' {
			i++ // the rename source follows
		}
		if isMulticaSidecarPath(path) || isInjectedInstructionFile(path) {
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func isInjectedInstructionFile(path string) bool {
	switch filepath.Base(path) {
	case "CLAUDE.md", "AGENTS.md", "GEMINI.md":
		return true
	}
	return false
}

// worktreeHolding returns the path of a worktree that has branch checked out.
func worktreeHolding(gitRoot, branch string) string {
	out, err := runGitStdout(gitRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	path := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return path
		}
	}
	return ""
}

func clip(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(append([]string{}, in[:n]...), fmt.Sprintf("…%d more", len(in)-n))
}
