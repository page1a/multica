package execenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/sparsecheckout"
)

// Local worktree mode gives every task on a local_directory resource its own
// git worktree of the user's repo, created inside the daemon-owned env root.
// Tasks on the same directory then run concurrently instead of queueing on the
// per-path mutex, and each one delivers its work as a branch in the user's own
// repo — discoverable with `git branch`, no new result channel needed.
//
// Three properties this file exists to guarantee:
//
//  1. The agent sees what the user sees. `git worktree add` alone would check
//     out HEAD, silently hiding the user's uncommitted work. We replay a
//     snapshot of their directory into the worktree instead — tracked edits and
//     untracked files as one tree (captureUserSnapshot).
//  2. The user's directory is never written to. Everything — including the
//     sidecar context files Prepare writes — lands inside the worktree, which
//     is disposable. What lasts in the user's repo is the branch, plus one
//     hidden ref per branch recording who owns it and what it carries.
//  3. Nothing is silently discarded on the first try. Whatever the agent leaves
//     uncommitted is committed to the branch before the worktree goes away, and
//     an edit of the user's that could not be merged is offered again next
//     turn. The same conflict a second time is skipped and named in the prompt,
//     so a replay that cannot be merged cannot block every later run.

const (
	// gitTimeout bounds every local git invocation this file makes. The one
	// network-capable baseline fetch uses localBaselineFetchTimeout below.
	gitTimeout = 2 * time.Minute
	// localBaselineFetchTimeout is deliberately shorter than gitRootLockWait:
	// a remote that stops responding must not hold the per-repository lock until
	// sibling tasks have all timed out waiting for it.
	localBaselineFetchTimeout = 45 * time.Second

	// maxUntrackedFiles / maxUntrackedBytes bound the untracked content a task
	// will replay. `--exclude-standard` already drops anything gitignored
	// (node_modules, build output, venvs), so a repo hitting these limits has an
	// unusual amount of untracked-but-not-ignored content, and snapshotting it
	// would write every byte of it into the user's own object database. The
	// task refuses instead, naming the fix.
	maxUntrackedFiles = 2000
	maxUntrackedBytes = 200 << 20 // 200 MiB

	// snapshotIndexFileName is the private index captureUserSnapshot builds the
	// user's snapshot in. It lives in the task's env root, never in the user's
	// repository: pointing GIT_INDEX_FILE at our own file is what keeps the
	// capture off the user's index entirely — no writes to it, and no wait on
	// .git/index.lock, which used to be able to end the task (#7434).
	snapshotIndexFileName = ".multica-snapshot-index"

	// localStateRefPrefix namespaces the per-branch record of the user's
	// directory. Outside refs/heads so it never appears in the user's
	// `git branch`, and a ref rather than a loose object so `git gc` in their
	// repo cannot reclaim a snapshot between two turns.
	localStateRefPrefix = "refs/multica/local-state/"
	// localReplayAttemptRefPrefix remembers a replay that conflicted, keyed by
	// the branch. It is not the snapshot record: a conflict must not advance
	// that record, and without a separate mark the next turn would retry the
	// same replay forever (DENE-814).
	localReplayAttemptRefPrefix = "refs/multica/local-replay-attempt/"

	// snapshotCommitTitle is the subject of the commit captureUserSnapshot
	// writes. The user's HEAD at that moment is its parent. Records written
	// before Multica-User-Head existed are read by walking to this commit.
	snapshotCommitTitle = "multica: local directory snapshot"
)

// LocalWorktreeParams describes the worktree Prepare should build for a
// local_directory task running in worktree mode.
type LocalWorktreeParams struct {
	// LocalPath is the user's configured directory. It may be the repo root
	// or any subdirectory of it; the worktree always covers the whole repo,
	// and the agent's cwd is the matching subdirectory inside it.
	LocalPath string
	// EnvRoot is the daemon-owned task env root. The snapshot index and the
	// task's own sidecar files live here; the working copy no longer does —
	// see WorktreeRoot.
	EnvRoot string
	// WorktreeRoot is where the working copy goes. Empty means the default:
	// the repository's sibling `<repo>.multica-worktrees` (DefaultWorktreeRoot).
	//
	// It is on the USER's disk, not in the Multica workspace, because a copy
	// the workspace GC can reclaim is a copy that disappears exactly when
	// someone wants to look at why a run failed (DENE-617).
	WorktreeRoot string
	// AgentName and TaskID name the branch a task with no conversation behind
	// it gets: agent/<name>/<short-task-id>.
	AgentName string
	TaskID    string
	// ConversationKey names the work line this task belongs to — its issue, or
	// its chat session. Tasks sharing a key share one branch, so the second
	// turn of a conversation continues the first turn's work instead of
	// forking from HEAD again (MUL-6881). Empty for a task with no durable
	// conversation behind it; those keep the task-scoped branch.
	//
	// It is a DISPLAY key: `mul-6881` is what the user recognises in
	// `git branch`, and two workspaces can produce the same one. Continuing a
	// branch is therefore never decided by the name — see WorkspaceID /
	// AgentID / ConversationID, which are what a branch's recorded owner is
	// compared against.
	ConversationKey string
	// WorkspaceID, AgentID and ConversationID identify the conversation
	// itself. They are recorded with the branch and re-checked before any
	// later task continues it, so a same-named branch belonging to the user,
	// to another agent, or to another workspace is never adopted. All three
	// empty means "no conversation": the task gets a task-scoped branch.
	WorkspaceID    string
	AgentID        string
	ConversationID string
	// CheckoutPaths is the task's checkout_paths declaration. Empty checks
	// out the whole repository. A declaration narrows only this task's
	// worktree; the user's own checkout stays complete.
	CheckoutPaths string
	// CanonicalBranch is the issue's canonical delivery branch as the server
	// records it (DENE-820). When it exists in this repository and its record
	// belongs to the same workspace and conversation, the task continues it
	// even though another seat created it — a rerun by a different agent
	// must land on the issue's one delivery line, not open a second one
	// named after itself. Empty, or absent locally, falls back to the
	// conversation branch rules below.
	CanonicalBranch string
	// ResumeWorkDir is the previous run's agent cwd on a same-seat retry.
	// When it names a working copy this repository can recreate — a direct
	// child of the worktree root that is not on disk — Prepare builds this
	// turn's worktree there, so a CLI that stores its conversation under the
	// working directory finds the interrupted session. Anything else is
	// ignored and a fresh directory is created. The daemon sets this only for
	// a same-seat continuation; a different seat leaves it empty.
	ResumeWorkDir string
}

// owner is the identity a branch created for this task is recorded under.
func (p LocalWorktreeParams) owner() branchOwner {
	return branchOwner{WorkspaceID: p.WorkspaceID, AgentID: p.AgentID, ConversationID: p.ConversationID}
}

// localWorktreeConversation names the work line a worktree task belongs to, so
// every turn of it delivers onto one branch (MUL-6881).
//
// Two values, because they answer different questions. The key is what the
// branch is CALLED — the issue identifier is preferred there because it is what
// the user recognises in `git branch`, agent/j/mul-6881 rather than a uuid
// tail. The id is what the branch BELONGS to, and only it decides whether a
// later task may continue that branch: identifiers are per-workspace and
// human-chosen, so two workspaces can mint the same one for different issues.
// Tasks with neither an issue nor a chat session have no conversation to
// continue and get "", "".
func localWorktreeConversation(params PrepareParams) (key, id string) {
	if params.Task.IssueID != "" {
		if params.IssueIdentifier != "" {
			return params.IssueIdentifier, params.Task.IssueID
		}
		return taskKey(params.Task.IssueID), params.Task.IssueID
	}
	if params.Task.ChatSessionID != "" {
		return "chat-" + taskKey(params.Task.ChatSessionID), params.Task.ChatSessionID
	}
	return "", ""
}

// LocalWorktree is a prepared worktree plus everything the daemon needs to
// finalize it after the agent exits.
type LocalWorktree struct {
	// GitRoot is the user's repository root — the repo that owns the branch.
	GitRoot string
	// Path is the working copy's directory, inside WorktreeRoot.
	Path string
	// WorktreeRoot is the directory holding this repository's Multica working
	// copies — the user's disk, beside their repository. Carried so teardown
	// can drop the copy's ownership record from the same place Prepare wrote
	// it (DENE-617).
	WorktreeRoot string
	// WorkDir is the agent's cwd: Path, plus the offset of LocalPath inside
	// the repo when the user pointed the resource at a subdirectory.
	WorkDir string
	// Branch is the branch created for this task, in the user's repo.
	Branch string
	// BaseCommit is the commit the worktree started from — this turn's baseline
	// when one was committed, otherwise the branch tip it continued. Finalize
	// compares the delivered tip against it twice: to decide whether the task
	// produced anything, and to decide whether it may be recorded at all.
	//
	// It is the strongest commit this turn can point at, which is why the second
	// question uses it rather than the checkpoint an earlier turn recorded. That
	// checkpoint is only an ancestor of it: the user may have committed on the
	// delivered branch since, and this turn's own baseline sits later still.
	// Measuring against the older commit let a run reset away everything this
	// turn replayed and still be recorded as having delivered it, which is how
	// the user's edits went missing from the turn after (MUL-6881 review).
	BaseCommit string
	// DirtyBaseCaptured records that the user had uncommitted tracked edits
	// which were replayed into the worktree.
	DirtyBaseCaptured bool
	// Continued reports that this worktree checked out a branch an earlier turn
	// of the same conversation left behind, instead of forking a new one from
	// the user's HEAD.
	Continued bool
	// StaleBaselineNotice explains why the local repository could not be
	// fast-forwarded to its tracking branch before this worktree was created.
	// It is surfaced in the run's opening message so the agent does not mistake
	// an intentionally preserved, older baseline for the latest remote code.
	StaleBaselineNotice string `json:"stale_baseline_notice,omitempty"`
	// ReplayConflicts names the files where the user's edits since the previous
	// turn could not be merged with what the branch already carries. The
	// worktree is handed to the agent WITH those conflicts in it — resolving
	// them is ordinary git work, and it is the only party that can judge which
	// version is right — so this is what the turn's prompt tells it to fix.
	// Finalize refuses to deliver while any of them are still unmerged.
	ReplayConflicts []string
	// ReplaySkippedNotice is set when this turn did not replay the user's
	// uncommitted edits because that same replay already conflicted once.
	// The worktree is clean. The prompt tells the agent to say so: the edits
	// are still in the user's checkout, and they are not on this branch.
	ReplaySkippedNotice string `json:"replay_skipped_notice,omitempty"`
	// createdBranch records that this prepare put the branch where it is, so
	// dropping it discards nothing an earlier turn delivered. False for a
	// continued branch: that one has to survive even a turn that produced
	// nothing, because it carries every turn before it.
	createdBranch bool
	// userState is the commit describing the user's directory as this task saw
	// it: their tracked edits and untracked files in one tree. Recorded against
	// the branch once it actually carries them, so the next turn replays only
	// what changed after that.
	userState string
	// owner is the conversation this branch belongs to, recorded with the
	// snapshot so a later task can prove the branch is its own before
	// continuing it.
	owner branchOwner
	// tracksState is false for a branch no later turn will continue — a
	// task-scoped branch, or the one a busy sibling forked. Recording a
	// snapshot for those would leave a ref nothing ever reads.
	tracksState bool
	// priorState is the snapshot the branch carried when this turn started, and
	// the one to record when this turn could not get its own into the branch.
	priorState string
	// userHead is the user's HEAD when userState was captured. Movement of
	// this commit is committed history, not a local edit. priorUserHead is the
	// same fact for the snapshot the branch already carries.
	userHead      string
	priorUserHead string
	// replayAbandoned is the fingerprint of a replay this turn deliberately
	// did not apply. The branch does not contain it, so the recorded snapshot
	// stays the one the branch already carries. The fingerprint is what lets
	// the next turn try the same edit again instead of treating it as delivered.
	replayAbandoned string
	// snapshotPending is set when Prepare left the user's edits unmerged in the
	// worktree: the branch does not carry userState yet, and only a commit made
	// after the agent resolves can put it there.
	snapshotPending bool
	// aborted, when set, makes Finalize refuse to commit or remove anything.
	// Set by the daemon when a pre-commit step failed in a way that would make
	// the committed branch wrong (see AbortWithReason).
	aborted error
}

// MarshalJSON / UnmarshalJSON carry this struct's unexported state across the
// preparation helper boundary.
//
// Prepare runs in a short-lived helper process (PrepareIsolated) and the
// Environment it built comes back to the daemon as JSON, so everything Finalize
// needs has to be on the wire. Ordinary struct marshalling drops unexported
// fields silently: the daemon then finalized a worktree whose owner, snapshot
// and tracksState were all zero, which turned every proof this file makes into
// a no-op — no delivery verification, no record of the delivered tip, and a
// read-only turn keeping its branch. Nothing failed; the guarantees were simply
// absent in production while every in-process test still passed.
//
// aborted is deliberately NOT carried: it is set by the daemon after Prepare
// returns (AbortWithReason), so it belongs to the parent process only.
func (w *LocalWorktree) MarshalJSON() ([]byte, error) {
	type wire LocalWorktree
	return json.Marshal(struct {
		*wire
		CreatedBranch   bool        `json:"created_branch"`
		UserState       string      `json:"user_state"`
		PriorState      string      `json:"prior_state"`
		UserHead        string      `json:"user_head"`
		PriorUserHead   string      `json:"prior_user_head"`
		ReplayAbandoned string      `json:"replay_abandoned"`
		Owner           branchOwner `json:"owner"`
		TracksState     bool        `json:"tracks_state"`
		SnapshotPending bool        `json:"snapshot_pending"`
	}{
		wire:            (*wire)(w),
		CreatedBranch:   w.createdBranch,
		UserState:       w.userState,
		PriorState:      w.priorState,
		UserHead:        w.userHead,
		PriorUserHead:   w.priorUserHead,
		ReplayAbandoned: w.replayAbandoned,
		Owner:           w.owner,
		TracksState:     w.tracksState,
		SnapshotPending: w.snapshotPending,
	})
}

