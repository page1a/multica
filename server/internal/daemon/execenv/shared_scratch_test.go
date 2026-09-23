package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedScratch_OneFolderPerSessionNotPerTask(t *testing.T) {
	root := t.TempDir()
	const workspace = "ws-1"
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	var first string
	for i := 0; i < 20; i++ {
		dir, err := OpenSharedSession(root, workspace, "sessions/chat-7", now)
		if err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
		if i == 0 {
			first = dir
			if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("from the first turn"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else if dir != first {
			t.Fatalf("turn %d opened %s, want %s", i, dir, first)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != SharedScratchRootName {
		t.Fatalf("workspaces root = %v, want only %s", names(entries), SharedScratchRootName)
	}
	body, err := os.ReadFile(filepath.Join(first, "note.txt"))
	if err != nil || string(body) != "from the first turn" {
		t.Fatalf("first turn's file = %q, %v", body, err)
	}

	other, err := OpenSharedSession(root, workspace, "sessions/chat-8", now)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two sessions landed in one directory")
	}
}

func TestSharedScratch_RefusesAPathThatLeavesTheFolder(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"sessions/../../etc",
		"../sessions/chat-7",
		"/tmp/chat-7",
		"sessions/chat/extra",
		"sessions/..",
		"workdir",
		"sessions/chat 7",
	} {
		if _, err := ResolveSharedSession(root, "ws-1", rel); err == nil {
			t.Fatalf("path %q was accepted", rel)
		}
	}
}

func TestPrepare_SharedScratchDoesNotCreateATaskDirectoryOrWipeTheSession(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	dir, err := OpenSharedSession(root, "ws-1", "sessions/chat-7", now)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimSharedSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()

	env, err := Prepare(PrepareParams{
		WorkspacesRoot:    root,
		WorkspaceID:       "ws-1",
		TaskID:            "01900000-0000-7000-8000-000000000001",
		AgentName:         "agent",
		EnvRootPreclaimed: true,
		SharedScratchDir:  dir,
		Provider:          "claude",
		Task:              TaskContextForEnv{ChatSessionID: "chat-7", AgentName: "agent"},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if env.RootDir != dir {
		t.Fatalf("root = %s, want session dir %s", env.RootDir, dir)
	}
	if !env.SharedScratch {
		t.Fatal("environment was not marked as the shared session folder")
	}
	note := filepath.Join(env.WorkDir, "note.txt")
	if err := os.WriteFile(note, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	taskRoot, err := ResolveRootDir(RootDirParams{
		WorkspacesRoot: root,
		WorkspaceID:    "ws-1",
		TaskID:         "01900000-0000-7000-8000-000000000001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(taskRoot); !os.IsNotExist(statErr) {
		t.Fatalf("prepare created a task directory at %s", taskRoot)
	}

	// The claim is already held, and Prepare must not try to take it again.
	// A second turn reuses the same folder; the note has to still be there.
	claim.Release()
	secondClaim, err := ClaimSharedSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer secondClaim.Release()
	second, err := Prepare(PrepareParams{
		WorkspacesRoot:    root,
		WorkspaceID:       "ws-1",
		TaskID:            "01900000-0000-7000-8000-000000000002",
		AgentName:         "agent",
		EnvRootPreclaimed: true,
		SharedScratchDir:  dir,
		Provider:          "claude",
		Task:              TaskContextForEnv{ChatSessionID: "chat-7", AgentName: "agent"},
	}, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(second.WorkDir, "note.txt"))
	if err != nil || string(body) != "keep me" {
		t.Fatalf("second turn saw %q, %v", body, err)
	}
	if err := second.Cleanup(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("cleanup removed the session folder: %v", err)
	}
}

func TestPruneSharedScratch_LeavesUserDirectoriesAndWorkCopiesAlone(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)

	stale, err := OpenSharedSession(root, "ws-1", "sessions/chat-old", old)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenSharedSession(root, "ws-1", "sessions/chat-new", now)
	if err != nil {
		t.Fatal(err)
	}
	busy, err := OpenSharedSession(root, "ws-1", "sessions/chat-busy", old)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimSharedSession(busy)
	if err != nil {
		t.Fatal(err)
	}
	defer claim.Release()

	// A directory sitting in the shared tree without our marker is not ours,
	// even when it is old. This is the stand-in for "something a person put
	// here"; the pruner has no business deciding it is scratch.
	stranger := filepath.Join(SharedScratchRoot(root), "ws-1", "sessions", "hand-made")
	if err := os.MkdirAll(stranger, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stranger, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A user's repository and a working copy beside it. Neither is under
	// .sessions. Passing their paths to the remover is what a confused caller
	// would do; both must come back refused and untouched.
	userRepo := filepath.Join(root, "user-repo")
	workCopy := filepath.Join(root, "user-repo.multica-worktrees", "task-1")
	for _, dir := range []string{userRepo, workCopy} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("mine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A symlink inside the session tree pointing at the user's working copy.
	// Deleting "the session" must not delete the target.
	link := filepath.Join(SharedScratchRoot(root), "ws-1", "sessions", "linked")
	if err := os.Symlink(workCopy, link); err != nil {
		t.Fatal(err)
	}

	removed, _ := PruneSharedScratch(root, now, 14*24*time.Hour, map[string]bool{busy: true})
	if removed != 1 {
		t.Fatalf("removed %d session folders, want 1", removed)
	}
	for _, keep := range []string{
		fresh,
		busy,
		stranger,
		userRepo,
		workCopy,
		filepath.Join(workCopy, "keep.txt"),
		link,
	} {
		if _, err := os.Lstat(keep); err != nil {
			t.Fatalf("pruner removed %s: %v", keep, err)
		}
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("idle session still at %s (stat %v)", stale, err)
	}

	for _, banned := range []string{userRepo, workCopy, link, stranger} {
		err := RemoveSharedSession(root, banned, nil, now)
		if !errors.Is(err, ErrSharedSessionKept) {
			t.Fatalf("RemoveSharedSession(%s) = %v, want kept", banned, err)
		}
		if _, statErr := os.Lstat(filepath.Join(banned, "keep.txt")); banned != link && statErr != nil {
			t.Fatalf("refused path %s lost its file: %v", banned, statErr)
		}
	}
	if _, err := os.Lstat(filepath.Join(workCopy, "keep.txt")); err != nil {
		t.Fatalf("working copy was removed through the symlink: %v", err)
	}
}

func TestPruneSharedScratch_ReopenAfterScanIsKept(t *testing.T) {
	root := t.TempDir()
	scannedAt := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	old := scannedAt.Add(-30 * 24 * time.Hour)

	reopened, err := OpenSharedSession(root, "ws-1", "sessions/chat-back", old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reopened, "before.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	stillIdle, err := OpenSharedSession(root, "ws-1", "sessions/chat-idle", old)
	if err != nil {
		t.Fatal(err)
	}

	// The scan walks every session before it deletes any. A turn can reopen
	// one in that gap, and the in-use snapshot from before the scan does not
	// contain it. Removal must notice the rewritten marker anyway.
	sharedScratchBeforeRemove = func(path string) {
		if path != reopened {
			return
		}
		if _, openErr := OpenSharedSession(root, "ws-1", "sessions/chat-back", scannedAt.Add(time.Minute)); openErr != nil {
			t.Errorf("reopen: %v", openErr)
		}
		if writeErr := os.WriteFile(filepath.Join(reopened, "during.txt"), []byte("new"), 0o644); writeErr != nil {
			t.Errorf("write during reopen: %v", writeErr)
		}
	}
	t.Cleanup(func() { sharedScratchBeforeRemove = nil })

	removed, _ := PruneSharedScratch(root, scannedAt, 14*24*time.Hour, nil)
	if removed != 1 {
		t.Fatalf("removed %d, want only the session that stayed idle", removed)
	}
	if _, err := os.Lstat(stillIdle); !os.IsNotExist(err) {
		t.Fatalf("idle session = %v, want removed", err)
	}
	for _, name := range []string{"before.txt", "during.txt"} {
		body, err := os.ReadFile(filepath.Join(reopened, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(body) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
	claim, err := ClaimSharedSession(reopened)
	if err != nil {
		t.Fatalf("claim after prune kept the folder: %v", err)
	}
	claim.Release()
}

func TestClaimSharedSession_SecondTurnDoesNotReset(t *testing.T) {
	root := t.TempDir()
	dir, err := OpenSharedSession(root, "ws-1", "sessions/chat-7", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("stay"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := ClaimSharedSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimSharedSession(dir); !errors.Is(err, ErrSharedSessionBusy) {
		t.Fatalf("second claim = %v, want busy", err)
	}
	first.Release()
	second, err := ClaimSharedSession(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	body, err := os.ReadFile(filepath.Join(dir, "note.txt"))
	if err != nil || string(body) != "stay" {
		t.Fatalf("folder after reclaim = %q, %v", body, err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Name()
	}
	return out
}
