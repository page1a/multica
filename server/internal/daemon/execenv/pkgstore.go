package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PackageStoreDirName is the machine-global dependency store inside the
// workspaces root. Like .repos and .skill-cache it is a sibling of the
// per-workspace task directories, never one of them, so every walk over the
// root decides explicitly what to do with it.
const PackageStoreDirName = ".pkg-store"

// Package-manager subdirectories inside the store root. Each one is the
// content-addressed (or otherwise immutable) cache of a single ecosystem, so
// two tasks pointed at the same directory share bytes instead of copying them.
const (
	pkgStorePnpmDir      = "pnpm"       // pnpm's content-addressable store: the dir node_modules is linked/cloned FROM
	pkgStorePnpmCacheDir = "pnpm-cache" // pnpm's registry metadata cache (not package content)
	pkgStoreNpmDir       = "npm"        // npm/npx tarball cache
	pkgStoreYarnDir      = "yarn"
	pkgStoreBunDir       = "bun"
	pkgStoreGoModDir     = "go-mod"
	pkgStorePipDir       = "pip"
	pkgStoreUvDir        = "uv"
)

// PackageStoreRoot returns the store root for a workspaces root. Empty in,
// empty out — callers treat that as "no shared store for this task".
func PackageStoreRoot(workspacesRoot string) string {
	workspacesRoot = strings.TrimSpace(workspacesRoot)
	if workspacesRoot == "" {
		return ""
	}
	return filepath.Join(workspacesRoot, PackageStoreDirName)
}

// PackageStoreEnvKeys lists every environment name PreparePackageStore sets,
// sorted. Tests and diagnostics use it to reason about the set without
// reconstructing paths.
func PackageStoreEnvKeys() []string {
	keys := make([]string, 0, len(packageStoreLayout))
	for key := range packageStoreLayout {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// packageStoreLayout maps each environment variable to its subdirectory.
//
// The npm_config_* names are lowercase on purpose: that is the form npm, pnpm
// and yarn classic read as config overrides. Uppercase NPM_CONFIG_* works too
// on npm but not consistently across the others, so the lowercase spelling is
// the one that holds for every consumer.
//
// Deliberately absent:
//   - GOCACHE — the Go *build* cache holds compiled output, not dependencies,
//     and is already machine-global under the daemon user's home. Relocating it
//     buys no sharing and costs every agent one cold rebuild.
//   - CARGO_HOME — it is not a cache: it also holds credentials and installed
//     binaries, and cargo offers no separate knob for just the registry cache.
var packageStoreLayout = map[string]string{
	"npm_config_store_dir":  pkgStorePnpmDir,
	"npm_config_cache_dir":  pkgStorePnpmCacheDir,
	"npm_config_cache":      pkgStoreNpmDir,
	"YARN_CACHE_FOLDER":     pkgStoreYarnDir,
	"BUN_INSTALL_CACHE_DIR": pkgStoreBunDir,
	"GOMODCACHE":            pkgStoreGoModDir,
	"PIP_CACHE_DIR":         pkgStorePipDir,
	"UV_CACHE_DIR":          pkgStoreUvDir,
}

// PreparePackageStore creates the shared store under workspacesRoot and
// returns the environment that points every supported package manager at it.
//
// Why a store next to the task directories rather than each tool's default in
// $HOME: pnpm only shares bytes with a node_modules on the SAME filesystem —
// it clones (APFS) or hardlinks (Linux) out of the store, and silently falls
// back to a full copy across a volume boundary. MULTICA_WORKSPACES_ROOT can
// point at any volume, so anchoring the store to the workspaces root is what
// makes the sharing hold instead of merely usually holding. It also turns the
// store into a daemon-owned asset the GC can prune and `daemon status` can
// measure, which an implicit $HOME default never was.
//
// Isolation falls out of the design rather than needing enforcement: every
// directory here is an immutable, content-addressed cache. A task mutating or
// deleting its own node_modules never writes back to the store, and removing a
// store entry cannot break a node_modules that already linked it — a clone owns
// its blocks outright and a hardlink keeps the inode alive.
//
// Returns an empty map (no error) when workspacesRoot is empty.
func PreparePackageStore(workspacesRoot string) (map[string]string, error) {
	root := PackageStoreRoot(workspacesRoot)
	if root == "" {
		return map[string]string{}, nil
	}
	env := make(map[string]string, len(packageStoreLayout))
	for key, sub := range packageStoreLayout {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("prepare package store %s: %w", sub, err)
		}
		env[key] = dir
	}
	return env, nil
}

// PnpmStoreDir returns the pnpm content-addressable store inside the shared
// store root. This is the only directory with a real reclamation story
// (`pnpm store prune`), so the GC addresses it by name.
func PnpmStoreDir(workspacesRoot string) string {
	root := PackageStoreRoot(workspacesRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, pkgStorePnpmDir)
}
