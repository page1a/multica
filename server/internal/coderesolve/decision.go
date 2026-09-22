// Package coderesolve answers one question, in one place, for the whole
// platform: which code does this run use?
//
// Before it, three components answered it independently. The daemon walked the
// project's resources and picked a directory (internal/daemon/local_directory.go);
// the claim path narrowed the resource set to what the daemon could act on
// (internal/handler/daemon.go); and the desktop UI guessed at the answer from
// the resource list so it could tell the user where a run would land. Three
// derivations of one rule is three chances to disagree, and the user only sees
// the disagreement after a run has already written to the wrong directory.
//
// So the rule lives here, as a pure function: no filesystem, no clock, no
// randomness, no database. Given a task, its projects' resources and the
// capabilities the claiming daemon advertised, Resolve returns a Decision. The
// server computes it once per claim, ships it on the task, and both the daemon
// and the frontend read it instead of re-deriving it.
//
// # Purity, and what it costs
//
// The server cannot see the user's disk. Everything this package decides is
// therefore decided from what the project RECORDED about a machine, not from
// what is on that machine right now. "Available on this machine" means "bound
// to the daemon that is claiming", which is the only availability question the
// server can answer without lying. Whether the directory still exists, is
// writable, or is a git repository is the daemon's to verify at run time — and
// a daemon that finds otherwise must FAIL the task with a code, never quietly
// pick somewhere else. Silently re-choosing is the failure mode this package
// exists to remove.
package coderesolve

import (
	"encoding/json"
	"fmt"
)

// Kind names one of the six answers. The set is closed: Decision cannot be
// constructed outside this package, so every value in the wild carries one of
// these and nothing else.
type Kind string

const (
	// KindLocalInPlace runs the agent in the user's own directory, serialised
	// against other runs on the same path. The default for a bound directory.
	KindLocalInPlace Kind = "local_in_place"
	// KindLocalShared runs in the user's own directory without the path lock,
	// for a directory that holds several repositories rather than one tree.
	KindLocalShared Kind = "local_shared"
	// KindLocalWorktree runs in a git worktree of the user's repository,
	// placed beside that repository on the user's own disk.
	KindLocalWorktree Kind = "local_worktree"
	// KindRemoteCache runs in a Multica-owned checkout of a remote repository,
	// because no directory on the claiming machine holds this project's code.
	KindRemoteCache Kind = "remote_cache"
	// KindSharedScratch runs in the machine's shared per-session folder: the
	// task bound no directory and named no repository, so there is no code to
	// work in, only somewhere to leave artifacts.
	KindSharedScratch Kind = "shared_scratch"
	// KindUnresolvable is a first-class answer, not an error channel. The
	// inputs describe a run that cannot be placed, and the task must fail with
	// the carried code rather than fall back to any of the five above.
	KindUnresolvable Kind = "unresolvable"
)

// Code identifies why a Decision is Unresolvable, in a form the UI can
// translate and a test can assert on. The human sentence is carried alongside
// it in Failure.Reason; this is the stable half.
type Code string

const (
	// CodeUnset is the zero Decision: a value that was never computed. It
	// exists so that "nobody resolved this" is one of the six answers rather
	// than a nil that each caller re-interprets.
	CodeUnset Code = "unset"
	// CodeMalformedResource is a local_directory row the server cannot read:
	// unparseable ref, no daemon_id, or a local_path that is not absolute. A
	// directory that cannot even be named is not a directory to skip past on
	// the way to someone else's.
	CodeMalformedResource Code = "malformed_resource"
	// CodePinnedResourceNotFound is an explicit resource id that names nothing
	// in the task's projects.
	CodePinnedResourceNotFound Code = "pinned_resource_not_found"
	// CodePinnedResourceOtherMachine is an explicit resource id naming a
	// directory bound to a DIFFERENT daemon. Running it here would mean
	// running against a path that, on this machine, is some other directory or
	// none at all.
	CodePinnedResourceOtherMachine Code = "pinned_resource_other_machine"
	// CodePinnedResourceUnusable is an explicit resource id naming a resource
	// type this run cannot be placed in.
	CodePinnedResourceUnusable Code = "pinned_resource_unusable"
	// CodeUnknownExecutionMode is a mode string this server does not know. The
	// row was written by a newer server than the one resolving it, and guessing
	// in_place would run the agent inside a directory the user may have asked
	// to isolate.
	CodeUnknownExecutionMode Code = "unknown_execution_mode"
	// CodeDaemonCannotRunMode is a mode the claiming daemon did not advertise.
	// Degrading to in_place is refused for the same reason: losing concurrency
	// is a nuisance, ignoring a request not to touch someone's files is not.
	CodeDaemonCannotRunMode Code = "daemon_cannot_run_mode"
)

