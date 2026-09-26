package execenv

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// The task branch is the delivery contract of a worktree run; where the agent
// left HEAD is only evidence of what it built. Finalize used to demand that the
// two were the same commit and failed the run otherwise — so an agent that ran
// `git checkout -b` somewhere else, typically onto the latest origin because
// its baseline was stale, got a failed run even when nothing was lost
// (DENE-365 / 240 / 383 / 871). Finalize now sorts HEAD into one of the classes
// below, and each class has exactly one action (DENE-874).

// deliveryClass is what the end state of a worktree run means.
type deliveryClass int

const (
	// deliveryOnBranch: the work is on the task branch. Recorded as is.
	deliveryOnBranch deliveryClass = iota
	// deliveryReadOnly: the run made no commit of its own anywhere. Nothing is
	// recorded; a branch this turn created is dropped, a continued one is put
	// back where the turn found it.
	deliveryReadOnly
	// deliveryRenamed: HEAD carries this turn's start plus the agent's work,
	// under another name. The task branch is moved to HEAD.
	deliveryRenamed
	// deliveryRebased: HEAD carries the agent's work on a different base (it
	// restarted from origin). The task branch is moved to HEAD and recorded as
	// not carrying the local-directory snapshot, so the next turn replays it.
	deliveryRebased
	// deliveryStuck: nothing above can be proven safe. The worktree is kept.
	deliveryStuck
)

func (c deliveryClass) String() string {
	switch c {
	case deliveryOnBranch:
		return "on_branch"
	case deliveryReadOnly:
		return "read_only"
	case deliveryRenamed:
		return "renamed"
	case deliveryRebased:
		return "rebased"
	default:
		return "stuck"
	}
}

// deliveryFacts is everything the classification needs, already answered by
// git. Keeping the questions here and the decision in classifyDelivery is what
// lets the decision table be tested without a repository.
//
// "Own commits" are commits this run made: reachable from the tip in question
// but not from the turn's start, the user's HEAD, or any remote-tracking ref.
// It is a set question, never a question about branch names.
type deliveryFacts struct {
	// Head is the worktree's HEAD after Finalize committed leftovers; "" when
	// it could not be resolved.
	Head string
	// Base is the commit this turn started from (LocalWorktree.BaseCommit).
	Base string
	// BranchTip is the task branch's tip now; "" when the branch is gone.
	BranchTip string
	// HeadContainsBase: Base is an ancestor of (or equal to) Head.
	HeadContainsBase bool
	// BranchContainsBase: Base is an ancestor of (or equal to) BranchTip.
	BranchContainsBase bool
	// HeadHasOwnCommits: Head carries commits this run made.
	HeadHasOwnCommits bool
	// BranchStrandsWork: the task branch carries own commits that Head does
	// not, so moving the branch to Head would drop them.
	BranchStrandsWork bool
}

// deliveryDecision is the classification plus the commit the task branch
// should end on. point is "" for a read-only run and for a stuck one.
type deliveryDecision struct {
	class  deliveryClass
	point  string
	reason string
}

// classifyDelivery is the decision table. Pure: it reads only its argument.
func classifyDelivery(f deliveryFacts) deliveryDecision {
	if f.Head == "" {
		return deliveryDecision{class: deliveryStuck, reason: "the task worktree has no resolvable HEAD"}
	}
	// The contract case, including a run that ended exactly where it started.
	if f.Head == f.BranchTip && f.HeadContainsBase {
		return deliveryDecision{class: deliveryOnBranch, point: f.Head}
	}
	// The agent committed on the task branch and then wandered off: the branch
	// still carries the work, and HEAD adds nothing.
	branchAdvanced := f.BranchTip != "" && f.BranchTip != f.Base && f.BranchContainsBase
	if !f.HeadHasOwnCommits {
		if branchAdvanced {
			return deliveryDecision{class: deliveryOnBranch, point: f.BranchTip}
		}
		// The agent reset the task branch elsewhere, committed on it, then
		// left: the branch is the rebased delivery, HEAD adds nothing.
		if f.BranchStrandsWork {
			return deliveryDecision{class: deliveryRebased, point: f.BranchTip}
		}
		return deliveryDecision{class: deliveryReadOnly}
	}
	// Both the branch and HEAD carry work the other lacks. Picking either one
	// drops the other, so a person has to merge them.
	if f.BranchStrandsWork {
		return deliveryDecision{class: deliveryStuck,
			reason: "the task branch and the worktree's HEAD each carry commits the other does not"}
	}
	if f.HeadContainsBase {
		return deliveryDecision{class: deliveryRenamed, point: f.Head}
	}
	return deliveryDecision{class: deliveryRebased, point: f.Head}
}