func (w *LocalWorktree) UnmarshalJSON(data []byte) error {
	type wire LocalWorktree
	aux := struct {
		*wire
		CreatedBranch   bool        `json:"created_branch"`
		UserState       string      `json:"user_state"`
		PriorState      string      `json:"prior_state"`
		UserHead        string      `json:"user_head"`
		PriorUserHead   string      `json:"prior_user_head"`
		ReplayAbandoned string      `json:"replay_abandoned"`
		Owner           branchOwner `json:"owner"`
		TracksState     bool        `json:"tracks_state"`
		SnapshotPending bool        `json:"snapshot_pending"`
	}{wire: (*wire)(w)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	w.createdBranch = aux.CreatedBranch
	w.userState = aux.UserState
	w.priorState = aux.PriorState
	w.userHead = aux.UserHead
	w.priorUserHead = aux.PriorUserHead
	w.replayAbandoned = aux.ReplayAbandoned
	w.owner = aux.Owner
	w.tracksState = aux.TracksState
	w.snapshotPending = aux.SnapshotPending
	return nil
}

// LocalWorktreeOutcome is what a finished worktree task delivered.
type LocalWorktreeOutcome struct {
	// Branch is the branch holding the task's work, or "" when the task made
	// no changes at all (a read-only run) — in that case the branch is deleted
	// so it never shows up in the user's `git branch` as an empty artifact.
	Branch string
	// AutoCommitted is true when the agent left uncommitted changes that
	// Finalize committed so they would survive the worktree's removal.
	AutoCommitted bool
	// PreservedPath is set only when Finalize could NOT commit the agent's
	// changes. The worktree at this path was intentionally left on disk because
	// it is the only remaining copy of that work.
	PreservedPath string
}

// PrepareLocalWorktree creates the task's worktree and replays the user's
// uncommitted state into it. It never writes to the user's working tree: the
// snapshot is built in a private index inside the task's env root, which leaves
// their index, their files and their refs exactly as they were.
func PrepareLocalWorktree(params LocalWorktreeParams, logger *slog.Logger) (*LocalWorktree, error) {
	if params.LocalPath == "" {
		return nil, errors.New("execenv: local worktree requires a local path")
	}
	if params.EnvRoot == "" {
		return nil, errors.New("execenv: local worktree requires an env root")
	}
	if params.TaskID == "" {
		return nil, errors.New("execenv: local worktree requires a task id")
	}

	gitRoot, err := resolveGitRoot(params.LocalPath)
	if err != nil {
		return nil, err
	}

	// The agent's cwd keeps the user's chosen depth: a resource pointed at
	// <repo>/services/api must land the agent in <worktree>/services/api, not
	// at the repo root, or the task's whole notion of "the project" shifts.
	//
	// Canonicalise before the comparison: gitRoot comes back canonical, while
	// the configured path routinely isn't (on macOS every /tmp and /var path is
	// a symlink into /private). Comparing the two forms directly reads a repo
	// root as "outside itself".
	localPath := params.LocalPath
	if resolved, evalErr := filepath.EvalSymlinks(localPath); evalErr == nil {
		localPath = resolved
	}
	rel, err := filepath.Rel(gitRoot, localPath)
	if err != nil {
		return nil, fmt.Errorf("execenv: locate %q inside repo %q: %w", localPath, gitRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("execenv: %q is not inside its repository root %q", localPath, gitRoot)
	}

	worktreeRoot, err := ResolveWorktreeRoot(gitRoot, params.WorktreeRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return nil, fmt.Errorf("execenv: create worktree root %q: %w", worktreeRoot, err)
	}
	// Named after the task's env root directory, which the daemon already
	// builds from the conversation key plus a unique suffix
	// (`dene-617-926d754d34e9`). That makes the copy identifiable in a file
	// browser while keeping the one-copy-per-task mapping the env root had.
	//
	// A same-seat retry pins the previous run's path instead. The env root
	// stays per-task — a new suffix — but the CLI keys its conversation to
	// the working-copy path, so a new suffix is a new conversation. The pin
	// is only a missing directory under this repo's own worktree root;
	// deciding it here, and again under the repo lock before anything is
	// deleted, is what keeps a retry from removing another run's copy.
	freshPath := filepath.Join(worktreeRoot, filepath.Base(params.EnvRoot))
	worktreePath := freshPath
	reusedPath := false
	if pinned, ok := ReusableWorktreeDir(worktreeRoot, gitRoot, localPath, params.ResumeWorkDir); ok {
		worktreePath = pinned
		reusedPath = true
	} else if params.ResumeWorkDir != "" && logger != nil {
		logger.Info("execenv: previous worktree unavailable; starting a fresh one",
			"prior_work_dir", params.ResumeWorkDir)
	}

	// Everything below mutates the repo's worktree admin state or its refs, so
	// take the per-repo lock first. It covers the stale-path cleanup, which runs
	// `git worktree remove` and would otherwise race a sibling task's `worktree
	// add`, and the branch decision, which has to see a branch that another task
	// is creating at the same moment. The lock is cross-process because every
	// prepare runs in its own helper process (#7434).
	unlock, err := lockGitRoot(gitRoot, logger)
	if err != nil {
		return nil, err
	}
	defer unlock()

	if reusedPath {
		if _, statErr := os.Lstat(worktreePath); statErr == nil || !os.IsNotExist(statErr) {
			// Appeared between the check and the lock, or could not be
			// stated. Deleting it would remove whatever landed there.
			if logger != nil {
				logger.Info("execenv: previous worktree path is occupied; starting a fresh one",
					"path", worktreePath, "error", statErr)
			}
			worktreePath = freshPath
			reusedPath = false
		}
	}
	if _, statErr := os.Stat(worktreePath); statErr == nil {
		// Prepare wipes and recreates envRoot, so an existing worktree path
		// means a stale registration in the user's repo pointing here. Remove
		// both rather than failing the task. A reused path never reaches this
		// branch: it was required to be absent, and an occupied one fell back
		// to freshPath above.
		removeLocalWorktreeDir(gitRoot, worktreePath, logger)
	}
	if reusedPath && logger != nil {
		logger.Info("execenv: same-seat retry reusing the previous worktree",
			"path", worktreePath)
	}

	// Self-heal registrations orphaned by a crashed daemon: their env roots are
	// long gone, but the user's repo still lists them. Prune only drops entries
	// whose directory no longer exists, so it can never disturb a live task.
	if out, pruneErr := runGit(gitRoot, "worktree", "prune"); pruneErr != nil && logger != nil {
		logger.Warn("execenv: git worktree prune failed (non-fatal)",
			"git_root", gitRoot, "output", out, "error", pruneErr)
	}
	pruneOrphanedStateRefs(gitRoot, logger)

	// A pinned local directory is the source of truth for this task. Refresh
	// its tracking branch before taking the snapshot, but only when the user's
	// checkout is clean and the update is a true fast-forward. Dirty or
	// diverged repositories are deliberately left alone; the notice travels
	// with the prepared worktree and is rendered in the agent's first message.
	staleBaselineNotice := refreshLocalBaseline(gitRoot, logger)

	headSHA, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("execenv: repository %q has no commit to branch from "+
			"(worktree mode needs at least one commit; make an initial commit or switch the resource back to in_place): %w", gitRoot, err)
	}

	// Refuse an untracked payload too large to reproduce BEFORE snapshotting it:
	// the snapshot writes every one of those bytes into the user's own object
	// database, which is not something to do by accident on a directory full of
	// un-ignored build output.
	if err := checkUntrackedReplayable(gitRoot, logger); err != nil {
		return nil, err
	}

	// The commit describing the user's directory as this task sees it — their
	// tracked edits and their untracked files in one tree. Everything below
	// reasons about the user's state through this single object.
	userState, err := captureUserSnapshot(gitRoot, params.EnvRoot, headSHA, logger)
	if err != nil {
		// Fail closed. The promise of this mode is that the agent reasons about
		// the code the user actually has; silently starting from HEAD instead
		// would have it review a tree the user never saw and report confidently
		// on it. A task that does not start is recoverable — one that answers
		// from the wrong sources is not.
		return nil, fmt.Errorf("execenv: could not capture the uncommitted changes in %q, "+
			"so the worktree would not match what you have on disk: %w", gitRoot, err)
	}

	plan := resolveTaskBranch(gitRoot, params, headSHA, logger)
	scope, scopeErr := sparsecheckout.Parse(params.CheckoutPaths)
	if scopeErr != nil {
		return nil, fmt.Errorf("execenv: checkout paths: %w", scopeErr)
	}
	actualBranch, createdBranch, err := addLocalWorktree(gitRoot, worktreePath, plan, params.TaskID, scope)
	if err != nil {
		return nil, err
	}

	// Record the copy as Multica's before anything else can fail. Cleanup only
	// ever removes a copy it can prove it made, so a missing record is not a
	// cosmetic gap — it permanently exempts this directory from cleanup.
	if recErr := writeWorktreeRecord(worktreeRoot, WorktreeRecord{
		Path:        worktreePath,
		GitRoot:     gitRoot,
		Branch:      actualBranch,
		TaskID:      params.TaskID,
		WorkspaceID: params.WorkspaceID,
	}); recErr != nil && logger != nil {
		logger.Warn("execenv: could not record the task worktree as Multica-created; automatic cleanup will leave it alone",
			"path", worktreePath, "root", worktreeRoot, "error", recErr)
	}

	wt := &LocalWorktree{
		GitRoot:             gitRoot,
		Path:                worktreePath,
		WorktreeRoot:        worktreeRoot,
		WorkDir:             filepath.Join(worktreePath, rel),
		Branch:              actualBranch,
		StaleBaselineNotice: staleBaselineNotice,
		BaseCommit:          plan.base,
		Continued:           plan.continues,
		createdBranch:       createdBranch,
		userState:           userState,
		priorState:          plan.priorState,
		userHead:            headSHA,
		priorUserHead:       plan.priorUserHead,
		owner:               plan.owner,
		// A branch a sibling task forked because the conversation's own branch
		// was busy is delivered once and never continued, so it records nothing.
		tracksState: plan.tracksState && actualBranch == plan.name,
	}

	// Tear the worktree back down on every failure below. A half-replayed tree
	// is the worst outcome available: it looks like a working checkout, so
	// nothing downstream questions it, while the agent silently reads different
	// code than the user has. The branch goes with it only when this prepare
	// created it — a continued branch carries earlier turns' work, and dropping
	// it because THIS turn could not start would destroy the very thing the
	// task was meant to build on.
	rollback := func() {
		removeLocalWorktreeDir(gitRoot, worktreePath, logger)
		if createdBranch {
			dropBranch(gitRoot, actualBranch, logger)
		}
	}

	// Replay the user's directory into the worktree.
	//
	// A branch forked from HEAD carries none of it, so the whole snapshot goes
	// in. A continued branch already carries the snapshot the previous turn
	// recorded, and the agent's commits sit on top of it, so only what the user
	// changed after that is new information there.
	replay, replayErr := replayUserState(worktreePath, plan, userState, logger)
	if replayErr != nil {
		rollback()
		return nil, replayErr
	}
	wt.ReplayConflicts = replay.conflicts
	wt.ReplaySkippedNotice = replay.skippedNotice
	wt.replayAbandoned = replay.abandoned
	// An unresolved merge means the branch does not carry this turn's snapshot
	// yet; only a commit after the agent resolves can put it there.
	wt.snapshotPending = len(replay.conflicts) > 0
	// A skipped replay is not on the branch either. Recording this turn's
	// snapshot would make the next diff empty, and the edit would never be
	// offered again. Keep the snapshot the branch actually carries. The
	// abandoned fingerprint stays on the record so a later turn applies the
	// edit once the branch can take it.
	if replay.abandoned != "" && wt.priorState != "" {
		wt.userState = wt.priorState
		if wt.priorUserHead != "" {
			wt.userHead = wt.priorUserHead
		} else if recovered := recoverCarriedUserHead(gitRoot, wt.priorState); recovered != "" {
			wt.userHead = recovered
		}
	}
	// Whether the user has uncommitted work at all — replayed by this turn or
	// already carried by the branch it continued.
	_, diffErr := runGit(gitRoot, "diff", "--quiet", headSHA, userState)
	wt.DirtyBaseCaptured = diffErr != nil

	// Commit the replayed state as a baseline so "did this task change
	// anything?" has an exact answer later. Without it the user's own
	// uncommitted work counts as a change: a read-only task on a repo with an
	// untracked scratch file would auto-commit that file at the end and leave
	// behind a branch the agent never touched. The baseline also makes the
	// delivered branch readable — `git diff <baseline>..<branch>` is precisely
	// the agent's work, with the user's WIP as its own labelled commit.
	//
	// Skipped entirely while the replay is unresolved: an index with unmerged
	// entries cannot be committed without committing conflict markers, and the
	// agent has not had its turn at them yet.
	if len(replay.conflicts) == 0 {
		dirty, dirtyErr := worktreeIsDirty(worktreePath)
		if dirtyErr != nil {
			rollback()
			return nil, fmt.Errorf("execenv: could not inspect the prepared worktree for %q: %w", gitRoot, dirtyErr)
		}
		// A branch this prepare created gets its baseline even when there was
		// nothing to replay. Two things need it. The empty case is the one that
		// used to be skipped, and it is the one where the branch sits exactly on
		// the user's own HEAD — so the branch had no commit of its own, and
		// nothing distinguished it from a branch the user creates there
		// themselves. And `git diff <baseline>..<branch>` is then the agent's
		// work on every branch, not only on the ones that started dirty.
		if dirty || !plan.continues {
			baseline, baseErr := commitBaseline(worktreePath, plan.continues, dirty)
			if baseErr != nil {
				// Without a baseline the task cannot tell the user's work from the
				// agent's, so it would later commit the user's files as if the agent
				// had produced them. Refuse rather than deliver a misleading branch.
				rollback()
				return nil, fmt.Errorf("execenv: could not record a baseline commit for the replayed state of %q: %w", gitRoot, baseErr)
			}
			// The branch now stands on a commit that carries this turn's snapshot,
			// and everything downstream measures the delivery against it.
			wt.BaseCommit = baseline
		}
		// Record what this branch carries, together with the owner that lets the
		// next task prove the branch is its own. A skipped replay records the
		// previous snapshot (the branch does not contain the edit); every other
		// clean prepare records this turn's. Recorded here as well as at
		// Finalize so a turn that never reaches Finalize still leaves the
		// branch identifiable.
		if err := wt.recordState(wt.BaseCommit, logger); err != nil && logger != nil {
			logger.Warn("execenv: could not record the task branch before the run (non-fatal; Finalize records the delivered tip)",
				"branch", wt.Branch, "error", err)
		}
	}

	// Note on keeping sidecars out of the delivered branch: we deliberately do
	// NOT write .git/info/exclude here. A linked worktree reads info/exclude
	// from the repo's COMMON git dir, so the only file that would take effect
	// is the user's own .git/info/exclude — editing it would change what `git
	// status` shows in the user's checkout, which is theirs, not ours. Instead
	// the daemon runs the existing CleanupRuntimeConfig + CleanupSidecars pass
	// over the worktree before Finalize, so the sidecars are simply gone by the
	// time anything is committed. That also preserves a genuine agent edit to a
	// tracked CLAUDE.md, which a blanket exclude would have swallowed.

	if logger != nil {
		logger.Info("execenv: local worktree ready",
			"git_root", gitRoot,
			"path", worktreePath,
			"branch", actualBranch,
			"base", wt.BaseCommit,
			"continued", wt.Continued,
			"dirty_base_captured", wt.DirtyBaseCaptured,
			"replay_conflicts", len(wt.ReplayConflicts),
			"replay_skipped", wt.ReplaySkippedNotice != "",
		)
	}
	return wt, nil
}

// refreshLocalBaseline updates a clean local checkout to its fetched upstream
// tip when that update is a fast-forward. It never resets, merges, or touches
// a dirty/diverged checkout. A non-empty return value is safe to show in the
// run prompt and explains why the task intentionally started from an older
// baseline.
func refreshLocalBaseline(gitRoot string, logger *slog.Logger) string {
	branch, err := runGitTrimmed(gitRoot, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch == "" {
		// Detached HEADs have no tracking branch to refresh.
		return ""
	}
	upstream, err := runGitTrimmed(gitRoot, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil || upstream == "" {
		return ""
	}

	fetchCtx, cancelFetch := context.WithTimeout(context.Background(), localBaselineFetchTimeout)
	fetchErr := fetchOrigin(fetchCtx, gitRoot)
	cancelFetch()
	if fetchErr != nil {
		notice := fmt.Sprintf("The local checkout on branch %q may be stale: refreshing its upstream %q failed (%s). Do not assume this baseline is the latest remote code.", branch, upstream, strings.Join(strings.Fields(fetchErr.Error()), " "))
		if logger != nil {
			logger.Warn("execenv: local baseline refresh failed", "git_root", gitRoot, "branch", branch, "upstream", upstream, "error", fetchErr)
		}
		return notice
	}

	status, statusErr := runGit(gitRoot, "status", "--porcelain", "--untracked-files=all")
	if statusErr != nil {
		return fmt.Sprintf("The local checkout on branch %q may be stale: its working tree could not be checked before refresh (%s).", branch, strings.Join(strings.Fields(statusErr.Error()), " "))
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Sprintf("The local checkout on branch %q may be stale: it has local edits, so Multica left it untouched instead of fast-forwarding to %q.", branch, upstream)
	}

	counts, countErr := runGitTrimmed(gitRoot, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if countErr != nil {
		return fmt.Sprintf("The local checkout on branch %q may be stale: could not compare it with %q (%s).", branch, upstream, strings.Join(strings.Fields(countErr.Error()), " "))
	}
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return fmt.Sprintf("The local checkout on branch %q may be stale: git returned an unexpected comparison with %q.", branch, upstream)
	}
	ahead, aheadErr := strconv.Atoi(fields[0])
	behind, behindErr := strconv.Atoi(fields[1])
	if aheadErr != nil || behindErr != nil {
		return fmt.Sprintf("The local checkout on branch %q may be stale: git returned an invalid comparison with %q.", branch, upstream)
	}
	if behind == 0 {
		return ""
	}
	if ahead != 0 {
		return fmt.Sprintf("The local checkout on branch %q may be stale: it diverges from %q (%d local commit%s, %d upstream commit%s), so Multica left it untouched.", branch, upstream, ahead, pluralSuffix(ahead), behind, pluralSuffix(behind))
	}

	if _, err := runGit(gitRoot, "merge", "--ff-only", upstream); err != nil {
		if logger != nil {
			logger.Warn("execenv: local baseline fast-forward failed", "git_root", gitRoot, "branch", branch, "upstream", upstream, "error", err)
		}
		return fmt.Sprintf("The local checkout on branch %q may be stale: it is %d commit%s behind %q, but the fast-forward failed. Multica left it untouched.", branch, behind, pluralSuffix(behind), upstream)
	}
	if logger != nil {
		logger.Info("execenv: fast-forwarded local baseline", "git_root", gitRoot, "branch", branch, "upstream", upstream, "commits", behind)
	}
	return ""
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Finalize commits whatever the agent left behind, removes the worktree, and
// reports the branch. Called after the agent exits, before the env root is
// handed to the GC.
//
// The auto-commit is the reason a worktree task can't lose work: `git worktree
// remove --force` would happily delete uncommitted edits, and the user would
// have no way to get them back. Committing first turns "the agent edited files"
// into "the branch has a commit", which is the delivery contract for this mode.
//
// If that commit cannot be made — a repo with commit.gpgSign and no signing key
// available to the daemon, a full disk, a ref lock we lost — Finalize returns an
// error and DELIBERATELY LEAVES THE WORKTREE IN PLACE. Removing it would be the
// one operation in this file that destroys work with no way back, and a warning
// in the daemon log is not an acceptable substitute for the user's changes. The
// surviving worktree stays registered in the user's repo, so `git worktree list`
// points straight at it.
func (w *LocalWorktree) Finalize(logger *slog.Logger) (LocalWorktreeOutcome, error) {
	if w == nil {
		return LocalWorktreeOutcome{}, nil
	}
	outcome := LocalWorktreeOutcome{Branch: w.Branch}

	unlock, err := lockGitRoot(w.GitRoot, logger)
	if err != nil {
		// Nothing has been committed or removed yet, so the agent's work is
		// still sitting in the worktree. Report it as preserved rather than
		// naming a branch that does not carry it.
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		return outcome, fmt.Errorf("could not lock %q to finalize branch %s: %w; "+
			"the work is preserved in the worktree at %s", w.GitRoot, w.Branch, err, w.Path)
	}
	defer unlock()

	// Something before the commit went wrong in a way that would make the
	// delivered branch misleading. Commit nothing and keep the worktree: the
	// agent's work is still in it, and so is whatever the caller could not
	// clean up, which a human can now look at directly.
	if w.aborted != nil {
		// Report NO branch. One exists in the user's repo, but nothing was
		// committed to it, so naming it as this task's result would point them
		// at a branch that is missing the very work they are looking for. The
		// preserved worktree path below is the honest pointer.
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		if logger != nil {
			logger.Error("execenv: worktree finalize aborted; nothing committed, worktree kept for inspection",
				"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", w.aborted)
		}
		return outcome, fmt.Errorf(
			"refusing to deliver branch %s: %w; the task worktree is preserved at %s (listed by `git worktree list` in %s)",
			w.Branch, w.aborted, w.Path, w.GitRoot)
	}

	// An unresolved merge is never committed. The worktree may be carrying the
	// user's edits from before this turn (replayUserState hands the conflict to
	// the agent rather than dropping it), and `git add -A` would turn conflict
	// markers into a delivered commit — the one way this mode can produce a
	// branch that compiles nowhere and looks deliberate. Keep the worktree, say
	// which files are still open, and leave the snapshot unrecorded so the next
	// turn replays the same edits instead of assuming they landed.
	if unmerged, unmergedErr := unmergedPaths(w.Path); unmergedErr != nil || len(unmerged) > 0 {
		outcome.Branch = ""
		outcome.PreservedPath = w.Path
		if unmergedErr != nil {
			return outcome, fmt.Errorf("could not check %s for an unresolved merge: %w; "+
				"the work is preserved in the worktree at %s", w.Branch, unmergedErr, w.Path)
		}
		if logger != nil {
			logger.Error("execenv: worktree left with an unresolved merge; nothing committed, worktree kept",
				"path", w.Path, "branch", w.Branch, "files", unmerged)
		}
		return outcome, w.unmergedReplayError(unmerged)
	}

	// Treat "can't tell" like "dirty": committing costs an empty commit at
	// worst, while assuming clean risks deleting the agent's edits.
	dirty, statusErr := worktreeIsDirty(w.Path)
	if statusErr != nil {
		if logger != nil {
			logger.Warn("execenv: inspect worktree status failed; committing defensively",
				"path", w.Path, "error", statusErr)
		}
		dirty = true
	}
	if dirty {
		committed, err := w.commitAll(logger)
		if err != nil {
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: could not commit the agent's changes; keeping the worktree so the work is recoverable",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", err)
			}
			return outcome, fmt.Errorf(
				"could not commit the agent's changes to branch %s: %w; the work is preserved in the worktree at %s (listed by `git worktree list` in %s) — recover it before that directory is reclaimed",
				w.Branch, err, w.Path, w.GitRoot)
		}
		outcome.AutoCommitted = committed
	}

	// A branch still sitting exactly on its base commit means the task changed
	// nothing — the read-only case. Delete it so the user's branch list only
	// ever grows for tasks that actually produced work. Only ever the branch
	// this task created: a continued branch sits on its base precisely because
	// this turn added nothing to what earlier turns delivered, and deleting it
	// would take their work with it.
	tip, err := runGitTrimmed(w.Path, "rev-parse", "--verify", "HEAD")
	producedWork := err != nil || tip != w.BaseCommit
	dropped := !producedWork && w.createdBranch

	// A turn that started mid-merge only gets to advance the branch's recorded
	// state if it committed something after resolving. When the branch is still
	// where THIS turn found it, nothing distinguishes "the agent resolved in
	// favour of the version already on the branch" from "the agent threw the
	// merge away" — the tree is identical either way — so the record keeps the
	// state the branch is known to carry, and the next turn offers the user's
	// edits again.
	//
	// The cost is a merge the agent may have to conclude more than once, in the
	// one case where its conclusion left no trace. The alternative is recording
	// edits as delivered that may have been discarded, which is how they go
	// missing without anyone seeing it.
	if w.snapshotPending && tip == w.BaseCommit {
		if w.priorState == "" {
			outcome.Branch = ""
			outcome.PreservedPath = w.Path
			return outcome, fmt.Errorf(
				"refusing to record branch %s: the merge with your local edits was never concluded and this branch has no earlier "+
					"state to fall back on; the task worktree is preserved at %s (listed by `git worktree list` in %s)",
				w.Branch, w.Path, w.GitRoot)
		}
		if logger != nil {
			logger.Info("execenv: the run concluded its merge without committing; keeping the branch's previous recorded state so the edits are offered again",
				"path", w.Path, "branch", w.Branch)
		}
		w.userState = w.priorState
		// The tree being kept was captured against the previous user HEAD.
		// Stamping this turn's HEAD on it would make the next replay treat
		// every commit in between as a local edit again.
		if w.priorUserHead != "" {
			w.userHead = w.priorUserHead
		} else if w.priorState != "" {
			w.userHead = recoverCarriedUserHead(w.GitRoot, w.priorState)
		}
	}

	// Record BEFORE the worktree goes away, and treat a failure as a failure to
	// deliver. The record is what makes the branch continuable: without it the
	// next turn cannot prove the branch is this conversation's and starts a new
	// line of work, stranding what this turn produced. Ordering it first is what
	// makes that recoverable — the worktree is still there to preserve, exactly
	// as for a commit that could not be made.
	if !dropped {
		if verifyErr := w.verifyDeliveryPoint(tip); verifyErr != nil {
			outcome.Branch = ""
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: the run's delivery point cannot be recorded as this conversation's; nothing recorded, worktree kept",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "tip", tip, "base", w.BaseCommit, "error", verifyErr)
			}
			return outcome, fmt.Errorf(
				"refusing to record branch %s: %w; the task worktree is preserved at %s (listed by `git worktree list` in %s) — "+
					"recover the work from there, and let the run keep the commit the worktree started from instead of resetting past it",
				w.Branch, verifyErr, w.Path, w.GitRoot)
		}
		if recErr := w.recordState(tip, logger); recErr != nil {
			outcome.PreservedPath = w.Path
			if logger != nil {
				logger.Error("execenv: could not record the delivered task branch; keeping the worktree",
					"path", w.Path, "branch", w.Branch, "git_root", w.GitRoot, "error", recErr)
			}
			return outcome, fmt.Errorf(
				"could not record branch %s as this conversation's: %w; the work is committed to that branch and the "+
					"task worktree is preserved at %s (listed by `git worktree list` in %s) — a follow-up run will start "+
					"a new branch instead of continuing this one",
				w.Branch, recErr, w.Path, w.GitRoot)
		}
	}

	if removeErr := removeLocalWorktreeDir(w.GitRoot, w.Path, logger); removeErr != nil {
		outcome.PreservedPath = w.Path
		return outcome, fmt.Errorf(
			"could not remove finalized worktree for branch %s: %w; the task worktree remains at %s",
			w.Branch, removeErr, w.Path)
	}

	if dropped {
		dropBranch(w.GitRoot, w.Branch, logger)
		outcome.Branch = ""
	}

	if logger != nil {
		logger.Info("execenv: local worktree finalized",
			"git_root", w.GitRoot,
			"branch", outcome.Branch,
			"auto_committed", outcome.AutoCommitted,
			"produced_work", producedWork,
			"continued", w.Continued,
		)
	}
	return outcome, nil
}

// Discard tears a worktree down without delivering anything: unregister it,
// delete its directory, drop its branch.
//
// For the abandon-before-the-agent-ran case only. Finalize is the path that
// preserves work; this one assumes there is none to preserve, so callers must
// be sure nothing has run in the worktree yet.
func (w *LocalWorktree) Discard(logger *slog.Logger) {
	if w == nil {
		return
	}
	unlock, err := lockGitRoot(w.GitRoot, logger)
	if err != nil {
		// Best-effort by contract: every step below only logs on failure. The
		// registration this leaves behind is pruned by the next prepare on
		// this repo, which is the same self-heal path a crashed daemon uses.
		if logger != nil {
			logger.Warn("execenv: could not lock the repository to discard the task worktree",
				"git_root", w.GitRoot, "path", w.Path, "branch", w.Branch, "error", err)
		}
		return
	}
	defer unlock()
	removeLocalWorktreeDir(w.GitRoot, w.Path, logger)
	// Same rule as every other teardown: a branch this prepare did not create
	// belongs to the turns before it and outlives this task.
	if w.createdBranch {
		dropBranch(w.GitRoot, w.Branch, logger)
	}
	if logger != nil {
		logger.Info("execenv: local worktree discarded before the agent ran",
			"git_root", w.GitRoot, "path", w.Path, "branch", w.Branch, "branch_dropped", w.createdBranch)
	}
}

// AbortWithReason marks the worktree undeliverable. Finalize will then commit
// nothing, remove nothing, and return an error naming the preserved path.
//
// This exists because the decision "is this branch safe to deliver?" is made
// outside this package — the daemon knows whether its own sidecar cleanup
// succeeded — while the only code that can act on it is Finalize. The first
// reason wins: it is the one closest to the root cause.
func (w *LocalWorktree) AbortWithReason(err error) {
	if w == nil || err == nil || w.aborted != nil {
		return
	}
	w.aborted = err
}

// commitBaseline records the user's replayed uncommitted state as its own
// commit on the task branch, returning the new tip. On a continued branch that
// state is the increment since the previous turn, so the message says so rather
// than claiming to be the branch's baseline.
//
// A new branch gets one even with nothing to record. The commit is then empty,
// and that is the point: it is the branch's first commit of its own, the thing
// that makes it distinguishable later from a branch the user creates at the
// same place — see LocalWorktree.BaseCommit.
func commitBaseline(worktreePath string, continued, dirty bool) (string, error) {
	message := "chore(agent): baseline — uncommitted work from the local directory"
	switch {
	case continued:
		message = "chore(agent): uncommitted work from the local directory since the previous turn"
	case !dirty:
		message = "chore(agent): baseline — the task worktree started here"
	}
	if _, err := commitEverything(worktreePath, message, !dirty); err != nil {
		return "", err
	}
	tip, err := runGitTrimmed(worktreePath, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve baseline commit: %w", err)
	}
	return tip, nil
}

// commitAll stages and commits everything the agent left behind. Returns
// whether a commit was actually created; an error means the changes are still
// only on disk and the caller must not delete the worktree.
func (w *LocalWorktree) commitAll(logger *slog.Logger) (bool, error) {
	// Never --allow-empty here: an empty commit would make a read-only turn look
	// like it produced work and leave its branch behind.
	return commitEverything(w.Path, "chore(agent): uncommitted changes from task", false)
}

// commitEverything returns (false, nil) for the benign "there was nothing to
// commit" case and (false, err) for a real failure — the distinction callers
// need to decide whether the tree is safe to discard.
func commitEverything(worktreePath, message string, allowEmpty bool) (bool, error) {
	if out, err := runGit(worktreePath, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
	}
	// --no-verify: the user's commit hooks are written for the user's own
	// workflow (interactive linters, test suites, signing prompts) and a hook
	// failure here would mean losing the agent's work to save a lint run. Note
	// it does NOT disable commit.gpgSign, which is why the caller has to treat
	// a commit failure as "keep the worktree" rather than a warning.
	args := append(commitIdentityArgs(worktreePath), "commit", "--no-verify")
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	args = append(args, "-m", message)
	if out, err := runGit(worktreePath, args...); err != nil {
		if strings.Contains(out, "nothing to commit") {
			return false, nil
		}
		return false, fmt.Errorf("git commit: %s: %w", strings.TrimSpace(out), err)
	}
	return true, nil
}

// commitIdentityArgs supplies a committer identity only when the repo doesn't
// already have one. A repo with user.email configured keeps it, so commits
// still look like they came from the user's own setup.
func commitIdentityArgs(dir string) []string {
	if email, err := runGitTrimmed(dir, "config", "user.email"); err == nil && email != "" {
		return nil
	}
	return []string{
		"-c", "user.name=Multica Agent",
		"-c", "user.email=agent@multica.local",
	}
}

func worktreeIsDirty(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %s: %w", strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out) != "", nil
}

// removeLocalWorktreeDir unregisters the worktree from the user's repo and
// deletes its directory. The branch is deliberately left alone — it is the
// task's deliverable.
func removeLocalWorktreeDir(gitRoot, worktreePath string, logger *slog.Logger) error {
	var removeErr error
	if out, err := runGit(gitRoot, "worktree", "remove", "--force", worktreePath); err != nil {
		removeErr = err
		if logger != nil {
			logger.Warn("execenv: git worktree remove failed; pruning registration",
				"path", worktreePath, "output", out, "error", err)
		}
		// Fall back to deleting the directory ourselves and dropping the now
		// dangling registration, so the user's repo isn't left listing a
		// worktree that no longer exists.
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			removeErr = errors.Join(removeErr, rmErr)
			if logger != nil {
				logger.Warn("execenv: remove worktree directory failed", "path", worktreePath, "error", rmErr)
			}
		}
		if out, pruneErr := runGit(gitRoot, "worktree", "prune"); pruneErr != nil && logger != nil {
			logger.Warn("execenv: git worktree prune failed", "output", out, "error", pruneErr)
		}
	}
	// Lstat verifies the path entry itself is gone. Stat would treat a broken
	// symlink as absent even though a stale entry still occupies the handoff path.
	if _, statErr := os.Lstat(worktreePath); errors.Is(statErr, os.ErrNotExist) {
		// The copy is gone, so its ownership record describes nothing. Dropped
		// from the directory holding the copy, which is the worktree root
		// writeWorktreeRecord wrote it under (DENE-617).
		removeWorktreeRecord(filepath.Dir(worktreePath), worktreePath)
		return nil
	} else if statErr != nil {
		return fmt.Errorf("confirm worktree removal: %w", statErr)
	}
	if removeErr != nil {
		return fmt.Errorf("worktree directory still exists after removal fallback: %w", removeErr)
	}
	return errors.New("worktree directory still exists after git removal reported success")
}

// deleteBranch drops a task branch that carries nothing worth keeping — an
// empty read-only run, or a prepare that aborted partway. Best-effort: a
// leftover branch is untidy, never harmful.
func deleteBranch(gitRoot, branch string, logger *slog.Logger) {
	if branch == "" {
		return
	}
	if out, err := runGit(gitRoot, "branch", "-D", branch); err != nil && logger != nil {
		logger.Warn("execenv: delete task branch failed (non-fatal)",
			"branch", branch, "output", out, "error", err)
	}
}

// ResolveGitRoot is resolveGitRoot for the daemon. A same-seat retry has to
// decide whether the previous working copy can be reused before preparation
// runs in the helper process, and that decision needs the repository root.
func ResolveGitRoot(dir string) (string, error) {
	return resolveGitRoot(dir)
}

// ReusableWorktreeDir reports the working-copy directory a same-seat retry
// may recreate so a cwd-keyed CLI session still resolves.
//
// The previous run's cwd is priorWorkDir. This returns the copy that contains
// it — priorWorkDir itself when the resource points at the repository root,
// or the parent copy when the resource points at a subdirectory — but only
// when that copy is a direct child of worktreeRoot, is not inside the user's
// checkout, and is not on disk. An existing directory is left alone: it may
// be another run's copy or a finalize that kept the work, and recreating a
// session is not a reason to delete either.
//
// False is the safe answer. The caller then builds a fresh copy under a new
// name, and the resume gate drops the session instead of pointing the CLI at
// a directory it cannot find.
func ReusableWorktreeDir(worktreeRoot, gitRoot, localPath, priorWorkDir string) (string, bool) {
	if strings.TrimSpace(worktreeRoot) == "" || strings.TrimSpace(gitRoot) == "" ||
		strings.TrimSpace(localPath) == "" || strings.TrimSpace(priorWorkDir) == "" {
		return "", false
	}
	if !filepath.IsAbs(priorWorkDir) {
		return "", false
	}
	root := canonicalize(worktreeRoot)
	repo := canonicalize(gitRoot)
	resource := localPath
	if resolved, err := filepath.EvalSymlinks(localPath); err == nil {
		resource = resolved
	}
	resource = canonicalize(resource)
	rel, err := filepath.Rel(repo, resource)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	prior := canonicalize(priorWorkDir)
	var copyDir string
	if rel == "." {
		copyDir = prior
	} else {
		suffix := string(filepath.Separator) + rel
		if !strings.HasSuffix(prior, suffix) {
			return "", false
		}
		copyDir = filepath.Clean(strings.TrimSuffix(prior, suffix))
	}
	if copyDir == "" || copyDir == root || filepath.Dir(copyDir) != root {
		return "", false
	}
	base := filepath.Base(copyDir)
	if base == "." || base == ".." || base == "" || strings.HasPrefix(base, ".") {
		return "", false
	}
	if inside, err := pathIsInside(repo, copyDir); err != nil || inside {
		return "", false
	}
	if _, err := os.Lstat(copyDir); err == nil {
		return "", false
	} else if !os.IsNotExist(err) {
		return "", false
	}
	return copyDir, true
}

// resolveGitRoot returns the repository root containing dir. Worktree mode is
// opt-in per resource, so a non-git directory here is a misconfiguration the
// user needs to see and fix — we fail closed with an actionable message rather
// than silently degrading to the in-place lock, which would leave the user
// wondering why their tasks still queue.
func resolveGitRoot(dir string) (string, error) {
	root, err := runGitTrimmed(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return "", fmt.Errorf("execenv: local_directory %q is not a git repository, "+
			"but its project resource is set to execution_mode=worktree; "+
			"initialise a repository there or switch the resource back to in_place", dir)
	}
	// EvalSymlinks so the root matches the path git reports from inside the
	// worktree later — on macOS /tmp vs /private/tmp otherwise produce two
	// different lock keys for one repo.
	if resolved, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		root = resolved
	}
	return filepath.Clean(root), nil
}

