package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Automatic cleanup of parallel-mode working copies (DENE-617).
//
// Moving working copies onto the user's disk moved the disk cost with them.
// The copies that survive a run are the ones a run could NOT finish cleanly —
// an unresolved merge, a commit that failed, a daemon that died — and those
// are exactly the ones worth keeping around, because they are the only record
// of what went wrong. So this is not a GC that reclaims routine leftovers; it
// is a narrow rule for the subset that has demonstrably become worthless.
//
// It is OFF by default, and every rule below is a reason to KEEP. A copy is
// removed only when all five conditions hold; any one of them failing keeps
// it, and the UI shows which one. The asymmetry is the point: the cost of
// keeping a copy is disk, the cost of removing one wrongly is work the user
// cannot get back.
//
// Removal always goes through `git worktree remove`. An `rm -rf` would leave
// the repository's `.git/worktrees/<name>` metadata behind, and git then
// refuses to create a worktree at that name again — a cleanup that quietly
// breaks the next run is worse than no cleanup.

// DefaultWorktreeCleanupMinAgeDays is how long a finished working copy is kept
// before it can qualify. Two weeks: long enough that a run you meant to come
// back to on Monday is still there after a holiday.
const DefaultWorktreeCleanupMinAgeDays = 14

// WorktreeCleanupSettings is the machine-level cleanup policy.
//
// Machine-level, not per-resource: disk is a property of the computer, and a
// user reasoning about "am I running out of space" is reasoning about one
// number, not about each project's opinion.
type WorktreeCleanupSettings struct {
	// Enabled is false by default. When false, nothing is ever removed
	// automatically — but every candidate is still evaluated, so the user can
	// see what the policy WOULD do before agreeing to it.
	Enabled bool `json:"enabled"`
	// MinAgeDays is how long after its last run a copy becomes eligible.
	// Zero or negative reads as the default rather than "immediately":
	// an unset field must not mean the most destructive possible policy.
	MinAgeDays int `json:"min_age_days,omitempty"`
	// TrunkBranch is the branch a copy's branch must be merged into. Empty
	// means the repository's own default branch, resolved per repository.
	TrunkBranch string `json:"trunk_branch,omitempty"`
}

