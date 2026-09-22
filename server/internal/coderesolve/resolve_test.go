package coderesolve

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests are the canonical matrix for "which code does this run use".
// They are pure by construction — no database, no filesystem, no clock — which
// is the property the package exists to have: the same inputs must produce the
// same Decision on the server, in the daemon that executes it, and in the UI
// that displays it.

const (
	thisDaemon  = "daemon-here"
	otherDaemon = "daemon-elsewhere"
	taskID      = "01a0b898-5f0d-7a4d-aa1b-142f15c86d34"
	issueKey    = "DENE-619"
)

// taskSuffix is the last 12 hex chars of taskID: the tail the directory name
// is built from. Spelled out rather than computed so a change to the naming
// rule fails this test instead of silently agreeing with itself.
const taskSuffix = "142f15c86d34"

func localDir(id, path string, mutate ...func(*LocalDirRef)) Resource {
	ref := LocalDirRef{LocalPath: path, DaemonID: thisDaemon}
	for _, m := range mutate {
		m(&ref)
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		panic(err)
	}
	return Resource{ID: id, ResourceType: ResourceTypeLocalDirectory, Ref: raw}
}

func repoRes(id, url string) Resource {
	return Resource{ID: id, ResourceType: ResourceTypeGitHubRepo, Ref: json.RawMessage(`{"url":"` + url + `"}`)}
}

func oneProject(resources ...Resource) []Project {
	return []Project{{ID: "project-1", Resources: resources}}
}

// fullDaemon advertises everything: the decision then reflects the project's
// configuration rather than the machine's limits.
func fullDaemon() Daemon {
	return Daemon{ID: thisDaemon, MultiLocalDirectory: true, WorktreeUserRoot: true, LocalWorktree: true, LocalShared: true}
}

func baseTask() Task {
	return Task{ID: taskID, IssueIdentifier: issueKey}
}

func requireKind(t *testing.T, d Decision, want Kind) {
	t.Helper()
	if d.Kind() != want {
		if f, ok := d.Failure(); ok {
			t.Fatalf("decision kind = %q (%s: %s), want %q", d.Kind(), f.Code, f.Reason, want)
		}
		t.Fatalf("decision kind = %q, want %q", d.Kind(), want)
	}
}

func requireCode(t *testing.T, d Decision, want Code) {
	t.Helper()
	f, ok := d.Failure()
	if !ok {
		t.Fatalf("decision kind = %q, want %q", d.Kind(), KindUnresolvable)
	}
	if f.Code != want {
		t.Fatalf("failure code = %q (%s), want %q", f.Code, f.Reason, want)
	}
	if strings.TrimSpace(f.Reason) == "" {
		t.Errorf("failure %q carries no reason; a code with no sentence leaves the user nothing to act on", want)
	}
}

// --- the six Decision values -------------------------------------------------

func TestResolve_LocalInPlace_IsTheDefaultForABoundDirectory(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/app")), fullDaemon())

	requireKind(t, d, KindLocalInPlace)
	target, _ := d.Local()
	if target.Path != "/Users/u/app" {
		t.Errorf("path = %q, want the bound directory itself", target.Path)
	}
	if target.LockKey != "/Users/u/app" {
		t.Errorf("lock_key = %q, want the bound path when the machine reported no real_path", target.LockKey)
	}
	if target.ResourceID != "res-1" {
		t.Errorf("resource_id = %q, want the row the user can edit", target.ResourceID)
	}
	if target.DisplayName != "app" {
		t.Errorf("display_name = %q, want the basename, never the absolute path", target.DisplayName)
	}
}

