package execenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Shared scratch is where a run with no code goes (DENE-622).
//
// The server's Decision names a path relative to a root only the daemon
// knows. That root is one directory per workspace on this machine, under
// {WorkspacesRoot}/.sessions/{workspaceID}. Every turn of one session uses
// the same child, so a conversation does not mint a task directory.
//
// The leading dot is load-bearing: the workspace GC and the disk-usage walk
// both skip dot-directories. This tree is reclaimed only by PruneSharedScratch,
// which never leaves the root and never follows a symlink.

const (
	// SharedScratchRootName is the single directory, under a workspaces root,
	// that holds every workspace's shared session folders on this machine.
	SharedScratchRootName = ".sessions"
	// DefaultSharedScratchRetentionDays is how long an idle session folder
	// stays. Fourteen days matches the other conversation stores (Codex and
	// Hermes sessions): long enough to reopen a thread, short enough that a
	// machine does not accumulate one directory per question.
	DefaultSharedScratchRetentionDays = 14
	sharedScratchMarkerFile           = ".multica-session.json"
)

// sessionSegment is the only shape a workspace id or a session id may take
// inside the shared folder. Anything else — a slash, a dot-dot, a space —
// is a path, and a path here is a way out of the folder.
var sessionSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrSharedSessionBusy means another turn already holds this session folder.
var ErrSharedSessionBusy = errors.New("shared session folder is in use")

// ErrSharedSessionKept means a removal was refused because the path is not
// a session folder this daemon created, is in use, or is not inside the
// shared root. Callers surface the text; the sentinel is what tests assert.
var ErrSharedSessionKept = errors.New("shared session folder was not removed")

type sharedSessionMarker struct {
	WorkspaceID string    `json:"workspace_id"`
	SessionID   string    `json:"session_id"`
	LastUsed    time.Time `json:"last_used"`
}

// SharedScratchSettings is this machine's policy for the shared folder.
// Enabled defaults to true: the folder is Multica's, and the reason it
// exists is so it does not grow without a bound. RetentionDays is how long
// an idle session stays.
type SharedScratchSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
}

