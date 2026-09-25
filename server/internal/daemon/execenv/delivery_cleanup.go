package execenv

import (
	"fmt"
	"log/slog"
	"strings"
)

// DeliveryCleanupResult reports what CleanupDeliveryBranch did to one
// non-canonical delivery line (DENE-820).
type DeliveryCleanupResult struct {
	Branch string
	// Evidence is the merge signal that allowed the removal, "" when the
	// branch was kept.
	Evidence WorktreeMergeEvidence
	// Removed lists the worktree paths unregistered from the repository.
	Removed []string
	// Kept is the reason nothing was touched; empty when the branch is gone.
	Kept string
}

// Cleaned reports whether the branch and its worktrees were removed.
func (r DeliveryCleanupResult) Cleaned() bool { return r.Kept == "" }

// CleanupDeliveryBranch removes a delivery line's local presence after its
// issue has been merged: every worktree checked out on the branch, the branch
// itself, and the Multica refs that pin its state.
//
// The gate is merge evidence against trunk: a branch whose work is not in
// trunk (ancestor or squash) is kept, with a reason, because the server's
// classification says "resolved" but only the repository knows whether the
// commits actually landed. A rescue line marked discarded is the one case where
// the server verdict must win, so the caller passes force=true for it.
//
// Uses git for every step (never `rm -rf`), like the rest of the package.
func CleanupDeliveryBranch(gitRoot, branch, trunk string, force bool, logger *slog.Logger) (DeliveryCleanupResult, error) {
	res := DeliveryCleanupResult{Branch: branch}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return res, fmt.Errorf("execenv: delivery cleanup: empty branch")
	}
	if strings.TrimSpace(trunk) == "" {
		return res, fmt.Errorf("execenv: delivery cleanup: empty trunk")
	}
	if _, err := runGit(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		// Nothing local to clean: refs may still linger, drop them and report
		// the branch as already gone.
		dropBranch(gitRoot, branch, logger)
		res.Kept = ""
		return res, nil
	}
	if !force {
		evidence, err := GitWorktreeProbe{}.MergeEvidenceOf(gitRoot, branch, trunk)
		if err != nil {
			return res, err
		}
		if evidence == MergeEvidenceNone {
			res.Kept = fmt.Sprintf("branch %s is not merged into %s; classify it as discarded to force removal", branch, trunk)
			return res, nil
		}
		res.Evidence = evidence
	}
	paths, err := worktreesOnBranch(gitRoot, branch)
	if err != nil {
		return res, err
	}
	for _, p := range paths {
		if err := forceRemoveWorktree(gitRoot, p); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, p)
	}
	if out, err := runGit(gitRoot, "branch", "-D", branch); err != nil {
		return res, fmt.Errorf("execenv: git branch -D %s: %s: %w", branch, strings.TrimSpace(out), err)
	}
	dropBranch(gitRoot, branch, logger) // refs only; the branch is already gone
	return res, nil
}

// worktreesOnBranch lists the paths of every registered worktree whose HEAD is
// the given branch, excluding the main working tree.
func worktreesOnBranch(gitRoot, branch string) ([]string, error) {
	out, err := runGit(gitRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("execenv: git worktree list: %s: %w", strings.TrimSpace(out), err)
	}
	var paths []string
	var current string
	first := true
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			if !first && ref == "refs/heads/"+branch && current != "" {
				paths = append(paths, current)
			}
		case line == "":
			if current != "" {
				first = false
			}
			current = ""
		}
	}
	return paths, nil
}

// RevParseQuiet reports whether ref resolves in gitRoot, returning its SHA.
func RevParseQuiet(gitRoot, ref string) (string, error) {
	return runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", ref)
}