// LocalTarget describes a run that writes somewhere on the user's own disk.
// Which fields carry meaning depends on the Kind, and the accessors are the
// only way to reach it, so no caller can read RepoPath off an in-place run.
type LocalTarget struct {
	// ResourceID and ProjectID say which binding produced this target, so the
	// UI can link the answer back to the row the user can edit.
	ResourceID string `json:"resource_id"`
	ProjectID  string `json:"project_id,omitempty"`
	// Path is the directory the agent runs in. For in-place and shared it is
	// the bound directory itself; for worktree it is the working copy beside
	// the repository.
	Path string `json:"path"`
	// RepoPath is the repository a worktree run branches from — the bound
	// directory. Empty for the other two kinds, where Path already is it.
	RepoPath string `json:"repo_path,omitempty"`
	// WorktreeRoot is the directory holding this repository's working copies.
	// Worktree runs only.
	WorktreeRoot string `json:"worktree_root,omitempty"`
	// LockKey is the identity the per-path mutex serialises on: the recorded
	// real_path when the machine reported one, otherwise the bound path.
	// Non-empty ONLY for in-place runs — shared and worktree runs skip the
	// mutex by definition, and an empty key is how they say so.
	LockKey string `json:"lock_key,omitempty"`
	// ExecutionMode is the mode string as stored, echoed so a daemon can
	// verify the server read the row the same way it does.
	ExecutionMode string `json:"execution_mode,omitempty"`
	// DisplayName is the label the user gave the directory, or its basename.
	// Never the absolute path: this is what the UI renders into screenshots.
	DisplayName string `json:"display_name,omitempty"`
}

