package coderesolve

import (
	"encoding/json"
	"testing"
)

// The invariant these tests pin: what a daemon receives is a subset of what it
// declared it can handle (DENE-617 invariant 15), and a daemon that declared
// nothing new behaves exactly as it does today (invariant 18).

func refFields(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("resource_ref is not readable after filtering: %v", err)
	}
	return out
}

func localDirsOf(resources []Resource) []Resource {
	var out []Resource
	for _, r := range resources {
		if r.ResourceType == ResourceTypeLocalDirectory {
			out = append(out, r)
		}
	}
	return out
}

// mixedSet is one project as the database returns it, in position order: three
// directories for THIS machine, one for another, and a repository.
func mixedSet() []Resource {
	return []Resource{
		localDir("res-first", "/Users/u/first", func(r *LocalDirRef) {
			r.ExecutionMode = ModeWorktree
			r.WorktreeRoot = "/Volumes/scratch/copies"
		}),
		localDir("res-second", "/Users/u/second"),
		repoRes("res-repo", "https://github.com/example/app"),
		localDir("res-third", "/Users/u/third"),
		localDir("res-elsewhere", "/srv/app", func(r *LocalDirRef) { r.DaemonID = otherDaemon }),
	}
}

// Every combination of the two capabilities, which is what the ticket asks for
// and what a table proves in one place.
func TestFilterForCapabilities_EveryCombination(t *testing.T) {
	cases := []struct {
		name string
		// inputs
		multi, userRoot bool
		// expectations
		wantLocalIDs     []string
		wantWorktreeRoot string // "" means the field must be absent
	}{
		{
			name: "neither capability: one directory per machine, no worktree_root",
			// The oldest daemon still in the field. It must see exactly what it
			// saw before multiple directories and user-owned roots existed.
			multi: false, userRoot: false,
			wantLocalIDs: []string{"res-first", "res-elsewhere"}, wantWorktreeRoot: "",
		},
		{
			name:  "user root only: one directory per machine, root preserved",
			multi: false, userRoot: true,
			wantLocalIDs: []string{"res-first", "res-elsewhere"}, wantWorktreeRoot: "/Volumes/scratch/copies",
		},
		{
			name:  "multi only: every directory, no worktree_root",
			multi: true, userRoot: false,
			wantLocalIDs: []string{"res-first", "res-second", "res-third", "res-elsewhere"}, wantWorktreeRoot: "",
		},
		{
			name:  "both: the set the project configured, untouched",
			multi: true, userRoot: true,
			wantLocalIDs: []string{"res-first", "res-second", "res-third", "res-elsewhere"}, wantWorktreeRoot: "/Volumes/scratch/copies",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FilterForCapabilities(mixedSet(), Daemon{ID: thisDaemon, MultiLocalDirectory: tc.multi, WorktreeUserRoot: tc.userRoot})

			var ids []string
			for _, r := range localDirsOf(got) {
				ids = append(ids, r.ID)
			}
			if len(ids) != len(tc.wantLocalIDs) {
				t.Fatalf("local directories = %v, want %v", ids, tc.wantLocalIDs)
			}
			for i, want := range tc.wantLocalIDs {
				if ids[i] != want {
					t.Fatalf("local directories = %v, want %v", ids, tc.wantLocalIDs)
				}
			}

			// The kept row is the FIRST in position order — the same row a
			// current daemon would choose to write. Narrowing must not change
			// which directory the run lands in.
			first := localDirsOf(got)[0]
			fields := refFields(t, first.Ref)
			if got, want := fields["worktree_root"], tc.wantWorktreeRoot; tc.wantWorktreeRoot == "" {
				if _, present := fields["worktree_root"]; present {
					t.Errorf("worktree_root = %v, want the field absent for this daemon", got)
				}
			} else if got != want {
				t.Errorf("worktree_root = %v, want %q", got, want)
			}

			// Whatever else the row said is still what the project configured.
			if fields["local_path"] != "/Users/u/first" || fields["daemon_id"] != thisDaemon || fields["execution_mode"] != ModeWorktree {
				t.Errorf("filtering rewrote the binding: %v", fields)
			}

			// Repositories are not a local-directory concern and are never
			// narrowed; a project with a directory may still reference others.
			var repos int
			for _, r := range got {
				if r.ResourceType == ResourceTypeGitHubRepo {
					repos++
				}
			}
			if repos != 1 {
				t.Errorf("github_repo resources = %d, want 1", repos)
			}
		})
	}
}