// captureUserSnapshot records the user's working directory as one commit: their
// tracked modifications AND their untracked-but-not-ignored files, in a single
// tree parented at their HEAD.
//
// One snapshot rather than the older split — a `git stash create` for tracked
// edits and a file copy for untracked ones — because a continued branch has to
// answer "what has the user changed since the turn I already carry?", and that
// question is unanswerable for a file no snapshot ever recorded: an untracked
// file the agent then edited would be re-copied from the user's older version
// every turn, or its later deletion would never carry. A tree also expresses
// deletions and mode changes, which a copy cannot.
//
// Nothing here touches the user's index, working tree or refs. GIT_INDEX_FILE
// points at a private index in the task's env root, so `git add` writes only
// blob objects and that file — which is also what makes the capture immune to
// the .git/index.lock races that used to be able to end the task (#7434): the
// only lock taken is on our own temporary file.
func captureUserSnapshot(gitRoot, envRoot, headSHA string, logger *slog.Logger) (string, error) {
	if envRoot == "" {
		return "", errors.New("execenv: user snapshot requires an env root to build its index in")
	}
	indexPath := filepath.Join(envRoot, snapshotIndexFileName)
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("clear snapshot index: %w", err)
	}
	// It is scratch space, not task state: the env root is handed to the agent.
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}

	// Seed from the user's own index so git can trust its stat cache instead of
	// re-hashing the whole repository on every turn. A missing or torn copy is
	// not a failure — read-tree rebuilds a correct index, merely a colder one —
	// so the fallback runs on any error from the add, not just from the copy.
	seeded := seedSnapshotIndex(gitRoot, indexPath)
	addArgs := append([]string{"add", "-A", "--"}, snapshotExcludes()...)
	if out, err := runGitEnv(gitRoot, env, addArgs...); err != nil {
		if !seeded {
			return "", fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
		}
		if logger != nil {
			logger.Debug("execenv: snapshot index seeded from the repository index was unusable; rebuilding it",
				"git_root", gitRoot, "output", strings.TrimSpace(out), "error", err)
		}
		if out, resetErr := runGitEnv(gitRoot, env, "read-tree", headSHA); resetErr != nil {
			return "", fmt.Errorf("git read-tree: %s: %w", strings.TrimSpace(out), resetErr)
		}
		if out, retryErr := runGitEnv(gitRoot, env, addArgs...); retryErr != nil {
			return "", fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), retryErr)
		}
	}
	tree, err := runGitTrimmedEnv(gitRoot, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("git write-tree: %w", err)
	}
	// The identity args cover a repo with no user.email configured: writing a
	// commit object needs a committer, and without them the user's uncommitted
	// work would be dropped on a technicality.
	args := append(commitIdentityArgs(gitRoot), "commit-tree", tree, "-p", headSHA, "-m",
		snapshotCommitTitle+"\n\nThe tree of this commit is the user's working directory as a task saw it. Its parent is the user's HEAD at that moment.")
	snapshot, err := runGitTrimmed(gitRoot, args...)
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	return snapshot, nil
}

