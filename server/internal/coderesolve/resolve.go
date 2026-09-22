package coderesolve

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Resource types this package understands. Mirrors the discriminators in
// handler/project_resource.go.
const (
	ResourceTypeLocalDirectory = "local_directory"
	ResourceTypeGitHubRepo     = "github_repo"
)

// Execution modes for a local_directory. Mirrors the constants in
// handler/project_resource.go and internal/daemon/local_directory.go. The
// empty value means ModeInPlace, so rows written before parallel mode existed
// keep their original behaviour without a migration.
const (
	ModeInPlace  = "in_place"
	ModeWorktree = "worktree"
	ModeShared   = "shared"
)

// Resource is one project_resource row, as the server holds it.
type Resource struct {
	ID           string
	ResourceType string
	Ref          json.RawMessage
	Label        string
}

// Project is one project attached to the task, with its resources in POSITION
// order. Position order is not decoration: it is the answer to "which of my
// directories does this machine write?", and reordering the list in the UI is
// how a user changes that answer. Callers must preserve the order the database
// returned.
type Project struct {
	ID        string
	Resources []Resource
}

// Task is the decision's view of a run. Everything here is a value the server
// already holds when it builds a claim.
type Task struct {
	// ID and IssueIdentifier name the run's own directory, for the kinds that
	// need one.
	ID              string
	IssueIdentifier string
	// SessionID groups scratch work. A chat turn has one; an issue run does
	// not, and falls back to the task id.
	SessionID string
	// IsLeader marks a squad coordinator. A leader creates issues and
	// comments; it does not bind the user's working tree, and must not hold a
	// directory while the workers that will write it are ready to run.
	IsLeader bool
	// PinnedResourceID is the resource the task explicitly asked for, empty
	// when it asked for none. An explicit ask is the FIRST of the two
	// selection rules and is never overridden by position.
	PinnedResourceID string
	// Repos are the run's repository URLs in priority order, including any
	// inherited from the workspace. Used only when no directory on this
	// machine holds the project's code.
	Repos []string
}

// Daemon is the machine asking, and what it said it can do. Capabilities are
// read from what the runtime ADVERTISED on this request, never from a version
// string: the git-describe dev-build exemption once let a daemon without the
// implementation straight through a version gate, and two tasks ran in a
// directory the user had asked to isolate (MUL-5707).
type Daemon struct {
	// ID is the daemon registration claiming the task. A local_directory is
	// bound to one of these; "available on this machine" means "bound to this
	// id", which is the only availability the server can assert without
	// looking at a disk it cannot see.
	ID string
	// MultiLocalDirectory: the daemon can receive more than one
	// local_directory for a project and knows the first in position order is
	// the writable one. A daemon without it FAILS the task on the second row.
	MultiLocalDirectory bool
	// WorktreeUserRoot: parallel working copies go under the user's chosen
	// root. A daemon without it builds them inside its own env root, where the
	// workspace GC reclaims them on a schedule the user never agreed to.
	WorktreeUserRoot bool
	// LocalWorktree and LocalShared: the daemon implements those execution
	// modes at all.
	LocalWorktree bool
	LocalShared   bool
}

