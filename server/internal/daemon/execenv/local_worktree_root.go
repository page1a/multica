package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Where a parallel-mode working copy lives, and how it is later recognised as
// Multica's (DENE-617).
//
// Until this file, every worktree was created inside the daemon's env root —
// Multica's own workspace directory. Two consequences the user could not do
// anything about: the copy vanished when the ordinary env-root GC reclaimed
// that task's root, taking with it the one place a failed run's state could be
// inspected; and the disk it occupied was Multica's, in a directory the user
// had no reason to browse.
//
// Now the copy lands beside the user's repository, on their disk, in a
// directory they can `cd` into. Three rules follow from that move, and this
// file exists to hold all three in one place:
//
//  1. Beside the repository, never inside it. A copy under the working tree
//     would show up in `git status`, in ripgrep, and in every editor index,
//     and would need an entry in the user's .gitignore to behave — a cost the
//     sibling layout simply does not have.
//  2. Every copy Multica creates leaves a record. Cleanup may only remove a
//     copy it can prove is its own; a worktree the user made with
//     `git worktree add` has no record and is therefore never touched.
//  3. Nothing here deletes anything. Removal is worktreeCleanup's job, and it
//     is off by default.

const (
	// worktreeRootSuffix names the sibling directory holding a repository's
	// Multica working copies: `<repo>.multica-worktrees`. Suffix rather than
	// prefix so it sorts next to the repository it belongs to.
	worktreeRootSuffix = ".multica-worktrees"

	// worktreeRecordDirName holds one ownership record per working copy,
	// inside the worktree root but outside every worktree. Inside would mean
	// the record shows up in the copy's own `git status` as an untracked file
	// and gets swept into the delivered branch.
	worktreeRecordDirName = ".multica"
)

// DefaultWorktreeRoot is where a repository's Multica working copies go when
// the resource does not name a location: the repository's sibling.
//
// `~/code/app` → `~/code/app.multica-worktrees`. The user can see it next to
// the repository in a file browser, which is the point — a copy they cannot
// find is a copy they cannot inspect after a failed run.
func DefaultWorktreeRoot(gitRoot string) string {
	clean := filepath.Clean(gitRoot)
	return filepath.Join(filepath.Dir(clean), filepath.Base(clean)+worktreeRootSuffix)
}

// ResolveWorktreeRoot decides where this repository's working copies go, and
// refuses the placements that would break something.
//
// An empty `configured` means the default sibling. A configured root must be
// absolute — a relative path would resolve against whatever the daemon's
// working directory happens to be — and must not sit inside the repository's
// working tree, which is rule 1 above.
func ResolveWorktreeRoot(gitRoot, configured string) (string, error) {
	root := strings.TrimSpace(configured)
	if root == "" {
		return DefaultWorktreeRoot(gitRoot), nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("execenv: worktree_root must be an absolute path, got %q", root)
	}
	root = filepath.Clean(root)
	if inside, err := pathIsInside(gitRoot, root); err != nil {
		return "", err
	} else if inside {
		return "", fmt.Errorf(
			"execenv: worktree_root %q is inside the repository %q — working copies there would appear in the repository's own `git status` "+
				"and in every editor and search index; put them beside the repository instead (the default is %q)",
			root, gitRoot, DefaultWorktreeRoot(gitRoot))
	}
	return root, nil
}