// seedSnapshotIndex copies the repository's index to path, reporting whether it
// got one. Read as a plain file rather than through git: git would want the
// index lock, and this copy exists precisely to avoid waiting on it.
func seedSnapshotIndex(gitRoot, path string) bool {
	src, err := runGitTrimmed(gitRoot, "rev-parse", "--git-path", "index")
	if err != nil || src == "" {
		return false
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(gitRoot, src)
	}
	return copyFile(src, path) == nil
}

// snapshotExcludes keeps the daemon's own sidecars out of the user's snapshot.
// They are untracked files in the user's directory whenever an in_place task is
// mid-flight on the same path, or was killed before its cleanup ran; carrying
// them would put another issue's brief inside this task's worktree — where the
// agent would read it as its own context — and commit it to the branch.
// Matched at any depth, because an in_place resource may point at a
// subdirectory of this repo.
func snapshotExcludes() []string {
	specs := make([]string, 0, len(multicaSidecarDirNames))
	for _, name := range multicaSidecarDirNames {
		specs = append(specs, ":(exclude,glob)**/"+name+"/**")
	}
	return specs
}

// branchOwner is the conversation a task branch belongs to. Recorded with the
// branch's snapshot and compared before any later task continues it: the branch
// NAME carries a human-readable issue key, which the user can also type and
// which two workspaces can mint identically, so the name alone can never
// establish that a branch is ours to append to (MUL-6881 review).
type branchOwner struct {
	WorkspaceID    string
	AgentID        string
	ConversationID string
}