// One per MACHINE, not one overall. A project can bind a directory on each of
// several machines, and narrowing must not hide another machine's row — the
// daemon skips it anyway, and dropping it would change what the brief lists.
func TestFilterForCapabilities_KeepsOneDirectoryPerMachine(t *testing.T) {
	resources := []Resource{
		localDir("res-a1", "/Users/u/a1"),
		localDir("res-b1", "/srv/b1", func(r *LocalDirRef) { r.DaemonID = otherDaemon }),
		localDir("res-a2", "/Users/u/a2"),
		localDir("res-b2", "/srv/b2", func(r *LocalDirRef) { r.DaemonID = otherDaemon }),
	}

	got := FilterForCapabilities(resources, Daemon{ID: thisDaemon})

	var ids []string
	for _, r := range localDirsOf(got) {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != "res-a1" || ids[1] != "res-b1" {
		t.Fatalf("local directories = %v, want [res-a1 res-b1]: the first row of each machine", ids)
	}
}

// A row the server cannot parse is left in place. This function narrows what a
// daemon receives, and a row it cannot read is not a row it can prove is a
// duplicate; the daemon's own parser reports the malformed ref, which is where
// that error belongs.
func TestFilterForCapabilities_KeepsAnUnreadableRef(t *testing.T) {
	broken := Resource{ID: "res-broken", ResourceType: ResourceTypeLocalDirectory, Ref: json.RawMessage(`{`)}
	resources := []Resource{broken, localDir("res-ok", "/Users/u/app")}

	got := FilterForCapabilities(resources, Daemon{ID: thisDaemon})

	if len(got) != 2 || got[0].ID != "res-broken" {
		t.Fatalf("resources = %+v, want the unreadable row kept in place", got)
	}
}

// Stripping one key must not disturb the others — including keys a newer
// server wrote that this binary has no field for.
func TestFilterForCapabilities_StrippingPreservesUnknownKeys(t *testing.T) {
	resources := []Resource{{
		ID:           "res-1",
		ResourceType: ResourceTypeLocalDirectory,
		Ref: json.RawMessage(`{"local_path":"/Users/u/app","daemon_id":"` + thisDaemon +
			`","worktree_root":"/Volumes/copies","future_field":{"nested":true}}`),
	}}

	got := FilterForCapabilities(resources, Daemon{ID: thisDaemon, MultiLocalDirectory: true})

	fields := refFields(t, got[0].Ref)
	if _, present := fields["worktree_root"]; present {
		t.Error("worktree_root survived a daemon that cannot honour it")
	}
	nested, ok := fields["future_field"].(map[string]any)
	if !ok || nested["nested"] != true {
		t.Errorf("future_field = %v, want it preserved for the server that wrote it", fields["future_field"])
	}
}

// The narrowed set is what Resolve must run on, or the server would promise a
// directory the daemon never received.
func TestFilterForCapabilities_NarrowedSetStillResolvesToTheSameDirectory(t *testing.T) {
	for _, daemonCaps := range []Daemon{
		{ID: thisDaemon, LocalWorktree: true},
		{ID: thisDaemon, LocalWorktree: true, MultiLocalDirectory: true},
		{ID: thisDaemon, LocalWorktree: true, WorktreeUserRoot: true},
		{ID: thisDaemon, LocalWorktree: true, MultiLocalDirectory: true, WorktreeUserRoot: true},
	} {
		narrowed := FilterForCapabilities(mixedSet(), daemonCaps)
		d := Resolve(baseTask(), []Project{{ID: "project-1", Resources: narrowed}}, daemonCaps)

		requireKind(t, d, KindLocalWorktree)
		target, _ := d.Local()
		if target.ResourceID != "res-first" {
			t.Fatalf("caps %+v wrote %q, want res-first on every capability set", daemonCaps, target.ResourceID)
		}
		wantRoot := "/Volumes/scratch/copies"
		if !daemonCaps.WorktreeUserRoot {
			// Without the capability the root never reached the daemon, so the
			// decision must state the default it will actually use.
			wantRoot = "/Users/u/first.multica-worktrees"
		}
		if target.WorktreeRoot != wantRoot {
			t.Errorf("caps %+v: worktree_root = %q, want %q", daemonCaps, target.WorktreeRoot, wantRoot)
		}
		// A daemon that cannot take multiple directories must not be told
		// about read-only extras it has no way to use.
		if got := len(d.ReadOnly()); daemonCaps.MultiLocalDirectory && got != 2 {
			t.Errorf("caps %+v: read-only dirs = %d, want 2", daemonCaps, got)
		} else if !daemonCaps.MultiLocalDirectory && got != 0 {
			t.Errorf("caps %+v: read-only dirs = %d, want 0", daemonCaps, got)
		}
	}
}

// The unchosen directories are what carries access: "read-only" into the brief
// and .multica/project/resources.json. One run writes one directory.
func TestAccessFor_OnlyTheChosenDirectoryIsWritable(t *testing.T) {
	resources := []Resource{localDir("res-first", "/Users/u/first"), localDir("res-second", "/Users/u/second"), repoRes("res-repo", "https://github.com/example/app")}
	d := Resolve(baseTask(), []Project{{ID: "project-1", Resources: resources}}, fullDaemon())

	if got := AccessFor(d, ResourceTypeLocalDirectory, "res-first"); got != AccessReadWrite {
		t.Errorf("chosen directory access = %q, want %q", got, AccessReadWrite)
	}
	if got := AccessFor(d, ResourceTypeLocalDirectory, "res-second"); got != AccessReadOnly {
		t.Errorf("unchosen directory access = %q, want %q", got, AccessReadOnly)
	}
	// A repository is checked out, not written in place; it has no access
	// dimension and must not carry the field.
	if got := AccessFor(d, ResourceTypeGitHubRepo, "res-repo"); got != "" {
		t.Errorf("repository access = %q, want empty", got)
	}
}

// When the run is not in a local directory at all, every directory on the
// machine is read-only — including the one position would have chosen.
func TestAccessFor_NothingIsWritableWhenTheRunIsNotLocal(t *testing.T) {
	d := NewRemoteCache(RemoteTarget{URL: "https://github.com/example/app"})

	if got := AccessFor(d, ResourceTypeLocalDirectory, "res-first"); got != AccessReadOnly {
		t.Errorf("access = %q, want %q for a run that writes no local directory", got, AccessReadOnly)
	}
}