// MinAge returns the configured retention window as a duration.
func (s WorktreeCleanupSettings) MinAge() time.Duration {
	days := s.MinAgeDays
	if days <= 0 {
		days = DefaultWorktreeCleanupMinAgeDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// WorktreeKeepReason names the single condition that keeps a working copy.
// Empty means every condition is satisfied and the copy qualifies.
type WorktreeKeepReason string

const (
	// KeepNotMulticaCreated: no ownership record. Either the user made this
	// worktree themselves, or a daemon crashed between creating the copy and
	// recording it. Both resolve the same way — it is not ours to delete.
	KeepNotMulticaCreated WorktreeKeepReason = "not_multica_created"
	// KeepInUse: a task is running in it right now.
	KeepInUse WorktreeKeepReason = "in_use"
	// KeepTooRecent: its last run finished less than MinAgeDays ago.
	KeepTooRecent WorktreeKeepReason = "too_recent"
	// KeepUncommittedChanges: `git status --porcelain` is not empty. This is
	// the most important rule in the file: "the branch is merged" says nothing
	// about what is still sitting in the directory unrecorded, and a failed
	// run's leftovers live exactly there.
	KeepUncommittedChanges WorktreeKeepReason = "uncommitted_changes"
	// KeepBranchNotMerged: none of the delivery signals hold — the branch is
	// neither contained in trunk by ancestry nor by content — so it carries
	// work that is not anywhere else. See WorktreeMergeEvidence.
	KeepBranchNotMerged WorktreeKeepReason = "branch_not_merged"
	// KeepStatusUnknown: git could not be asked. Unknown is not permission.
	KeepStatusUnknown WorktreeKeepReason = "status_unknown"
)

// WorktreeMergeEvidence names HOW a branch was found to be delivered (DENE-647).
//
// The original rule asked one question — is the branch an ancestor of trunk —
// and that question has no true answer in a squash-merge repository: squashing
// rewrites the work as a new commit, so the branch's own commits are never
// ancestors of trunk and the condition fails forever. A cleanup that can never
// fire is the same as no cleanup, so "delivered" is now a set of signals.
//
// What is accepted, and what is deliberately not:
//
//   - Ancestry. The branch is literally contained in trunk. Unchanged.
//   - Content. The branch's net change against the merge base is already
//     applied in trunk — either the two trees are identical, or git's own
//     patch-id equivalence (`git cherry`) finds the change upstream. This is
//     what a squash merge leaves behind, and it needs nothing but the local
//     repository: no network, no GitHub, no forge account.
//   - NOT "the remote branch is gone". `git ls-remote` cannot distinguish a
//     branch deleted after merge from one that was never pushed, and the
//     second is exactly the branch whose work exists nowhere else. It is the
//     one signal here that could delete work, so it is not accepted.
//   - NOT "its pull request is merged". The most accurate answer, and the one
//     that stops working on an offline machine, a self-hosted forge, or any
//     remote that is not the one we wired up. Cleanup must not need a network
//     to be correct about a local disk.
//
// Both accepted signals can only be WRONG in the keep direction: a rebased or
// conflict-rewritten branch whose patch no longer matches reads as undelivered
// and stays on disk. That is the asymmetry the whole file is built on.
type WorktreeMergeEvidence string

const (
	// MergeEvidenceNone: no signal held; the branch's work is only here.
	MergeEvidenceNone WorktreeMergeEvidence = ""
	// MergeEvidenceAncestor: the branch is an ancestor of trunk.
	MergeEvidenceAncestor WorktreeMergeEvidence = "ancestor"
	// MergeEvidenceSquash: the branch's content is in trunk, but its commits
	// are not — the fingerprint of a squash (or rebase-and-merge) landing.
	MergeEvidenceSquash WorktreeMergeEvidence = "squash"
)

// WorktreeCandidate is one working copy as the scan found it. Every field the
// rules read is here, so the rules themselves touch no filesystem and no clock
// beyond the `now` they are handed.
type WorktreeCandidate struct {
	// Path is the copy's directory; GitRoot the repository it belongs to.
	Path    string `json:"path"`
	GitRoot string `json:"git_root"`
	// Branch is the branch checked out in it, "" on a detached HEAD.
	Branch string `json:"branch"`
	// Multica is true when an ownership record proves Multica created it.
	Multica bool `json:"multica_created"`
	// InUse is true when a task is running in it right now.
	InUse bool `json:"in_use"`
	// LastRunAt is when its last run finished. Zero when unknown, which the
	// age rule treats as "not old enough" rather than "infinitely old".
	LastRunAt time.Time `json:"last_run_at"`
	// Dirty reports uncommitted TRACKED changes plus untracked files that are
	// not gitignored. Gitignored files are excluded deliberately: node_modules
	// and build output are present in every copy, and counting them would make
	// this condition permanently true and the whole feature a no-op.
	Dirty bool `json:"dirty"`
	// Merged reports that the branch's work is in trunk by any accepted
	// signal. MergedVia says which one, so the screen can tell a user why a
	// branch whose commits are not in trunk still counts as delivered.
	Merged    bool                  `json:"merged"`
	MergedVia WorktreeMergeEvidence `json:"merged_via,omitempty"`
	// Unknown is set when git could not answer Dirty or Merged.
	Unknown bool `json:"unknown"`
	// SizeBytes is what removing it would reclaim, for the preview.
	SizeBytes int64 `json:"size_bytes"`
}

// EvaluateWorktreeCleanup decides whether one working copy qualifies for
// removal, and when it does not, which single condition stopped it.
//
// Pure: no filesystem, no clock, no git. Everything it reads was measured by
// the scan and is on the candidate, so the policy can be tested as a table and
// the preview shown to the user is computed by the same code that would do the
// removing.
//
// Order matters, because the user sees ONE reason. It runs cheapest-and-most
// absolute first: whose copy it is, then whether it is busy, then the two
// content rules that describe work at risk, then age — which is the only
// reason that resolves itself by waiting.
func EvaluateWorktreeCleanup(c WorktreeCandidate, s WorktreeCleanupSettings, now time.Time) WorktreeKeepReason {
	if !c.Multica {
		return KeepNotMulticaCreated
	}
	if c.InUse {
		return KeepInUse
	}
	if c.Unknown {
		return KeepStatusUnknown
	}
	if c.Dirty {
		return KeepUncommittedChanges
	}
	if !c.Merged {
		return KeepBranchNotMerged
	}
	if c.LastRunAt.IsZero() || now.Sub(c.LastRunAt) < s.MinAge() {
		return KeepTooRecent
	}
	return ""
}

// WorktreeCleanupItem is a candidate plus the verdict on it, which is what the
// settings screen lists: path, size, branch, last run, and why it will or will
// not be removed.
type WorktreeCleanupItem struct {
	WorktreeCandidate
	// KeepReason is "" when the copy qualifies for removal.
	KeepReason WorktreeKeepReason `json:"keep_reason,omitempty"`
}

// Eligible reports whether the policy would remove this copy. It says nothing
// about whether the policy is switched on — see WorktreeCleanupSettings.Enabled,
// which the caller checks separately so a disabled setting still produces a
// full preview (invariant 6).
func (i WorktreeCleanupItem) Eligible() bool { return i.KeepReason == "" }

// WorktreeGitProbe is the set of git questions the scan asks about one working
// copy. Injected so the rules and the scan can both be tested without building
// repositories on disk.
type WorktreeGitProbe interface {
	// Dirty reports uncommitted tracked changes or non-ignored untracked
	// files in the working copy.
	Dirty(worktreePath string) (bool, error)
	// MergeEvidenceOf reports which delivery signal holds for branch against
	// trunk in gitRoot, or MergeEvidenceNone when none does.
	MergeEvidenceOf(gitRoot, branch, trunk string) (WorktreeMergeEvidence, error)
	// CurrentBranch reports the branch checked out in the working copy.
	CurrentBranch(worktreePath string) (string, error)
	// DefaultBranch reports the repository's trunk when the settings name none.
	DefaultBranch(gitRoot string) (string, error)
}

// ScanWorktreeRoot lists the working copies under one worktree root and
// evaluates each against the policy.
//
// `inUse` holds the paths of copies a task is running in right now, which only
// the daemon knows. Comparison is on cleaned paths.
//
// A root that does not exist is not an error: a repository whose parallel mode
// has never run has no root, and reporting that as a failure would put an
// error in front of a user who has nothing wrong with their setup.
func ScanWorktreeRoot(root string, settings WorktreeCleanupSettings, inUse map[string]bool, probe WorktreeGitProbe, now time.Time) ([]WorktreeCleanupItem, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("execenv: read worktree root %q: %w", root, err)
	}
	var items []WorktreeCleanupItem
	for _, e := range entries {
		if !e.IsDir() || e.Name() == worktreeRecordDirName {
			continue
		}
		path := filepath.Join(root, e.Name())
		items = append(items, inspectWorktree(root, path, settings, inUse, probe, now))
	}
	sort.Slice(items, func(a, b int) bool { return items[a].Path < items[b].Path })
	return items, nil
}

// inspectWorktree measures one working copy and applies the policy to it.
func inspectWorktree(root, path string, settings WorktreeCleanupSettings, inUse map[string]bool, probe WorktreeGitProbe, now time.Time) WorktreeCleanupItem {
	c := WorktreeCandidate{Path: path, InUse: inUse[filepath.Clean(path)]}

	rec, err := readWorktreeRecord(root, path)
	if err == nil && rec != nil {
		c.Multica = true
		c.GitRoot = rec.GitRoot
		c.Branch = rec.Branch
		c.LastRunAt = rec.LastRunAt
		if c.LastRunAt.IsZero() {
			c.LastRunAt = rec.CreatedAt
		}
	}
	c.SizeBytes = directorySize(path)

	// Everything below asks git, and every one of those questions is only
	// meaningful for a copy we would otherwise be willing to remove. Asking
	// them about a busy copy, or one that is not ours, spends a subprocess to
	// produce an answer that cannot change the verdict.
	if !c.Multica || c.InUse || probe == nil {
		return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
	}

	dirty, dirtyErr := probe.Dirty(path)
	if dirtyErr != nil {
		c.Unknown = true
		return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
	}
	c.Dirty = dirty

	if strings.TrimSpace(c.Branch) == "" {
		if branch, branchErr := probe.CurrentBranch(path); branchErr == nil {
			c.Branch = branch
		}
	}
	if strings.TrimSpace(c.Branch) == "" || strings.TrimSpace(c.GitRoot) == "" {
		// Nothing to compare against trunk. Not knowing whether the work is
		// anywhere else is a reason to keep it.
		c.Unknown = true
		return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
	}

	trunk := strings.TrimSpace(settings.TrunkBranch)
	if trunk == "" {
		resolved, trunkErr := probe.DefaultBranch(c.GitRoot)
		if trunkErr != nil || strings.TrimSpace(resolved) == "" {
			c.Unknown = true
			return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
		}
		trunk = resolved
	}
	evidence, mergedErr := probe.MergeEvidenceOf(c.GitRoot, c.Branch, trunk)
	if mergedErr != nil {
		c.Unknown = true
		return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
	}
	c.MergedVia = evidence
	c.Merged = evidence != MergeEvidenceNone

	return WorktreeCleanupItem{WorktreeCandidate: c, KeepReason: EvaluateWorktreeCleanup(c, settings, now)}
}

// directorySize sums the copy's files so the preview can say what removing it
// reclaims. Best effort: a size that could not be measured reads as 0 rather
// than failing the scan, because an unknown size is not a reason to hide a
// candidate from the user.
func directorySize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // unreadable subtrees are skipped, not fatal
		}
		if info, statErr := d.Info(); statErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// RemoveCleanableWorktree removes one working copy, re-checking the verdict