func (o branchOwner) valid() bool {
	return o.WorkspaceID != "" && o.AgentID != "" && o.ConversationID != ""
}

// fingerprint is the stable, collision-resistant form of the same identity,
// used to name a branch when the readable name is already taken by someone
// else's. Stable across turns, so the fallback branch is continued too.
func (o branchOwner) fingerprint() string {
	sum := sha256.Sum256([]byte(o.WorkspaceID + "\x00" + o.AgentID + "\x00" + o.ConversationID))
	return hex.EncodeToString(sum[:])[:12]
}

const (
	ownerTrailerWorkspace    = "Multica-Workspace"
	ownerTrailerAgent        = "Multica-Agent"
	ownerTrailerConversation = "Multica-Conversation"
	// userHeadTrailer is the user's HEAD the recorded tree was captured
	// against. Absent on records written before DENE-814; those recover it
	// by walking to the snapshot commit.
	userHeadTrailer = "Multica-User-Head"
	// replayAbandonedTrailer names a replay that was skipped after it had
	// already conflicted once. The record's tree stays the snapshot the
	// branch actually carries, not the user's current directory, so the next
	// turn can try that replay again.
	replayAbandonedTrailer   = "Multica-Replay-Abandoned"
	replayFingerprintTrailer = "Multica-Replay-Fingerprint"
	replayCountTrailer       = "Multica-Replay-Count"
	replayFileTrailer        = "Multica-Replay-File"
	replaySnapshotTrailer    = "Multica-Replay-Snapshot"
	replayUserHeadTrailer    = "Multica-Replay-User-Head"
)

// branchRecord is what refs/multica/local-state/<branch> holds: a commit whose
// TREE is the user's directory as the branch last carried it, whose SECOND
// PARENT is the branch tip at that moment, and whose message names the owner.
//
// The checkpoint is what makes the record about this BRANCH rather than merely
// about its name. Owner alone proved only that Multica once wrote a branch
// called this, and that stayed true after the user deleted it and created their
// own under the same name — the next task then continued into their work
// (MUL-6881 review). Requiring the checkpoint to still be an ancestor of the
// tip is the continuity proof: a branch deleted and recreated, force-moved onto
// unrelated history, or rebased no longer contains the commit recorded here.
type branchRecord struct {
	// state is the commit whose tree is the user snapshot the branch carries.
	state string
	// checkpoint is the branch tip this record was written against.
	checkpoint string
	// userHead is the user's HEAD that tree was captured against. Empty on a
	// record written before the trailer existed; recoverCarriedUserHead reads
	// it from the snapshot commit in that case.
	userHead string
	// replayAbandoned is set when userHead/state were kept from the previous
	// record because a replay was skipped. Empty on records that predate the
	// trailer, and on records whose tree really is what the branch carries.
	replayAbandoned string
	owner           branchOwner
}

// writeBranchRecord records the branch as carrying userState at checkpoint, and
// points the branch's ref at that record.
//
// The checkpoint is passed in, never re-read from the branch ref here: the
// caller knows which commit it actually delivered, while the ref is the user's
// and can move between the delivery and this write. Recording what we delivered
// means a branch that moved in that window simply fails the ancestor test next
// time, which is the safe direction.
func writeBranchRecord(gitRoot, branch, userState, checkpoint string, owner branchOwner, userHead, abandoned string) (string, error) {
	if checkpoint == "" {
		return "", fmt.Errorf("no checkpoint to record for branch %s", branch)
	}
	args := append(commitIdentityArgs(gitRoot), "commit-tree", userState+"^{tree}",
		"-p", userState, "-p", checkpoint, "-m", branchRecordMessage(owner, userHead, abandoned))
	record, err := runGitTrimmed(gitRoot, args...)
	if err != nil {
		return "", fmt.Errorf("git commit-tree: %w", err)
	}
	if out, err := runGit(gitRoot, "update-ref", userStateRef(branch), record); err != nil {
		return "", fmt.Errorf("git update-ref: %s: %w", strings.TrimSpace(out), err)
	}
	return record, nil
}

func branchRecordMessage(owner branchOwner, userHead, abandoned string) string {
	var b strings.Builder
	b.WriteString("multica: task branch record\n\n")
	b.WriteString("Written by Multica for a local_directory task running in worktree mode. Its\n")
	b.WriteString("tree is the user's working directory as this branch last carried it, and its\n")
	b.WriteString("second parent is the branch tip at that moment — together they let the next\n")
	b.WriteString("turn replay only what changed since, and prove the branch is still the one\n")
	b.WriteString("recorded here. Multica-User-Head is the user's HEAD that tree was captured\n")
	b.WriteString("against, so a later commit on the user's branch is not replayed as a local\n")
	b.WriteString("edit. Safe to delete along with the branch.\n\n")
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerWorkspace, owner.WorkspaceID)
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerAgent, owner.AgentID)
	fmt.Fprintf(&b, "%s: %s\n", ownerTrailerConversation, owner.ConversationID)
	if userHead != "" {
		fmt.Fprintf(&b, "%s: %s\n", userHeadTrailer, userHead)
	}
	if abandoned != "" {
		fmt.Fprintf(&b, "%s: %s\n", replayAbandonedTrailer, abandoned)
	}
	return b.String()
}

// readBranchRecord reads back what a branch is recorded as carrying. A commit
// without all three trailers or without a second parent — anything not written
// by writeBranchRecord — yields a record that can never match a valid owner.
func readBranchRecord(gitRoot, commit string) (branchRecord, error) {
	body, err := runGitTrimmed(gitRoot, "log", "-1", "--format=%B", commit)
	if err != nil {
		return branchRecord{}, err
	}
	record := branchRecord{state: commit}
	for _, line := range strings.Split(body, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case ownerTrailerWorkspace:
			record.owner.WorkspaceID = value
		case ownerTrailerAgent:
			record.owner.AgentID = value
		case ownerTrailerConversation:
			record.owner.ConversationID = value
		case userHeadTrailer:
			record.userHead = value
		case replayAbandonedTrailer:
			record.replayAbandoned = value
		}
	}
	if checkpoint, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", commit+"^2"); err == nil {
		record.checkpoint = checkpoint
	}
	return record, nil
}

// addLocalWorktree creates the worktree, retrying once under a suffixed branch
// name when the branch already exists (a re-dispatched task keeps its id, so
// its branch can survive from the previous run).
// taskBranchPlan is the decision about which branch a task's worktree checks
// out and which commit it starts from.
type taskBranchPlan struct {
	// name is the branch to check out or create.
	name string
	// base is the commit the worktree starts from.
	base string
	// continues is true when base is an earlier turn's branch tip rather than
	// the user's HEAD, so the checkout already carries that turn's work.
	continues bool
	// priorState is the user snapshot that branch is recorded as already
	// carrying. Set only when continues is true; it is the merge base for this
	// turn's replay when the user's HEAD has not moved.
	priorState string
	// priorUserHead is the user's HEAD priorState was captured against.
	priorUserHead string
	// priorCheckpoint is the commit that branch was recorded at and still
	// contains — this turn's proof that the branch is the conversation's. The
	// branch tip this turn starts from descends from it, so everything after
	// prepare measures against that tip instead.
	priorCheckpoint string
	// reset is true when the conversation's branch exists but is fully merged
	// into HEAD, and this task restarts it there.
	reset bool
	// conversational is true when name is keyed to the conversation rather than
	// to this one task, so a sibling task may legitimately want it too.
	conversational bool
	// tracksState is true when a later turn may continue this branch, and it
	// therefore has to record what the user's directory looked like.
	tracksState bool
	// owner is the identity the branch is recorded under.
	owner branchOwner
	// replayAbandoned is the fingerprint of a replay an earlier turn skipped.
	// The recorded tree does not contain it. Empty when that tree is what the
	// branch carries.
	replayAbandoned string
}

// altName disambiguates a branch a live sibling already holds.
func (p taskBranchPlan) altName(taskID string) string {
	if p.conversational {
		// Task-scoped, and readable next to the conversation branch it forked
		// from: agent/j/mul-6881-<task>.
		return p.name + "-" + taskKey(taskID)
	}
	// Already task-scoped, so only a re-run of the same task can collide here.
	return fmt.Sprintf("%s-%d", p.name, time.Now().Unix())
}

// resolveTaskBranch picks the branch for this task.
//
// Tasks that belong to a conversation — the turns of one issue, one chat
// session — share a branch, because they are one line of work: the user says
// "now also fix the caller" and expects the agent to be standing on what it
// wrote a minute ago. Keying the branch to the task instead gave every turn its
// own branch forked from HEAD, so turn two started in a tree that did not
// contain turn one's work and nothing said so (MUL-6881). Per-task env roots
// stay as they are — sibling tasks still run concurrently, they just deliver
// onto the branch their conversation owns.
//
// Continuing a branch is decided by its recorded OWNER, never by its name.
// `agent/j/mul-6881` is a name the user can type, another agent of the same
// display name can produce, and a second workspace can mint for a different
// issue; appending to any of those would silently mix two lines of work. So a
// same-named branch we cannot prove is ours pushes this task onto
// `agent/j/mul-6881-<fingerprint>` — stable for this exact conversation, so its
// own follow-ups continue it — and, if that is somehow taken too, onto a
// task-scoped branch that continues nothing.
func resolveTaskBranch(gitRoot string, params LocalWorktreeParams, headSHA string, logger *slog.Logger) taskBranchPlan {
	agentSegment := sanitizeName(params.AgentName)
	taskScoped := taskBranchPlan{name: fmt.Sprintf("agent/%s/%s", agentSegment, taskKey(params.TaskID)), base: headSHA}

	owner := params.owner()
	if params.ConversationKey == "" || !owner.valid() {
		return taskScoped
	}
	// The issue's canonical line first (DENE-820). Only an EXISTING branch is
	// continued: inventing the canonical name on a machine that never had it
	// would claim to be that line while sharing no history with it.
	if canonical := strings.TrimSpace(params.CanonicalBranch); canonical != "" {
		if tip, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+canonical); err == nil && tip != "" {
			plan, ok := planForBranch(gitRoot, canonical, headSHA, owner, sameDeliveryLine, logger)
			if ok {
				return plan
			}
			if logger != nil {
				logger.Info("execenv: canonical delivery branch is not this issue's; not continuing it",
					"git_root", gitRoot, "branch", canonical)
			}
		}
	}
	preferred := fmt.Sprintf("agent/%s/%s", agentSegment, sanitizeName(params.ConversationKey))
	for _, name := range []string{preferred, preferred + "-" + owner.fingerprint()} {
		plan, ok := planForConversationBranch(gitRoot, name, headSHA, owner, logger)
		if ok {
			return plan
		}
		if logger != nil {
			logger.Info("execenv: branch exists but is not this conversation's; not continuing it",
				"git_root", gitRoot, "branch", name)
		}
	}
	return taskScoped
}