// real_path is the directory's IDENTITY, so it — not the path the user typed —
// is what the per-path mutex serialises on. Without this, one directory reached
// through a symlink and through its real path takes two different locks and two
// heavyweight runs interleave edits into one working tree.
func TestResolve_LocalInPlace_LocksOnRealPathWhenTheMachineReportedOne(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/link", func(r *LocalDirRef) {
		r.RealPath = "/Users/u/real/app"
	})), fullDaemon())

	target, _ := d.Local()
	if target.LockKey != "/Users/u/real/app" {
		t.Errorf("lock_key = %q, want the recorded real_path", target.LockKey)
	}
	if target.Path != "/Users/u/link" {
		t.Errorf("path = %q, want the path the user bound; real_path is identity, not where to run", target.Path)
	}
}

func TestResolve_LocalShared_TakesNoLock(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/code", func(r *LocalDirRef) {
		r.ExecutionMode = ModeShared
	})), fullDaemon())

	requireKind(t, d, KindLocalShared)
	target, _ := d.Local()
	if target.Path != "/Users/u/code" {
		t.Errorf("path = %q, want the user's own directory", target.Path)
	}
	// Not taking the mutex IS the mode. An empty key is how the decision says
	// so; a populated one invites a reader to take it and re-serialise the
	// directory the user asked to share.
	if target.LockKey != "" {
		t.Errorf("lock_key = %q, want empty: shared mode is defined by not taking the lock", target.LockKey)
	}
}

func TestResolve_LocalWorktree_DefaultsToTheRepositorySibling(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/app", func(r *LocalDirRef) {
		r.ExecutionMode = ModeWorktree
	})), fullDaemon())

	requireKind(t, d, KindLocalWorktree)
	target, _ := d.Local()
	if target.RepoPath != "/Users/u/app" {
		t.Errorf("repo_path = %q, want the repository the copy branches from", target.RepoPath)
	}
	if target.WorktreeRoot != "/Users/u/app.multica-worktrees" {
		t.Errorf("worktree_root = %q, want the repository's sibling", target.WorktreeRoot)
	}
	if want := "/Users/u/app.multica-worktrees/dene-619-" + taskSuffix; target.Path != want {
		t.Errorf("path = %q, want %q", target.Path, want)
	}
	if target.LockKey != "" {
		t.Errorf("lock_key = %q, want empty: each parallel run has its own copy", target.LockKey)
	}
}

func TestResolve_LocalWorktree_HonoursAConfiguredRoot(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/app", func(r *LocalDirRef) {
		r.ExecutionMode = ModeWorktree
		r.WorktreeRoot = "/Volumes/scratch/copies"
	})), fullDaemon())

	target, _ := d.Local()
	if target.WorktreeRoot != "/Volumes/scratch/copies" {
		t.Errorf("worktree_root = %q, want the location the user chose", target.WorktreeRoot)
	}
	if want := "/Volumes/scratch/copies/dene-619-" + taskSuffix; target.Path != want {
		t.Errorf("path = %q, want %q", target.Path, want)
	}
}

// A Windows daemon's paths reach a Linux server as strings. path/filepath would
// read `C:\...` as relative and rewrite its separators; this package must not.
func TestResolve_LocalWorktree_KeepsWindowsPathsIntact(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", `C:\Users\u\app`, func(r *LocalDirRef) {
		r.ExecutionMode = ModeWorktree
	})), fullDaemon())

	requireKind(t, d, KindLocalWorktree)
	target, _ := d.Local()
	if target.WorktreeRoot != `C:\Users\u\app.multica-worktrees` {
		t.Errorf("worktree_root = %q, want the Windows sibling with backslashes preserved", target.WorktreeRoot)
	}
	if want := `C:\Users\u\app.multica-worktrees\dene-619-` + taskSuffix; target.Path != want {
		t.Errorf("path = %q, want %q", target.Path, want)
	}
}

