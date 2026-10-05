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

	// An ordinary next turn of the same conversation keeps the copy too
	// (DENE-1356): the folder follows the session, not the retry flag.
	followUp := task
	followUp.ContinueInterruptedSession = false
	if got := sameSeatRetryWorkDir(followUp, true, false); got != task.PriorWorkDir {
		t.Fatalf("ordinary follow-up workdir = %q, want %q", got, task.PriorWorkDir)
	}
	// A different seat gets no prior pointers from the server — they are
	// scoped to (agent, issue) or the chat — so it has nothing to inherit.
	otherSeat := task
	otherSeat.ContinueInterruptedSession = false
	otherSeat.PriorWorkDir = ""
	otherSeat.PriorSessionID = ""
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
	if !gateResumeToReachableSession(&task, &taskCtx, "claude", second.WorkDir, true, false, false, logger) {
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
	if gateResumeToReachableSession(&dropped, &droppedCtx, "claude", fresh, true, false, false, logger) {
		t.Fatal("a different workdir kept the session")
	}
	if shouldContinueInterruptedSession(dropped) {
		t.Fatal("continue flag survived a workdir mismatch")
	}
	if strings.Contains(BuildPrompt(dropped, "claude"), "Continue from where you left off") {
		t.Fatal("mismatched workdir still produced the continue prompt")
	}
}

// Three ordinary turns of one conversation (DENE-1356): each turn finalizes
// its copy, the next rebuilds it at the same path, and the resume gate keeps
// the session for a cwd-keyed CLI without any cross-directory allowance.
func TestFollowUpTurnsKeepOneWorktreeDir(t *testing.T) {
	t.Parallel()
	repo := retryReuseTestRepo(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	params := func(taskID, resume string) execenv.LocalWorktreeParams {
		envRoot := filepath.Join(t.TempDir(), "dene-1356-"+taskID[:12])
		if err := os.MkdirAll(envRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		return execenv.LocalWorktreeParams{
			LocalPath:       repo,
			EnvRoot:         envRoot,
			AgentName:       "J",
			TaskID:          taskID,
			ConversationKey: "dene-1356",
			WorkspaceID:     "ws-1",
			AgentID:         "agent-1",
			ConversationID:  "issue-1",
			ResumeWorkDir:   resume,
		}
	}

	first, err := execenv.PrepareLocalWorktree(params("aaaaaaaaaaaa-1111-2222-3333-444455556666", ""), logger)
	if err != nil {
		t.Fatalf("turn 1 prepare: %v", err)
	}
	prior := first.WorkDir
	if err := os.WriteFile(filepath.Join(prior, "turn1.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Finalize(logger); err != nil {
		t.Fatalf("turn 1 finalize: %v", err)
	}

	for i, taskID := range []string{"bbbbbbbbbbbb-1111-2222-3333-444455556666", "cccccccccccc-1111-2222-3333-444455556666"} {
		task := Task{
			PriorSessionID: "session-1",
			PriorWorkDir:   prior,
			IssueID:        "issue-1",
		}
		resume := sameSeatRetryWorkDir(task, true, false)
		if resume != prior {
			t.Fatalf("turn %d offered %q, want %q", i+2, resume, prior)
		}
		wt, err := execenv.PrepareLocalWorktree(params(taskID, resume), logger)
		if err != nil {
			t.Fatalf("turn %d prepare: %v", i+2, err)
		}
		if !execenv.SameCanonicalPath(wt.WorkDir, prior) {
			t.Fatalf("turn %d workdir = %q, want %q", i+2, wt.WorkDir, prior)
		}
		if !wt.Continued {
			t.Fatalf("turn %d did not continue the conversation branch", i+2)
		}
		taskCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}
		if !gateResumeToReachableSession(&task, &taskCtx, "kimi", wt.WorkDir, true, false, false, logger) {
			t.Fatalf("turn %d dropped the session in the same directory", i+2)
		}
		if _, err := os.Stat(filepath.Join(wt.WorkDir, "turn1.txt")); err != nil {
			t.Fatalf("turn %d lost the first turn's work: %v", i+2, err)
		}
		if err := os.WriteFile(filepath.Join(wt.WorkDir, taskID[:12]+".txt"), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Finalize(logger); err != nil {
			t.Fatalf("turn %d finalize: %v", i+2, err)
		}
	}
}

// Two agents on one issue each get only their own pointers from the server,
// so the second agent's turn never lands in the first agent's copy, even
// while that copy is busy.
func TestBusyPriorWorktreeIsNotReused(t *testing.T) {
	t.Parallel()
	task := Task{PriorSessionID: "session-1", PriorWorkDir: "/tmp/dene-1356-a"}
	if got := sameSeatRetryWorkDir(task, true, true); got != "" {
		t.Fatalf("busy copy was reused: %q", got)
	}
}

// Claude and Codex resume a stored session from another directory; every
// other runtime keeps the same-directory rule (DENE-1356).
func TestResumeGateAcrossDirectories(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prior, moved := t.TempDir(), t.TempDir()

	configDir := t.TempDir()
	projectDir := filepath.Join(configDir, "projects", "-old-cwd")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "stored-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withSession := func(id string) Task {
		return Task{
			PriorSessionID: id,
			PriorWorkDir:   prior,
			Agent:          &AgentData{CustomEnv: map[string]string{"CLAUDE_CONFIG_DIR": configDir}},
		}
	}

	stored := withSession("stored-session")
	if !priorSessionStoredAnywhere("claude", true, stored, moved) {
		t.Fatal("stored Claude transcript was not found")
	}
	if priorSessionStoredAnywhere("claude", false, stored, moved) {
		t.Fatal("a custom Claude-protocol command was trusted across directories")
	}
	if priorSessionStoredAnywhere("claude", true, withSession("missing-session"), moved) {
		t.Fatal("a missing Claude transcript was reported stored")
	}
	if !priorSessionStoredAnywhere("codex", true, stored, moved) {
		t.Fatal("codex should defer to its rollout gate")
	}
	for _, provider := range []string{"kimi", "cursor", "grok", "gemini"} {
		if priorSessionStoredAnywhere(provider, true, stored, moved) {
			t.Fatalf("%s was trusted across directories", provider)
		}
	}

	task := withSession("stored-session")
	taskCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}
	if !gateResumeToReachableSession(&task, &taskCtx, "claude", moved, true,
		false, priorSessionStoredAnywhere("claude", true, task, moved), logger) {
		t.Fatal("Claude dropped a stored session after the directory moved")
	}
	if task.PriorSessionID != "stored-session" || task.PriorSessionResumeUnavailable {
		t.Fatalf("gate mutated a kept session: %+v", task)
	}

	kimi := withSession("stored-session")
	kimiCtx := execenv.TaskContextForEnv{PriorSessionResumed: true}
	if gateResumeToReachableSession(&kimi, &kimiCtx, "kimi", moved, true,
		false, priorSessionStoredAnywhere("kimi", true, kimi, moved), logger) {
		t.Fatal("kimi kept a session in a different directory")
	}
	if kimi.PriorSessionID != "" || !kimi.PriorSessionResumeUnavailable {
		t.Fatalf("dropped session was not disclosed: %+v", kimi)
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