// planForConversationBranch reports how this task would use one candidate
// branch name, and whether it may use it at all.
func planForConversationBranch(gitRoot, name, headSHA string, owner branchOwner, logger *slog.Logger) (taskBranchPlan, bool) {
	return planForBranch(gitRoot, name, headSHA, owner, sameOwner, logger)
}

// ownerMatch decides whether a branch's recorded owner lets this task
// continue it. sameOwner is the conversation-branch rule: all three ids.
// sameDeliveryLine is the canonical-branch rule (DENE-820): the issue is the
// delivery aggregate, so any seat of the same workspace working the same
// conversation continues its canonical line; the agent id may differ.
type ownerMatch func(recorded, owner branchOwner) bool

func sameOwner(recorded, owner branchOwner) bool { return recorded == owner }

func sameDeliveryLine(recorded, owner branchOwner) bool {
	return recorded.WorkspaceID == owner.WorkspaceID && recorded.ConversationID == owner.ConversationID
}

func planForBranch(gitRoot, name, headSHA string, owner branchOwner, match ownerMatch, logger *slog.Logger) (taskBranchPlan, bool) {
	plan := taskBranchPlan{
		name:           name,
		base:           headSHA,
		conversational: true,
		tracksState:    true,
		owner:          owner,
	}
	tip, err := runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	if err != nil || tip == "" {
		// Free to create.
		return plan, true
	}
	record, owned := branchOwnedByMatch(gitRoot, name, owner, match, logger)
	if !owned {
		return taskBranchPlan{}, false
	}
	// The user merged it: the branch tip carries nothing HEAD does not, so
	// continuing from it would strand this task behind their own commits.
	if _, mergedErr := runGit(gitRoot, "merge-base", "--is-ancestor", name, "HEAD"); mergedErr == nil {
		plan.reset = true
		return plan, true
	}
	plan.base = tip
	plan.continues = true
	plan.priorState = record.state
	plan.priorUserHead = record.userHead
	if plan.priorUserHead == "" {
		plan.priorUserHead = recoverCarriedUserHead(gitRoot, record.state)
	}
	plan.replayAbandoned = record.replayAbandoned
	plan.priorCheckpoint = record.checkpoint
	if logger != nil {
		logger.Info("execenv: continuing the conversation's existing branch",
			"git_root", gitRoot, "branch", name, "tip", tip)
	}
	return plan, true
}

// branchOwnedBy reports whether a branch is still the one this conversation
// recorded, returning the user snapshot it carries.
//
// Two questions, and both have to hold. Is the record ours — workspace, agent
// and conversation ids. And is the branch still the one it was written against
// — the recorded checkpoint has to be an ancestor of the current tip. The
// second is not pedantry: a branch the user deleted and recreated under the
// same name still satisfies the first, and continuing it would append this
// conversation onto their unrelated work.
//
// A branch with no record is not ours by definition: every branch this code
// creates writes one before its task is allowed to run.
func branchOwnedBy(gitRoot, branch string, owner branchOwner, logger *slog.Logger) (branchRecord, bool) {
	return branchOwnedByMatch(gitRoot, branch, owner, sameOwner, logger)
}

func branchOwnedByMatch(gitRoot, branch string, owner branchOwner, match ownerMatch, logger *slog.Logger) (branchRecord, bool) {
	ref, err := readUserStateRef(gitRoot, branch)
	if err != nil || ref == "" {
		return branchRecord{}, false
	}
	record, err := readBranchRecord(gitRoot, ref)
	if err != nil || !record.owner.valid() || !match(record.owner, owner) || record.checkpoint == "" {
		return branchRecord{}, false
	}
	if _, err := runGit(gitRoot, "merge-base", "--is-ancestor", record.checkpoint, "refs/heads/"+branch); err != nil {
		if logger != nil {
			logger.Info("execenv: branch no longer contains the commit it was recorded at; not continuing it",
				"git_root", gitRoot, "branch", branch, "checkpoint", record.checkpoint)
		}
		return branchRecord{}, false
	}
	return record, true
}

// addLocalWorktree materialises the planned branch as a worktree and reports
// the branch actually used, plus whether this call is the one that put it
// there — the caller may only delete a branch it created itself.
//
// The fallback covers a sibling task already holding the conversation's branch:
// git allows one worktree per branch, and refusing to run is worse than
// delivering onto a task-scoped branch. It forks from the same base, so the
// sibling still stands on the conversation's latest work.
func addLocalWorktree(gitRoot, worktreePath string, plan taskBranchPlan, taskID string, scope sparsecheckout.Scope) (string, bool, error) {
	var args []string
	switch {
	case plan.continues:
		args = []string{"worktree", "add", worktreePath, plan.name}
	case plan.reset:
		// -B moves the branch to base. Nothing is lost: this path only runs
		// once the branch has been proven an ancestor of HEAD.
		args = []string{"worktree", "add", "-B", plan.name, worktreePath, plan.base}
	default:
		args = []string{"worktree", "add", "-b", plan.name, worktreePath, plan.base}
	}
	if scope.Active() {
		args = append([]string{"worktree", "add", "--no-checkout"}, args[2:]...)
	}
	out, err := runGit(gitRoot, args...)
	if err != nil {
		if !branchUnavailable(out) {
			return "", false, fmt.Errorf("execenv: git worktree add: %s: %w", strings.TrimSpace(out), err)
		}
		alt := plan.altName(taskID)
		args = []string{"worktree", "add", "-b", alt, worktreePath, plan.base}
		if scope.Active() {
			args = []string{"worktree", "add", "--no-checkout", "-b", alt, worktreePath, plan.base}
		}
		if out, err = runGit(gitRoot, args...); err != nil {
			return "", false, fmt.Errorf("execenv: git worktree add: %s: %w", strings.TrimSpace(out), err)
		}
		plan.name = alt
		plan.continues = false
	}
	created := !plan.continues
	if scope.Active() {
		if err := materializeLocalSparse(gitRoot, worktreePath, plan.name, created, scope); err != nil {
			return "", false, err
		}
	}
	return plan.name, created, nil
}

// materializeLocalSparse checks out only the declared cone into a worktree
// that was added with --no-checkout. Failure removes the worktree, and the
// branch too when this call created it, so a retry does not collide with a
// half-built checkout.
func materializeLocalSparse(gitRoot, worktreePath, branch string, created bool, scope sparsecheckout.Scope) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	fail := func(err error) error {
		removeLocalWorktreeDir(gitRoot, worktreePath, nil)
		if created {
			dropBranch(gitRoot, branch, nil)
		}
		return err
	}
	if err := sparsecheckout.Enable(ctx, worktreePath, scope); err != nil {
		return fail(fmt.Errorf("execenv: sparse checkout: %w", err))
	}
	if out, err := runGit(worktreePath, "checkout"); err != nil {
		return fail(fmt.Errorf("execenv: sparse checkout: %s: %w", strings.TrimSpace(out), err))
	}
	if err := sparsecheckout.Finish(ctx, worktreePath); err != nil {
		return fail(fmt.Errorf("execenv: sparse checkout: %w", err))
	}
	return nil
}

// branchUnavailable recognises git refusing a branch that another worktree
// holds, or that already exists under a name we meant to create.
func branchUnavailable(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "already exists") ||
		strings.Contains(lower, "already checked out") ||
		strings.Contains(lower, "already used by worktree")
}

// replayResult is what the replay left in the worktree.
type replayResult struct {
	// conflicts names the files git could not merge. Non-empty means the
	// worktree holds an unresolved merge, on purpose.
	conflicts []string
	// skippedNotice explains a replay this turn left out because the same one
	// still conflicted. Empty when the replay ran or there was nothing to replay.
	skippedNotice string
	// abandoned is the fingerprint recorded with the branch when the replay
	// was skipped. The recorded tree stays the snapshot the branch already
	// carries, so the next turn can try this replay again.
	abandoned string
}

// replayUserState brings the user's directory into the worktree.
//
// A fresh branch is checked out at the user's HEAD, so the whole snapshot
// applies and cannot conflict. A continued branch already carries the previous
// turn. What it still needs is the user's uncommitted work, not the commits
// that landed on their own branch in between.
//
// The snapshot commit is parented at the user HEAD it was taken from, and the
// branch record remembers that HEAD (Multica-User-Head, or, for a record
// written before the trailer, the snapshot commit's parent). Two cases:
//
//   - That HEAD is still the user's HEAD. The tree-to-tree diff is exactly the
//     uncommitted delta, so cherry-picking it replays "what changed since last
//     turn" and does not re-propose lines the agent already edited. This is the
//     path a follow-up comment takes when the user did not commit.
//   - That HEAD moved. The tree-to-tree diff now contains every commit in
//     between, and cherry-picking it onto the conversation branch imports the
//     user's branch as if it were a dirty working tree — conflicting on any
//     file the agent also changed, and, because a conflict does not record the
//     snapshot, repeating forever (DENE-814). A moved HEAD replays only
//     diff(HEAD, snapshot): the uncommitted remainder. A clean checkout
//     therefore replays nothing. An uncommitted patch identical to the one
//     already carried is not applied a second time.
//
// The tradeoff is that a clean checkout after the user's branch moves does not
// strip uncommitted work a previous turn already carried onto this branch, and
// does not bring the new commits onto it either. This branch is a separate
// line of work. The other way — rebasing the old uncommitted patch onto the
// new HEAD and three-way merging — conflicts exactly when those commits touched
// the same lines, which is the failure being removed.
//
// A conflict that is still real (the user rewrote lines the agent also rewrote)
// stays in the worktree for the agent the first time. The same replay is tried
// again on the next turn. When it still conflicts, this turn skips it so
// delivery is not blocked, and the recorded snapshot stays the one the branch
// actually carries — recording the user's tree here would make the next diff
// empty and the edit would never land. A later turn applies it once the branch
// can take it. Records already in refs/multica/local-state/* are not rewritten.
// The next continuation reads the old user HEAD from the trailer or the
// snapshot parent, so a clean checkout stops replaying committed history.
// Commits an earlier buggy replay already put on the branch stay there until
// someone resets the branch. An abandoned fingerprint on a record whose tree
// was already advanced (the unmerged first cut of this fix) is not repaired:
// that tree diff is empty, and there is no earlier snapshot left to replay.
func replayUserState(worktreePath string, plan taskBranchPlan, snapshot string, logger *slog.Logger) (replayResult, error) {
	if !plan.continues {
		return replayIncrement(worktreePath, plan.base, snapshot, plan, logger)
	}
	newHead, err := runGitTrimmed(worktreePath, "rev-parse", "--verify", snapshot+"^")
	if err != nil || newHead == "" {
		return replayResult{}, fmt.Errorf("execenv: local-directory snapshot %s has no parent, so uncommitted edits cannot be separated from committed history: %w", shortID(snapshot), err)
	}
	oldHead := plan.priorUserHead
	if oldHead == "" && plan.priorState != "" {
		oldHead = recoverCarriedUserHead(worktreePath, plan.priorState)
	}
	if oldHead != "" && oldHead == newHead {
		return replayIncrement(worktreePath, plan.priorState, snapshot, plan, logger)
	}
	if logger != nil {
		logger.Info("execenv: user HEAD moved since this branch's snapshot; replaying uncommitted edits only",
			"branch", plan.name, "recorded_head", shortID(oldHead), "current_head", shortID(newHead))
	}
	// Clean working tree: the commits between the two HEADs are the whole
	// difference, and they are not local edits.
	if _, diffErr := runGit(worktreePath, "diff", "--quiet", newHead, snapshot); diffErr == nil {
		clearReplayAttempt(worktreePath, plan.name, logger)
		return replayResult{}, nil
	}
	if oldHead != "" && plan.priorState != "" && uncommittedPatchesEqual(worktreePath, oldHead, plan.priorState, newHead, snapshot) {
		clearReplayAttempt(worktreePath, plan.name, logger)
		if logger != nil {
			logger.Info("execenv: uncommitted edits are unchanged since the previous turn; not replaying them again",
				"branch", plan.name)
		}
		return replayResult{}, nil
	}
	return replayIncrement(worktreePath, newHead, snapshot, plan, logger)
}

