package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// The machine's side of parallel-copy cleanup (DENE-617).
//
// execenv owns the RULES — which copy qualifies, and why one does not. This
// file owns the three things only the daemon can answer:
//
//   - Where the copies are. A working copy now lives beside the user's
//     repository, so nothing in the Multica workspace points at it. The daemon
//     records each root the first time it uses one.
//   - Which are busy. Only the process running the tasks knows that, and a
//     copy with a task in it must never be a candidate.
//   - What the policy is. Disk is a property of the computer, so the setting
//     lives with the machine's profile, not with a project.

const (
	// worktreeCleanupSettingsFileName holds this machine's cleanup policy.
	// Keep in sync with apps/desktop/src/main/worktree-cleanup.ts.
	worktreeCleanupSettingsFileName = "worktree-cleanup.json"
	// worktreeRootsFileName is the daemon's record of which worktree roots it
	// has created copies in. Without it, cleanup would have to guess where to
	// look — and a cleanup that guesses at directories is not something to
	// put on a user's disk.
	worktreeRootsFileName = "worktree-roots.json"
)

// worktreeRootEntry is one directory this machine has put working copies in.
type worktreeRootEntry struct {
	Root       string    `json:"root"`
	GitRoot    string    `json:"git_root,omitempty"`
	LastUsedAt time.Time `json:"last_used_at"`
}

type worktreeRootsFile struct {
	Roots []worktreeRootEntry `json:"roots"`
}

// worktreeCleanupState is the daemon's live view: the profile files plus the
// set of copies a task is currently running in.
type worktreeCleanupState struct {
	mu      sync.Mutex
	profile string
	active  map[string]int
	nowFunc func() time.Time
	probe   execenv.WorktreeGitProbe
}

func newWorktreeCleanupState(profile string) *worktreeCleanupState {
	return &worktreeCleanupState{
		profile: profile,
		active:  map[string]int{},
		nowFunc: time.Now,
		probe:   execenv.GitWorktreeProbe{},
	}
}

func (s *worktreeCleanupState) now() time.Time {
	if s == nil || s.nowFunc == nil {
		return time.Now()
	}
	return s.nowFunc()
}

func (s *worktreeCleanupState) path(name string) (string, error) {
	// Nil receiver: a Daemon built field-by-field in a test has no cleanup
	// state, and a run that reaches this path there must not panic on a
	// feature it never configured.
	if s == nil {
		return "", errors.New("worktree cleanup: no profile on this daemon")
	}
	dir, err := cli.ProfileDir(s.profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// MarkActive records that a task is running in a working copy, so the scan
// reports it as in use rather than as a removal candidate.
func (s *worktreeCleanupState) MarkActive(path string) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[filepath.Clean(path)]++
}

// IsActive reports whether a task is currently inside this working copy.
// Spellings that canonicalise to the same path count as the same copy, so a
// retry cannot miss a live holder just because one side went through /tmp and
// the other through /private/tmp.
func (s *worktreeCleanupState) IsActive(path string) bool {
	if s == nil || strings.TrimSpace(path) == "" {
		return false
	}
	want := filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[want] > 0 {
		return true
	}
	for active := range s.active {
		if execenv.SameCanonicalPath(active, want) {
			return true
		}
	}
	return false
}

// ReleaseActive undoes MarkActive. Counted rather than boolean: a copy can be
// entered more than once across a task's prepare/finalize path, and clearing
// the flag on the first exit would expose a still-running copy to cleanup.
func (s *worktreeCleanupState) ReleaseActive(path string) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	key := filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[key] <= 1 {
		delete(s.active, key)
		return
	}
	s.active[key]--
}

func (s *worktreeCleanupState) activeSnapshot() map[string]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.active))
	for k := range s.active {
		out[k] = true
	}
	return out
}

// Settings reads this machine's policy. A missing or unreadable file reads as
// the default, which is DISABLED — the conservative direction, and the one a
// user who has never opened the screen expects.
func (s *worktreeCleanupState) Settings() execenv.WorktreeCleanupSettings {
	def := execenv.WorktreeCleanupSettings{MinAgeDays: execenv.DefaultWorktreeCleanupMinAgeDays}
	path, err := s.path(worktreeCleanupSettingsFileName)
	if err != nil {
		return def
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return def
	}
	var out execenv.WorktreeCleanupSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return def
	}
	if out.MinAgeDays <= 0 {
		out.MinAgeDays = execenv.DefaultWorktreeCleanupMinAgeDays
	}
	return out
}

