package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// taskBranchPrefix is the namespace every branch Multica names for a task lives
// under (agent/<seat>/<issue>). Only these are ever swept: a branch outside it
// is the user's, whatever it contains.
const taskBranchPrefix = "agent/"

// sweepBudget bounds one sweep. A repository that has never been swept can hold
// hundreds of task branches, and each unmerged one costs a few git calls to
// rule out; the sweep is housekeeping on the way into a task, so it takes what
// it can inside the budget and the next task continues from there.
const sweepBudget = 20 * time.Second

// sweepManifestDir holds the manifest of every branch the sweep deleted, in the same
// place and shape as the refresh-project prune script's manifest, so a branch
// removed by mistake can be found again with `git branch <name> <sha>` before
// git gc takes the commits.
const sweepManifestDir = "agent-branch-prune"

// SweepMergedTaskBranches deletes local task branches whose work is already in
// trunk (DENE-1666).
//
// Finalize removes a task's worktree but keeps its branch on purpose: the
// branch is the deliverable, and at that moment nothing is merged yet. After
// the pull request lands, nothing ever came back for the branch, so a repo
// that runs many tasks grew one stale branch per task without bound. This is
// the "coming back": it runs under the repository lock whenever a task prepares
// in the repo, and again when cleanup reclaims a preserved copy.
//
// The verdict is GitWorktreeProbe.MergeEvidenceOf — ancestor, identical tree,
// or squash patch-equivalence — the same judgement the refresh-project script
// `prune-agent-branches.py` applies. The script also accepts "same-named PR
// merged" and "ticket closed", which need the network or the platform; the
// daemon does not, so it only ever deletes a subset of what the script would.
//
// Kept, always:
//   - a branch with no merge evidence (carries work found nowhere else, which
//     includes a failed run's partial commits);
//   - a branch checked out in any worktree (a running task, or a preserved
//     copy);
//   - a branch with an interrupted or superseded ref: those hold uncommitted
//     or replaced work that is NOT in the branch, and they are pruned together
//     with it.
//
// Best effort and never fatal: the caller is preparing a task, not cleaning.
// Returns the deleted branch names.
func SweepMergedTaskBranches(gitRoot string, trunks []string, logger *slog.Logger) []string {
	if strings.TrimSpace(gitRoot) == "" {
		return nil
	}
	resolved := resolveSweepTrunks(gitRoot, trunks)
	if len(resolved) == 0 {
		return nil
	}
	list, err := runGitTrimmed(gitRoot, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/"+taskBranchPrefix)
	if err != nil || list == "" {
		return nil
	}
	busy := checkedOutBranches(gitRoot)
	deadline := time.Now().Add(sweepBudget)

	var deleted []string
	for _, line := range strings.Split(list, "\n") {
		branch, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || branch == "" || busy[branch] {
			continue
		}
		if time.Now().After(deadline) {
			break
		}
		if !deleteIfMerged(gitRoot, branch, sha, resolved, logger) {
			continue
		}
		deleted = append(deleted, branch)
	}
	if len(deleted) > 0 && logger != nil {
		logger.Info("execenv: swept task branches already merged into trunk",
			"git_root", gitRoot, "deleted", len(deleted), "trunks", resolved)
	}
	return deleted
}

// SweepMergedTaskBranch is the single-branch form, for cleanup reclaiming a
// preserved copy: the copy is gone, so its branch is judged by the same rule.
func SweepMergedTaskBranch(gitRoot, branch string, trunks []string, logger *slog.Logger) bool {
	if !strings.HasPrefix(branch, taskBranchPrefix) || strings.TrimSpace(gitRoot) == "" {
		return false
	}
	if checkedOutBranches(gitRoot)[branch] {
		return false
	}
	sha, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil || sha == "" {
		return false
	}
	return deleteIfMerged(gitRoot, branch, sha, resolveSweepTrunks(gitRoot, trunks), logger)
}

// deleteIfMerged is the verdict and the delete for one branch the caller has
// already established is not checked out.
func deleteIfMerged(gitRoot, branch, sha string, trunks []string, logger *slog.Logger) bool {
	if holdsUnrecordedWork(gitRoot, branch) {
		return false
	}
	evidence := MergeEvidenceNone
	for _, trunk := range trunks {
		if found, err := (GitWorktreeProbe{}).MergeEvidenceOf(gitRoot, branch, trunk); err == nil && found != MergeEvidenceNone {
			evidence = found
			break
		}
	}
	if evidence == MergeEvidenceNone {
		return false
	}
	// Recorded BEFORE the delete: a manifest missing a branch that is gone is
	// the failure to avoid; a line for a branch that survived is only noise.
	recordSweptBranch(gitRoot, branch, sha, evidence, logger)
	if out, err := runGit(gitRoot, "branch", "-D", "--", branch); err != nil {
		if logger != nil {
			logger.Warn("execenv: could not delete merged task branch (non-fatal)",
				"branch", branch, "output", strings.TrimSpace(out), "error", err)
		}
		return false
	}
	dropBranchRefs(gitRoot, branch, logger)
	return true
}

// resolveSweepTrunks is the set of refs a branch must be delivered into to be
// swept: the trunks the caller names (the machine's configured integration
// line) plus the repository's default branch, and nothing else.
//
// Deliberately NOT the branch the user happens to have checked out. A branch
// is "delivered" when it is in the line the team integrates into; the user's
// checkout can be any feature branch, including one that was cut from an agent
// branch's unmerged work, and treating it as trunk would delete exactly the
// partial work the sweep exists to keep. When neither a named trunk nor a
// default resolves, nothing is swept — unknown is not permission.
func resolveSweepTrunks(gitRoot string, given []string) []string {
	candidates := append([]string{}, given...)
	if def, err := (GitWorktreeProbe{}).DefaultBranch(gitRoot); err == nil && def != "" {
		candidates = append(candidates, def)
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := runGit(gitRoot, "rev-parse", "--verify", "--quiet", c+"^{commit}"); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// checkedOutBranches is every branch some worktree of the repo (main one
// included) currently has checked out.
func checkedOutBranches(gitRoot string) map[string]bool {
	busy := map[string]bool{}
	out, err := runGit(gitRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return busy
	}
	for _, line := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			busy[strings.TrimSpace(ref)] = true
		}
	}
	return busy
}

// holdsUnrecordedWork reports a branch with refs that keep work the branch
// itself does not contain. See SweepMergedTaskBranches.
func holdsUnrecordedWork(gitRoot, branch string) bool {
	for _, prefix := range []string{localInterruptedRefPrefix, localSupersededRefPrefix} {
		if sha, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", prefix+branch); err == nil && sha != "" {
			return true
		}
	}
	return false
}

// dropBranchRefs is dropBranch minus the `branch -D` the sweep already did.
func dropBranchRefs(gitRoot, branch string, logger *slog.Logger) {
	if out, err := runGit(gitRoot, "update-ref", "-d", userStateRef(branch)); err != nil && logger != nil {
		logger.Debug("execenv: no local-directory snapshot to drop for swept branch",
			"branch", branch, "output", strings.TrimSpace(out))
	}
	clearReplayAttempt(gitRoot, branch, logger)
}

func recordSweptBranch(gitRoot, branch, sha string, evidence WorktreeMergeEvidence, logger *slog.Logger) {
	common, err := runGitTrimmed(gitRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		return
	}
	dir := filepath.Join(common, sweepManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "daemon.tsv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		if logger != nil {
			logger.Debug("execenv: could not write the swept-branch manifest", "error", err)
		}
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\tlocal\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), branch, sha, evidence)
}