// replayIncrement cherry-picks the difference between carried and snapshot
// onto the worktree. carried is the merge base: the previous snapshot when
// the user's HEAD has not moved, the user's current HEAD when only the
// uncommitted remainder should apply, and the branch base for a fresh branch.
func replayIncrement(worktreePath, carried, snapshot string, plan taskBranchPlan, logger *slog.Logger) (replayResult, error) {
	if carried == "" {
		return replayResult{}, fmt.Errorf("execenv: no baseline to replay the local directory against for branch %s", plan.name)
	}
	// Nothing new since the state this checkout already carries. On a follow-up
	// turn that is the ordinary case: the user commented, they did not edit.
	if _, err := runGit(worktreePath, "diff", "--quiet", carried, snapshot); err == nil {
		clearReplayAttempt(worktreePath, plan.name, logger)
		return replayResult{}, nil
	}

	fingerprint, fpErr := replayFingerprint(worktreePath, carried, snapshot)
	treeFingerprint, treeErr := replayTreeFingerprint(worktreePath, carried, snapshot)
	// Two ways to recognise a replay that already conflicted. The attempt ref
	// is keyed to the carried commit, so a resolution that committed nothing
	// (and rewrote that commit) is offered again. The abandoned trailer is
	// keyed to the trees, so a skip — which keeps those trees and may rewrite
	// the record — is tried again without blocking delivery, and is not
	// recorded as delivered.
	var repeated replayAttempt
	repeat := false
	if plan.continues && treeErr == nil && plan.replayAbandoned == treeFingerprint {
		repeated = replayAttempt{fingerprint: fingerprint, count: 1, snapshot: snapshot}
		if head, headErr := runGitTrimmed(worktreePath, "rev-parse", "--verify", "--quiet", snapshot+"^"); headErr == nil {
			repeated.userHead = head
		}
		if prev, ok := readReplayAttempt(worktreePath, plan.name); ok && len(prev.files) > 0 {
			repeated.files = prev.files
		}
		repeat = true
	} else if plan.continues && fpErr == nil {
		if prev, ok := readReplayAttempt(worktreePath, plan.name); ok && prev.fingerprint == fingerprint && prev.count >= 1 {
			repeated = prev
			repeat = true
		}
	}

	// A commit whose parent is the carried state and whose tree is the user's
	// current one. Its parent is what git uses as the merge base, and that is
	// the entire point: it is not reachable any other way.
	args := append(commitIdentityArgs(worktreePath), "commit-tree", snapshot+"^{tree}", "-p", carried,
		"-m", "multica: local directory edits to replay")
	increment, err := runGitTrimmed(worktreePath, args...)
	if err != nil || increment == "" {
		return replayResult{}, fmt.Errorf("execenv: could not describe your local edits for replay into the task worktree: %w", err)
	}

	// A file outside the cone is staged by cherry-pick and left off disk.
	// Widen first so the user's edit is actually in the worktree.
	if names, nameErr := runGit(worktreePath, "diff", "--name-only", carried, snapshot); nameErr != nil {
		return replayResult{}, fmt.Errorf("execenv: list local edits before replay: %s: %w", strings.TrimSpace(names), nameErr)
	} else if err := widenSparseForReplay(worktreePath, names); err != nil {
		return replayResult{}, err
	}

	out, pickErr := runGit(worktreePath, "cherry-pick", "--no-commit", increment)
	if pickErr == nil {
		clearReplayAttempt(worktreePath, plan.name, logger)
		return replayResult{}, nil
	}
	conflicts, listErr := unmergedPaths(worktreePath)
	if listErr != nil || len(conflicts) == 0 {
		if listErr == nil && cherryPickIsEmpty(out) {
			// The uncommitted patch is already on the branch. Nothing to apply.
			abortCherryPick(worktreePath, logger)
			clearReplayAttempt(worktreePath, plan.name, logger)
			return replayResult{}, nil
		}
		// Not a conflict, so the replay failed for a reason the agent cannot
		// resolve. Fail closed rather than start on a half-applied tree.
		abortCherryPick(worktreePath, logger)
		return replayResult{}, fmt.Errorf("execenv: could not replay your local edits into the task worktree "+
			"(the agent would have seen a different tree than you have): %s: %w", strings.TrimSpace(out), pickErr)
	}
	if !plan.continues {
		// Unreachable by construction: a fresh branch is checked out at the
		// increment's own parent, so there is nothing for git to disagree with.
		// If it ever happens the tree is not one the user would recognise, and
		// the old fail-closed rule is the right one.
		abortCherryPick(worktreePath, logger)
		return replayResult{}, fmt.Errorf("execenv: could not replay your local edits onto a fresh task worktree: %s: %w",
			strings.TrimSpace(out), pickErr)
	}
	if repeat {
		abortCherryPick(worktreePath, logger)
		if still, stillErr := unmergedPaths(worktreePath); stillErr == nil && len(still) == 0 {
			if len(repeated.files) == 0 {
				repeated.files = conflicts
			}
			if repeated.snapshot == "" {
				repeated.snapshot = snapshot
			}
			if writeErr := writeReplayAttempt(worktreePath, plan.name, repeated); writeErr != nil && logger != nil {
				logger.Warn("execenv: could not keep the skipped replay on record, so the next turn may offer the conflict again",
					"branch", plan.name, "error", writeErr)
			}
			notice := replaySkippedNotice(plan.name, repeated)
			if logger != nil {
				logger.Warn("execenv: local-directory replay still conflicts; leaving it outstanding and not blocking delivery",
					"branch", plan.name, "fingerprint", treeFingerprint, "files", conflicts)
			}
			abandoned := treeFingerprint
			if abandoned == "" {
				abandoned = fingerprint
			}
			return replayResult{skippedNotice: notice, abandoned: abandoned}, nil
		}
		// The worktree is still conflicted. Hand that to the agent rather than
		// claiming the replay was left out of a clean tree.
	}

	// Keep the conflict, drop only the sequencer state: the agent should see an
	// ordinary conflicted worktree it can resolve with `git status` / `git add`,
	// not a cherry-pick it is expected to conclude with a command it never
	// started.
	if out, quitErr := runGit(worktreePath, "cherry-pick", "--quit"); quitErr != nil && logger != nil {
		logger.Warn("execenv: could not clear the cherry-pick state after a conflicting replay (non-fatal)",
			"path", worktreePath, "output", strings.TrimSpace(out), "error", quitErr)
	}
	if fpErr == nil {
		attempt := replayAttempt{fingerprint: fingerprint, count: 1, files: conflicts, snapshot: snapshot}
		if head, headErr := runGitTrimmed(worktreePath, "rev-parse", "--verify", "--quiet", snapshot+"^"); headErr == nil {
			attempt.userHead = head
		}
		if prev, ok := readReplayAttempt(worktreePath, plan.name); ok && prev.fingerprint == fingerprint && prev.count >= 1 {
			attempt.count = prev.count + 1
		}
		if writeErr := writeReplayAttempt(worktreePath, plan.name, attempt); writeErr != nil && logger != nil {
			logger.Warn("execenv: could not record the conflicting replay, so the next turn may retry it",
				"branch", plan.name, "error", writeErr)
		}
	} else if logger != nil {
		logger.Warn("execenv: could not fingerprint the conflicting replay, so the next turn may retry it",
			"branch", plan.name, "error", fpErr)
	}
	if logger != nil {
		logger.Warn("execenv: your uncommitted edits conflict with the work on this branch; handing the conflict to the agent",
			"path", worktreePath, "branch", plan.name, "files", conflicts, "snapshot", shortID(snapshot))
	}
	return replayResult{conflicts: conflicts}, nil
}

func cherryPickIsEmpty(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "empty") || strings.Contains(lower, "nothing to commit")
}

func uncommittedPatchesEqual(dir, oldHead, oldTree, newHead, newTree string) bool {
	oldPatch, err := runGitStdout(dir, "diff", "--no-ext-diff", oldHead, oldTree)
	if err != nil {
		return false
	}
	newPatch, err := runGitStdout(dir, "diff", "--no-ext-diff", newHead, newTree)
	if err != nil {
		return false
	}
	return oldPatch == newPatch
}

func replayFingerprint(dir, carried, snapshot string) (string, error) {
	// The carried commit, not only its tree. Rewriting the record — a new
	// checkpoint after the user commits on the branch, or a resolution that
	// committed nothing — changes this id, so the next turn offers the
	// conflict again instead of treating that resolution as final.
	tree, err := runGitTrimmed(dir, "rev-parse", "--verify", snapshot+"^{tree}")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(carried + "\n" + tree))
	return hex.EncodeToString(sum[:]), nil
}

// replayTreeFingerprint identifies the outstanding edit itself. The record
// commit changes when a skipped replay is written down again, and the edit
// has to stay recognisable across that rewrite or the next turn forgets it.
func replayTreeFingerprint(dir, carried, snapshot string) (string, error) {
	carriedTree, err := runGitTrimmed(dir, "rev-parse", "--verify", carried+"^{tree}")
	if err != nil {
		return "", err
	}
	tree, err := runGitTrimmed(dir, "rev-parse", "--verify", snapshot+"^{tree}")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("tree\n" + carriedTree + "\n" + tree))
	return hex.EncodeToString(sum[:]), nil
}

// recoverCarriedUserHead reads the user HEAD a carried snapshot was taken
// against. New records store it in a trailer. Older ones parent the record on
// the snapshot commit (possibly through earlier records that re-saved the same
// state), and that commit's parent is the user HEAD.
func recoverCarriedUserHead(dir, commit string) string {
	cur := commit
	for i := 0; i < 32 && cur != ""; i++ {
		body, err := runGitTrimmed(dir, "log", "-1", "--format=%B", cur)
		if err != nil {
			return ""
		}
		if head := trailerValue(body, userHeadTrailer); head != "" {
			return head
		}
		if strings.HasPrefix(body, snapshotCommitTitle) {
			parent, err := runGitTrimmed(dir, "rev-parse", "--verify", cur+"^")
			if err != nil {
				return ""
			}
			return parent
		}
		parent, err := runGitTrimmed(dir, "rev-parse", "--verify", "--quiet", cur+"^1")
		if err != nil || parent == "" || parent == cur {
			return ""
		}
		cur = parent
	}
	return ""
}

func trailerValue(body, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func (w *LocalWorktree) resolvedUserHead() string {
	if w == nil {
		return ""
	}
	if w.userHead != "" {
		return w.userHead
	}
	if w.userState == "" || w.GitRoot == "" {
		return ""
	}
	if head := recoverCarriedUserHead(w.GitRoot, w.userState); head != "" {
		return head
	}
	parent, err := runGitTrimmed(w.GitRoot, "rev-parse", "--verify", "--quiet", w.userState+"^")
	if err != nil {
		return ""
	}
	return parent
}

// unmergedReplayError is the delivery refusal for a replay that is still
// conflicted. It names the snapshot, because the user's checkout can be clean
// while an earlier snapshot is what failed to merge.
func (w *LocalWorktree) unmergedReplayError(unmerged []string) error {
	snapshot := shortID(w.userState)
	head := shortID(w.resolvedUserHead())
	if head == "" {
		head = "unknown"
	}
	prior := ""
	if w.priorState != "" {
		prior = fmt.Sprintf(" The branch's previously recorded snapshot was %s. This conflict is that replay against the branch, not a dirty file in your checkout.", shortID(w.priorState))
	}
	return fmt.Errorf(
		"refusing to deliver branch %s: replaying local-directory snapshot %s (user HEAD %s when the snapshot was taken) left %s unmerged in the task worktree.%s "+
			"The worktree is preserved at %s (listed by `git worktree list` in %s) — resolve the conflict there, or re-run the task and let the agent finish the merge. "+
			"The same replay conflict is offered once. If it still conflicts, the next run skips it without recording those edits as delivered, and tries again once the branch can take them",
		w.Branch, snapshot, head, quotedPaths(unmerged), prior, w.Path, w.GitRoot)
}

type replayAttempt struct {
	fingerprint string
	count       int
	files       []string
	snapshot    string
	userHead    string
}

func replayAttemptRef(branch string) string {
	return localReplayAttemptRefPrefix + branch
}

func replaySkippedNotice(branch string, prev replayAttempt) string {
	files := "the files that conflicted last time"
	if len(prev.files) > 0 {
		files = quotedPaths(prev.files)
	}
	snapshot := shortID(prev.snapshot)
	if snapshot == "" {
		snapshot = "unknown"
	}
	head := shortID(prev.userHead)
	if head == "" {
		head = "unknown"
	}
	return fmt.Sprintf(
		"Replay of local-directory snapshot %s (user HEAD %s when it was taken) onto branch %s was skipped. The same conflict already happened once, on %s, and it still conflicts, so this turn left those edits out rather than block delivery. This worktree does not contain that replay: the edits are still in the user's checkout and they are not on this branch. Say that in your reply. They are not recorded as delivered; a later turn applies them if this branch can take them.",
		snapshot, head, branch, files)
}

func readReplayAttempt(dir, branch string) (replayAttempt, bool) {
	commit, err := runGitTrimmed(dir, "rev-parse", "--verify", "--quiet", replayAttemptRef(branch))
	if err != nil || commit == "" {
		return replayAttempt{}, false
	}
	body, err := runGitTrimmed(dir, "log", "-1", "--format=%B", commit)
	if err != nil {
		return replayAttempt{}, false
	}
	var attempt replayAttempt
	for _, line := range strings.Split(body, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case replayFingerprintTrailer:
			attempt.fingerprint = value
		case replayCountTrailer:
			attempt.count, _ = strconv.Atoi(value)
		case replayFileTrailer:
			if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
				attempt.files = append(attempt.files, unquoted)
			} else if value != "" {
				attempt.files = append(attempt.files, value)
			}
		case replaySnapshotTrailer:
			attempt.snapshot = value
		case replayUserHeadTrailer:
			attempt.userHead = value
		}
	}
	if attempt.fingerprint == "" || attempt.count < 1 {
		return replayAttempt{}, false
	}
	return attempt, true
}

