package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// packageStorePruneMarker records when the shared store was last pruned. Its
// mtime is the whole state: the interval check is "has it been long enough",
// and a missing marker means "never pruned, do it now".
const packageStorePruneMarker = ".last_prune"

// packageStorePruneTimeout bounds one `pnpm store prune`. The command walks
// every package in the store, so on a cold machine it is minutes, not seconds
// — but it must never outlive a GC cycle.
const packageStorePruneTimeout = 10 * time.Minute

// prunePackageStore drops packages no project references out of the shared
// pnpm store.
//
// Only the pnpm store is pruned. It is the one directory here with a real
// reclamation story: pnpm knows which of its content-addressed entries no
// node_modules on this machine still points at. The npm/yarn/bun/pip/uv caches
// are download caches their own tools bound, and GOMODCACHE is read-only by
// design — deleting entries out from under them by hand is how you get a
// half-populated cache that reports a hit and then fails to extract.
//
// Pruning is safe while tasks are running, and safe for node_modules trees
// already on disk: pnpm takes its own store lock, and a node_modules that was
// populated from the store does not depend on the store entry surviving — a
// clone (APFS/btrfs) owns its blocks outright, and a hardlink keeps the inode
// alive until the last link goes. Verified on pnpm 10 against three task
// checkouts: a prune that emptied the whole store left every one of them
// resolving and running.
//
// What a prune costs is warmth, not correctness — pnpm removes everything no
// tracked project claims, which in a daemon store is all of it, so the next
// task pays a cold install. That is why this runs on a size ceiling rather
// than on the clock alone.
func (d *Daemon) prunePackageStore(ctx context.Context, workspacesRoot string, stats *gcStats) {
	if !d.cfg.SharedPackageStoreEnabled || d.cfg.GCPackageStorePruneInterval <= 0 {
		return
	}
	// Do not race a live install. pnpm's store lock protects pnpm's own
	// bookkeeping, but a task may still be populating entries that no project
	// has claimed yet; pruning at that point can discard the work underneath
	// the install. The daemon-wide active-task count covers preparation and
	// execution, so one running task is enough to defer this best-effort pass.
	if d.activeTasks.Load() > 0 {
		d.logger.Debug("gc: shared package store prune deferred while tasks are active")
		return
	}
	storeDir := execenv.PnpmStoreDir(workspacesRoot)
	if storeDir == "" {
		return
	}
	if _, err := os.Stat(storeDir); err != nil {
		// Never populated: nothing to reclaim, and creating it here would make
		// the GC responsible for a directory only task preparation owns.
		return
	}

	markerPath := filepath.Join(execenv.PackageStoreRoot(workspacesRoot), packageStorePruneMarker)
	if info, err := os.Stat(markerPath); err == nil {
		if time.Since(info.ModTime()) < d.cfg.GCPackageStorePruneInterval {
			return
		}
	}

	before := dirSize(storeDir)
	// Below the ceiling the warm store is worth more than the bytes. See
	// DefaultGCPackageStoreMaxBytes: a prune empties the store rather than
	// evicting selectively, so pruning a small one just buys the next task a
	// cold install.
	if max := d.cfg.GCPackageStoreMaxBytes; max > 0 && before <= max {
		// This cycle did the expensive size check, so advance the marker even
		// though no prune was needed. Otherwise every GC cycle after the
		// interval would walk the entire store again until it crosses the
		// ceiling.
		touchPackageStoreMarker(markerPath)
		return
	}

	pnpmPath, err := exec.LookPath("pnpm")
	if err != nil {
		d.logger.Debug("gc: shared package store prune skipped; pnpm not on PATH", "store", storeDir)
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, packageStorePruneTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, pnpmPath, "store", "prune")
	// The prune must address the shared store, not whichever store this
	// daemon's own environment resolves to.
	cmd.Env = append(os.Environ(), "npm_config_store_dir="+storeDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Warn("gc: shared package store prune failed",
			"store", storeDir,
			"error", err,
			"output", truncateForLog(string(output)),
		)
		return
	}

	// Touch the marker only on success, so a failing prune retries next cycle
	// rather than going quiet for a whole interval.
	touchPackageStoreMarker(markerPath)

	if freed := before - dirSize(storeDir); freed > 0 {
		stats.bytesReclaimed += freed
		stats.packageStoreBytesReclaimed += freed
		d.logger.Info("gc: shared package store pruned", "store", storeDir, "bytes_reclaimed", freed)
	}
}

func touchPackageStoreMarker(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_ = f.Close()
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// truncateForLog keeps a subprocess's output usable in a structured log line.
func truncateForLog(s string) string {
	const limit = 2000
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(truncated)"
}
