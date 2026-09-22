// Package localdir probes a directory on the machine running the process.
//
// The server cannot see a user's disk, so identity fields (real_path, repo_key,
// is_git_repo) have to be measured where the directory lives — the CLI, the
// daemon, and the desktop app. This package is the Go side of that measurement.
package localdir

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/repoident"
)

const gitProbeTimeout = 5 * time.Second

// ProbeResult is what this machine can say about a directory. Zero values mean
// "could not tell" — the caller omits those fields rather than storing a guess.
type ProbeResult struct {
	// Exists is true when the path is an existing directory on this machine.
	// A path that belongs to another daemon will fail this, and the caller
	// must not invent identity fields for it.
	Exists bool
	// RealPath is the symlink-resolved absolute path, the directory's identity.
	RealPath string
	// RepoKey is the normalized identity of the repository the directory holds,
	// from its origin remote. Empty when there is no git remote to identify.
	RepoKey string
	// IsGitRepo is true only when the directory is a git working tree WITH at
	// least one commit — the bar parallel mode needs. An empty repository is
	// not a git repo for this purpose: every worktree task would fail.
	IsGitRepo bool
	// GitRoot is the repository root containing the directory, when there is one.
	GitRoot string
}

// Probe measures identity fields for path. A missing or unreadable path
// returns Exists=false and no identity — the caller then sends only what the
// user typed.
func Probe(path string) ProbeResult {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ProbeResult{}
	}
	st, err := os.Stat(trimmed)
	if err != nil || !st.IsDir() {
		return ProbeResult{}
	}
	out := ProbeResult{Exists: true}
	if abs, err := filepath.Abs(trimmed); err == nil {
		trimmed = abs
	}
	if real, err := filepath.EvalSymlinks(trimmed); err == nil {
		out.RealPath = real
	} else {
		out.RealPath = filepath.Clean(trimmed)
	}
	gitRoot := gitTopLevel(out.RealPath)
	if gitRoot == "" {
		return out
	}
	out.GitRoot = gitRoot
	if gitHasCommit(gitRoot) {
		out.IsGitRepo = true
	}
	if key := originRepoKey(gitRoot); key != "" {
		out.RepoKey = key
	}
	return out
}

// WritableLocation reports whether path can be created-in: the path itself is
// writable, or (if it does not exist yet) the nearest existing ancestor is.
// Used for worktree_root, which the first task often creates.
func WritableLocation(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	current := filepath.Clean(trimmed)
	for {
		if info, err := os.Stat(current); err == nil {
			if !info.IsDir() {
				return false
			}
			tmp, err := os.CreateTemp(current, ".multica-write-*")
			if err != nil {
				return false
			}
			name := tmp.Name()
			_ = tmp.Close()
			_ = os.Remove(name)
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

func gitTopLevel(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitHasCommit(gitRoot string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "rev-parse", "--verify", "HEAD")
	return cmd.Run() == nil
}

func originRepoKey(gitRoot string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(repoident.NormalizeURL(strings.TrimSpace(string(out))))
}
