package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Keep in sync with apps/desktop/src/main/local-directory-overrides.ts.
const localSharedOverridesFileName = "local-directory-overrides.json"

// localSharedOverride is one persisted skip-mutex row. Official cloud cannot
// store execution_mode=shared, so the desktop client writes in_place to the
// API and records the user's "run in parallel" choice here. The daemon
// applies it at assignment time so the path mutex is skipped and sidecars
// stay in the env root.
type localSharedOverride struct {
	DaemonID      string `json:"daemon_id"`
	LocalPath     string `json:"local_path"`
	SkipPathMutex bool   `json:"skip_path_mutex"`
}

type localSharedOverrideFile struct {
	Overrides []localSharedOverride `json:"overrides"`
}

type localSharedOverrideStore struct {
	mu      sync.Mutex
	path    string
	mtime   time.Time
	size    int64
	entries []localSharedOverride
}

func localSharedOverridesPath(profile string) (string, error) {
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, localSharedOverridesFileName), nil
}

func newLocalSharedOverrideStore(path string) *localSharedOverrideStore {
	return &localSharedOverrideStore{path: path}
}

func (s *localSharedOverrideStore) Has(daemonID, absPath, realPath string) bool {
	if s == nil {
		return false
	}
	s.reloadIfStale()
	s.mu.Lock()
	defer s.mu.Unlock()
	daemonID = strings.TrimSpace(daemonID)
	if daemonID == "" {
		return false
	}
	for _, e := range s.entries {
		if strings.TrimSpace(e.DaemonID) != daemonID {
			continue
		}
		if !e.SkipPathMutex {
			continue
		}
		if overridePathMatches(e.LocalPath, absPath, realPath) {
			return true
		}
	}
	return false
}

func overridePathMatches(stored, absPath, realPath string) bool {
	storedClean := filepath.Clean(strings.TrimSpace(stored))
	if storedClean == "" || storedClean == "." {
		return false
	}
	candidates := []string{
		filepath.Clean(strings.TrimSpace(absPath)),
		filepath.Clean(strings.TrimSpace(realPath)),
	}
	storedReal, err := filepath.EvalSymlinks(storedClean)
	if err == nil {
		storedReal = filepath.Clean(storedReal)
	} else {
		storedReal = storedClean
	}
	for _, candidate := range candidates {
		if candidate == "" || candidate == "." {
			continue
		}
		if candidate == storedClean || candidate == storedReal {
			return true
		}
		if real, err := filepath.EvalSymlinks(candidate); err == nil && filepath.Clean(real) == storedReal {
			return true
		}
	}
	return false
}

func (s *localSharedOverrideStore) reloadIfStale() {
	if s.path == "" {
		return
	}
	info, err := os.Stat(s.path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			s.mtime = time.Time{}
			s.size = 0
		}
		return
	}
	if info.ModTime().Equal(s.mtime) && info.Size() == s.size && s.entries != nil {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file localSharedOverrideFile
	if err := json.Unmarshal(raw, &file); err != nil {
		s.entries = nil
		s.mtime = info.ModTime()
		s.size = info.Size()
		return
	}
	s.entries = file.Overrides
	s.mtime = info.ModTime()
	s.size = info.Size()
}

func (d *Daemon) applyLocalSharedOverride(a *localDirectoryAssignment) {
	if d == nil || a == nil || a.UsesWorktree() || a.IsShared() {
		return
	}
	if d.localSharedOverrides == nil {
		return
	}
	if d.localSharedOverrides.Has(a.Ref.DaemonID, a.AbsPath, a.RealPath) {
		a.Ref.ExecutionMode = localDirectoryModeShared
	}
}

func (d *Daemon) resolveLocalDirectoryAssignment(task Task) (*localDirectoryAssignment, error) {
	assignment, _, err := d.resolveLocalDirectoryPlan(task)
	return assignment, err
}

// resolveLocalDirectoryPlan is resolveLocalDirectoryAssignment plus the
// project's other local directories on this machine, which the run may read
// but not write (DENE-617 invariant 1). The shared-mode override applies only
// to the writable one: it decides whether THIS run takes the path mutex, and
// a directory nothing runs in has no mutex to skip.
//
// Resolving is a pure read: the identity backfill is NOT fired from here. This
// runs during the lock pre-flight, where a task that ends up cancelled while
// queued for a contended path would still have POSTed its identity for a
// directory it never uses (DENE-730). runTask fires the backfill once the task
// is actually committed to the directory.
func (d *Daemon) resolveLocalDirectoryPlan(task Task) (*localDirectoryAssignment, []*localDirectoryAssignment, error) {
	assignment, readOnly, err := localDirectoryPlanForTask(task, d.cfg.DaemonID)
	if err != nil {
		return assignment, readOnly, err
	}
	if assignment != nil {
		d.applyLocalSharedOverride(assignment)
	}
	return assignment, readOnly, nil
}
