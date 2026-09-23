package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestTaskSharedScratch_OnlyWhenTheDecisionSaysSo(t *testing.T) {
	root := t.TempDir()
	d := &Daemon{cfg: Config{WorkspacesRoot: root}}

	dir, _, ok, err := d.taskSharedScratch(Task{})
	if ok || err != nil || dir != "" {
		t.Fatalf("missing decision: dir=%q ok=%v err=%v", dir, ok, err)
	}

	inPlace := coderesolve.NewLocalInPlace(coderesolve.LocalTarget{ResourceID: "res", Path: "/Users/me/repo"})
	dir, _, ok, err = d.taskSharedScratch(Task{CodeDecision: &inPlace, WorkspaceID: "ws-1"})
	if ok || err != nil || dir != "" {
		t.Fatalf("in-place decision: dir=%q ok=%v err=%v", dir, ok, err)
	}

	scratch := coderesolve.NewSharedScratch(coderesolve.ScratchTarget{
		SessionID: "chat-7",
		Path:      "sessions/chat-7",
	})
	dir, rel, ok, err := d.taskSharedScratch(Task{CodeDecision: &scratch, WorkspaceID: "ws-1"})
	if err != nil || !ok {
		t.Fatalf("scratch: ok=%v err=%v", ok, err)
	}
	if rel != "sessions/chat-7" {
		t.Fatalf("relative = %q", rel)
	}
	want := filepath.Join(root, ".sessions", "ws-1", "sessions", "chat-7")
	if dir != want {
		t.Fatalf("dir = %s, want %s", dir, want)
	}

	escaped := coderesolve.NewSharedScratch(coderesolve.ScratchTarget{Path: "sessions/../../etc"})
	_, _, ok, err = d.taskSharedScratch(Task{CodeDecision: &escaped, WorkspaceID: "ws-1"})
	if !ok || err == nil {
		t.Fatal("a path that leaves the folder was accepted")
	}
}

func TestSharedScratchPrune_DoesNotTouchAUserDirectory(t *testing.T) {
	root := t.TempDir()
	state := newSharedScratchState("")
	state.nowFunc = func() time.Time { return time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC) }

	user := filepath.Join(root, "my-repo")
	if _, err := execenv.OpenSharedSession(root, "ws-1", "sessions/chat-old", state.now().Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	workCopy := filepath.Join(root, "my-repo.multica-worktrees", "task-9")
	for _, dir := range []string{user, workCopy} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, _ := state.Prune(root)
	if removed != 1 {
		t.Fatalf("removed %d, want the one idle session", removed)
	}
	for _, dir := range []string{user, workCopy} {
		if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	if err := state.Remove(root, user); err == nil {
		t.Fatal("Remove accepted a user directory")
	}
	if err := state.Remove(root, workCopy); err == nil {
		t.Fatal("Remove accepted a working copy")
	}
}
