package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// The machine's side of the shared session folder (DENE-622).
//
// execenv owns where the folder is and which of its children may be
// removed. This file owns the three things only the daemon can answer:
// which folders a task is in right now, what retention this machine asked
// for, and the loopback endpoints the settings screen calls.

const sharedScratchSettingsFileName = "shared-scratch.json"

type sharedScratchState struct {
	mu      sync.Mutex
	profile string
	active  map[string]int
	nowFunc func() time.Time
}

func newSharedScratchState(profile string) *sharedScratchState {
	return &sharedScratchState{
		profile: profile,
		active:  map[string]int{},
		nowFunc: time.Now,
	}
}

func (s *sharedScratchState) now() time.Time {
	if s == nil || s.nowFunc == nil {
		return time.Now()
	}
	return s.nowFunc()
}

func (s *sharedScratchState) path() (string, error) {
	if s == nil {
		return "", errors.New("shared scratch: no profile on this daemon")
	}
	dir, err := cli.ProfileDir(s.profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sharedScratchSettingsFileName), nil
}

// Settings reads this machine's policy. A missing file is the default:
// automatic cleanup on, sessions kept for 14 idle days. The folder is
// Multica's, so the conservative direction is to bound it.
func (s *sharedScratchState) Settings() execenv.SharedScratchSettings {
	out := execenv.SharedScratchSettings{
		Enabled:       true,
		RetentionDays: execenv.DefaultSharedScratchRetentionDays,
	}
	path, err := s.path()
	if err != nil {
		return out
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	// Enabled is a pointer on the wire so a file that predates the field,
	// or one that omits it, stays on. A bool would read that as "off".
	var stored struct {
		Enabled       *bool `json:"enabled"`
		RetentionDays int   `json:"retention_days"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return out
	}
	if stored.Enabled != nil {
		out.Enabled = *stored.Enabled
	}
	if stored.RetentionDays > 0 {
		out.RetentionDays = stored.RetentionDays
	}
	return out
}

// SaveSettings writes the policy for this machine.
func (s *sharedScratchState) SaveSettings(next execenv.SharedScratchSettings) error {
	if next.RetentionDays <= 0 {
		next.RetentionDays = execenv.DefaultSharedScratchRetentionDays
	}
	path, err := s.path()
	if err != nil {
		return err
	}
	stored := struct {
		Enabled       bool `json:"enabled"`
		RetentionDays int  `json:"retention_days"`
	}{Enabled: next.Enabled, RetentionDays: next.RetentionDays}
	return writeJSONFileAtomic(path, stored)
}

func (s *sharedScratchState) MarkActive(path string) {
	if s == nil || strings.TrimSpace(path) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[filepath.Clean(path)]++
}

func (s *sharedScratchState) ReleaseActive(path string) {
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

func (s *sharedScratchState) activeSnapshot() map[string]bool {
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

func (s *sharedScratchState) retention() time.Duration {
	days := s.Settings().RetentionDays
	if days <= 0 {
		days = execenv.DefaultSharedScratchRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Report is what the settings screen renders.
func (s *sharedScratchState) Report(workspacesRoot string) (execenv.SharedScratchReport, error) {
	report, err := execenv.ScanSharedScratch(workspacesRoot, s.now(), s.retention(), s.activeSnapshot())
	if err != nil {
		return report, err
	}
	report.Settings = s.Settings()
	return report, nil
}

// Prune removes idle session folders when this machine's policy says to.
// Called from the workspace GC cycle. A disabled policy removes nothing;
// the screen can still remove a folder by hand.
func (s *sharedScratchState) Prune(workspacesRoot string) (int, int64) {
	if s == nil || !s.Settings().Enabled {
		return 0, 0
	}
	return execenv.PruneSharedScratch(workspacesRoot, s.now(), s.retention(), s.activeSnapshot())
}

// PruneNow removes idle folders older than the retention window even when
// automatic cleanup is off. It is the screen's "clean expired" action.
func (s *sharedScratchState) PruneNow(workspacesRoot string) (int, int64) {
	if s == nil {
		return 0, 0
	}
	return execenv.PruneSharedScratch(workspacesRoot, s.now(), s.retention(), s.activeSnapshot())
}

// Remove deletes one idle session folder. The rules are re-checked here;
// the screen does not get to name an arbitrary path and have it deleted.
func (s *sharedScratchState) Remove(workspacesRoot, path string) error {
	return execenv.RemoveSharedSession(workspacesRoot, path, s.activeSnapshot(), s.now())
}

// taskSharedScratch resolves the session folder for a run the server placed
// in SharedScratch. ok is false for every other decision, including a task
// from a server that did not send one — those keep today's per-task
// directory. A SharedScratch path that is not a session id fails the run
// instead of picking somewhere else.
func (d *Daemon) taskSharedScratch(task Task) (dir, relative string, ok bool, err error) {
	if d == nil || task.CodeDecision == nil {
		return "", "", false, nil
	}
	scratch, isScratch := task.CodeDecision.Scratch()
	if !isScratch {
		return "", "", false, nil
	}
	dir, err = execenv.ResolveSharedSession(d.cfg.WorkspacesRoot, task.WorkspaceID, scratch.Path)
	if err != nil {
		return "", scratch.Path, true, err
	}
	return dir, scratch.Path, true, nil
}

// compile-time check that a Decision value is what Scratch expects. The
// method set is the contract; this stops a signature drift from compiling
// into a silent "never scratch".
var _ interface {
	Scratch() (coderesolve.ScratchTarget, bool)
} = coderesolve.Decision{}

func (d *Daemon) sharedScratchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.sharedScratch == nil {
			http.Error(w, "shared scratch is not available", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			report, err := d.sharedScratch.Report(d.cfg.WorkspacesRoot)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeDaemonJSON(w, http.StatusOK, report)
		case http.MethodPut:
			var next execenv.SharedScratchSettings
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := d.sharedScratch.SaveSettings(next); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			report, err := d.sharedScratch.Report(d.cfg.WorkspacesRoot)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeDaemonJSON(w, http.StatusOK, report)
		case http.MethodPost:
			// Clean the folders the policy already calls expired. The body
			// is empty on purpose: this does not take a path, so it cannot
			// be aimed at a user directory.
			if r.URL.Query().Get("expired") != "1" {
				http.Error(w, "expired=1 is required", http.StatusBadRequest)
				return
			}
			d.sharedScratch.PruneNow(d.cfg.WorkspacesRoot)
			report, err := d.sharedScratch.Report(d.cfg.WorkspacesRoot)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeDaemonJSON(w, http.StatusOK, report)
		case http.MethodDelete:
			path := strings.TrimSpace(r.URL.Query().Get("path"))
			if path == "" {
				http.Error(w, "path is required", http.StatusBadRequest)
				return
			}
			if err := d.sharedScratch.Remove(d.cfg.WorkspacesRoot, path); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			report, err := d.sharedScratch.Report(d.cfg.WorkspacesRoot)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeDaemonJSON(w, http.StatusOK, report)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}
