package daemon

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestSameSeatRetryWorkDirGate(t *testing.T) {
	t.Parallel()
	task := Task{
		ContinueInterruptedSession: true,
		PriorSessionID:             "session-1",
		PriorWorkDir:               "/tmp/dene-717-old",
	}
	if got := sameSeatRetryWorkDir(task, true, false); got != task.PriorWorkDir {
		t.Fatalf("same-seat retry workdir = %q, want %q", got, task.PriorWorkDir)
	}
	if got := sameSeatRetryWorkDir(task, false, false); got != "" {
		t.Fatalf("unavailable directory was reused: %q", got)
	}
	if got := sameSeatRetryWorkDir(task, true, true); got != "" {
		t.Fatalf("in-use directory was reused: %q", got)
	}

	// A different seat does not continue the interrupted session, so it does
	// not inherit the previous working copy either.
	otherSeat := task
	otherSeat.ContinueInterruptedSession = false
	if got := sameSeatRetryWorkDir(otherSeat, true, false); got != "" {
		t.Fatalf("seat change reused %q", got)
	}
	unavailable := task
	unavailable.PriorSessionResumeUnavailable = true
	if got := sameSeatRetryWorkDir(unavailable, true, false); got != "" {
		t.Fatalf("unresumable session reused %q", got)
	}
	noSession := task
	noSession.PriorSessionID = ""
	if got := sameSeatRetryWorkDir(noSession, true, false); got != "" {
		t.Fatalf("retry without a session reused %q", got)
	}
}

func TestWorktreeCleanupIsActiveMatchesCanonicalPath(t *testing.T) {
	t.Parallel()
	state := newWorktreeCleanupState(t.TempDir())
	dir := t.TempDir()
	state.MarkActive(dir)
	if !state.IsActive(dir) {
		t.Fatal("active copy was reported idle")
	}
	if state.IsActive(filepath.Join(dir, "elsewhere")) {
		t.Fatal("a different path was reported active")
	}
	var nilState *worktreeCleanupState
	if nilState.IsActive(dir) {
		t.Fatal("nil cleanup state reported a copy active")
	}
}

// The path a same-seat retry rebuilds is the path the resume gate compares.
// When they match, the interrupted session stays and the prompt is the
// continue instruction rather than a fresh reading of the issue.
func TestSameSeatRetryReusedWorktreeKeepsContinueSession(t *testing.T) {
	t.Parallel()
	repo := retryReuseTestRepo(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	first, err := execenv.PrepareLocalWorktree(execenv.LocalWorktreeParams{
		LocalPath: repo,
		EnvRoot:   t.TempDir(),
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, logger)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	prior := first.WorkDir
	first.Discard(logger)

	second, err := execenv.PrepareLocalWorktree(execenv.LocalWorktreeParams{
		LocalPath:     repo,
		EnvRoot:       t.TempDir(),
		AgentName:     "J",
		TaskID:        "99998888-7777-6666-5555-444433332222",
		ResumeWorkDir: prior,
	}, logger)
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	t.Cleanup(func() { second.Discard(logger) })
	if !execenv.SameCanonicalPath(second.WorkDir, prior) {
		t.Fatalf("retry workdir = %q, want %q", second.WorkDir, prior)
	}

	task := Task{
		ContinueInterruptedSession: true,
		PriorSessionID:             "session-1",
		PriorWorkDir:               prior,
		IssueID:                    "issue-1",
	}
	taskCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}
	if !gateResumeToReachableSession(&task, &taskCtx, "claude", second.WorkDir, true, false, logger) {
		t.Fatal("reused worktree dropped the interrupted session")
	}
	if !shouldContinueInterruptedSession(task) {
		t.Fatal("continue flag was cleared after the worktree was reused")
	}
	prompt := BuildPrompt(task, "claude")
	if !strings.Contains(prompt, "Continue from where you left off") {
		t.Fatalf("prompt did not continue the interrupted session:\n%s", prompt)
	}

	// The failure mode this fixes: a new directory drops the session and the
	// continue instruction with it.
	fresh := t.TempDir()
	dropped := Task{
		ContinueInterruptedSession: true,
		PriorSessionID:             "session-1",
		PriorWorkDir:               prior,
		IssueID:                    "issue-1",
	}
	droppedCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}
	if gateResumeToReachableSession(&dropped, &droppedCtx, "claude", fresh, true, false, logger) {
		t.Fatal("a different workdir kept the session")
	}
	if shouldContinueInterruptedSession(dropped) {
		t.Fatal("continue flag survived a workdir mismatch")
	}
	if strings.Contains(BuildPrompt(dropped, "claude"), "Continue from where you left off") {
		t.Fatal("mismatched workdir still produced the continue prompt")
	}
}

func retryReuseTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "Test User"},
		{"config", "user.email", "test@test.com"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	return dir
}
