package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/ghpr"
	"github.com/spf13/cobra"
)

const sedimentTestSession = "abcd1234-2222-3333-4444-555555555555"

func knowledgeGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// knowledgeRepo is a project directory on main plus a task worktree on
// branch, which is the cwd when the test returns. The worktree carries the
// daemon's task marker, as a real one does.
func knowledgeRepo(t *testing.T, branch string) (repo, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not available: %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(root, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	knowledgeGit(t, repo, "init", "-q", "-b", "main")
	knowledgeGit(t, repo, "config", "user.name", "Test")
	knowledgeGit(t, repo, "config", "user.email", "t@t")
	knowledgeGit(t, repo, "config", "gc.auto", "0")
	writeKnowledgeFile(t, repo, "README.md", "hi\n")
	knowledgeGit(t, repo, "add", ".")
	knowledgeGit(t, repo, "commit", "-q", "-m", "initial")
	wt = filepath.Join(root, "wt")
	knowledgeGit(t, repo, "worktree", "add", "-q", "-b", branch, wt, "main")
	writeKnowledgeFile(t, wt, execenv.TaskContextMarkerRelPath,
		`{"managed_by":"`+execenv.TaskContextMarkerManagedBy+`","agent_id":"agent-1","issue_id":"issue-1"}`)
	writeKnowledgeFile(t, wt, "CONTEXT.md", "# terms\n")
	writeKnowledgeFile(t, wt, "server/x.go", "package x\n")
	knowledgeGit(t, wt, "add", "CONTEXT.md", "server/x.go")
	knowledgeGit(t, wt, "commit", "-q", "-m", "Chat abcd1234: 沉淀记录词条")
	prev, _ := os.Getwd()
	if err := os.Chdir(wt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return repo, wt
}

func writeKnowledgeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunIssueCloseSendsDeliveredFilesForAKnowledgeChange(t *testing.T) {
	knowledgeRepo(t, "agent/agent/dene-9")
	const issueID = "99999999-9999-4999-8999-999999999999"
	origList := ghListPRs
	t.Cleanup(func() { ghListPRs = origList })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies[r.URL.Path] = body
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "identifier": "DENE-9"})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "加了词条")
	_ = cmd.Flags().Set("no-code", "纯文档")
	_ = cmd.Flags().Set("knowledge", "context=加了沉淀记录词条")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	for _, path := range []string{"/api/issues/" + issueID + "/close/check", "/api/issues/" + issueID + "/close"} {
		files, _ := bodies[path]["delivered_files"].([]any)
		if len(files) != 2 || files[0] != "CONTEXT.md" || files[1] != "server/x.go" {
			t.Fatalf("%s delivered_files = %#v; want what the branch adds against main", path, bodies[path]["delivered_files"])
		}
	}
}

func TestRunIssueCloseKnowledgeNoneSendsNoFileList(t *testing.T) {
	knowledgeRepo(t, "agent/agent/dene-10")
	const issueID = "10101010-9999-4999-8999-999999999999"
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/close") {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "blocked"})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "blocked")
	_ = cmd.Flags().Set("evidence", "等依赖")
	_ = cmd.Flags().Set("blocked-by", "DENE-1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if _, ok := body["delivered_files"]; ok {
		t.Fatalf("body = %#v; a close that ships no knowledge sends no file list", body)
	}
}

func newChatSedimentTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sediment"}
	cmd.Flags().String("session", "", "")
	cmd.Flags().StringArray("knowledge", nil, "")
	cmd.Flags().String("pr", "", "")
	cmd.Flags().Bool("history", false, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func chatSedimentServer(t *testing.T) (*map[string]any, *int) {
	t.Helper()
	var body map[string]any
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat/sessions/"+sedimentTestSession+"/sediment":
			posts++
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"mainline": body["mainline"], "changes": body["changes"]})
		case r.Method == http.MethodGet && r.URL.Path == "/api/chat/sessions/"+sedimentTestSession:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sedimentTestSession})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_CHAT_SESSION_ID", sedimentTestSession)
	return &body, &posts
}