func TestResolve_RemoteCache_WhenNoDirectoryOnThisMachineHoldsTheCode(t *testing.T) {
	projects := oneProject(
		localDir("res-elsewhere", "/srv/app", func(r *LocalDirRef) { r.DaemonID = otherDaemon }),
		repoRes("res-repo", "https://github.com/example/app"),
	)

	d := Resolve(baseTask(), projects, fullDaemon())

	requireKind(t, d, KindRemoteCache)
	remote, _ := d.Remote()
	if remote.URL != "https://github.com/example/app" {
		t.Errorf("url = %q, want the project's repository", remote.URL)
	}
	if remote.ResourceID != "res-repo" {
		t.Errorf("resource_id = %q, want the github_repo row", remote.ResourceID)
	}
	// Another machine's directory is not this run's read-only context: on this
	// machine that path is some other directory, or none.
	if dirs := d.ReadOnly(); len(dirs) != 0 {
		t.Errorf("read-only dirs = %+v, want none: those directories are on a different machine", dirs)
	}
}

func TestResolve_RemoteCache_UsesAWorkspaceRepoWhenNoResourceNamesOne(t *testing.T) {
	task := baseTask()
	task.Repos = []string{"https://github.com/example/inherited"}

	d := Resolve(task, oneProject(), fullDaemon())

	requireKind(t, d, KindRemoteCache)
	remote, _ := d.Remote()
	if remote.URL != "https://github.com/example/inherited" {
		t.Errorf("url = %q, want the inherited repository", remote.URL)
	}
	if remote.ResourceID != "" {
		t.Errorf("resource_id = %q, want empty: a workspace repo has no project_resource row", remote.ResourceID)
	}
}

func TestResolve_SharedScratch_WhenTheRunHasNoCodeAtAll(t *testing.T) {
	task := baseTask()
	task.SessionID = "chat-session-7"

	d := Resolve(task, nil, fullDaemon())

	requireKind(t, d, KindSharedScratch)
	scratch, _ := d.Scratch()
	if scratch.SessionID != "chat-session-7" {
		t.Errorf("session_id = %q, want the conversation's id", scratch.SessionID)
	}
	if scratch.Path != "sessions/chat-session-7" {
		t.Errorf("path = %q, want a path relative to the daemon's own scratch root", scratch.Path)
	}
}

// Twenty questions in one conversation must land in ONE folder. The session id
// is what guarantees that; two tasks of the same session must agree on it.
func TestResolve_SharedScratch_IsPerSessionNotPerTask(t *testing.T) {
	first := baseTask()
	first.SessionID = "chat-session-7"
	second := Task{ID: "01a0b898-0000-7a4d-aa1b-000000000001", SessionID: "chat-session-7"}

	a, _ := Resolve(first, nil, fullDaemon()).Scratch()
	b, _ := Resolve(second, nil, fullDaemon()).Scratch()
	if a.Path != b.Path {
		t.Errorf("two tasks of one session resolved to %q and %q; a session gets one folder", a.Path, b.Path)
	}
}

func TestResolve_SharedScratch_FallsBackToTheTaskWhenThereIsNoSession(t *testing.T) {
	d := Resolve(baseTask(), nil, fullDaemon())

	scratch, _ := d.Scratch()
	if scratch.SessionID != taskID {
		t.Errorf("session_id = %q, want the task id when the run has no session", scratch.SessionID)
	}
}

func TestResolve_Unresolvable_OnAMalformedDirectory(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"unreadable json", `{`},
		{"no daemon", `{"local_path":"/Users/u/app"}`},
		{"relative path", `{"local_path":"app","daemon_id":"` + thisDaemon + `"}`},
		{"no path", `{"daemon_id":"` + thisDaemon + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projects := oneProject(
				Resource{ID: "res-broken", ResourceType: ResourceTypeLocalDirectory, Ref: json.RawMessage(tc.ref)},
				localDir("res-next", "/Users/u/other"),
			)

			d := Resolve(baseTask(), projects, fullDaemon())

			// The point of failing rather than skipping: the next row in the
			// list is a DIFFERENT directory, and choosing it would mean a bug
			// picked where the agent writes.
			requireCode(t, d, CodeMalformedResource)
		})
	}
}

