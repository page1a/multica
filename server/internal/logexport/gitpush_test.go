package logexport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests drive a real `git` against throwaway local repositories, the
// same way the repocache suite does. The pusher's whole job is the git
// invocation, so a fake around `exec` would test the fake.

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// initBareOrigin creates a bare repository that already has a `main` branch, so
// a clone checks out a real ref rather than an unborn one.
func initBareOrigin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	runGitTest(t, "", "init", "--bare", "--initial-branch=main", origin)

	seed := filepath.Join(root, "seed")
	runGitTest(t, "", "init", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	runGitTest(t, seed, "add", "-A")
	runGitTest(t, seed, "commit", "-m", "initial")
	runGitTest(t, seed, "remote", "add", "origin", origin)
	runGitTest(t, seed, "push", "origin", "main")
	return origin
}

func TestGitCLIPusherPushesBundleUnderLogs(t *testing.T) {
	requireGit(t)
	origin := initBareOrigin(t)

	pusher := &GitCLIPusher{}
	repo := GitRepo{Enabled: true, URL: "file://" + origin, Branch: "main"}
	content := []byte("{\n  \"format\": \"multica.log-export\"\n}\n")
	res, err := pusher.Push(context.Background(), repo, "", PushRequest{
		Filename: "log-export-DENE-599-abc123-run-20260921T170000Z.json",
		Content:  content,
		Message:  "chore(log-export): DENE-599 run",
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if want := "logs/log-export-DENE-599-abc123-run-20260921T170000Z.json"; res.Path != want {
		t.Fatalf("path = %q, want %q", res.Path, want)
	}
	if res.Branch != "main" {
		t.Fatalf("branch = %q, want main", res.Branch)
	}

	got := runGitTest(t, "", "--git-dir", origin, "show", "main:"+res.Path)
	if got != strings.TrimSpace(string(content)) {
		t.Fatalf("committed content = %q, want the bundle", got)
	}
	subject := runGitTest(t, "", "--git-dir", origin, "log", "-1", "--format=%s", "main")
	if subject != "chore(log-export): DENE-599 run" {
		t.Fatalf("commit subject = %q", subject)
	}
	author := runGitTest(t, "", "--git-dir", origin, "log", "-1", "--format=%an <%ae>", "main")
	if author != "multica-log-export <log-export@multica.local>" {
		t.Fatalf("commit author = %q, want the bot identity", author)
	}
}

// TestGitCLIPusherSecondPushOfIdenticalBytesIsInert is the retry case: the same
// export pushed twice must not add a second commit, and must still answer with
// the same path and link so the caller can render a comment either way.
func TestGitCLIPusherSecondPushOfIdenticalBytesIsInert(t *testing.T) {
	requireGit(t)
	origin := initBareOrigin(t)

	pusher := &GitCLIPusher{}
	repo := GitRepo{Enabled: true, URL: "file://" + origin, Dir: "logs"}
	req := PushRequest{Filename: "log-export-DENE-599-abc123-run.json", Content: []byte("{\"a\":1}\n"), Message: "export"}

	first, err := pusher.Push(context.Background(), repo, "", req)
	if err != nil {
		t.Fatalf("first Push: %v", err)
	}
	// No branch was named, so the pusher must report the branch the clone
	// checked out.
	if first.Branch != "main" {
		t.Fatalf("first branch = %q, want main", first.Branch)
	}
	before := runGitTest(t, "", "--git-dir", origin, "rev-list", "--count", "main")

	second, err := pusher.Push(context.Background(), repo, "", req)
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	after := runGitTest(t, "", "--git-dir", origin, "rev-list", "--count", "main")
	if before != after {
		t.Fatalf("identical bytes added a commit: %s -> %s", before, after)
	}
	if first.Path != second.Path || first.URL != second.URL || first.Branch != second.Branch {
		t.Fatalf("second push disagreed with the first: %+v vs %+v", first, second)
	}
}

// TestGitCLIPusherNeverLeaksTheToken is the security property: the token is
// pasted into the git URL to authenticate, and NOTHING that leaves Push may
// carry it — not the result, not the error.
func TestGitCLIPusherNeverLeaksTheToken(t *testing.T) {
	requireGit(t)
	const token = "ghp_SUPERSECRETTOKENVALUE123"
	pusher := &GitCLIPusher{Timeout: 10 * time.Second, InvocationTimeout: 8 * time.Second}
	// A closed local port fails immediately and deterministically, without
	// needing the network, while still making git print the URL it tried.
	repo := GitRepo{Enabled: true, URL: "https://127.0.0.1:1/unreachable-repo.git", Branch: "main"}

	res, err := pusher.Push(context.Background(), repo, token, PushRequest{
		Filename: "log-export-x.json",
		Content:  []byte("{}"),
		Message:  "export",
	})
	if err == nil {
		t.Fatal("an unreachable remote did not fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked the token: %s", err)
	}
	if strings.Contains(res.URL, token) || strings.Contains(res.Path, token) {
		t.Fatalf("result leaked the token: %+v", res)
	}
}

// TestGitRedactorStripsCredentials pins the sanitizer directly, so the rule
// survives a git version whose error output quotes the URL differently.
func TestGitRedactorStripsCredentials(t *testing.T) {
	const token = "ghp_secret_value"
	plain := "https://github.com/org/repo.git"
	auth := authenticatedURL(plain, token)
	if !strings.Contains(auth, token) {
		t.Fatalf("authenticatedURL did not inject the token: %q", auth)
	}
	r := newGitRedactor(token, plain, auth)
	out := r.redact("fatal: unable to access '" + auth + "': could not resolve host")
	if strings.Contains(out, token) {
		t.Fatalf("redact left the token: %q", out)
	}
	if !strings.Contains(out, "github.com/org/repo.git") {
		t.Fatalf("redact lost the host, which is what makes the error useful: %q", out)
	}
	// A credential form the pusher never built must still be removed.
	other := r.redact("fatal: could not read from 'https://someone:hunter2@example.com/x.git'")
	if strings.Contains(other, "hunter2") {
		t.Fatalf("redact left third-party userinfo: %q", other)
	}
}

// TestGitCLIPusherRefusesEscapingDir is the regression for the configured
// directory escape: with Dir "../<name>" the joined path wrote the bundle
// beside the temporary clone — outside it, where the RemoveAll that only
// covers the clone cannot reach. The pusher must refuse before it touches the
// filesystem or the remote.
func TestGitCLIPusherRefusesEscapingDir(t *testing.T) {
	requireGit(t)
	origin := initBareOrigin(t)
	escapedName := "dene599-escape-" + strings.ReplaceAll(t.Name(), "/", "-")
	escapedPath := filepath.Join(os.TempDir(), escapedName, "log-export-x.json")

	pusher := &GitCLIPusher{}
	repo := GitRepo{Enabled: true, URL: "file://" + origin, Branch: "main", Dir: "../" + escapedName}
	_, err := pusher.Push(context.Background(), repo, "", PushRequest{
		Filename: "log-export-x.json",
		Content:  []byte("{}"),
		Message:  "export",
	})
	if err == nil {
		t.Fatal("Push accepted a directory that escapes the clone")
	}
	if !strings.Contains(err.Error(), "invalid log export repository directory") {
		t.Fatalf("error = %v, want it to name the directory", err)
	}
	if _, statErr := os.Stat(escapedPath); statErr == nil {
		t.Fatalf("bundle escaped the clone to %s", escapedPath)
	}
	if count := runGitTest(t, "", "--git-dir", origin, "rev-list", "--count", "main"); count != "1" {
		t.Fatalf("origin history changed to %s commits", count)
	}
}

// TestGitCLIPusherRefusesSymlinkedDirectoryEscape is the second half of the
// directory-escape regression: the configured Dir is inside the clone on
// paper, but the repository tracks `logs` as a symbolic link pointing out of
// it. insideDir cannot see that, WriteFile follows the link, the write lands
// before `git add` fails, and the temporary directory the pusher removes is
// not where the file went. The push must be refused before anything is
// written, and the remote must not move.
func TestGitCLIPusherRefusesSymlinkedDirectoryEscape(t *testing.T) {
	requireGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links needs privileges on Windows")
	}

	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("create the outside directory: %v", err)
	}

	origin := filepath.Join(root, "origin.git")
	runGitTest(t, "", "init", "--bare", "--initial-branch=main", origin)

	seed := filepath.Join(root, "seed")
	runGitTest(t, "", "init", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	// Git records a link verbatim, so the clone really does contain `logs`
	// pointing at a directory no clone owns.
	if err := os.Symlink(outside, filepath.Join(seed, "logs")); err != nil {
		t.Fatalf("create the seed symlink: %v", err)
	}
	runGitTest(t, seed, "add", "-A")
	runGitTest(t, seed, "commit", "-m", "initial")
	runGitTest(t, seed, "remote", "add", "origin", origin)
	runGitTest(t, seed, "push", "origin", "main")

	pusher := &GitCLIPusher{}
	repo := GitRepo{Enabled: true, URL: "file://" + origin, Branch: "main", Dir: "logs"}
	_, err := pusher.Push(context.Background(), repo, "", PushRequest{
		Filename: "log-export-x.json",
		Content:  []byte("{}"),
		Message:  "export",
	})
	if err == nil {
		t.Fatal("Push wrote through a symlinked directory")
	}
	// The escape, not the error string, is the property under test: assert the
	// filesystem first so a regression reports what actually went wrong.
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatalf("read the outside directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("bundle escaped the clone into %s: %v", outside, entries)
	}
	if !strings.Contains(err.Error(), "resolves outside the cloned repository") {
		t.Fatalf("error = %v, want it to name the escape", err)
	}
	if count := runGitTest(t, "", "--git-dir", origin, "rev-list", "--count", "main"); count != "1" {
		t.Fatalf("origin history changed to %s commits", count)
	}
}

func TestAuthenticatedURLOnlyTouchesHTTP(t *testing.T) {
	if got := authenticatedURL("git@github.com:org/repo.git", "tok"); got != "git@github.com:org/repo.git" {
		t.Fatalf("ssh URL was rewritten: %q", got)
	}
	if got := authenticatedURL("file:///srv/repo.git", "tok"); got != "file:///srv/repo.git" {
		t.Fatalf("file URL was rewritten: %q", got)
	}
	if got := authenticatedURL("https://github.com/o/r.git", ""); got != "https://github.com/o/r.git" {
		t.Fatalf("empty token changed the URL: %q", got)
	}
}

func TestFileWebURLFormats(t *testing.T) {
	cases := []struct {
		repo   string
		branch string
		path   string
		want   string
	}{
		{"https://github.com/org/repo.git", "kun", "logs/x.json", "https://github.com/org/repo/blob/kun/logs/x.json"},
		{"https://gitlab.com/org/repo.git", "kun", "logs/x.json", "https://gitlab.com/org/repo/-/blob/kun/logs/x.json"},
		{"https://git.example.com/org/repo.git", "kun", "logs/x.json", "https://git.example.com/org/repo/kun/logs/x.json"},
		{"https://github.com/org/repo", "main", "logs/a b.json", "https://github.com/org/repo/blob/main/logs/a%20b.json"},
	}
	for _, tc := range cases {
		if got := fileWebURL(tc.repo, tc.branch, tc.path); got != tc.want {
			t.Fatalf("fileWebURL(%q,%q,%q) = %q, want %q", tc.repo, tc.branch, tc.path, got, tc.want)
		}
	}
}

func TestWebURLDropsCredentialsAndSuffix(t *testing.T) {
	if got := WebURL("https://x-access-token:tok@github.com/org/repo.git"); got != "https://github.com/org/repo" {
		t.Fatalf("WebURL = %q", got)
	}
	if got := WebURL("https://github.com/org/repo/"); got != "https://github.com/org/repo" {
		t.Fatalf("WebURL = %q", got)
	}
}