// gatherDeliveryFacts answers deliveryFacts for this worktree. Every git error
// resolves in the direction that keeps work: an unknown "own commits" counts
// as yes, which leads to delivering or keeping the worktree, never to a drop.
func (w *LocalWorktree) gatherDeliveryFacts(head, headBranch string) deliveryFacts {
	f := deliveryFacts{Head: head, Base: w.BaseCommit}
	if tip, err := runGitTrimmed(w.GitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+w.Branch); err == nil {
		f.BranchTip = tip
	}
	if head == "" {
		return f
	}
	f.HeadContainsBase = isAncestor(w.GitRoot, w.BaseCommit, head)
	f.BranchContainsBase = f.BranchTip != "" && isAncestor(w.GitRoot, w.BaseCommit, f.BranchTip)

	remotes := remoteTrackingRefs(w.GitRoot)
	known := w.knownCommits()
	// A branch the agent pushed is still its own work: its remote copy must
	// not make those commits look like someone else's.
	f.HeadHasOwnCommits = hasCommitsOutside(w.GitRoot, head,
		append(known, withoutPushedCopies(w.GitRoot, remotes, headBranch)...))
	if f.BranchTip != "" && f.BranchTip != head {
		f.BranchStrandsWork = hasCommitsOutside(w.GitRoot, f.BranchTip,
			append(append(known, head), withoutPushedCopies(w.GitRoot, remotes, w.Branch)...))
	}
	return f
}