// against the CURRENT state of the directory first.
//
// The re-check is not redundant. A preview is a snapshot; between rendering it
// and the user clicking, a run can start in the copy or the user can open it
// and start editing. Acting on the stale verdict is how a cleanup deletes
// work that was not there when the list was drawn.
func RemoveCleanableWorktree(root, path string, settings WorktreeCleanupSettings, inUse map[string]bool, probe WorktreeGitProbe, now time.Time) error {
	item := inspectWorktree(root, filepath.Clean(path), settings, inUse, probe, now)
	if !item.Eligible() {
		return fmt.Errorf("execenv: refusing to remove %q: %s", path, item.KeepReason)
	}
	return forceRemoveWorktree(item.GitRoot, item.Path)
}

// forceRemoveWorktree unregisters the copy from its repository and deletes it.
//
// Through git, never `rm -rf`: removing the directory alone leaves
// `.git/worktrees/<name>` behind, and git then refuses to create a worktree at
// that name again. The prune is what makes this true even when `remove` itself
// failed partway.
func forceRemoveWorktree(gitRoot, path string) error {
	if strings.TrimSpace(gitRoot) == "" {
		return fmt.Errorf("execenv: cannot remove %q: its repository is unknown", path)
	}
	if out, err := runGit(gitRoot, "worktree", "remove", "--force", path); err != nil {
		return fmt.Errorf("execenv: git worktree remove %q: %s: %w", path, strings.TrimSpace(out), err)
	}
	if _, err := runGit(gitRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("execenv: git worktree prune in %q after removing %q: %w", gitRoot, path, err)
	}
	removeWorktreeRecord(filepath.Dir(filepath.Clean(path)), path)
	return nil
}