func TestResolve_Unresolvable_WhenThePinnedResourceIsOnAnotherMachine(t *testing.T) {
	task := baseTask()
	task.PinnedResourceID = "res-elsewhere"
	projects := oneProject(
		localDir("res-here", "/Users/u/app"),
		localDir("res-elsewhere", "/srv/app", func(r *LocalDirRef) { r.DaemonID = otherDaemon }),
	)

	d := Resolve(task, projects, fullDaemon())

	// Falling back to res-here would be the silent re-choice this package
	// exists to remove: the user asked for one directory and would get another.
	requireCode(t, d, CodePinnedResourceOtherMachine)
}

func TestResolve_Unresolvable_WhenThePinnedResourceIsNotAttached(t *testing.T) {
	task := baseTask()
	task.PinnedResourceID = "res-gone"

	d := Resolve(task, oneProject(localDir("res-here", "/Users/u/app")), fullDaemon())

	requireCode(t, d, CodePinnedResourceNotFound)
}

func TestResolve_Unresolvable_OnAnExecutionModeThisServerDoesNotKnow(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/app", func(r *LocalDirRef) {
		r.ExecutionMode = "quantum"
	})), fullDaemon())

	requireCode(t, d, CodeUnknownExecutionMode)
}

func TestResolve_Unresolvable_WhenTheDaemonCannotRunTheMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode string
		caps Daemon
	}{
		{"worktree without the capability", ModeWorktree, Daemon{ID: thisDaemon, MultiLocalDirectory: true, WorktreeUserRoot: true, LocalShared: true}},
		{"shared without the capability", ModeShared, Daemon{ID: thisDaemon, MultiLocalDirectory: true, WorktreeUserRoot: true, LocalWorktree: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/u/app", func(r *LocalDirRef) {
				r.ExecutionMode = tc.mode
			})), tc.caps)

			// Never KindLocalInPlace: degrading to in-place would write into a
			// directory the mode may exist to keep the agent out of.
			requireCode(t, d, CodeDaemonCannotRunMode)
		})
	}
}

// --- the selection rule ------------------------------------------------------

func TestResolve_PositionOrderDecidesTheWritableDirectory(t *testing.T) {
	projects := oneProject(
		localDir("res-first", "/Users/u/first"),
		localDir("res-second", "/Users/u/second"),
		localDir("res-third", "/Users/u/third"),
	)

	d := Resolve(baseTask(), projects, fullDaemon())

	target, _ := d.Local()
	if target.ResourceID != "res-first" {
		t.Fatalf("wrote %q, want the first in position order", target.ResourceID)
	}
	dirs := d.ReadOnly()
	if len(dirs) != 2 || dirs[0].ResourceID != "res-second" || dirs[1].ResourceID != "res-third" {
		t.Fatalf("read-only dirs = %+v, want the other two in order", dirs)
	}
}

func TestResolve_AnExplicitPinBeatsPosition(t *testing.T) {
	task := baseTask()
	task.PinnedResourceID = "res-second"
	projects := oneProject(
		localDir("res-first", "/Users/u/first"),
		localDir("res-second", "/Users/u/second"),
	)

	d := Resolve(task, projects, fullDaemon())

	target, _ := d.Local()
	if target.ResourceID != "res-second" {
		t.Fatalf("wrote %q, want the pinned resource", target.ResourceID)
	}
	dirs := d.ReadOnly()
	if len(dirs) != 1 || dirs[0].ResourceID != "res-first" {
		t.Fatalf("read-only dirs = %+v, want the unchosen first row", dirs)
	}
}