// LocalDirRef is the stored JSONB shape of a local_directory resource_ref.
// Mirrors handler.localDirectoryRef; only the fields the decision reads are
// declared, and unknown keys are ignored so a row written by a newer server
// still resolves.
type LocalDirRef struct {
	LocalPath     string `json:"local_path"`
	DaemonID      string `json:"daemon_id"`
	Label         string `json:"label,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
	RealPath      string `json:"real_path,omitempty"`
	WorktreeRoot  string `json:"worktree_root,omitempty"`
}

// gitHubRepoRef is the stored shape of a github_repo resource_ref.
type gitHubRepoRef struct {
	URL string `json:"url"`
}

// candidate is one local_directory bound to the claiming daemon, with its ref
// already parsed and its project remembered.
type candidate struct {
	resource  Resource
	projectID string
	ref       LocalDirRef
}

// displayName is the human-facing name for a directory, safe to render in a
// UI. Deliberately not the absolute path: a path carries the account name, and
// this string ends up in chat transcripts, screen shares and screenshots.
func (c candidate) displayName() string {
	if label := strings.TrimSpace(c.ref.Label); label != "" {
		return label
	}
	if label := strings.TrimSpace(c.resource.Label); label != "" {
		return label
	}
	return Base(c.ref.LocalPath)
}

func (c candidate) dir() Dir {
	return Dir{
		ResourceID: c.resource.ID,
		ProjectID:  c.projectID,
		Path:       c.ref.LocalPath,
		Name:       c.displayName(),
	}
}

// Resolve answers "which code does this run use?" — the whole rule, and the
// only copy of it.
//
// It has no IO, no clock and no randomness: the same inputs always produce the
// same Decision, which is what lets the server compute it once, hand it to the
// daemon to execute and to the frontend to display, and have all three agree.
//
// The selection rule is two steps and no more:
//
//  1. The task named a resource explicitly — use that one, or fail saying why
//     it cannot be used. An explicit ask is never quietly overridden.
//  2. Otherwise take the first local directory, in position order, that is
//     bound to the claiming machine.
//
// Only when neither yields a directory does the run look outward: a repository
// checkout when the run has one, and the shared session folder when it has
// nothing at all. Every other outcome is Unresolvable, which is an answer the
// caller must act on — never a nil, never a quiet fall-through to somewhere
// else on the user's disk.
func Resolve(task Task, projects []Project, daemon Daemon) Decision {
	here, others, repos, failure := gather(task, projects, daemon)
	if failure != nil {
		return *failure
	}

	// A squad leader coordinates: it may create child issues and comments, but
	// it must not bind the user's working tree — nor hold its path mutex —
	// while the workers that will actually write it are waiting to run.
	if task.IsLeader {
		return outward(task, repos)
	}

	if pinned := strings.TrimSpace(task.PinnedResourceID); pinned != "" {
		return resolvePinned(task, pinned, here, others, repos, daemon)
	}

	if len(here) == 0 {
		return outward(task, repos).WithReadOnly(nil)
	}
	return local(task, here[0], daemon).WithReadOnly(dirsExcept(here, 0))
}

// gather walks every project's resources once, in the order given, and splits
// them into the three things the rule needs: the directories bound to THIS
// machine, the directories bound to another one, and the repositories.
//
// A malformed local_directory stops the walk. Skipping past a directory the
// server cannot even name would mean silently choosing the NEXT one — which is
// someone else's directory, chosen by a bug rather than by the user.
func gather(task Task, projects []Project, daemon Daemon) (here []candidate, others map[string]candidate, repos []RemoteTarget, failure *Decision) {
	others = map[string]candidate{}
	seenRepo := map[string]bool{}
	daemonID := strings.TrimSpace(daemon.ID)

	for _, project := range projects {
		for _, res := range project.Resources {
			switch res.ResourceType {
			case ResourceTypeLocalDirectory:
				ref, err := parseLocalDirRef(res)
				if err != nil {
					d := NewUnresolvable(CodeMalformedResource, err.Error())
					return nil, nil, nil, &d
				}
				c := candidate{resource: res, projectID: project.ID, ref: ref}
				if daemonID != "" && ref.DaemonID == daemonID {
					here = append(here, c)
					continue
				}
				others[res.ID] = c
			case ResourceTypeGitHubRepo:
				var ref gitHubRepoRef
				// An unreadable github_repo ref is NOT fatal: unlike a
				// directory it cannot send the agent to the wrong place on
				// the user's disk, and the remaining repos still work.
				if err := json.Unmarshal(res.Ref, &ref); err != nil {
					continue
				}
				url := strings.TrimSpace(ref.URL)
				if url == "" || seenRepo[url] {
					continue
				}
				seenRepo[url] = true
				repos = append(repos, RemoteTarget{URL: url, ResourceID: res.ID, ProjectID: project.ID})
			}
		}
	}

	// Repos the task carries that no resource row named — workspace-level
	// repositories, mostly. They are real checkout targets and belong in the
	// list, they simply have no row to link back to.
	for _, url := range task.Repos {
		url = strings.TrimSpace(url)
		if url == "" || seenRepo[url] {
			continue
		}
		seenRepo[url] = true
		repos = append(repos, RemoteTarget{URL: url})
	}
	return here, others, repos, nil
}

func parseLocalDirRef(res Resource) (LocalDirRef, error) {
	var ref LocalDirRef
	if err := json.Unmarshal(res.Ref, &ref); err != nil {
		return ref, fmt.Errorf("local_directory %s: resource_ref is not readable: %v", res.ID, err)
	}
	ref.DaemonID = strings.TrimSpace(ref.DaemonID)
	ref.LocalPath = strings.TrimSpace(ref.LocalPath)
	ref.RealPath = strings.TrimSpace(ref.RealPath)
	ref.WorktreeRoot = strings.TrimSpace(ref.WorktreeRoot)
	ref.ExecutionMode = strings.TrimSpace(ref.ExecutionMode)
	if ref.DaemonID == "" {
		return ref, fmt.Errorf("local_directory %s: resource_ref names no daemon, so no machine can claim it", res.ID)
	}
	if !IsAbsolutePath(ref.LocalPath) {
		return ref, fmt.Errorf("local_directory %s: local_path %q is not an absolute path", res.ID, ref.LocalPath)
	}
	return ref, nil
}

// resolvePinned handles the first selection rule: the task named a resource.
func resolvePinned(task Task, pinned string, here []candidate, others map[string]candidate, repos []RemoteTarget, daemon Daemon) Decision {
	for i, c := range here {
		if c.resource.ID == pinned {
			return local(task, c, daemon).WithReadOnly(dirsExcept(here, i))
		}
	}
	if c, ok := others[pinned]; ok {
		return NewUnresolvable(CodePinnedResourceOtherMachine, fmt.Sprintf(
			"this run was pinned to the directory %q, which is bound to a different machine than the one claiming it; "+
				"run it on that machine, or pin it to a directory this one holds", c.displayName()))
	}
	for _, repo := range repos {
		if repo.ResourceID == pinned {
			return NewRemoteCache(repo).WithReadOnly(dirsOf(here))
		}
	}
	return NewUnresolvable(CodePinnedResourceNotFound, fmt.Sprintf(
		"this run was pinned to resource %s, which is not attached to any of its projects", pinned))
}

// local turns the chosen directory into the decision its execution mode calls
// for, or into the reason it cannot be run here.
func local(task Task, c candidate, daemon Daemon) Decision {
	target := LocalTarget{
		ResourceID:    c.resource.ID,
		ProjectID:     c.projectID,
		ExecutionMode: normalizeMode(c.ref.ExecutionMode),
		DisplayName:   c.displayName(),
	}
	switch target.ExecutionMode {
	case ModeInPlace:
		target.Path = c.ref.LocalPath
		// The lock serialises on the directory's IDENTITY, so two routes to
		// one directory collapse to one lock. real_path is that identity when
		// the machine reported one; the bound path is the best the server can
		// do when it did not, and the daemon re-resolves it either way.
		target.LockKey = c.ref.RealPath
		if target.LockKey == "" {
			target.LockKey = c.ref.LocalPath
		}
		return NewLocalInPlace(target)

	case ModeShared:
		if !daemon.LocalShared {
			return cannotRunMode(c, ModeShared, "shared workspace")
		}
		// No LockKey by design: not taking the mutex IS the mode. Leaving it
		// empty is how the decision says so, rather than carrying a key a
		// reader might take.
		target.Path = c.ref.LocalPath
		return NewLocalShared(target)

	case ModeWorktree:
		if !daemon.LocalWorktree {
			return cannotRunMode(c, ModeWorktree, "parallel (worktree)")
		}
		target.RepoPath = c.ref.LocalPath
		target.WorktreeRoot = c.ref.WorktreeRoot
		if target.WorktreeRoot == "" || !daemon.WorktreeUserRoot {
			// Two ways to end up on the default sibling: the resource names no
			// root, or the daemon cannot honour one. The second case is not a
			// silent downgrade — a daemon without the capability never
			// receives worktree_root at all (see FilterForCapabilities), so
			// stating the default here is stating what will actually happen.
			target.WorktreeRoot = DefaultWorktreeRoot(c.ref.LocalPath)
		}
		target.Path = Join(target.WorktreeRoot, TaskPathSegment(task.IssueIdentifier, "task", task.ID))
		return NewLocalWorktree(target)

	default:
		return NewUnresolvable(CodeUnknownExecutionMode, fmt.Sprintf(
			"directory %q asks for execution mode %q, which this server does not know; "+
				"refusing to guess in_place, since that would write into a directory the mode may exist to keep out of",
			c.displayName(), c.ref.ExecutionMode))
	}
}

func cannotRunMode(c candidate, mode, humanMode string) Decision {
	return NewUnresolvable(CodeDaemonCannotRunMode, fmt.Sprintf(
		"directory %q runs in %s mode, but the Multica runtime claiming this task did not advertise it. "+
			"Update the app on that machine, or set the directory back to in-place. "+
			"Running it in place instead is refused: losing concurrency is a nuisance, "+
			"writing into a directory the mode exists to protect is not",
		c.displayName(), humanMode))
}

// outward is the answer when no directory on this machine holds the code: a
// repository checkout if the run has a repository, and the shared session
// folder if it has nothing at all.
func outward(task Task, repos []RemoteTarget) Decision {
	if len(repos) > 0 {
		return NewRemoteCache(repos[0])
	}
	// No directory, no repository — a question, a plan, a conversation. It
	// still needs somewhere to leave artifacts, and that somewhere is archived
	// per SESSION, not per task: twenty questions in one conversation must not
	// make twenty directories.
	//
	// Path is RELATIVE to the daemon's scratch root. Which root that is, is the
	// daemon's to choose and not something the server can know.
	session := strings.TrimSpace(task.SessionID)
	if session == "" {
		session = task.ID
	}
	return NewSharedScratch(ScratchTarget{SessionID: session, Path: "sessions/" + session})
}

func normalizeMode(mode string) string {
	if mode == "" {
		return ModeInPlace
	}
	return mode
}

func dirsOf(cands []candidate) []Dir {
	return dirsExcept(cands, -1)
}

// dirsExcept lists every candidate as a read-only directory except the one at
// skip, which is the directory this run writes.
func dirsExcept(cands []candidate, skip int) []Dir {
	var out []Dir
	for i, c := range cands {
		if i == skip {
			continue
		}
		out = append(out, c.dir())
	}
	return out
}