// knownCommits are the commits a run started with: its own starting point and
// the user's HEAD, as captured and as carried from the previous turn.
func (w *LocalWorktree) knownCommits() []string {
	var out []string
	for _, c := range []string{w.BaseCommit, w.userHead, w.priorUserHead} {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func isAncestor(gitRoot, ancestor, commit string) bool {
	if ancestor == "" || commit == "" {
		return false
	}
	_, err := runGit(gitRoot, "merge-base", "--is-ancestor", ancestor, commit)
	return err == nil
}

// hasCommitsOutside reports whether tip reaches a commit none of excludes
// reaches. An error counts as yes.
func hasCommitsOutside(gitRoot, tip string, excludes []string) bool {
	args := []string{"rev-list", "--count", tip}
	if len(excludes) > 0 {
		args = append(args, "--not")
		args = append(args, excludes...)
	}
	out, err := runGitTrimmed(gitRoot, args...)
	if err != nil {
		return true
	}
	n, err := strconv.Atoi(out)
	return err != nil || n > 0
}

// remoteTrackingRefs lists refs/remotes/*, skipping the symbolic */HEAD.
func remoteTrackingRefs(gitRoot string) []string {
	out, err := runGitTrimmed(gitRoot, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return nil
	}
	var refs []string
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || strings.HasSuffix(ref, "/HEAD") {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// withoutPushedCopies drops the remote copies of a local branch — its
// upstream and any same-named branch on a remote.
func withoutPushedCopies(gitRoot string, remotes []string, branch string) []string {
	if branch == "" {
		return remotes
	}
	upstream, _ := runGitTrimmed(gitRoot, "rev-parse", "--symbolic-full-name", "refs/heads/"+branch+"@{upstream}")
	out := make([]string, 0, len(remotes))
	for _, ref := range remotes {
		if ref == upstream || strings.HasSuffix(ref, "/"+branch) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

// worktreeBranch names the branch the worktree has checked out, or "" for a
// detached HEAD.
func worktreeBranch(worktreePath string) string {
	name, err := runGitTrimmed(worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return name
}

// moveTaskBranch points the task branch at point, guarded by the tip it was
// read at so a concurrent move by the user is not overwritten.
func (w *LocalWorktree) moveTaskBranch(point, expected string) error {
	old := expected
	if old == "" {
		old = strings.Repeat("0", len(point)) // must not exist yet
	}
	out, err := runGit(w.GitRoot, "update-ref", "-m", "multica: deliver the run onto its task branch",
		"refs/heads/"+w.Branch, point, old)
	if err != nil {
		return fmt.Errorf("move %s to %s: %s: %w", w.Branch, shortID(point), strings.TrimSpace(out), err)
	}
	return nil
}

// supersededRef keeps the line a rebased delivery replaced. Outside refs/heads
// so it stays out of `git branch`; pruned with the branch like the state ref.
func supersededRef(branch string) string {
	return localSupersededRefPrefix + branch
}

// agentCreatedBranch reports whether branch was created after this worktree
// was prepared — the agent's own name for its work, safe to drop once that
// work is on the task branch. The reflog is the only witness; without one (or
// without a recorded prepare time) the branch is treated as the user's.
func (w *LocalWorktree) agentCreatedBranch(branch string) bool {
	if branch == "" || branch == w.Branch || w.PreparedAt == 0 {
		return false
	}
	out, err := runGitTrimmed(w.GitRoot, "reflog", "show", "--date=unix", "--format=%gd", "refs/heads/"+branch)
	if err != nil || out == "" {
		return false
	}
	lines := strings.Split(out, "\n")
	oldest := strings.TrimSpace(lines[len(lines)-1])
	open := strings.LastIndex(oldest, "@{")
	if open < 0 || !strings.HasSuffix(oldest, "}") {
		return false
	}
	created, err := strconv.ParseInt(oldest[open+2:len(oldest)-1], 10, 64)
	return err == nil && created > w.PreparedAt
}

// dropAgentBranch deletes the agent's own branch after its work reached the
// task branch. Only when it still points at the delivered commit: anything
// else would mean it holds something the task branch does not.
func (w *LocalWorktree) dropAgentBranch(branch, delivered string, logger *slog.Logger) {
	if !w.agentCreatedBranch(branch) {
		return
	}
	tip, err := runGitTrimmed(w.GitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil || tip != delivered {
		return
	}
	deleteBranch(w.GitRoot, branch, logger)
}

// stuckDeliveryError is the one failure left after classification. It says
// where the work is and how to keep it, not just which hashes disagree.
func (w *LocalWorktree) stuckDeliveryError(reason, head, headBranch string) error {
	where := "the worktree's HEAD"
	switch {
	case headBranch != "":
		where = fmt.Sprintf("branch %s", headBranch)
	case head != "":
		where = fmt.Sprintf("a detached HEAD at %s", shortID(head))
	}
	uncommitted := "nothing is left uncommitted"
	if dirty, err := worktreeIsDirty(w.Path); err != nil || dirty {
		uncommitted = "it still has uncommitted changes"
	}
	recover := fmt.Sprintf("git -C %q status", w.Path)
	if head != "" {
		recover = fmt.Sprintf("git -C %q branch %s-recovered %s", w.GitRoot, w.Branch, head)
	}
	return fmt.Errorf(
		"refusing to record branch %s: %s. The run's work is on %s in the task worktree at %s (%s; listed by `git worktree list` in %s). "+
			"Keep it with: %s — then merge it into %s yourself",
		w.Branch, reason, where, w.Path, uncommitted, w.GitRoot, recover, w.Branch)
}
