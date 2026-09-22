package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// The machine's side of cleanup (DENE-617): where the copies are, which are
// busy, and what the policy is. The policy RULES are execenv's, tested there.

// withProfile points cli.ProfileDir at a temp directory so the state reads and
// writes its own files rather than the developer's real ~/.multica.
//
// The variable matters: cli.multicaConfigRoot only honours
// MULTICA_TASK_CONFIG_ROOT, and falls back to $HOME for anything else. Setting
// some other name would leave these tests writing a real settings file — with
// cleanup ENABLED — into the profile of whoever ran them.
func withProfile(t *testing.T) *worktreeCleanupState {
	t.Helper()
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	return newWorktreeCleanupState("")
}

// The guard for the paragraph above: if the override ever stops being honoured,
// these tests must fail rather than quietly start writing to a real profile.
func TestCleanupStateWritesUnderTheOverriddenProfileRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	state := newWorktreeCleanupState("")

	path, err := state.path(worktreeCleanupSettingsFileName)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if filepath.Dir(path) != filepath.Clean(root) {
		t.Fatalf("settings path %q is outside the overridden profile root %q", path, root)
	}
	if err := state.SaveSettings(execenv.WorktreeCleanupSettings{Enabled: true}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("settings were not written where path() said: %v", err)
	}
}

