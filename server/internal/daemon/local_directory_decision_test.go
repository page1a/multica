package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func decisionTask(t *testing.T, daemonID string, resources []ProjectResourceData, decision coderesolve.Decision) Task {
	t.Helper()
	return Task{
		ID:               "task-1",
		WorkspaceID:      "ws-1",
		ProjectResources: resources,
		CodeDecision:     &decision,
	}
}

func TestDecisionNamesTheDirectoryInsteadOfTheFirstRow(t *testing.T) {
	const daemonID = "d-mine"
	first, second := t.TempDir(), t.TempDir()
	resources := []ProjectResourceData{
		localDirResource(t, "r1", first, daemonID),
		localDirResource(t, "r2", second, daemonID),
	}
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID:    "r2",
		Path:          second,
		LockKey:       second,
		ExecutionMode: "in_place",
	}).WithReadOnly([]coderesolve.Dir{{
		ResourceID: "r1",
		Path:       first,
		Name:       "first",
	}})

	chosen, readOnly, err := localDirectoryPlanForTask(decisionTask(t, daemonID, resources, decision), daemonID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if chosen == nil || chosen.AbsPath != second || chosen.ResourceID != "r2" {
		t.Fatalf("chosen = %+v, want the decision's directory %q", chosen, second)
	}
	if len(readOnly) != 1 || readOnly[0].AbsPath != first {
		t.Fatalf("read-only = %+v, want only %q", readOnly, first)
	}
	if readOnly[0].RealPath == chosen.RealPath && first != second {
		t.Fatalf("read-only directory collapsed onto the writable one")
	}
}

func TestDecisionDoesNotFallThroughToAnotherDirectory(t *testing.T) {
	const daemonID = "d-mine"
	here := t.TempDir()
	resources := []ProjectResourceData{
		localDirResource(t, "r-other", here, daemonID),
	}
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID:    "r-missing",
		Path:          here,
		ExecutionMode: "in_place",
	})

	chosen, _, err := localDirectoryPlanForTask(decisionTask(t, daemonID, resources, decision), daemonID)
	if chosen != nil {
		t.Fatalf("chosen = %+v, want no directory — a missing id must not pick %q", chosen, here)
	}
	var placed *codePlacementError
	if err == nil || !asPlacement(err, &placed) || placed.Code != string(coderesolve.CodePinnedResourceNotFound) {
		t.Fatalf("err = %v, want pinned_resource_not_found", err)
	}
}

func TestDecisionUnresolvableFailsWithItsCode(t *testing.T) {
	decision := coderesolve.NewUnresolvable(coderesolve.CodeDaemonCannotRunMode, "runtime cannot run this mode")
	_, _, err := localDirectoryPlanForTask(Task{ID: "t", CodeDecision: &decision}, "d-mine")
	var placed *codePlacementError
	if err == nil || !asPlacement(err, &placed) || placed.Code != string(coderesolve.CodeDaemonCannotRunMode) {
		t.Fatalf("err = %v, want daemon_cannot_run_mode", err)
	}
}

func TestDecisionRemoteCacheDoesNotPickALocalDirectory(t *testing.T) {
	const daemonID = "d-mine"
	dir := t.TempDir()
	decision := coderesolve.NewRemoteCache(coderesolve.RemoteTarget{URL: "https://github.com/o/r"})
	chosen, _, err := localDirectoryPlanForTask(decisionTask(t, daemonID, []ProjectResourceData{
		localDirResource(t, "r1", dir, daemonID),
	}, decision), daemonID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if chosen != nil {
		t.Fatalf("remote decision chose local directory %+v", chosen)
	}
}

func TestDecisionWorktreeRejectsADirectoryThatIsNotGit(t *testing.T) {
	const daemonID = "d-mine"
	dir := t.TempDir()
	raw := mustLocalRef(t, localDirectoryRef{LocalPath: dir, DaemonID: daemonID, ExecutionMode: "worktree"})
	decision := coderesolve.NewLocalWorktree(coderesolve.LocalTarget{
		ResourceID:    "r1",
		Path:          filepath.Join(dir+".multica-worktrees", "task"),
		RepoPath:      dir,
		ExecutionMode: "worktree",
	})
	chosen, _, err := localDirectoryPlanForTask(Task{
		ID:               "t",
		ProjectResources: []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}},
		CodeDecision:     &decision,
	}, daemonID)
	if chosen != nil {
		t.Fatalf("chose %+v for a non-git directory", chosen)
	}
	var placed *codePlacementError
	if err == nil || !asPlacement(err, &placed) || placed.Code != "not_git_worktree" {
		t.Fatalf("err = %v, want not_git_worktree", err)
	}
}