// GitWorktreeProbe answers the cleanup questions with the git binary. It is
// the production WorktreeGitProbe; tests substitute their own.
type GitWorktreeProbe struct{}

// Dirty reports uncommitted tracked changes or untracked-and-not-ignored files.
//
// `--porcelain` without `--ignored` is exactly the rule condition 4 needs:
// gitignored content (node_modules, build output) is excluded, so a copy that
// merely has dependencies installed still counts as clean, while a single
// unsaved edit or a stray new file keeps it forever.
func (GitWorktreeProbe) Dirty(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status in %q: %s: %w", worktreePath, strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out) != "", nil
}

// MergeEvidenceOf reports how — if at all — the branch's work is already in
// trunk. See WorktreeMergeEvidence for which signals are accepted and why.
//
// Cheapest and most absolute first: ancestry, then identical trees, then
// patch equivalence. An error is returned only when trunk itself cannot be
// resolved, because a wrong trunk makes every answer below meaningless; a
// question git declines to answer reads as MergeEvidenceNone, which keeps the
// copy.
func (GitWorktreeProbe) MergeEvidenceOf(gitRoot, branch, trunk string) (WorktreeMergeEvidence, error) {
	if _, err := runGit(gitRoot, "rev-parse", "--verify", trunk); err != nil {
		return MergeEvidenceNone, fmt.Errorf("git: %q has no branch %q to compare against", gitRoot, trunk)
	}
	// `merge-base --is-ancestor` exits 0 for yes and 1 for no, so a non-zero
	// exit is not distinguishable here from a real failure — and it does not
	// need to be: both answers just mean the next signal gets its turn.
	if _, err := runGit(gitRoot, "merge-base", "--is-ancestor", branch, trunk); err == nil {
		return MergeEvidenceAncestor, nil
	}
	return squashMergeEvidence(gitRoot, branch, trunk)
}