// SaveSettings writes the policy for this machine.
func (s *worktreeCleanupState) SaveSettings(next execenv.WorktreeCleanupSettings) error {
	if next.MinAgeDays <= 0 {
		next.MinAgeDays = execenv.DefaultWorktreeCleanupMinAgeDays
	}
	next.TrunkBranch = strings.TrimSpace(next.TrunkBranch)
	path, err := s.path(worktreeCleanupSettingsFileName)
	if err != nil {
		return err
	}
	return writeJSONFileAtomic(path, next)
}

// RecordRoot remembers a directory this machine put a working copy in.
//
// Called on every prepare rather than only the first: the timestamp is what
// lets a root that no longer holds anything age out of the list, and re-writing
// a small JSON file per task is cheaper than the scan that would otherwise have
// to walk the filesystem looking for roots.
func (s *worktreeCleanupState) RecordRoot(root, gitRoot string) error {
	root = strings.TrimSpace(root)
	if s == nil || root == "" {
		return nil
	}
	root = filepath.Clean(root)
	path, err := s.path(worktreeRootsFileName)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file := readWorktreeRoots(path)
	now := s.now().UTC()
	found := false
	for i := range file.Roots {
		if filepath.Clean(file.Roots[i].Root) != root {
			continue
		}
		file.Roots[i].LastUsedAt = now
		if strings.TrimSpace(gitRoot) != "" {
			file.Roots[i].GitRoot = gitRoot
		}
		found = true
		break
	}
	if !found {
		file.Roots = append(file.Roots, worktreeRootEntry{Root: root, GitRoot: gitRoot, LastUsedAt: now})
	}
	sort.Slice(file.Roots, func(a, b int) bool { return file.Roots[a].Root < file.Roots[b].Root })
	return writeJSONFileAtomic(path, file)
}

// Roots lists the worktree roots this machine knows about, dropping the ones
// whose directory is gone — a repository the user deleted should not keep
// showing up in a disk report.
func (s *worktreeCleanupState) Roots() []worktreeRootEntry {
	if s == nil {
		return nil
	}
	path, err := s.path(worktreeRootsFileName)
	if err != nil {
		return nil
	}
	file := readWorktreeRoots(path)
	var live []worktreeRootEntry
	for _, entry := range file.Roots {
		if info, statErr := os.Stat(entry.Root); statErr == nil && info.IsDir() {
			live = append(live, entry)
		}
	}
	return live
}

func readWorktreeRoots(path string) worktreeRootsFile {
	var file worktreeRootsFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return file
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return worktreeRootsFile{}
	}
	return file
}

// WorktreeCleanupReport is what the settings screen renders: the policy in
// force, and every working copy on this machine with the verdict on it.
//
// It is produced identically whether the policy is enabled or not — a preview
// the user can read BEFORE switching cleanup on is the whole reason the
// feature can be off by default and still be trustworthy (invariant 6).
type WorktreeCleanupReport struct {
	Settings execenv.WorktreeCleanupSettings `json:"settings"`
	Items    []execenv.WorktreeCleanupItem   `json:"items"`
	// Errors names roots that could not be scanned, so a partial report is
	// visibly partial instead of quietly short.
	Errors []string `json:"errors,omitempty"`
}

// Scan evaluates every working copy this machine holds.
func (s *worktreeCleanupState) Scan() WorktreeCleanupReport {
	settings := s.Settings()
	report := WorktreeCleanupReport{Settings: settings}
	inUse := s.activeSnapshot()
	now := s.now()
	for _, root := range s.Roots() {
		items, err := execenv.ScanWorktreeRoot(root.Root, settings, inUse, s.probe, now)
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
			continue
		}
		report.Items = append(report.Items, items...)
	}
	sort.Slice(report.Items, func(a, b int) bool { return report.Items[a].Path < report.Items[b].Path })
	return report
}