func TestDecisionWorktreeAcceptsAGitDirectory(t *testing.T) {
	const daemonID = "d-mine"
	dir := initTempGitRepo(t)
	raw := mustLocalRef(t, localDirectoryRef{LocalPath: dir, DaemonID: daemonID, ExecutionMode: "worktree", WorktreeRoot: dir + ".multica-worktrees"})
	decision := coderesolve.NewLocalWorktree(coderesolve.LocalTarget{
		ResourceID:    "r1",
		Path:          filepath.Join(dir+".multica-worktrees", "task-1"),
		RepoPath:      dir,
		WorktreeRoot:  dir + ".multica-worktrees",
		ExecutionMode: "worktree",
	})
	chosen, _, err := localDirectoryPlanForTask(Task{
		ID:               "t",
		ProjectResources: []ProjectResourceData{{ID: "r1", ResourceType: localDirectoryResourceType, ResourceRef: raw}},
		CodeDecision:     &decision,
	}, daemonID)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if chosen == nil || chosen.AbsPath != dir || !chosen.UsesWorktree() {
		t.Fatalf("chosen = %+v, want worktree on %q", chosen, dir)
	}
	if chosen.Ref.WorktreeRoot != dir+".multica-worktrees" {
		t.Fatalf("worktree root = %q", chosen.Ref.WorktreeRoot)
	}
}

func TestDecisionLockIsOnlyTheChosenRealPath(t *testing.T) {
	const daemonID = "d-mine"
	first, second := t.TempDir(), t.TempDir()
	resources := []ProjectResourceData{
		localDirResource(t, "r1", first, daemonID),
		localDirResource(t, "r2", second, daemonID),
	}
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r1", Path: first, ExecutionMode: "in_place",
	})
	d := &Daemon{
		cfg:            Config{DaemonID: daemonID},
		localPathLocks: NewLocalPathLocker(),
		logger:         discardLogger(),
	}
	task := decisionTask(t, daemonID, resources, decision)
	release, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), task, discardLogger(), nil)
	if abort || release == nil {
		t.Fatalf("acquire abort=%v releaseNil=%v", abort, release == nil)
	}
	defer release()

	chosen, _, err := localDirectoryPlanForTask(task, daemonID)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := localDirectoryPlanForTask(decisionTask(t, daemonID, resources, coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r2", Path: second, ExecutionMode: "in_place",
	})), daemonID)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.localPathLocks.Holder(chosen.RealPath); got != task.ID {
		t.Fatalf("holder of chosen path = %q, want %q", got, task.ID)
	}
	if got := d.localPathLocks.Holder(other.RealPath); got != "" {
		t.Fatalf("read-only / other directory is locked by %q", got)
	}

	// The other directory can be taken while the first is held.
	release2, abort2 := d.acquireLocalDirectoryLockIfNeeded(context.Background(), decisionTask(t, daemonID, resources, coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r2", Path: second, ExecutionMode: "in_place",
	})), discardLogger(), nil)
	if abort2 || release2 == nil {
		t.Fatalf("second acquire abort=%v releaseNil=%v", abort2, release2 == nil)
	}
	release2()
}

