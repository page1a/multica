package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// A same-seat retry rebuilds the working copy at the previous run's path.
// The env root (and therefore the default copy name) belongs to the new task;
// the CLI's conversation does not.
func TestPrepareLocalWorktreeSameSeatRetryRecreatesTheSamePath(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	firstEnv := t.TempDir()
	first, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath: repo,
		EnvRoot:   firstEnv,
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	priorPath, priorWork := first.Path, first.WorkDir
	first.Discard(worktreeTestLogger())
	if _, statErr := os.Stat(priorPath); !os.IsNotExist(statErr) {
		t.Fatalf("discard left the copy on disk: %v", statErr)
	}

	secondEnv := t.TempDir()
	second, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:     repo,
		EnvRoot:       secondEnv,
		AgentName:     "J",
		TaskID:        "99998888-7777-6666-5555-444433332222",
		ResumeWorkDir: priorWork,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	t.Cleanup(func() { second.Discard(worktreeTestLogger()) })

	if !SameCanonicalPath(second.Path, priorPath) {
		t.Fatalf("retry copy = %q, want the previous copy %q", second.Path, priorPath)
	}
	if !SameCanonicalPath(second.WorkDir, priorWork) {
		t.Fatalf("retry workdir = %q, want %q", second.WorkDir, priorWork)
	}
	if filepath.Base(second.Path) == filepath.Base(secondEnv) {
		t.Fatalf("retry used the new env-root name %q", filepath.Base(secondEnv))
	}
}

// A resource pointed at a subdirectory still has to land the agent at that
// depth inside the same copy. The pin is the copy, not the cwd spelling.
func TestPrepareLocalWorktreeSameSeatRetryKeepsSubdirectory(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	writeFile(t, filepath.Join(repo, "services/api/main.go"), "package main\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "add service")
	sub := filepath.Join(repo, "services", "api")

	first, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath: sub,
		EnvRoot:   t.TempDir(),
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	priorPath, priorWork := first.Path, first.WorkDir
	first.Discard(worktreeTestLogger())

	second, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:     sub,
		EnvRoot:       t.TempDir(),
		AgentName:     "J",
		TaskID:        "99998888-7777-6666-5555-444433332222",
		ResumeWorkDir: priorWork,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	t.Cleanup(func() { second.Discard(worktreeTestLogger()) })
	if !SameCanonicalPath(second.Path, priorPath) || !SameCanonicalPath(second.WorkDir, priorWork) {
		t.Fatalf("retry path %q workdir %q, want copy %q cwd %q", second.Path, second.WorkDir, priorPath, priorWork)
	}
}

// A copy that is still on disk is not reusable. Wiping it would delete the
// work a failed finalize kept, or a sibling run's tree.
func TestPrepareLocalWorktreeSameSeatRetryLeavesAnOccupiedCopy(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	first, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath: repo,
		EnvRoot:   t.TempDir(),
		AgentName: "J",
		TaskID:    "11112222-3333-4444-5555-666677778888",
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	t.Cleanup(func() { first.Discard(worktreeTestLogger()) })
	marker := filepath.Join(first.Path, "do-not-touch")
	writeFile(t, marker, "stay")

	second, err := PrepareLocalWorktree(LocalWorktreeParams{
		LocalPath:     repo,
		EnvRoot:       t.TempDir(),
		AgentName:     "J",
		TaskID:        "99998888-7777-6666-5555-444433332222",
		ResumeWorkDir: first.WorkDir,
	}, worktreeTestLogger())
	if err != nil {
		t.Fatalf("retry prepare: %v", err)
	}
	t.Cleanup(func() { second.Discard(worktreeTestLogger()) })
	if SameCanonicalPath(second.Path, first.Path) {
		t.Fatal("retry reused a copy that is still on disk")
	}
	if got := readFile(t, marker); got != "stay" {
		t.Fatalf("occupied copy was modified, marker = %q", got)
	}
}

// A previous cwd outside this repository's worktree root, or inside the
// checkout itself, is not a copy we may rebuild. The foreign directory stays
// untouched and the task gets a fresh copy beside the repo.
func TestPrepareLocalWorktreeSameSeatRetryRefusesForeignDirectories(t *testing.T) {
	t.Parallel()
	repo := newTestRepo(t)
	foreign := t.TempDir()
	writeFile(t, filepath.Join(foreign, "marker"), "keep")
	inside := filepath.Join(repo, "dene-717-nope")

	for i, prior := range []string{foreign, inside} {
		wt, err := PrepareLocalWorktree(LocalWorktreeParams{
			LocalPath:     repo,
			EnvRoot:       t.TempDir(),
			AgentName:     "J",
			TaskID:        "11112222-3333-4444-5555-66667777888" + string(rune('0'+i)),
			ResumeWorkDir: prior,
		}, worktreeTestLogger())
		if err != nil {
			t.Fatalf("prepare with prior %q: %v", prior, err)
		}
		if SameCanonicalPath(wt.Path, prior) || SameCanonicalPath(wt.WorkDir, prior) {
			wt.Discard(worktreeTestLogger())
			t.Fatalf("prepare used foreign directory %q as the worktree", prior)
		}
		if rel, relErr := filepath.Rel(repo, wt.Path); relErr == nil && rel != ".." && !hasDotDotPrefix(rel) {
			wt.Discard(worktreeTestLogger())
			t.Fatalf("worktree %q landed inside the repository", wt.Path)
		}
		wt.Discard(worktreeTestLogger())
	}
	if got := readFile(t, filepath.Join(foreign, "marker")); got != "keep" {
		t.Fatalf("foreign directory was modified, marker = %q", got)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("a refused path inside the repository was created: %v", err)
	}
}

func hasDotDotPrefix(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}

func TestReusableWorktreeDirRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	root := t.TempDir()

	if _, ok := ReusableWorktreeDir(root, repo, repo, filepath.Join(repo, "dene-717-abc")); ok {
		t.Fatal("accepted a path inside the repository")
	}
	outside := filepath.Join(t.TempDir(), "dene-717-abc")
	if _, ok := ReusableWorktreeDir(root, repo, repo, outside); ok {
		t.Fatal("accepted a path outside the worktree root")
	}
	occupied := filepath.Join(root, "dene-717-busy")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReusableWorktreeDir(root, repo, repo, occupied); ok {
		t.Fatal("accepted a directory that is still on disk")
	}
	if _, ok := ReusableWorktreeDir(root, repo, repo, filepath.Join(root, ".multica")); ok {
		t.Fatal("accepted the record directory")
	}
	if _, ok := ReusableWorktreeDir(root, repo, repo, "relative/dene-717-abc"); ok {
		t.Fatal("accepted a relative path")
	}
	if _, ok := ReusableWorktreeDir(root, repo, repo, ""); ok {
		t.Fatal("accepted an empty path")
	}

	missing := filepath.Join(root, "dene-717-gone")
	got, ok := ReusableWorktreeDir(root, repo, repo, missing)
	if !ok || !SameCanonicalPath(got, missing) {
		t.Fatalf("missing copy = %q ok=%v, want %q", got, ok, missing)
	}

	sub := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	prior := filepath.Join(root, "dene-717-sub", "services", "api")
	got, ok = ReusableWorktreeDir(root, repo, sub, prior)
	want := filepath.Join(root, "dene-717-sub")
	if !ok || !SameCanonicalPath(got, want) {
		t.Fatalf("subdirectory copy = %q ok=%v, want %q", got, ok, want)
	}
}