// A chat can attach several projects and a run can only occupy one directory.
// The first project that pins one here decides; every other directory of every
// project on this machine is read-only, including that project's own lower rows.
func TestResolve_FirstProjectWithADirectoryHereWins(t *testing.T) {
	projects := []Project{
		{ID: "project-no-dirs", Resources: []Resource{repoRes("res-repo", "https://github.com/example/app")}},
		{ID: "project-b", Resources: []Resource{localDir("res-b1", "/Users/u/b1"), localDir("res-b2", "/Users/u/b2")}},
		{ID: "project-c", Resources: []Resource{localDir("res-c1", "/Users/u/c1")}},
	}

	d := Resolve(baseTask(), projects, fullDaemon())

	target, _ := d.Local()
	if target.ResourceID != "res-b1" || target.ProjectID != "project-b" {
		t.Fatalf("wrote %s of %s, want res-b1 of project-b", target.ResourceID, target.ProjectID)
	}
	var got []string
	for _, dir := range d.ReadOnly() {
		got = append(got, dir.ResourceID)
	}
	if len(got) != 2 || got[0] != "res-b2" || got[1] != "res-c1" {
		t.Fatalf("read-only dirs = %v, want [res-b2 res-c1]", got)
	}
}

// A squad leader creates issues and comments. Binding the user's working tree —
// and holding its lock — while the workers that will write it wait to run is
// exactly the deadlock the exclusion prevents.
func TestResolve_ASquadLeaderBindsNoDirectory(t *testing.T) {
	task := baseTask()
	task.IsLeader = true
	projects := oneProject(localDir("res-1", "/Users/u/app"), repoRes("res-repo", "https://github.com/example/app"))

	d := Resolve(task, projects, fullDaemon())

	requireKind(t, d, KindRemoteCache)
	if dirs := d.ReadOnly(); len(dirs) != 0 {
		t.Errorf("read-only dirs = %+v, want none for a leader", dirs)
	}
}

func TestResolve_ALeaderWithNothingAttachedGetsScratch(t *testing.T) {
	task := baseTask()
	task.IsLeader = true

	requireKind(t, Resolve(task, oneProject(localDir("res-1", "/Users/u/app")), fullDaemon()), KindSharedScratch)
}

// A run pinned to a repository works in a checkout; the machine's directories
// are still readable, and saying so is what stops the agent cloning a second
// copy of code it is already standing next to.
func TestResolve_PinningARepositoryKeepsLocalDirectoriesReadable(t *testing.T) {
	task := baseTask()
	task.PinnedResourceID = "res-repo"
	projects := oneProject(localDir("res-1", "/Users/u/app"), repoRes("res-repo", "https://github.com/example/app"))

	d := Resolve(task, projects, fullDaemon())

	requireKind(t, d, KindRemoteCache)
	dirs := d.ReadOnly()
	if len(dirs) != 1 || dirs[0].ResourceID != "res-1" {
		t.Fatalf("read-only dirs = %+v, want the machine's directory", dirs)
	}
}

func TestResolve_ALabelledDirectoryRendersItsLabelNotItsPath(t *testing.T) {
	d := Resolve(baseTask(), oneProject(localDir("res-1", "/Users/kunkun/work/app", func(r *LocalDirRef) {
		r.Label = "Multica 魔改"
	})), fullDaemon())

	target, _ := d.Local()
	if target.DisplayName != "Multica 魔改" {
		t.Errorf("display_name = %q, want the user's label", target.DisplayName)
	}
	if strings.Contains(target.DisplayName, "kunkun") {
		t.Errorf("display_name %q leaks the account name into a string the UI renders", target.DisplayName)
	}
}

// Purity is the whole contract: the server computes the decision, the daemon
// executes it and the frontend displays it, and all three must agree. A
// resolve that varied between calls would break that silently.
func TestResolve_IsDeterministic(t *testing.T) {
	projects := oneProject(localDir("res-1", "/Users/u/app"), localDir("res-2", "/Users/u/other"))

	first, err := json.Marshal(Resolve(baseTask(), projects, fullDaemon()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := json.Marshal(Resolve(baseTask(), projects, fullDaemon()))
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("call %d produced %s, first call produced %s", i, again, first)
		}
	}
}