// squashMergeEvidence answers the question a squash-merge repository actually
// poses: the branch's commits are gone from trunk's history, so is its WORK
// there?
//
// Two ways to be sure, both local:
//
//  1. `git diff --quiet trunk branch` — the two trees are identical, so there
//     is by definition nothing on the branch that trunk does not have.
//  2. The branch's net change against the merge base, replayed as one
//     synthetic commit, is patch-equivalent to something already in trunk.
//     That synthetic commit is exactly what a squash merge produces, so
//     git's own patch-id matching (`git cherry`, the "-" prefix) finds it.
//
// Step 2 is best effort by construction: if trunk changed the same lines after
// the squash landed, the patch ids no longer match and the answer is "no
// evidence". That is the safe direction — the copy stays on disk.
func squashMergeEvidence(gitRoot, branch, trunk string) (WorktreeMergeEvidence, error) {
	if _, err := runGit(gitRoot, "diff", "--quiet", trunk, branch); err == nil {
		return MergeEvidenceSquash, nil
	}
	base, err := runGitTrimmed(gitRoot, "merge-base", trunk, branch)
	if err != nil || base == "" {
		// No common ancestor: unrelated histories, nothing to replay against.
		return MergeEvidenceNone, nil //nolint:nilerr // no answer is not an error, it is a keep
	}
	tree, err := runGitTrimmed(gitRoot, "rev-parse", branch+"^{tree}")
	if err != nil || tree == "" {
		return MergeEvidenceNone, nil //nolint:nilerr // same: unanswerable means keep
	}
	// The synthetic commit is written to the object database and referenced by
	// nothing, so git's own gc collects it. Identity comes from the command
	// rather than the machine's git config: a user with no `user.email` set
	// must not turn cleanup into a permanent "unknown".
	ident := []string{
		"GIT_AUTHOR_NAME=multica", "GIT_AUTHOR_EMAIL=multica@localhost",
		"GIT_COMMITTER_NAME=multica", "GIT_COMMITTER_EMAIL=multica@localhost",
	}
	synthetic, err := runGitTrimmedEnv(gitRoot, ident, "commit-tree", tree, "-p", base, "-m", "multica cleanup squash probe")
	if err != nil || synthetic == "" {
		return MergeEvidenceNone, nil //nolint:nilerr // same: unanswerable means keep
	}
	out, err := runGitTrimmed(gitRoot, "cherry", trunk, synthetic)
	if err != nil {
		return MergeEvidenceNone, nil //nolint:nilerr // same: unanswerable means keep
	}
	if strings.HasPrefix(strings.TrimSpace(out), "-") {
		return MergeEvidenceSquash, nil
	}
	return MergeEvidenceNone, nil
}

// CurrentBranch reports the branch checked out in a working copy, or "" on a
// detached HEAD.
func (GitWorktreeProbe) CurrentBranch(worktreePath string) (string, error) {
	out, err := runGitTrimmed(worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if out == "HEAD" {
		return "", nil
	}
	return out, nil
}

// DefaultBranch resolves a repository's trunk when the settings name none.
//
// `origin/HEAD` is the authoritative answer when the repository has a remote;
// without one, the common local names are tried in order. Failing to resolve
// is reported rather than guessed: a wrong trunk makes "merged" mean nothing,
// and "merged" is the condition standing between a branch and deletion.
func (GitWorktreeProbe) DefaultBranch(gitRoot string) (string, error) {
	if out, err := runGitTrimmed(gitRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && out != "" {
		return out, nil
	}
	for _, candidate := range []string{"main", "master", "trunk"} {
		if _, err := runGit(gitRoot, "rev-parse", "--verify", candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("git: could not determine the default branch of %q; set one in the cleanup settings", gitRoot)
}