// The default is OFF. A machine whose owner has never opened the screen must
// not be deleting anything, and a missing settings file is exactly that case.
func TestCleanupIsDisabledUntilTheUserEnablesIt(t *testing.T) {
	state := withProfile(t)

	settings := state.Settings()
	if settings.Enabled {
		t.Fatal("cleanup is enabled with no settings file; a user who never opened the screen would be deleting copies")
	}
	if settings.MinAgeDays != execenv.DefaultWorktreeCleanupMinAgeDays {
		t.Fatalf("MinAgeDays = %d, want the %d-day default", settings.MinAgeDays, execenv.DefaultWorktreeCleanupMinAgeDays)
	}

	if err := state.SaveSettings(execenv.WorktreeCleanupSettings{Enabled: true, MinAgeDays: 7, TrunkBranch: " main "}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	saved := state.Settings()
	if !saved.Enabled || saved.MinAgeDays != 7 || saved.TrunkBranch != "main" {
		t.Fatalf("settings round-tripped as %+v", saved)
	}

	// A zero or negative window is the default, never "delete immediately".
	if err := state.SaveSettings(execenv.WorktreeCleanupSettings{Enabled: true, MinAgeDays: 0}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if got := state.Settings().MinAgeDays; got != execenv.DefaultWorktreeCleanupMinAgeDays {
		t.Fatalf("MinAgeDays = %d after saving 0, want the default", got)
	}
}

// A truncated or hand-edited settings file reads as the default rather than
// failing the daemon — and the default is the safe direction.
func TestUnreadableSettingsFallBackToDisabled(t *testing.T) {
	state := withProfile(t)
	path, err := state.path(worktreeCleanupSettingsFileName)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"enabled": tr`), 0o644); err != nil {
		t.Fatal(err)
	}
	if state.Settings().Enabled {
		t.Fatal("a corrupt settings file read as enabled")
	}
}

// Working copies now live outside the Multica workspace entirely, so the only
// way the screen can find them is the daemon recording each root it uses.
func TestRootsAreRecordedAndDroppedWhenTheyDisappear(t *testing.T) {
	state := withProfile(t)
	root := t.TempDir()

	for i := 0; i < 2; i++ {
		if err := state.RecordRoot(root, "/repo"); err != nil {
			t.Fatalf("RecordRoot: %v", err)
		}
	}
	roots := state.Roots()
	if len(roots) != 1 || filepath.Clean(roots[0].Root) != filepath.Clean(root) {
		t.Fatalf("roots = %+v, want exactly one entry for %q", roots, root)
	}
	if roots[0].GitRoot != "/repo" || roots[0].LastUsedAt.IsZero() {
		t.Fatalf("root entry = %+v, want its repository and a timestamp", roots[0])
	}

	// A repository the user deleted should stop appearing in a disk report.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if got := state.Roots(); len(got) != 0 {
		t.Fatalf("roots = %+v after the directory was removed, want none", got)
	}
}

// A copy a task is running in is reported as in use, and stops being so when
// the task lets go. Counted, because one task enters a copy more than once.
func TestActiveCopiesAreCountedNotFlagged(t *testing.T) {
	state := withProfile(t)
	path := "/Users/me/code/app.multica-worktrees/task-1"

	state.MarkActive(path)
	state.MarkActive(path)
	if !state.activeSnapshot()[filepath.Clean(path)] {
		t.Fatal("a copy with a task in it is not reported as in use")
	}
	state.ReleaseActive(path)
	if !state.activeSnapshot()[filepath.Clean(path)] {
		t.Fatal("the first release cleared the flag while the copy was still in use")
	}
	state.ReleaseActive(path)
	if state.activeSnapshot()[filepath.Clean(path)] {
		t.Fatal("the copy is still reported as in use after every hold was released")
	}
}

// Removal is bounded by the roots this daemon recorded. A path it never put a
// working copy in is never a path this code deletes, whatever the request says.
func TestRemoveRefusesAPathOutsideEveryRecordedRoot(t *testing.T) {
	state := withProfile(t)
	stranger := filepath.Join(t.TempDir(), "somebody-elses-worktree")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.Remove(stranger); err == nil {
		t.Fatal("removed a directory under a root this daemon never recorded")
	}
	if _, err := os.Stat(stranger); err != nil {
		t.Fatalf("the directory was removed anyway: %v", err)
	}
	if err := state.Remove(""); err == nil {
		t.Fatal("an empty path was accepted")
	}
}

// Invariant 6 at the machine level: with the policy off, the automatic pass
// removes nothing — even a copy that satisfies every other condition.
func TestAutomaticCleanupDoesNothingWhileDisabled(t *testing.T) {
	state := withProfile(t)
	root := t.TempDir()
	copyPath := filepath.Join(root, "task-1")
	if err := os.MkdirAll(copyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRoot(root, "/repo"); err != nil {
		t.Fatal(err)
	}
	state.nowFunc = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }

	removed, bytes, errs := state.RunAutomatic()
	if removed != 0 || bytes != 0 || len(errs) != 0 {
		t.Fatalf("RunAutomatic with cleanup disabled removed %d copies (%d bytes, errs %v)", removed, bytes, errs)
	}
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("the copy was removed while cleanup was off: %v", err)
	}

	// And the report is still produced, so the user can read the verdicts
	// before switching anything on.
	report := state.Scan()
	if report.Settings.Enabled {
		t.Fatal("the report claims cleanup is enabled")
	}
	if len(report.Items) != 1 || report.Items[0].KeepReason != execenv.KeepNotMulticaCreated {
		t.Fatalf("items = %+v, want the unrecorded copy kept as %q", report.Items, execenv.KeepNotMulticaCreated)
	}
}

// countingProbe records that the scan reached git at all. Its answers do not
// matter — the point of the test below is that it is never asked.
type countingProbe struct{ calls int }

func (p *countingProbe) Dirty(string) (bool, error) { p.calls++; return true, nil }
func (p *countingProbe) MergeEvidenceOf(_, _, _ string) (execenv.WorktreeMergeEvidence, error) {
	p.calls++
	return execenv.MergeEvidenceNone, nil
}
func (p *countingProbe) CurrentBranch(string) (string, error) { p.calls++; return "main", nil }
func (p *countingProbe) DefaultBranch(string) (string, error) { p.calls++; return "main", nil }

// With the policy off, the scheduled pass does not even LOOK (DENE-648). The
// scan sizes every working copy and probes git in each; on a machine where
// cleanup was never switched on that is gigabytes of disk IO every couple of
// hours to reach a decision already made by the switch.
func TestAutomaticCleanupDoesNotScanWhileDisabled(t *testing.T) {
	state := withProfile(t)
	probe := &countingProbe{}
	state.probe = probe
	root := t.TempDir()
	copyPath := filepath.Join(root, "task-1")
	if err := os.MkdirAll(copyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordRoot(root, "/repo"); err != nil {
		t.Fatal(err)
	}

	if _, _, errs := state.RunAutomatic(); len(errs) != 0 {
		t.Fatalf("RunAutomatic errored while disabled: %v", errs)
	}
	if probe.calls != 0 {
		t.Fatalf("the disabled pass ran %d git probes; it must return before scanning", probe.calls)
	}

	// The settings screen's preview still scans with the policy off — that is
	// what lets a user read the verdicts before consenting (invariant 6).
	if report := state.Scan(); len(report.Items) != 1 {
		t.Fatalf("the preview stopped reporting copies while disabled: %+v", report.Items)
	}
}

// The entry point (DENE-617 S1). Everything above tests the machine's rules;
// this tests that something in production actually RUNS them. The feature's
// whole promise — "turn it on and merged, aged, clean copies go away" — is a
// promise about a scheduled pass, and a pass nothing calls keeps none of it.
//
// It drives the scheduled loop itself rather than RunAutomatic, with daemon GC
// switched OFF (DENE-648): the pass has to keep its promise on a machine whose
// owner disabled workspace GC, because the storage screen's switch says nothing
// about workspace GC.
func TestScheduledCleanupRunsWithDaemonGCDisabled(t *testing.T) {
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	d := newGCTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	repo, root, copyPath := newCleanableWorktree(t)
	if err := d.worktreeCleanup.RecordRoot(root, repo); err != nil {
		t.Fatalf("RecordRoot: %v", err)
	}
	if err := d.worktreeCleanup.SaveSettings(execenv.WorktreeCleanupSettings{
		Enabled: true, MinAgeDays: 14, TrunkBranch: "main",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// Sanity: the copy qualifies, so a green result below means the pass ran
	// rather than that there was nothing to remove.
	report := d.worktreeCleanup.Scan()
	if len(report.Items) != 1 || !report.Items[0].Eligible() {
		t.Fatalf("fixture is not a removal candidate: %+v", report.Items)
	}

	d.cfg.GCEnabled = false
	worktreeCleanupStartupDelay = time.Millisecond
	t.Cleanup(func() { worktreeCleanupStartupDelay = 30 * time.Second })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.worktreeCleanupLoop(ctx)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(copyPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the scheduled pass left the qualifying copy at %q; automatic cleanup is not wired to anything (or is still gated on GCEnabled)", copyPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Removal went through git, so the repository has no orphan metadata left.
	if out := runGitForGC(t, repo, "worktree", "list"); strings.Contains(out, copyPath) {
		t.Fatalf("git still lists the removed copy:\n%s", out)
	}
}

// The same pass with the policy off removes nothing (invariant 6): the switch
// is the user's consent, and the scheduled pass must not be a way around it.
func TestScheduledCleanupRemovesNothingWhileCleanupIsDisabled(t *testing.T) {
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	d := newGCTestDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	repo, root, copyPath := newCleanableWorktree(t)
	if err := d.worktreeCleanup.RecordRoot(root, repo); err != nil {
		t.Fatalf("RecordRoot: %v", err)
	}

	d.runWorktreeCleanup()

	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("the scheduled pass removed a copy while cleanup was switched off: %v", err)
	}
}

// newCleanableWorktree builds a real repository plus one Multica-owned working
// copy that satisfies every one of the five conditions: recorded as ours, idle,
// clean, merged into main (its branch has no commits of its own) and long past
// the retention window. Returns the repository, the worktree root and the copy.
func newCleanableWorktree(t *testing.T) (repo, root, copyPath string) {
	t.Helper()
	base := t.TempDir()
	repo = filepath.Join(base, "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForGC(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForGC(t, repo, "add", ".")
	runGitForGC(t, repo, "commit", "-m", "initial")

	// Beside the repository, never inside it (invariant 5).
	root = filepath.Join(base, "app.multica-worktrees")
	copyPath = filepath.Join(root, "task-1")
	runGitForGC(t, repo, "worktree", "add", "-b", "multica/task-1", copyPath, "main")

	// The ownership record execenv writes next to a copy it created. Written
	// here rather than through execenv because the writer is unexported; the
	// layout is the one readWorktreeRecord reads.
	rec := execenv.WorktreeRecord{
		Path:      copyPath,
		GitRoot:   repo,
		Branch:    "multica/task-1",
		CreatedAt: time.Now().Add(-90 * 24 * time.Hour).UTC(),
		LastRunAt: time.Now().Add(-60 * 24 * time.Hour).UTC(),
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	recordDir := filepath.Join(root, ".multica")
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, filepath.Base(copyPath)+".json"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, root, copyPath
}