func TestDecisionFailureIsReportedWithItsCode(t *testing.T) {
	const daemonID = "d-mine"
	var gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if reason, _ := body["failure_reason"].(string); reason != "" {
			gotReason = reason
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	decision := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{
		ResourceID: "r-missing", Path: dir, ExecutionMode: "in_place",
	})
	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         discardLogger(),
		localPathLocks: NewLocalPathLocker(),
		cfg:            Config{DaemonID: daemonID},
	}
	release, abort := d.acquireLocalDirectoryLockIfNeeded(context.Background(), decisionTask(t, daemonID, []ProjectResourceData{
		localDirResource(t, "r-other", dir, daemonID),
	}, decision), discardLogger(), nil)
	if !abort || release != nil {
		t.Fatalf("abort=%v releaseNil=%v, want the task failed", abort, release == nil)
	}
	if gotReason != string(coderesolve.CodePinnedResourceNotFound) {
		t.Fatalf("failure_reason = %q, want pinned_resource_not_found", gotReason)
	}
}

func TestInPlaceNonGitDirectoryKeepsMulticaOutAndCreatesNoWorkCopy(t *testing.T) {
	workspaces := t.TempDir()
	userDir := t.TempDir() // not a git work tree
	if err := os.WriteFile(filepath.Join(userDir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	env, err := execenv.Prepare(execenv.PrepareParams{
		WorkspacesRoot:  workspaces,
		WorkspaceID:     "ws-inplace",
		TaskID:          "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		AgentName:       "Test Agent",
		Provider:        "claude",
		LocalWorkDir:    userDir,
		IsolateSidecars: true,
		Task: execenv.TaskContextForEnv{
			IssueID: "issue-1",
			AgentID: "agent-1",
			ProjectResources: []execenv.ProjectResourceForEnv{{
				ID:           "r1",
				ResourceType: "local_directory",
				ResourceRef:  json.RawMessage(`{"local_path":"/tmp/x","daemon_id":"d1"}`),
			}},
		},
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup(true)

	if _, err := os.Stat(filepath.Join(userDir, ".multica")); !os.IsNotExist(err) {
		t.Fatalf(".multica landed in the non-git user directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.RootDir, "workdir")); !os.IsNotExist(err) {
		t.Fatalf("in-place run created a work copy at %s: %v", filepath.Join(env.RootDir, "workdir"), err)
	}
	if _, err := os.Stat(filepath.Join(env.SidecarRoot, ".multica")); err != nil {
		t.Fatalf(".multica missing from the sidecar root: %v", err)
	}
}

func TestIsolateSidecarsForANonGitDirectoryEvenWhenTheRuntimeCan(t *testing.T) {
	dir := t.TempDir()
	a := &localDirectoryAssignment{AbsPath: dir, Ref: localDirectoryRef{ExecutionMode: localDirectoryModeInPlace}}
	isolate, err := isolateUserDirectorySidecars(a, "claude", false)
	if err != nil || !isolate {
		t.Fatalf("non-git in-place isolate=%v err=%v, want isolated", isolate, err)
	}
	isolate, err = isolateUserDirectorySidecars(a, "claude", true)
	if err != nil || !isolate {
		t.Fatalf("git in-place with a supported runtime isolate=%v err=%v, want isolated", isolate, err)
	}
	// A runtime that can only read the cwd keeps today's write-and-clean
	// behavior on a git checkout, and is refused on a folder with no git.
	isolate, err = isolateUserDirectorySidecars(a, "hermes", true)
	if err != nil || isolate {
		t.Fatalf("git in-place hermes isolate=%v err=%v, want the existing in-directory path", isolate, err)
	}
	if _, err := isolateUserDirectorySidecars(a, "hermes", false); err == nil {
		t.Fatal("non-git hermes was allowed to write .multica into the user directory")
	}
}

func asPlacement(err error, dst **codePlacementError) bool {
	return errors.As(err, dst)
}

func mustLocalRef(t *testing.T, ref localDirectoryRef) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func initTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}