// RemoteTarget describes a run that works in a Multica-owned checkout.
type RemoteTarget struct {
	// URL is the repository this run's checkout is for. A project may name
	// several repositories; the run is placed against the first, and the rest
	// stay reachable through `multica repo checkout`.
	URL string `json:"url"`
	// ResourceID is the github_repo binding, when the URL came from one. Repos
	// inherited from the workspace carry no resource row and leave it empty.
	ResourceID string `json:"resource_id,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
}

// ScratchTarget describes a run with no code at all: a question, a plan, a
// conversation. It gets a folder to leave artifacts in, archived per session
// rather than per task, so twenty questions do not make twenty directories.
type ScratchTarget struct {
	// SessionID is what the folder is archived under.
	SessionID string `json:"session_id"`
	// Path is RELATIVE to the daemon's own scratch root, which is the daemon's
	// to choose and the server's not to know. Joining it is the daemon's job.
	Path string `json:"path"`
}

// Failure is the Unresolvable payload. Code is for machines, Reason for the
// person who has to fix the binding.
type Failure struct {
	Code   Code   `json:"code"`
	Reason string `json:"reason"`
}

// Dir is one local directory the run may READ but not write. One run writes
// one directory (DENE-617 invariant 1); the others are named rather than
// hidden, so an agent that needs to look at one does not discover it by
// accident or check out a second copy of it.
type Dir struct {
	ResourceID string `json:"resource_id"`
	ProjectID  string `json:"project_id,omitempty"`
	Path       string `json:"path"`
	Name       string `json:"name,omitempty"`
}

// Decision is the closed sum type: exactly one of the six kinds, with only
// that kind's payload reachable.
//
// The fields are unexported and there is no literal form, so the only
// Decisions that exist are the ones the constructors below produce. The zero
// value is not a seventh state — it reads as Unresolvable{CodeUnset}, which is
// the true statement about a Decision nobody computed.
type Decision struct {
	kind     Kind
	local    LocalTarget
	remote   RemoteTarget
	scratch  ScratchTarget
	failure  Failure
	readOnly []Dir
}

// NewLocalInPlace and friends are the only ways to build a Decision.
func NewLocalInPlace(t LocalTarget) Decision {
	return Decision{kind: KindLocalInPlace, local: t}
}

func NewLocalShared(t LocalTarget) Decision {
	return Decision{kind: KindLocalShared, local: t}
}

func NewLocalWorktree(t LocalTarget) Decision {
	return Decision{kind: KindLocalWorktree, local: t}
}

func NewRemoteCache(t RemoteTarget) Decision {
	return Decision{kind: KindRemoteCache, remote: t}
}

func NewSharedScratch(t ScratchTarget) Decision {
	return Decision{kind: KindSharedScratch, scratch: t}
}

// NewUnresolvable builds the failure answer. It is a Decision like any other:
// callers switch on it, the wire carries it, and the UI renders it.
func NewUnresolvable(code Code, reason string) Decision {
	return Decision{kind: KindUnresolvable, failure: Failure{Code: code, Reason: reason}}
}

// WithReadOnly attaches the directories this run may read. Returned as a new
// Decision so a Decision is never mutated after it has been handed out.
func (d Decision) WithReadOnly(dirs []Dir) Decision {
	d.readOnly = append([]Dir(nil), dirs...)
	return d
}

// Kind reports which of the six this is. A zero Decision reports
// KindUnresolvable, so no caller ever has to handle a seventh case.
func (d Decision) Kind() Kind {
	if d.kind == "" {
		return KindUnresolvable
	}
	return d.kind
}

// Local returns the local target and true for the three local kinds.
func (d Decision) Local() (LocalTarget, bool) {
	switch d.Kind() {
	case KindLocalInPlace, KindLocalShared, KindLocalWorktree:
		return d.local, true
	default:
		return LocalTarget{}, false
	}
}

// Remote returns the remote target and true for KindRemoteCache.
func (d Decision) Remote() (RemoteTarget, bool) {
	if d.Kind() == KindRemoteCache {
		return d.remote, true
	}
	return RemoteTarget{}, false
}

// Scratch returns the scratch target and true for KindSharedScratch.
func (d Decision) Scratch() (ScratchTarget, bool) {
	if d.Kind() == KindSharedScratch {
		return d.scratch, true
	}
	return ScratchTarget{}, false
}

// Failure returns the failure and true for KindUnresolvable. A zero Decision
// yields CodeUnset rather than an empty-but-valid-looking failure.
func (d Decision) Failure() (Failure, bool) {
	if d.Kind() != KindUnresolvable {
		return Failure{}, false
	}
	if d.kind == "" {
		return Failure{Code: CodeUnset, Reason: "no code source decision was computed for this run"}, true
	}
	return d.failure, true
}

// ReadOnly lists the local directories this run may read but not write.
func (d Decision) ReadOnly() []Dir {
	return append([]Dir(nil), d.readOnly...)
}

// Visitor is one arm per kind. Every field is required: Fold panics on a nil
// arm before it looks at the decision, so a caller that forgot a case fails on
// its first call rather than on the one input that happens to reach it.
type Visitor[T any] struct {
	LocalInPlace  func(LocalTarget) T
	LocalShared   func(LocalTarget) T
	LocalWorktree func(LocalTarget) T
	RemoteCache   func(RemoteTarget) T
	SharedScratch func(ScratchTarget) T
	Unresolvable  func(Failure) T
}

// Fold is the exhaustive switch. Go has no compiler check for one, so this is
// the substitute: the check is a precondition, not a default branch, because a
// default branch is exactly how a new kind gets silently mishandled.
func Fold[T any](d Decision, v Visitor[T]) T {
	switch {
	case v.LocalInPlace == nil:
		panic("coderesolve: Visitor.LocalInPlace is nil")
	case v.LocalShared == nil:
		panic("coderesolve: Visitor.LocalShared is nil")
	case v.LocalWorktree == nil:
		panic("coderesolve: Visitor.LocalWorktree is nil")
	case v.RemoteCache == nil:
		panic("coderesolve: Visitor.RemoteCache is nil")
	case v.SharedScratch == nil:
		panic("coderesolve: Visitor.SharedScratch is nil")
	case v.Unresolvable == nil:
		panic("coderesolve: Visitor.Unresolvable is nil")
	}
	switch d.Kind() {
	case KindLocalInPlace:
		return v.LocalInPlace(d.local)
	case KindLocalShared:
		return v.LocalShared(d.local)
	case KindLocalWorktree:
		return v.LocalWorktree(d.local)
	case KindRemoteCache:
		return v.RemoteCache(d.remote)
	case KindSharedScratch:
		return v.SharedScratch(d.scratch)
	default:
		f, _ := d.Failure()
		return v.Unresolvable(f)
	}
}

// decisionWire is the JSON shape. Flat rather than nested-per-variant: the
// daemon and the frontend both switch on `kind` and read the fields that kind
// defines, and a flat object keeps an older reader that only understands
// `kind` and `path` working.
type decisionWire struct {
	Kind Kind `json:"kind"`
	// Local fields, present on the three local kinds.
	ResourceID    string `json:"resource_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	Path          string `json:"path,omitempty"`
	RepoPath      string `json:"repo_path,omitempty"`
	WorktreeRoot  string `json:"worktree_root,omitempty"`
	LockKey       string `json:"lock_key,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	// Remote.
	URL string `json:"url,omitempty"`
	// Scratch.
	SessionID string `json:"session_id,omitempty"`
	// Unresolvable.
	Code   Code   `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Read-only context, on every kind that can have it.
	ReadOnly []Dir `json:"read_only_dirs,omitempty"`
}