// Remove deletes one working copy on the user's explicit instruction.
//
// The policy's five conditions still apply — a user clicking "clean this up"
// is telling us WHICH copy, not overriding the rule that a copy with
// uncommitted work is never removed. What it does override is the enabled
// switch: an explicit click is consent for that one copy.
func (s *worktreeCleanupState) Remove(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return errors.New("worktree cleanup: path is required")
	}
	if !s.knownRoot(filepath.Dir(path)) {
		return fmt.Errorf("worktree cleanup: %q is not inside a worktree root this machine created", path)
	}
	return execenv.RemoveCleanableWorktree(filepath.Dir(path), path, s.Settings(), s.activeSnapshot(), s.probe, s.now())
}

// knownRoot reports whether a directory is one this daemon recorded. It is the
// outer boundary on every removal: a path the machine never put a working copy
// in is never a path this code deletes, whatever the request says.
func (s *worktreeCleanupState) knownRoot(dir string) bool {
	dir = filepath.Clean(dir)
	for _, entry := range s.Roots() {
		if filepath.Clean(entry.Root) == dir {
			return true
		}
	}
	return false
}

// RunAutomatic removes every copy the policy qualifies, and does nothing at
// all when the policy is off. Returns how many were removed and how many bytes
// that reclaimed, for the GC log.
func (s *worktreeCleanupState) RunAutomatic() (removed int, bytes int64, errs []error) {
	// The switch is read BEFORE the scan, not after it. Scanning means sizing
	// every working copy on disk and running a git probe in each — several
	// gigabytes' worth of IO per cycle on a machine whose owner never turned
	// cleanup on. The settings screen's preview deliberately scans while the
	// policy is off (invariant 6); this scheduled path must not.
	settings := s.Settings()
	if !settings.Enabled {
		return 0, 0, nil
	}
	report := s.Scan()
	for _, item := range report.Items {
		if !item.Eligible() {
			continue
		}
		if err := execenv.RemoveCleanableWorktree(
			filepath.Dir(item.Path), item.Path, report.Settings, s.activeSnapshot(), s.probe, s.now(),
		); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
		bytes += item.SizeBytes
	}
	return removed, bytes, errs
}

// runWorktreeCleanup is the automatic pass's one production entry point: the
// daemon's periodic GC cycle calls it, and it is what makes the setting on the
// storage screen do something rather than describe something.
//
// It reports nothing when there was nothing to do. A machine whose owner never
// switched cleanup on runs this every cycle and must stay silent, so only a
// removal or a failure reaches the log.
func (d *Daemon) runWorktreeCleanup() {
	removed, bytes, errs := d.worktreeCleanup.RunAutomatic()
	if d.logger == nil {
		return
	}
	for _, err := range errs {
		d.logger.Warn("gc: worktree cleanup failed for one copy", "error", err)
	}
	if removed > 0 {
		d.logger.Info("gc: worktree copies reclaimed", "removed", removed, "bytes_reclaimed", bytes)
	}
}

// writeJSONFileAtomic writes v as indented JSON through a temp file and a
// rename, so a reader never sees a half-written policy.
func writeJSONFileAtomic(path string, v any) error {
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// worktreeCleanupHandler serves the storage screen's three operations on the
// daemon's loopback HTTP server: read the report, change the policy, remove
// one copy.
//
// It lives on the local server rather than the platform API because everything
// it touches is on THIS machine — the directories, their sizes and their git
// state are not things the backend can see, and shipping them to a server
// would put the user's absolute paths somewhere they do not need to be.
func (d *Daemon) worktreeCleanupHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeDaemonJSON(w, http.StatusOK, d.worktreeCleanup.Scan())
		case http.MethodPut:
			var next execenv.WorktreeCleanupSettings
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := d.worktreeCleanup.SaveSettings(next); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeDaemonJSON(w, http.StatusOK, d.worktreeCleanup.Scan())
		case http.MethodDelete:
			// The copy to remove is named in the query string so a DELETE
			// carries no body, and the daemon answers with a fresh report so
			// the screen never has to reconcile its own optimistic edit
			// against what the filesystem actually looks like now.
			path := strings.TrimSpace(r.URL.Query().Get("path"))
			if path == "" {
				http.Error(w, "path is required", http.StatusBadRequest)
				return
			}
			if err := d.worktreeCleanup.Remove(path); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeDaemonJSON(w, http.StatusOK, d.worktreeCleanup.Scan())
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func writeDaemonJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