// A repository that only lives here: the chat's branch is merged into the
// project directory's branch with a merge commit naming the chat.
func TestChatSedimentMergesALocalOnlyRepoIntoTheProjectDirectory(t *testing.T) {
	repo, _ := knowledgeRepo(t, "agent/chat/abcd1234")
	body, posts := chatSedimentServer(t)
	before := knowledgeGit(t, repo, "rev-parse", "HEAD")

	// A chat's sediment is boss-layer: no declared action, nothing merged.
	bare := newChatSedimentTestCmd()
	_ = bare.Flags().Set("knowledge", "context=加了沉淀记录词条")
	_ = bare.Flags().Set("output", "table")
	stderrBare := captureStderr(t)
	_, err := captureStdout(t, func() error { return runChatSediment(bare, nil) })
	stderrBare.restore()
	if err == nil || !strings.Contains(err.Error(), "没声明动作") || *posts != 0 || knowledgeGit(t, repo, "rev-parse", "HEAD") != before {
		t.Fatalf("err = %v posts = %d; an undeclared change must be refused before merging", err, *posts)
	}

	cmd := newChatSedimentTestCmd()
	_ = cmd.Flags().Set("knowledge", "context:new=加了沉淀记录词条")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if _, err := captureStdout(t, func() error { return runChatSediment(cmd, nil) }); err != nil {
		t.Fatalf("runChatSediment: %v", err)
	}
	if *posts != 1 {
		t.Fatalf("posts = %d", *posts)
	}
	b := *body
	files, _ := b["delivered_files"].([]any)
	commits, _ := b["commits"].([]any)
	if b["mainline"] != "main" || len(files) != 2 || files[0] != "CONTEXT.md" || len(commits) != 2 {
		t.Fatalf("body = %#v", b)
	}
	if got := knowledgeGit(t, repo, "log", "-1", "--format=%s"); got != "Chat abcd1234: 沉淀 context" {
		t.Fatalf("project directory HEAD = %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "CONTEXT.md")); err != nil {
		t.Fatalf("CONTEXT.md did not land in the project directory: %v", err)
	}
}

func runSediment(t *testing.T, pr string) error {
	t.Helper()
	cmd := newChatSedimentTestCmd()
	_ = cmd.Flags().Set("knowledge", "context:new=加了沉淀记录词条")
	_ = cmd.Flags().Set("output", "table")
	if pr != "" {
		_ = cmd.Flags().Set("pr", pr)
	}
	stderr := captureStderr(t)
	defer stderr.restore()
	_, err := captureStdout(t, func() error { return runChatSediment(cmd, nil) })
	return err
}

// A remote repository counts a PR only when it merged into this repository's
// main line (DENE-1668 F2).
func TestChatSedimentWithARemoteNeedsAMergedPRIntoTheMainline(t *testing.T) {
	repo, wt := knowledgeRepo(t, "agent/chat/abcd1234")
	merged := knowledgeGit(t, wt, "rev-parse", "HEAD")
	knowledgeGit(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	knowledgeGit(t, repo, "config", "multica.mainline", "kun")
	body, posts := chatSedimentServer(t)
	before := knowledgeGit(t, repo, "rev-parse", "HEAD")

	orig := viewMergedPR
	t.Cleanup(func() { viewMergedPR = orig })
	base := "kun"
	viewMergedPR = func(_ context.Context, _, url string) (mergedPR, error) {
		return mergedPR{Title: "Chat abcd1234: 沉淀", Base: base, MergeCommit: merged, Files: []string{"CONTEXT.md"}}, nil
	}
	for name, tc := range map[string]struct{ pr, base, want string }{
		"no pr":      {"", "kun", "--pr"},
		"wrong repo": {"https://github.com/other/r/pull/7", "kun", "不在这个仓库"},
		"wrong base": {"https://github.com/o/r/pull/7", "feature/x", "项目主线是 kun"},
	} {
		base = tc.base
		if err := runSediment(t, tc.pr); err == nil || !strings.Contains(err.Error(), tc.want) || *posts != 0 {
			t.Fatalf("%s: err = %v posts = %d; want a refusal mentioning %q", name, err, *posts, tc.want)
		}
	}
	if after := knowledgeGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatal("a repository with a remote must not be merged locally")
	}

	base = "kun"
	if err := runSediment(t, "https://github.com/o/r/pull/7"); err != nil {
		t.Fatalf("runChatSediment --pr: %v", err)
	}
	b := *body
	landing, _ := b["landing"].(map[string]any)
	if b["mainline"] != "kun" || landing["via"] != "pr" || landing["pr_url"] != "https://github.com/o/r/pull/7" || landing["remote"] != "github.com/o/r" {
		t.Fatalf("body = %#v", b)
	}
	memory, _ := b["memory_files"].([]any)
	if len(memory) != 1 || memory[0].(map[string]any)["path"] != "CONTEXT.md" {
		t.Fatalf("memory_files = %#v; want what git says about the merged CONTEXT.md", b["memory_files"])
	}
}

// DENE-1668 F2: a shared directory's commit on local main that never reached
// origin is not a sediment; once pushed, it is.
func TestChatSedimentWithARemoteCountsOnlyWhatWasPushed(t *testing.T) {
	repo, _ := knowledgeRepo(t, "agent/chat/abcd1234")
	origin := filepath.Join(t.TempDir(), "origin.git")
	knowledgeGit(t, repo, "clone", "--bare", "-q", repo, origin)
	knowledgeGit(t, repo, "remote", "add", "origin", origin)
	knowledgeGit(t, repo, "fetch", "-q", "origin")
	knowledgeGit(t, repo, "branch", "--set-upstream-to=origin/main", "main")
	knowledgeGit(t, repo, "merge", "-q", "--ff-only", "agent/chat/abcd1234")
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	body, posts := chatSedimentServer(t)

	err := runSediment(t, "")
	ahead := knowledgeGit(t, repo, "rev-list", "--count", "origin/main..main")
	if err == nil || !strings.Contains(err.Error(), "还没收到") || *posts != 0 || ahead != "1" {
		t.Fatalf("err = %v posts = %d ahead = %s; an unpushed commit must not be recorded", err, *posts, ahead)
	}

	knowledgeGit(t, repo, "push", "-q", "origin", "main")
	if err := runSediment(t, ""); err != nil || *posts != 1 {
		t.Fatalf("after push: err = %v posts = %d", err, *posts)
	}
	b := *body
	landing, _ := b["landing"].(map[string]any)
	files, _ := b["delivered_files"].([]any)
	if b["mainline"] != "main" || landing["via"] != "push" || landing["remote_has_commits"] != true || len(files) == 0 {
		t.Fatalf("body = %#v", b)
	}
}

// The reviewer's reproducer, kept as it was written: a fake GitHub origin
// that is never pushed to records nothing.
func TestChatSedimentRemoteUnpushedMustNotReportSuccess(t *testing.T) {
	repo, _ := knowledgeRepo(t, "agent/chat/abcd1234")
	knowledgeGit(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	knowledgeGit(t, repo, "config", "multica.mainline", "main")
	knowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	knowledgeGit(t, repo, "branch", "--set-upstream-to=origin/main", "main")
	knowledgeGit(t, repo, "merge", "-q", "--ff-only", "agent/chat/abcd1234")
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	_, posts := chatSedimentServer(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	err := runSediment(t, "")
	if err == nil || *posts > 0 {
		t.Fatalf("err = %v posts = %d; remote sediment accepted while the commit exists only locally", err, *posts)
	}
}

func TestChatSedimentWithoutKnowledgeRecordsNothing(t *testing.T) {
	_, posts := chatSedimentServer(t)
	if err := runChatSediment(newChatSedimentTestCmd(), nil); err == nil || *posts != 0 {
		t.Fatalf("err = %v posts = %d", err, *posts)
	}
}

func TestParseKnowledgeItemReadsActionAndEntry(t *testing.T) {
	for in, want := range map[string]closeprotocol.KnowledgeChange{
		"context=加了词条":                        {Location: "context", Summary: "加了词条"},
		"agents:new=新规则":                      {Location: "agents", Action: "new", Summary: "新规则"},
		"agents:update:工作单=并入经验":              {Location: "agents", Action: "update", Entry: "工作单", Summary: "并入经验"},
		"agents:supersede:旧派单=已被自动派票取代=见 ADR": {Location: "agents", Action: "supersede", Entry: "旧派单", Summary: "已被自动派票取代=见 ADR"},
	} {
		if got := parseKnowledgeItem(in); got.Location != want.Location || got.Action != want.Action || got.Entry != want.Entry || got.Summary != want.Summary {
			t.Errorf("parseKnowledgeItem(%q) = %+v, want %+v", in, got, want)
		}
	}
}