// MarshalJSON writes the flat wire shape. A zero Decision marshals as the
// Unresolvable it reads as, so the wire never carries a kindless object.
func (d Decision) MarshalJSON() ([]byte, error) {
	w := decisionWire{Kind: d.Kind(), ReadOnly: d.readOnly}
	switch d.Kind() {
	case KindLocalInPlace, KindLocalShared, KindLocalWorktree:
		w.ResourceID = d.local.ResourceID
		w.ProjectID = d.local.ProjectID
		w.Path = d.local.Path
		w.RepoPath = d.local.RepoPath
		w.WorktreeRoot = d.local.WorktreeRoot
		w.LockKey = d.local.LockKey
		w.ExecutionMode = d.local.ExecutionMode
		w.DisplayName = d.local.DisplayName
	case KindRemoteCache:
		w.URL = d.remote.URL
		w.ResourceID = d.remote.ResourceID
		w.ProjectID = d.remote.ProjectID
	case KindSharedScratch:
		w.SessionID = d.scratch.SessionID
		w.Path = d.scratch.Path
	default:
		f, _ := d.Failure()
		w.Code = f.Code
		w.Reason = f.Reason
	}
	return json.Marshal(w)
}

// UnmarshalJSON rebuilds a Decision from the wire. A kind this binary does not
// know becomes Unresolvable rather than a zero-valued local run: a daemon
// reading a decision from a newer server must fail loudly, not run somewhere.
func (d *Decision) UnmarshalJSON(data []byte) error {
	var w decisionWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	local := LocalTarget{
		ResourceID:    w.ResourceID,
		ProjectID:     w.ProjectID,
		Path:          w.Path,
		RepoPath:      w.RepoPath,
		WorktreeRoot:  w.WorktreeRoot,
		LockKey:       w.LockKey,
		ExecutionMode: w.ExecutionMode,
		DisplayName:   w.DisplayName,
	}
	switch w.Kind {
	case KindLocalInPlace, KindLocalShared, KindLocalWorktree:
		*d = Decision{kind: w.Kind, local: local}
	case KindRemoteCache:
		*d = Decision{kind: w.Kind, remote: RemoteTarget{URL: w.URL, ResourceID: w.ResourceID, ProjectID: w.ProjectID}}
	case KindSharedScratch:
		*d = Decision{kind: w.Kind, scratch: ScratchTarget{SessionID: w.SessionID, Path: w.Path}}
	case KindUnresolvable:
		*d = NewUnresolvable(w.Code, w.Reason)
	default:
		*d = NewUnresolvable(CodeUnknownExecutionMode, fmt.Sprintf(
			"code source decision kind %q was produced by a newer server than this client; refusing to guess where this run belongs", w.Kind))
	}
	d.readOnly = append([]Dir(nil), w.ReadOnly...)
	return nil
}
