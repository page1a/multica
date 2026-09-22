package coderesolve

import (
	"encoding/json"
	"testing"
)

// allKinds is the closed set. A new variant must be added here, which is what
// makes the round-trip and exhaustiveness tests below fail until it is handled
// everywhere.
var allKinds = []Kind{
	KindLocalInPlace, KindLocalShared, KindLocalWorktree,
	KindRemoteCache, KindSharedScratch, KindUnresolvable,
}

func sampleOf(kind Kind) Decision {
	target := LocalTarget{
		ResourceID: "res-1", ProjectID: "project-1",
		Path: "/Users/u/app", LockKey: "/Users/u/app",
		ExecutionMode: ModeInPlace, DisplayName: "app",
	}
	readOnly := []Dir{{ResourceID: "res-2", ProjectID: "project-1", Path: "/Users/u/other", Name: "other"}}
	switch kind {
	case KindLocalInPlace:
		return NewLocalInPlace(target).WithReadOnly(readOnly)
	case KindLocalShared:
		target.ExecutionMode, target.LockKey = ModeShared, ""
		return NewLocalShared(target).WithReadOnly(readOnly)
	case KindLocalWorktree:
		target.ExecutionMode, target.LockKey = ModeWorktree, ""
		target.RepoPath = "/Users/u/app"
		target.WorktreeRoot = "/Users/u/app.multica-worktrees"
		target.Path = "/Users/u/app.multica-worktrees/dene-619-142f15c86d34"
		return NewLocalWorktree(target).WithReadOnly(readOnly)
	case KindRemoteCache:
		return NewRemoteCache(RemoteTarget{URL: "https://github.com/example/app", ResourceID: "res-repo", ProjectID: "project-1"}).WithReadOnly(readOnly)
	case KindSharedScratch:
		return NewSharedScratch(ScratchTarget{SessionID: "chat-7", Path: "sessions/chat-7"})
	default:
		return NewUnresolvable(CodePinnedResourceNotFound, "pinned resource is not attached to this run")
	}
}

// The wire is the whole point of the type: the server computes a Decision and
// two other processes read it back. A variant that does not survive the trip
// is a variant the daemon would execute wrongly.
func TestDecision_RoundTripsThroughJSON(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(string(kind), func(t *testing.T) {
			want := sampleOf(kind)
			raw, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			var got Decision
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Kind() != want.Kind() {
				t.Fatalf("kind = %q, want %q", got.Kind(), want.Kind())
			}
			again, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(raw) {
				t.Fatalf("re-marshalled to %s, first pass was %s", again, raw)
			}
		})
	}
}

// The zero value is not a seventh state. A Decision nobody computed reads as
// Unresolvable, so no caller has to invent behaviour for "no decision" — and
// in particular none can mistake it for a local run at path "".
func TestDecision_ZeroValueIsUnresolvableNotAnEmptyLocalRun(t *testing.T) {
	var zero Decision

	if zero.Kind() != KindUnresolvable {
		t.Fatalf("zero decision kind = %q, want %q", zero.Kind(), KindUnresolvable)
	}
	f, ok := zero.Failure()
	if !ok || f.Code != CodeUnset {
		t.Fatalf("zero decision failure = %+v (ok=%v), want code %q", f, ok, CodeUnset)
	}
	if _, ok := zero.Local(); ok {
		t.Error("zero decision offered a local target; it must not look like a run at path \"\"")
	}
	raw, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["kind"] != string(KindUnresolvable) {
		t.Fatalf("zero decision marshalled as %s; the wire must never carry a kindless object", raw)
	}
}

// Each variant's payload is reachable only through its own accessor. Without
// this a caller could read RepoPath off an in-place run and act on a field the
// kind never set.
func TestDecision_AccessorsAreVariantScoped(t *testing.T) {
	for _, kind := range allKinds {
		t.Run(string(kind), func(t *testing.T) {
			d := sampleOf(kind)
			_, isLocal := d.Local()
			_, isRemote := d.Remote()
			_, isScratch := d.Scratch()
			_, isFailure := d.Failure()

			count := 0
			for _, ok := range []bool{isLocal, isRemote, isScratch, isFailure} {
				if ok {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("%q answered %d accessors, want exactly 1 (local=%v remote=%v scratch=%v failure=%v)",
					kind, count, isLocal, isRemote, isScratch, isFailure)
			}
		})
	}
}

func TestFold_DispatchesEveryKind(t *testing.T) {
	visitor := Visitor[Kind]{
		LocalInPlace:  func(LocalTarget) Kind { return KindLocalInPlace },
		LocalShared:   func(LocalTarget) Kind { return KindLocalShared },
		LocalWorktree: func(LocalTarget) Kind { return KindLocalWorktree },
		RemoteCache:   func(RemoteTarget) Kind { return KindRemoteCache },
		SharedScratch: func(ScratchTarget) Kind { return KindSharedScratch },
		Unresolvable:  func(Failure) Kind { return KindUnresolvable },
	}
	for _, kind := range allKinds {
		if got := Fold(sampleOf(kind), visitor); got != kind {
			t.Errorf("Fold dispatched %q to the %q arm", kind, got)
		}
	}
}

// Go cannot check a switch for exhaustiveness, so Fold checks the visitor
// instead — and checks it BEFORE looking at the decision, so a missing arm
// fails on the first call rather than on the one input that happens to reach
// it months later.
func TestFold_PanicsOnAMissingArmRegardlessOfTheDecision(t *testing.T) {
	incomplete := Visitor[int]{
		LocalInPlace:  func(LocalTarget) int { return 1 },
		LocalShared:   func(LocalTarget) int { return 2 },
		LocalWorktree: func(LocalTarget) int { return 3 },
		RemoteCache:   func(RemoteTarget) int { return 4 },
		SharedScratch: func(ScratchTarget) int { return 5 },
		// Unresolvable deliberately absent.
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Fold accepted a visitor with no Unresolvable arm")
		}
	}()
	// An in-place decision never reaches the missing arm — and must still panic.
	Fold(sampleOf(KindLocalInPlace), incomplete)
}

// A daemon reading a decision from a newer server must fail loudly. Decoding
// an unknown kind into a zero-valued local run would send it to path "".
func TestDecision_AnUnknownKindDecodesAsUnresolvable(t *testing.T) {
	var got Decision
	if err := json.Unmarshal([]byte(`{"kind":"teleport","path":"/Users/u/app"}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Kind() != KindUnresolvable {
		t.Fatalf("kind = %q, want %q", got.Kind(), KindUnresolvable)
	}
	if _, ok := got.Local(); ok {
		t.Error("an unknown kind offered a local target")
	}
}

// WithReadOnly must not hand out a view into the caller's slice, or a later
// append by either side silently rewrites the other's decision.
func TestDecision_ReadOnlyIsCopiedNotAliased(t *testing.T) {
	dirs := []Dir{{ResourceID: "res-2", Path: "/Users/u/other"}}
	d := NewLocalInPlace(LocalTarget{ResourceID: "res-1", Path: "/Users/u/app"}).WithReadOnly(dirs)

	dirs[0].Path = "/Users/u/mutated"
	if got := d.ReadOnly(); got[0].Path != "/Users/u/other" {
		t.Fatalf("mutating the input changed the decision: %q", got[0].Path)
	}
	out := d.ReadOnly()
	out[0].Path = "/Users/u/also-mutated"
	if again := d.ReadOnly(); again[0].Path != "/Users/u/other" {
		t.Fatalf("mutating the output changed the decision: %q", again[0].Path)
	}
}