func writeReplayAttempt(dir, branch string, attempt replayAttempt) error {
	tree, err := runGitTrimmed(dir, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("multica: local directory replay attempt\n\n")
	b.WriteString("Records that a replay onto this branch conflicted, so the next turn can stop retrying the same one.\n\n")
	fmt.Fprintf(&b, "%s: %s\n", replayFingerprintTrailer, attempt.fingerprint)
	fmt.Fprintf(&b, "%s: %d\n", replayCountTrailer, attempt.count)
	fmt.Fprintf(&b, "%s: %s\n", replaySnapshotTrailer, attempt.snapshot)
	fmt.Fprintf(&b, "%s: %s\n", replayUserHeadTrailer, attempt.userHead)
	for _, file := range attempt.files {
		fmt.Fprintf(&b, "%s: %s\n", replayFileTrailer, strconv.Quote(file))
	}
	args := append(commitIdentityArgs(dir), "commit-tree", tree, "-m", b.String())
	commit, err := runGitTrimmed(dir, args...)
	if err != nil || commit == "" {
		return fmt.Errorf("git commit-tree: %w", err)
	}
	if out, err := runGit(dir, "update-ref", replayAttemptRef(branch), commit); err != nil {
		return fmt.Errorf("git update-ref: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

func clearReplayAttempt(dir, branch string, logger *slog.Logger) {
	if branch == "" {
		return
	}
	if out, err := runGit(dir, "update-ref", "-d", replayAttemptRef(branch)); err != nil && logger != nil {
		logger.Debug("execenv: no replay-attempt record to clear",
			"branch", branch, "output", strings.TrimSpace(out))
	}
}

func widenSparseForReplay(worktreePath, diffNames string) error {
	names := sparsecheckout.DiffNames(diffNames)
	if len(names) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	if err := sparsecheckout.WidenToCover(ctx, worktreePath, names); err != nil {
		return fmt.Errorf("execenv: could not include local edits that sit outside this task's sparse checkout: %w", err)
	}
	return nil
}

// quotedPaths renders repository paths for a human-facing message. Quoted
// because a git path may contain newlines, quotes or control characters, and
// this string is read in logs and task errors where a raw one would look like
// several entries.
func quotedPaths(paths []string) string {
	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, strconv.Quote(path))
	}
	return strings.Join(quoted, ", ")
}

// verifyDeliveryPoint checks that tip is a commit this conversation can put its
// name on before it becomes the branch's recorded checkpoint.
//
// Two things are asserted, and they are the two ways a delivery can be
// something other than what this task built. The tip has to BE the task's
// branch — a run that checked out something else, or a branch someone moved
// underneath it, delivers a commit this record has no business describing. And
// it has to still contain the commit this turn started from — this turn's own
// baseline when it made one, otherwise the branch tip it continued. A run that
// resets its worktree back to the user's own HEAD passes neither test but the
// second is the one that matters, twice over: recording a plain user commit as
// the checkpoint is what makes a branch they later recreate there look like
// ours, and a tip without this turn's starting point no longer carries the
// snapshot about to be recorded as delivered (MUL-6881 review).
func (w *LocalWorktree) verifyDeliveryPoint(tip string) error {
	if !w.tracksState {
		// Nothing will be recorded for this branch, so there is nothing to prove.
		return nil
	}
	if tip == "" {
		return errors.New("the task worktree has no resolvable HEAD")
	}
	branchTip, err := runGitTrimmed(w.GitRoot, "rev-parse", "--verify", "refs/heads/"+w.Branch)
	if err != nil {
		return fmt.Errorf("resolve branch %s: %w", w.Branch, err)
	}
	if branchTip != tip {
		return fmt.Errorf("the worktree delivered %s while branch %s points at %s, so the run did not deliver onto its own branch",
			shortID(tip), w.Branch, shortID(branchTip))
	}
	if w.BaseCommit == "" {
		return fmt.Errorf("branch %s has no commit of this task's own to prove it by", w.Branch)
	}
	if _, err := runGit(w.GitRoot, "merge-base", "--is-ancestor", w.BaseCommit, tip); err != nil {
		return fmt.Errorf("the delivered commit %s no longer contains %s, the commit this turn started from",
			shortID(tip), shortID(w.BaseCommit))
	}
	return nil
}

// unmergedPaths lists the files git considers unresolved in a worktree.
func unmergedPaths(worktreePath string) ([]string, error) {
	out, err := runGitStdout(worktreePath, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range strings.Split(out, "\x00") {
		if name != "" {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// abortCherryPick returns the worktree to the branch tip. Used only where the
// conflict is not something the agent can act on; the ordinary conflict path
// deliberately leaves the worktree as git left it.
func abortCherryPick(worktreePath string, logger *slog.Logger) {
	for _, args := range [][]string{{"cherry-pick", "--quit"}, {"reset", "--hard", "HEAD"}, {"clean", "-fdq"}} {
		if out, err := runGit(worktreePath, args...); err != nil && logger != nil {
			logger.Warn("execenv: could not restore the task worktree after a failed replay",
				"path", worktreePath, "command", args[0], "output", strings.TrimSpace(out), "error", err)
		}
	}
}

// userStateRef is where a branch's record lives: the snapshot of the user's
// directory it already carries, the conversation it belongs to, and the tip it
// was recorded at.
func userStateRef(branch string) string {
	return localStateRefPrefix + branch
}

// readUserStateRef returns the recorded snapshot, or "" when the branch has
// none.
func readUserStateRef(gitRoot, branch string) (string, error) {
	return runGitTrimmed(gitRoot, "rev-parse", "--verify", "--quiet", userStateRef(branch))
}

// recordState pins the user's directory as this task saw it together with the
// commit the branch stands at. Two things depend on the record: the next turn
// replays from its tree, and every later task proves the branch is still its
// own from its owner and checkpoint.
//
// The checkpoint must be a commit the branch could not plausibly be sitting at
// WITHOUT this conversation's work — that is the whole proof. A tip that is
// still the user's own HEAD is not one: it is exactly where a branch they
// delete and recreate lands, and recording it would authorise a later turn to
// append onto their unrelated work. Prepare therefore records only once the
// branch carries a commit of ours, and Finalize records the tip it actually
// delivered.
func (w *LocalWorktree) recordState(checkpoint string, logger *slog.Logger) error {
	if w == nil || !w.tracksState || w.Branch == "" || w.userState == "" {
		return nil
	}
	if _, err := writeBranchRecord(w.GitRoot, w.Branch, w.userState, checkpoint, w.owner, w.resolvedUserHead(), w.replayAbandoned); err != nil {
		return err
	}
	if logger != nil {
		logger.Debug("execenv: recorded the local-directory snapshot for the task branch",
			"branch", w.Branch, "checkpoint", checkpoint)
	}
	return nil
}

// dropBranch deletes a task branch that carries nothing worth keeping, together
// with its recorded snapshot — the two are meaningless apart.
func dropBranch(gitRoot, branch string, logger *slog.Logger) {
	if branch == "" {
		return
	}
	deleteBranch(gitRoot, branch, logger)
	if out, err := runGit(gitRoot, "update-ref", "-d", userStateRef(branch)); err != nil && logger != nil {
		logger.Debug("execenv: no local-directory snapshot to drop for task branch",
			"branch", branch, "output", strings.TrimSpace(out))
	}
	clearReplayAttempt(gitRoot, branch, logger)
}

// pruneOrphanedStateRefs drops the snapshot of any branch that is no longer
// there. Multica deletes both together, but the branch is the user's to delete,
// rename or merge away at any time, and a ref left behind would pin their whole
// working tree as of some past turn against `git gc` forever.
//
// Best-effort and non-fatal: this is housekeeping in the user's repository, not
// a precondition for the task.
func pruneOrphanedStateRefs(gitRoot string, logger *slog.Logger) {
	pruneOrphanedRefs(gitRoot, localStateRefPrefix, logger)
	pruneOrphanedRefs(gitRoot, localReplayAttemptRefPrefix, logger)
}

func pruneOrphanedRefs(gitRoot, prefix string, logger *slog.Logger) {
	out, err := runGitTrimmed(gitRoot, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		if logger != nil {
			logger.Debug("execenv: could not list local-directory snapshots", "git_root", gitRoot, "error", err)
		}
		return
	}
	for _, ref := range strings.Split(out, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		branch := strings.TrimPrefix(ref, prefix)
		if branch == ref {
			continue
		}
		if _, headErr := runGit(gitRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); headErr == nil {
			continue
		}
		if out, delErr := runGit(gitRoot, "update-ref", "-d", ref); delErr != nil {
			if logger != nil {
				logger.Warn("execenv: could not drop the snapshot of a deleted task branch (non-fatal)",
					"ref", ref, "output", strings.TrimSpace(out), "error", delErr)
			}
			continue
		}
		if logger != nil {
			logger.Info("execenv: dropped the local-directory snapshot of a branch that no longer exists",
				"git_root", gitRoot, "branch", branch)
		}
	}
}

// checkUntrackedReplayable refuses a directory whose untracked content is too
// large to reproduce faithfully, before anything is written anywhere.
//
// The bounds are the same ones the older file-copy replay enforced, and they
// exist for the same reason: `--exclude-standard` already drops everything
// gitignored, so a repo past them is one whose build output was never ignored.
// Snapshotting it would write every byte into the user's own object database.
// The untracked symlink case is refused for a narrower reason — it is content
// the user can see, and this replay does not decide whether to reproduce the
// link or its target, including targets outside the repo.
func checkUntrackedReplayable(gitRoot string, logger *slog.Logger) error {
	out, err := runGitStdout(gitRoot, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("execenv: could not list the untracked files in %q: %w", gitRoot, err)
	}
	var (
		files   int
		budget  int64 = maxUntrackedBytes
		skipped int
	)
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" || isMulticaSidecarPath(rel) {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(gitRoot, rel))
		if statErr != nil {
			// Listed a moment ago, unreadable now: the tree changed under us.
			// git will simply not find it either, so this is not a refusal.
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			skipped++
			if logger != nil {
				logger.Warn("execenv: untracked symlink cannot be replayed into a worktree", "file", rel)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			// Sockets, FIFOs, devices: not content, and git will not add them.
			continue
		}
		files++
		budget -= info.Size()
		if files > maxUntrackedFiles || budget < 0 {
			skipped++
		}
	}
	if skipped == 0 {
		return nil
	}
	return fmt.Errorf("execenv: cannot replay every untracked file from %q into a task worktree "+
		"(%d left over; the replay covers regular files up to %d files / %d MiB and does not follow symlinks) "+
		"— gitignore or clean up the untracked files, or switch the resource back to in_place",
		gitRoot, skipped, maxUntrackedFiles, maxUntrackedBytes>>20)
}

// multicaSidecarDirNames are the directories Prepare writes into a workdir. A
// task running in_place on the same directory leaves these present as
// untracked files for the length of its run, so a concurrent worktree snapshot
// sees them. CLAUDE.md / AGENTS.md are deliberately absent: those are
// ordinarily the user's own tracked files, and the runtime only injects a
// marker block into them, which CleanupRuntimeConfig removes.
var multicaSidecarDirNames = []string{
	".agent_context",
	".multica",
}

// isMulticaSidecarPath reports whether a repo-relative path is one of the
// daemon's own sidecars rather than the user's content. Matched as a whole
// path segment at ANY depth, not just the repo root: an in_place resource may
// point at a subdirectory of this repo, in which case its sidecars sit at
// <subdir>/.agent_context — replaying those would put another issue's brief
// inside this task's worktree and commit it to the delivered branch.
func isMulticaSidecarPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		for _, name := range multicaSidecarDirNames {
			if seg == name {
				return true
			}
		}
	}
	return false
}

// runGit runs git in dir and returns combined output. Callers inspect the
// output for git's own error text, so stdout and stderr stay merged.
func runGit(dir string, args ...string) (string, error) {
	return runGitEnv(dir, nil, args...)
}

// runGitEnv is runGit with extra environment entries, for the one caller that
// has to redirect GIT_INDEX_FILE.
func runGitEnv(dir string, extraEnv []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runGitTrimmed runs git for its stdout value, discarding stderr so a
// diagnostic line can't be mistaken for the value (`rev-parse` output, a
// config value, a stash sha).
func runGitTrimmed(dir string, args ...string) (string, error) {
	out, err := runGitStdout(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runGitTrimmedEnv is runGitTrimmed with extra environment entries.
func runGitTrimmedEnv(dir string, extraEnv []string, args ...string) (string, error) {
	out, err := runGitEnv(dir, extraEnv, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out), nil
}

// runGitStdout is runGitTrimmed without the trimming, for output where
// whitespace is significant — NUL-separated file listings, where a leading or
// trailing space is part of a filename.
func runGitStdout(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()

	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", withGitStderr(err)
	}
	return string(out), nil
}

// withGitStderr renders git's stderr into the error.
//
// cmd.Output() already captures stderr into ExitError.Stderr, but nothing ever
// read it back out, so every failure from this path reached the user as a bare
// "exit status 1" — git's own explanation was collected and then thrown away
// at the point it was needed. Discarding stderr from the RESULT is deliberate
// (see runGitTrimmed: a diagnostic line must never be mistaken for a value);
// discarding it from the error was not.
func withGitStderr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
	}
	return err
}