// pathIsInside reports whether candidate is the directory parent or lives
// under it, comparing canonical forms.
//
// Both sides must be canonicalised the SAME way or the comparison is
// meaningless. The worktree root routinely does not exist yet — the first task
// that needs it creates it — and EvalSymlinks fails on a path that is not
// there. Falling back to the cleaned literal for that side alone is what makes
// `/tmp/repo/.worktrees` read as outside `/tmp/repo` on macOS, where the
// existing parent canonicalises to `/private/tmp/repo` and the missing child
// does not. canonicalize walks up to the nearest existing ancestor and
// re-attaches the rest, so both sides land in the same namespace.
func pathIsInside(parent, candidate string) (bool, error) {
	rel, err := filepath.Rel(canonicalize(parent), canonicalize(candidate))
	if err != nil {
		return false, fmt.Errorf("execenv: compare %q against %q: %w", candidate, parent, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// canonicalize resolves as much of a path as exists and keeps the rest
// literally, so a directory that has not been created yet still compares in
// the same namespace as one that has.
func canonicalize(p string) string {
	clean := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	parent := filepath.Dir(clean)
	if parent == clean {
		return clean
	}
	return filepath.Join(canonicalize(parent), filepath.Base(clean))
}

// SameCanonicalPath reports whether two paths name the same place, including
// a path that does not exist yet. /tmp and /private/tmp on macOS are the case
// that makes a plain string compare lie.
func SameCanonicalPath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return canonicalize(a) == canonicalize(b)
}

// WorktreeRecord is the proof that Multica created a working copy, written
// beside it the moment it exists.
//
// It is what makes cleanup safe to automate. Without it the only way to tell a
// Multica copy from one the user made with `git worktree add` would be the
// directory's name, and guessing wrong means deleting someone's work.
type WorktreeRecord struct {
	// Path is the working copy this record describes.
	Path string `json:"path"`
	// GitRoot is the repository it was created from.
	GitRoot string `json:"git_root"`
	// Branch is the branch checked out in it.
	Branch string `json:"branch"`
	// TaskID and WorkspaceID say which run created it, so a preserved copy can
	// be traced back to the run that left it.
	TaskID      string `json:"task_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	// CreatedAt and LastRunAt are what the cleanup age rule reads. They are
	// equal on a fresh copy and diverge when a later turn reuses it.
	CreatedAt time.Time `json:"created_at"`
	LastRunAt time.Time `json:"last_run_at"`
}

// worktreeRecordPath is where a working copy's record lives: in the root's
// `.multica` directory, named after the copy's own directory.
func worktreeRecordPath(worktreeRoot, worktreePath string) string {
	return filepath.Join(worktreeRoot, worktreeRecordDirName, filepath.Base(worktreePath)+".json")
}

// writeWorktreeRecord records a working copy as Multica's.
//
// Written after the copy exists, so a record never claims a directory that was
// never created; a crash between the two leaves an unrecorded copy, which
// cleanup treats as the user's and keeps. That asymmetry is deliberate: the
// cost of keeping a copy is disk, the cost of deleting one wrongly is work.
func writeWorktreeRecord(worktreeRoot string, rec WorktreeRecord) error {
	if strings.TrimSpace(worktreeRoot) == "" || strings.TrimSpace(rec.Path) == "" {
		return errors.New("execenv: worktree record needs a root and a path")
	}
	dest := worktreeRecordPath(worktreeRoot, rec.Path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	if rec.LastRunAt.IsZero() {
		rec.LastRunAt = rec.CreatedAt
	}
	payload, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// readWorktreeRecord loads a working copy's record. A missing or unreadable
// record means "not proven to be Multica's", which every caller must treat as
// the user's.
func readWorktreeRecord(worktreeRoot, worktreePath string) (*WorktreeRecord, error) {
	raw, err := os.ReadFile(worktreeRecordPath(worktreeRoot, worktreePath))
	if err != nil {
		return nil, err
	}
	var rec WorktreeRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	if strings.TrimSpace(rec.Path) == "" {
		return nil, fmt.Errorf("execenv: worktree record for %q names no path", worktreePath)
	}
	return &rec, nil
}

// removeWorktreeRecord drops the record for a working copy that no longer
// exists. Best effort by contract: a leftover record points at nothing, and
// the scan skips records whose directory is gone.
func removeWorktreeRecord(worktreeRoot, worktreePath string) {
	_ = os.Remove(worktreeRecordPath(worktreeRoot, worktreePath))
}
