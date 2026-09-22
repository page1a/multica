package coderesolve

import (
	"encoding/json"
	"strings"
)

// Access marks what a run may do with a resource it receives. Only one
// directory per run is writable (DENE-617 invariant 1); the rest are named
// rather than hidden, so an agent that needs to read one does not have to
// discover it by accident or check out a second copy of it.
const (
	AccessReadWrite = "read-write"
	AccessReadOnly  = "read-only"
)

// FilterForCapabilities narrows a claim's resource set to what the claiming
// daemon can actually act on: what a daemon receives is a subset of what it
// declared it can handle (DENE-617 invariant 15).
//
// Narrowing at the dispatch point, rather than teaching old daemons new
// fields, is what makes "old daemons need no change and behave exactly as they
// do today" true (invariant 18). Two capabilities, two independent narrowings:
//
//   - Without MultiLocalDirectory, the daemon receives at most one
//     local_directory per machine. Before multiple directories existed, a
//     second row for its own daemon could only mean corrupt data, and such a
//     daemon fails the task outright when it sees one — correct then, a
//     task-killer now that a second row is a feature. It receives the FIRST in
//     position order, which is the same row a current daemon would choose to
//     write, so the narrowing costs it only the read-only extras it could not
//     have used anyway.
//
//   - Without WorktreeUserRoot, worktree_root is stripped. A daemon predating
//     the move of working copies onto the user's disk json-skips the field and
//     builds the copy inside its own env root, where the workspace GC reclaims
//     it. The user who chose a location would see their setting ignored,
//     silently and permanently, with the copy vanishing on a schedule they
//     never agreed to. Stripped rather than refused because ignoring the field
//     is exactly what such a daemon does today — the behaviour is unchanged,
//     and the decision says the same thing (see local(), worktree branch).
//
// Everything else survives untouched: the directory, its daemon binding and
// its execution mode are still what the project configured, because those an
// old daemon does implement. A ref the server cannot parse is left in place
// rather than dropped — this function narrows what a daemon receives, and a
// row it cannot read is not a row it can prove is a duplicate. The daemon's
// own parser reports the malformed ref, which is where that error belongs.
func FilterForCapabilities(resources []Resource, daemon Daemon) []Resource {
	if len(resources) == 0 {
		return resources
	}
	if daemon.MultiLocalDirectory && daemon.WorktreeUserRoot {
		return resources
	}
	seenDaemon := map[string]bool{}
	out := make([]Resource, 0, len(resources))
	for _, res := range resources {
		if res.ResourceType != ResourceTypeLocalDirectory {
			out = append(out, res)
			continue
		}
		if !daemon.MultiLocalDirectory {
			var ref struct {
				DaemonID string `json:"daemon_id"`
			}
			if err := json.Unmarshal(res.Ref, &ref); err == nil {
				if key := strings.TrimSpace(ref.DaemonID); key != "" {
					if seenDaemon[key] {
						continue
					}
					seenDaemon[key] = true
				}
			}
		}
		if !daemon.WorktreeUserRoot {
			if stripped, changed := stripRefField(res.Ref, "worktree_root"); changed {
				res.Ref = stripped
			}
		}
		out = append(out, res)
	}
	return out
}

// stripRefField removes one key from a resource ref, leaving every other key —
// including keys written by a newer server this binary cannot interpret —
// intact in value. Reports whether the key was there.
func stripRefField(ref json.RawMessage, key string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ref, &fields); err != nil {
		return ref, false
	}
	if _, ok := fields[key]; !ok {
		return ref, false
	}
	delete(fields, key)
	out, err := json.Marshal(fields)
	if err != nil {
		return ref, false
	}
	return out, true
}

// AccessFor reports how a run may use one resource, given the decision it is
// running under. The writable directory is the one the decision chose;
// every other local directory on the machine is read-only, and that is what
// the brief and .multica/project/resources.json say about it.
//
// Non-directory resources have no access dimension — a repository is checked
// out, not written in place — and report "" so the field stays off their wire.
func AccessFor(d Decision, resourceType, resourceID string) string {
	if resourceType != ResourceTypeLocalDirectory {
		return ""
	}
	if target, ok := d.Local(); ok && target.ResourceID == resourceID {
		return AccessReadWrite
	}
	return AccessReadOnly
}