// SharedScratchSession is one conversation folder, as the settings screen
// shows it. Ours is false for a directory the daemon did not create; those
// are listed so they are visible, and never deleted.
type SharedScratchSession struct {
	Path        string    `json:"path"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	LastUsed    time.Time `json:"last_used,omitempty"`
	SizeBytes   int64     `json:"size_bytes"`
	InUse       bool      `json:"in_use"`
	Ours        bool      `json:"ours"`
	Expired     bool      `json:"expired"`
}

// SharedScratchReport is the whole shared folder: where it is, how big it
// is, and the session folders inside it.
type SharedScratchReport struct {
	Root      string                 `json:"root"`
	SizeBytes int64                  `json:"size_bytes"`
	Settings  SharedScratchSettings  `json:"settings"`
	Sessions  []SharedScratchSession `json:"sessions"`
}

// SharedScratchRoot is {workspacesRoot}/.sessions. One per machine (per
// daemon workspaces root). Missing is not an error — a machine that has
// never had a scratch run has no folder yet.
func SharedScratchRoot(workspacesRoot string) string {
	return filepath.Join(workspacesRoot, SharedScratchRootName)
}

// ResolveSharedSession joins the server's relative path onto this
// workspace's scratch root. The relative path must be sessions/<id>;
// anything else is refused rather than reinterpreted.
func ResolveSharedSession(workspacesRoot, workspaceID, relative string) (string, error) {
	root, sessionID, err := scratchLocation(workspacesRoot, workspaceID, relative)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions", sessionID), nil
}

func scratchLocation(workspacesRoot, workspaceID, relative string) (root, sessionID string, err error) {
	workspacesRoot = strings.TrimSpace(workspacesRoot)
	workspaceID = strings.TrimSpace(workspaceID)
	if workspacesRoot == "" || workspaceID == "" {
		return "", "", fmt.Errorf("shared scratch: workspace root and id are required")
	}
	if !sessionSegment.MatchString(workspaceID) {
		return "", "", fmt.Errorf("shared scratch: workspace id %q is not a single path segment", workspaceID)
	}
	sessionID, err = sessionIDFromRelative(relative)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(workspacesRoot, SharedScratchRootName, workspaceID), sessionID, nil
}

func sessionIDFromRelative(relative string) (string, error) {
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relative)))
	if rel == "." || rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("shared scratch path %q is not inside the session folder", relative)
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || parts[0] != "sessions" || !sessionSegment.MatchString(parts[1]) {
		return "", fmt.Errorf("shared scratch path %q is not sessions/<id>", relative)
	}
	return parts[1], nil
}

// OpenSharedSession creates the session folder if needed and stamps when it
// was used. It does not delete anything already in the folder: the next
// turn of the same session has to find the previous turn's files.
func OpenSharedSession(workspacesRoot, workspaceID, relative string, now time.Time) (string, error) {
	root, sessionID, err := scratchLocation(workspacesRoot, workspaceID, relative)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "sessions", sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("shared scratch: create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("shared scratch: inspect %s: %w", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("shared scratch: %s is not a real directory", dir)
	}
	if err := writeSharedSessionMarker(dir, workspaceID, sessionID, now); err != nil {
		return "", err
	}
	return dir, nil
}

func writeSharedSessionMarker(dir, workspaceID, sessionID string, now time.Time) error {
	marker := sharedSessionMarker{
		WorkspaceID: workspaceID,
		SessionID:   sessionID,
		LastUsed:    now.UTC(),
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("shared scratch: marshal marker: %w", err)
	}
	path := filepath.Join(dir, sharedScratchMarkerFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("shared scratch: write marker: %w", err)
	}
	return nil
}

// TouchSharedSession moves a session folder's idle clock to now. A directory
// that is not a session folder (no marker) is left alone.
func TouchSharedSession(dir string, now time.Time) error {
	marker, err := readSharedSessionMarker(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeSharedSessionMarker(dir, marker.WorkspaceID, marker.SessionID, now)
}

func readSharedSessionMarker(dir string) (sharedSessionMarker, error) {
	data, err := os.ReadFile(filepath.Join(dir, sharedScratchMarkerFile))
	if err != nil {
		return sharedSessionMarker{}, err
	}
	var marker sharedSessionMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return sharedSessionMarker{}, err
	}
	if marker.WorkspaceID == "" || marker.SessionID == "" {
		return sharedSessionMarker{}, fmt.Errorf("shared scratch: marker in %s is incomplete", dir)
	}
	return marker, nil
}

// ClaimSharedSession takes the session folder's execution lock without
// emptying it. A second turn waits its own way or fails; it does not reset
// the conversation out from under the first.
func ClaimSharedSession(dir string) (*EnvRootClaim, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("shared scratch: inspect %s: %w", dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("shared scratch: %s is not a real directory", dir)
	}
	lock, err := openLockFile(filepath.Join(dir, envRootLockFile))
	if err != nil {
		return nil, fmt.Errorf("shared scratch: open lock for %s: %w", dir, err)
	}
	locked, err := lockFileExclusiveNonBlocking(lock)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("shared scratch: lock %s: %w", dir, err)
	}
	if !locked {
		lock.Close()
		return nil, fmt.Errorf("shared scratch: %s: %w", dir, ErrSharedSessionBusy)
	}
	return &EnvRootClaim{rootDir: dir, lock: lock}, nil
}

// ScanSharedScratch lists the session folders under the machine root.
// inUse is the set of absolute paths a task in THIS process is running in.
// That set, not a second lock, is what protects a live folder: taking the
// lock here to "check" it can release the turn that already holds it, on
// platforms where the lock belongs to the process rather than the file
// descriptor. Symlinks are not followed.
func ScanSharedScratch(workspacesRoot string, now time.Time, retention time.Duration, inUse map[string]bool) (SharedScratchReport, error) {
	report := SharedScratchReport{
		Root:     SharedScratchRoot(workspacesRoot),
		Sessions: []SharedScratchSession{},
	}
	if strings.TrimSpace(workspacesRoot) == "" {
		return report, fmt.Errorf("shared scratch: workspaces root is required")
	}
	entries, err := os.ReadDir(report.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil
		}
		return report, fmt.Errorf("shared scratch: read %s: %w", report.Root, err)
	}
	for _, ws := range entries {
		if !ws.IsDir() || ws.Type()&os.ModeSymlink != 0 {
			continue
		}
		sessionsDir := filepath.Join(report.Root, ws.Name(), "sessions")
		sessions, readErr := os.ReadDir(sessionsDir)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return report, fmt.Errorf("shared scratch: read %s: %w", sessionsDir, readErr)
		}
		for _, session := range sessions {
			path := filepath.Join(sessionsDir, session.Name())
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.IsDir() {
				continue
			}
			item := SharedScratchSession{Path: path, SizeBytes: sharedTreeSize(path)}
			report.SizeBytes += item.SizeBytes
			if info.Mode()&os.ModeSymlink != 0 {
				report.Sessions = append(report.Sessions, item)
				continue
			}
			marker, markerErr := readSharedSessionMarker(path)
			if markerErr != nil {
				report.Sessions = append(report.Sessions, item)
				continue
			}
			item.Ours = true
			item.WorkspaceID = marker.WorkspaceID
			item.SessionID = marker.SessionID
			item.LastUsed = marker.LastUsed
			item.InUse = inUse[filepath.Clean(path)]
			if retention > 0 && !item.LastUsed.IsZero() && now.Sub(item.LastUsed) >= retention {
				item.Expired = !item.InUse
			}
			report.Sessions = append(report.Sessions, item)
		}
	}
	return report, nil
}

// sharedScratchBeforeRemove, when non-nil, runs after a session has been
// judged expired and before it is removed. Production leaves it nil. Tests
// set it to reopen the session in that gap, which is where a turn can
// rewrite the marker while the scan is still walking other folders.
var sharedScratchBeforeRemove func(path string)

// PruneSharedScratch removes session folders this daemon created that have
// been idle for at least retention. retention <= 0 removes nothing.
//
// It deletes only a real directory, three segments under .sessions
// (workspace/sessions/id), carrying our marker, not in use, and not a
// symlink. A user directory, a working copy beside a repository, or a link
// that points at either of those is not this shape and is not touched.
// The marker is read again under the removal lock, and `now` is that
// scan's clock: a session opened after the scan started is kept.
func PruneSharedScratch(workspacesRoot string, now time.Time, retention time.Duration, inUse map[string]bool) (removed int, bytes int64) {
	if retention <= 0 {
		return 0, 0
	}
	report, err := ScanSharedScratch(workspacesRoot, now, retention, inUse)
	if err != nil {
		return 0, 0
	}
	for _, session := range report.Sessions {
		if !session.Expired || !session.Ours || session.InUse {
			continue
		}
		if sharedScratchBeforeRemove != nil {
			sharedScratchBeforeRemove(session.Path)
		}
		size := session.SizeBytes
		if err := RemoveSharedSession(workspacesRoot, session.Path, inUse, now); err != nil {
			continue
		}
		removed++
		bytes += size
	}
	return removed, bytes
}

// RemoveSharedSession deletes one idle session folder. It refuses anything
// that is not a marked session directory inside the shared root.
//
// scannedAt is when the caller decided the folder was idle. A marker
// rewritten after that — a turn opened the session while the scan was
// still walking other folders — is not idle anymore, and the folder stays.
func RemoveSharedSession(workspacesRoot, path string, inUse map[string]bool, scannedAt time.Time) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return fmt.Errorf("%w: path is empty", ErrSharedSessionKept)
	}
	if _, err := containedSessionPath(workspacesRoot, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSharedSessionKept, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is not a real directory", ErrSharedSessionKept, path)
	}
	if _, err := readSharedSessionMarker(path); err != nil {
		return fmt.Errorf("%w: %s was not created as a shared session", ErrSharedSessionKept, path)
	}
	if inUse[path] {
		return fmt.Errorf("%w: %s is in use", ErrSharedSessionKept, path)
	}
	claim, err := ClaimSharedSession(path)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSharedSessionKept, err)
	}
	defer claim.Release()
	// Re-check after the lock. The scan's verdict is not the verdict that
	// deletes: a turn can open this folder after the marker was read and
	// before this lock, rewriting LastUsed without holding the lock yet.
	// A directory that is merely still here is not enough — that is also
	// true of a folder just reopened.
	info, err = os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s changed before removal", ErrSharedSessionKept, path)
	}
	marker, markerErr := readSharedSessionMarker(path)
	if markerErr != nil {
		return fmt.Errorf("%w: %s changed before removal", ErrSharedSessionKept, path)
	}
	// Before, not After. The marker this scan already called expired was
	// older than the retention window, so it cannot already sit on the
	// scan clock. A stamp at that clock was written after the read.
	if !marker.LastUsed.Before(scannedAt) {
		return fmt.Errorf("%w: %s was opened again after the scan", ErrSharedSessionKept, path)
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("%w: %s", ErrSharedSessionKept, err)
	}
	return nil
}

// containedSessionPath reports whether path is workspace/sessions/id under
// the shared root. The check is lexical on purpose: EvalSymlinks would
// follow a link out of the root and then bless the target.
func containedSessionPath(workspacesRoot, path string) (string, error) {
	root := filepath.Clean(SharedScratchRoot(workspacesRoot))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %s is outside the shared session folder", ErrSharedSessionKept, path)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[1] != "sessions" || !sessionSegment.MatchString(parts[0]) || !sessionSegment.MatchString(parts[2]) {
		return "", fmt.Errorf("%w: %s is not a session folder", ErrSharedSessionKept, path)
	}
	return path, nil
}

// sharedTreeSize sums regular files under dir and does not follow symlinks,
// so a link out to a repository or a working copy is not counted as ours
// and a later removal cannot be argued from that count.
func sharedTreeSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}
