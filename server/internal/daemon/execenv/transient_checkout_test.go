package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/sparsecheckout"
)

// swapWorktreeRetry replaces the retry wait for one test. Not parallel: the
// hooks are package state.
func swapWorktreeRetry(t *testing.T, sleep func(time.Duration)) {
	t.Helper()
	prevSleep, prevDelays := sleepBeforeWorktreeRetry, worktreeAddRetryDelays
	sleepBeforeWorktreeRetry = sleep
	worktreeAddRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() {
		sleepBeforeWorktreeRetry, worktreeAddRetryDelays = prevSleep, prevDelays
	})
}

// A ref lock somebody else holds is the textbook passing fault: git refuses
// with "cannot lock ref … File exists" and succeeds once the holder lets go.
// The add must wait it out instead of failing the task (DENE-1339).
func TestAddLocalWorktreeRetriesPastAHeldRefLock(t *testing.T) {
	repo := newTestRepo(t)
	branch := "agent/j/lock-test"
	lock := filepath.Join(repo, ".git", "refs", "heads", "agent", "j", "lock-test.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waits := 0
	swapWorktreeRetry(t, func(time.Duration) {
		waits++
		// The other holder finishes while we wait.
		_ = os.Remove(lock)
	})

	head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(t.TempDir(), "wt")
	got, created, err := addLocalWorktree(repo, path, taskBranchPlan{name: branch, base: head}, "task-1", sparsecheckout.Scope{})
	if err != nil {
		t.Fatalf("addLocalWorktree: %v", err)
	}
	if got != branch || !created {
		t.Fatalf("got branch %q created=%v, want %q created", got, created, branch)
	}
	if waits != 1 {
		t.Fatalf("waited %d times, want 1", waits)
	}
	if content := readFile(t, filepath.Join(path, "tracked.txt")); content != "original\n" {
		t.Fatalf("worktree content %q", content)
	}
}

// A fault that does not pass gives up after the retries, leaves no half-built
// worktree or registration behind, and keeps git's own words in the error so
// the server can still recognise it as the passing kind.
func TestAddLocalWorktreeGivesUpCleanlyWhenTheLockStays(t *testing.T) {
	repo := newTestRepo(t)
	branch := "agent/j/stuck"
	lock := filepath.Join(repo, ".git", "refs", "heads", "agent", "j", "stuck.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waits := 0
	swapWorktreeRetry(t, func(time.Duration) { waits++ })

	head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	path := filepath.Join(t.TempDir(), "wt")
	_, _, err := addLocalWorktree(repo, path, taskBranchPlan{name: branch, base: head}, "task-1", sparsecheckout.Scope{})
	if err == nil {
		t.Fatal("addLocalWorktree succeeded with the lock still held")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cannot lock ref") {
		t.Fatalf("error lost git's text: %v", err)
	}
	if waits != 2 {
		t.Fatalf("waited %d times, want 2", waits)
	}
	if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		t.Fatalf("half-built worktree left at %s: %v", path, statErr)
	}
	if list := gitRun(t, repo, "worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Fatalf("registration left behind:\n%s", list)
	}
}

// A deterministic refusal is not retried: an existing branch name goes
// straight to the alternate-name path, as before.
func TestAddLocalWorktreeDoesNotRetryDeterministicFailures(t *testing.T) {
	repo := newTestRepo(t)
	head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	gitRun(t, repo, "branch", "agent/j/taken", head)
	swapWorktreeRetry(t, func(time.Duration) { t.Fatal("retried a deterministic failure") })

	path := filepath.Join(t.TempDir(), "wt")
	got, _, err := addLocalWorktree(repo, path, taskBranchPlan{name: "agent/j/taken", base: head}, "11112222-3333", sparsecheckout.Scope{})
	if err != nil {
		t.Fatalf("addLocalWorktree: %v", err)
	}
	if got == "agent/j/taken" {
		t.Fatalf("took a branch that already existed")
	}
}
