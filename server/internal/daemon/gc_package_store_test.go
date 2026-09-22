package daemon

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// fakePnpm puts a recording stub named pnpm on PATH. The prune path must never
// reach for a real, user-installed package manager in a default test run. The
// stub records its argv and the store it was pointed at, then runs script —
// which has no PATH to call out to, so it must stay shell-builtin only.
func fakePnpm(t *testing.T, script string) (argsLog, storeDirLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	storeDirLog = filepath.Join(dir, "store_dir")
	body := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + argsLog + "\n" +
		"printf '%s' \"$npm_config_store_dir\" > " + storeDirLog + "\n" +
		script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pnpm"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return argsLog, storeDirLog
}

func seedPackageStore(t *testing.T, root string) string {
	t.Helper()
	if _, err := execenv.PreparePackageStore(root); err != nil {
		t.Fatal(err)
	}
	store := execenv.PnpmStoreDir(root)
	if err := os.WriteFile(filepath.Join(store, "blob"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	return store
}

func newPackageStoreGCDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newGCTestDaemon(t, http.NewServeMux())
	d.cfg.SharedPackageStoreEnabled = true
	d.cfg.GCPackageStorePruneInterval = time.Hour
	// Prune on the interval alone, so each test states its own size policy.
	d.cfg.GCPackageStoreMaxBytes = 0
	return d
}

func markerPath(root string) string {
	return filepath.Join(execenv.PackageStoreRoot(root), packageStorePruneMarker)
}

func TestPrunePackageStoreRunsPnpmAgainstTheSharedStore(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	store := seedPackageStore(t, d.cfg.WorkspacesRoot)
	// Truncate rather than unlink: PATH holds only the stub, so the script has
	// no external commands to call.
	argsLog, _ := fakePnpm(t, ": > "+filepath.Join(store, "blob"))

	stats := &gcStats{byPattern: map[string]int{}}
	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, stats)

	got, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("pnpm was never invoked: %v", err)
	}
	if strings.TrimSpace(string(got)) != "store prune" {
		t.Errorf("pnpm args = %q, want \"store prune\"", strings.TrimSpace(string(got)))
	}
	if stats.packageStoreBytesReclaimed <= 0 {
		t.Errorf("packageStoreBytesReclaimed = %d, want the freed blob counted", stats.packageStoreBytesReclaimed)
	}
	if stats.bytesReclaimed != stats.packageStoreBytesReclaimed {
		t.Errorf("bytesReclaimed = %d, want it to include the %d freed here",
			stats.bytesReclaimed, stats.packageStoreBytesReclaimed)
	}
	if _, err := os.Stat(markerPath(d.cfg.WorkspacesRoot)); err != nil {
		t.Errorf("prune marker not written: %v", err)
	}
}

// The prune has to address the store the tasks fill, not whichever store the
// daemon's own environment happens to resolve to.
func TestPrunePackageStorePointsPnpmAtTheWorkspaceStore(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	store := seedPackageStore(t, d.cfg.WorkspacesRoot)
	_, envLog := fakePnpm(t, "")
	t.Setenv("npm_config_store_dir", filepath.Join(t.TempDir(), "some-other-store"))

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	got, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatalf("pnpm was never invoked: %v", err)
	}
	if string(got) != store {
		t.Errorf("npm_config_store_dir = %q, want the workspace store %q", got, store)
	}
}

func TestPrunePackageStoreHonoursTheInterval(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	seedPackageStore(t, d.cfg.WorkspacesRoot)
	argsLog, _ := fakePnpm(t, "")

	marker := markerPath(d.cfg.WorkspacesRoot)
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().Add(-time.Minute)
	if err := os.Chtimes(marker, recent, recent); err != nil {
		t.Fatal(err)
	}

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(argsLog); err == nil {
		t.Fatal("pnpm ran despite the store being pruned a minute ago")
	}
}

// A failed prune must retry on the next cycle rather than going quiet for a
// whole interval, so the marker is only touched on success.
func TestPrunePackageStoreKeepsRetryingAfterAFailure(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	seedPackageStore(t, d.cfg.WorkspacesRoot)
	fakePnpm(t, "exit 1")

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(markerPath(d.cfg.WorkspacesRoot)); err == nil {
		t.Fatal("marker written after a failed prune; the next cycle would skip the retry")
	}
}

func TestPrunePackageStoreSkipsWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*Daemon)
	}{
		{"feature off", func(d *Daemon) { d.cfg.SharedPackageStoreEnabled = false }},
		{"prune interval zero", func(d *Daemon) { d.cfg.GCPackageStorePruneInterval = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newPackageStoreGCDaemon(t)
			seedPackageStore(t, d.cfg.WorkspacesRoot)
			argsLog, _ := fakePnpm(t, "")
			tc.apply(d)

			d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

			if _, err := os.Stat(argsLog); err == nil {
				t.Fatalf("pnpm ran with %s", tc.name)
			}
		})
	}
}

// A store that was never populated is not the GC's to create: running here
// would make the prune, not task preparation, the thing that owns the path.
func TestPrunePackageStoreSkipsAnAbsentStore(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	argsLog, _ := fakePnpm(t, "")

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(argsLog); err == nil {
		t.Fatal("pnpm ran against a store that does not exist")
	}
}

func TestPrunePackageStoreSkipsWhenPnpmIsMissing(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	seedPackageStore(t, d.cfg.WorkspacesRoot)
	t.Setenv("PATH", t.TempDir())

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(markerPath(d.cfg.WorkspacesRoot)); err == nil {
		t.Fatal("marker written without a prune actually running")
	}
}

// A prune empties the store, so a small store must be left alone: reclaiming a
// few megabytes here costs the next task a full cold install.
func TestPrunePackageStoreLeavesAStoreUnderTheCeilingAlone(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	seedPackageStore(t, d.cfg.WorkspacesRoot)
	d.cfg.GCPackageStoreMaxBytes = 1 << 30
	argsLog, _ := fakePnpm(t, "")

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(argsLog); err == nil {
		t.Fatal("pnpm ran against a store far below the ceiling")
	}
	if _, err := os.Stat(markerPath(d.cfg.WorkspacesRoot)); err != nil {
		t.Fatalf("size check did not advance the prune marker: %v", err)
	}
}

func TestPrunePackageStoreDefersWhileATaskIsActive(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	seedPackageStore(t, d.cfg.WorkspacesRoot)
	d.cfg.GCPackageStoreMaxBytes = 1 // force the prune path if not guarded
	argsLog, _ := fakePnpm(t, "")
	d.activeTasks.Store(1)
	defer d.activeTasks.Store(0)

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(argsLog); err == nil {
		t.Fatal("pnpm ran while a task was active")
	}
	if _, err := os.Stat(markerPath(d.cfg.WorkspacesRoot)); err == nil {
		t.Fatal("active-task deferral advanced the prune marker")
	}
}

func TestPrunePackageStoreRunsOnceTheCeilingIsExceeded(t *testing.T) {
	d := newPackageStoreGCDaemon(t)
	store := seedPackageStore(t, d.cfg.WorkspacesRoot)
	d.cfg.GCPackageStoreMaxBytes = 1024 // the seeded blob is 4 KiB
	argsLog, _ := fakePnpm(t, ": > "+filepath.Join(store, "blob"))

	d.prunePackageStore(context.Background(), d.cfg.WorkspacesRoot, &gcStats{byPattern: map[string]int{}})

	if _, err := os.Stat(argsLog); err != nil {
		t.Fatalf("pnpm never ran on an oversized store: %v", err)
	}
}
