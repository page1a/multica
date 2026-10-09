package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// addTaskWorktree forks a task branch off from in its own worktree, the way
// a run's checkout sits next to the user's directory.
func addTaskWorktree(t *testing.T, repo, branch, from string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-b", branch, dir, from)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return dir
}

func TestDeliveredFilesListsWhatTheBranchAddsAgainstTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/agent/dene-1", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "DENE-1: context")
	if err := os.MkdirAll(filepath.Join(wt, "docs", "adr"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitIn(t, wt, "docs/adr/0001-x.md", "adr\n", "DENE-1: adr")
	gitRun(t, wt, "rm", "-q", "keep.txt")
	gitRun(t, wt, "commit", "-m", "DENE-1: drop keep")

	files, base, err := DeliveredFiles(wt)
	if err != nil {
		t.Fatal(err)
	}
	if base != "main" || !reflect.DeepEqual(files, []string{"CONTEXT.md", "docs/adr/0001-x.md"}) {
		t.Fatalf("files = %v base = %q; want the two added files against main, the deletion left out", files, base)
	}
}

func TestDeliveredFilesPrefersTheDeliveryLine(t *testing.T) {
	repo := newTestRepo(t)
	gitRun(t, repo, "branch", testDeliveryBranch, "main")
	sibling := addTaskWorktree(t, repo, "agent/agent/sibling", testDeliveryBranch)
	commitIn(t, sibling, "sibling.txt", "s\n", "sibling work")
	gitRun(t, repo, "branch", "-f", testDeliveryBranch, "agent/agent/sibling")

	wt := addTaskWorktree(t, repo, "agent/agent/dene-2", testDeliveryBranch)
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "DENE-2: context")

	files, base, err := DeliveredFiles(wt, "no-such-branch", testDeliveryBranch)
	if err != nil {
		t.Fatal(err)
	}
	if base != testDeliveryBranch || !reflect.DeepEqual(files, []string{"CONTEXT.md"}) {
		t.Fatalf("files = %v base = %q; the sibling's file is the line's, not this delivery's", files, base)
	}
}

func TestDeliveredFilesSaysNothingWhenHeadIsOnTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	files, base, err := DeliveredFiles(repo)
	if err != nil || files != nil || base != "" {
		t.Fatalf("files = %v base = %q err = %v; want no answer", files, base, err)
	}
}

func TestMergeIntoMainlineMergesTheChatBranchIntoTheProjectDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/chat/abc", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "context term")

	res, err := MergeIntoMainline(wt, "Chat abcd1234: 沉淀 context", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mainline != "main" || res.Source != "agent/chat/abc" || !reflect.DeepEqual(res.Files, []string{"CONTEXT.md"}) {
		t.Fatalf("res = %+v", res)
	}
	if got := subjects(res.Commits); !reflect.DeepEqual(got, []string{"context term", "Chat abcd1234: 沉淀 context"}) {
		t.Fatalf("commits = %v", got)
	}
	if got := gitRun(t, repo, "log", "-1", "--format=%s"); got != "Chat abcd1234: 沉淀 context" {
		t.Fatalf("project directory HEAD = %q; want the chat's merge commit", got)
	}
	if got := readFile(t, filepath.Join(repo, "CONTEXT.md")); got != "# terms\n" {
		t.Fatalf("project directory CONTEXT.md = %q", got)
	}
}

func TestMergeIntoMainlineRefusesADirtyProjectDirectory(t *testing.T) {
	repo := newTestRepo(t)
	wt := addTaskWorktree(t, repo, "agent/chat/abc", "main")
	commitIn(t, wt, "CONTEXT.md", "# terms\n", "context term")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "the user's edit\n")
	before := gitRun(t, repo, "rev-parse", "HEAD")

	_, err := MergeIntoMainline(wt, "Chat abcd1234: 沉淀 context", "")
	if !errors.Is(err, ErrDeliveryMergeRefused) || !strings.Contains(err.Error(), "tracked.txt") {
		t.Fatalf("err = %v; want a refusal naming the dirty file", err)
	}
	if after := gitRun(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("main moved %s -> %s on a refused merge", before, after)
	}
	if got := readFile(t, filepath.Join(repo, "tracked.txt")); got != "the user's edit\n" {
		t.Fatalf("the user's edit was touched: %q", got)
	}
}

func TestMergeIntoMainlineRecordsWorkAlreadyOnTheMainline(t *testing.T) {
	repo := newTestRepo(t)
	commitIn(t, repo, "CONTEXT.md", "# terms\n", "Chat abcd1234: context term")
	before := gitRun(t, repo, "rev-parse", "HEAD")

	res, err := MergeIntoMainline(repo, "Chat abcd1234: 沉淀 context", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tip != before || res.Source != "main" {
		t.Fatalf("res = %+v; a shared directory's work is recorded, not merged again", res)
	}
	// Without a session start or an upstream, the last commits stand in.
	if !strings.Contains(strings.Join(res.Files, ","), "CONTEXT.md") {
		t.Fatalf("files = %v", res.Files)
	}
}

// DENE-1668 F1: the project directory parked on a feature branch is not the
// main line. The sediment lands on main, and the feature branch is untouched.
func TestMergeIntoMainlineIgnoresTheProjectDirectorysFeatureBranch(t *testing.T) {
	repo := newTestRepo(t)
	before := gitRun(t, repo, "rev-parse", "main")
	gitRun(t, repo, "checkout", "-b", "feature/stale")
	feature := gitRun(t, repo, "rev-parse", "feature/stale")
	wt := addTaskWorktree(t, repo, "agent/chat/review", "main")
	commitIn(t, wt, "CONTEXT.md", "terms\n", "Chat review: terms")

	res, err := MergeIntoMainline(wt, "Chat review: sediment", "")
	if err != nil {
		t.Fatal(err)
	}
	after := gitRun(t, repo, "rev-parse", "main")
	if res.Mainline != "main" || after == before || res.Tip != after {
		t.Fatalf("res = %+v; main %s -> %s", res, before, after)
	}
	if got := gitRun(t, repo, "show", "main:CONTEXT.md"); got != "terms" {
		t.Fatalf("main:CONTEXT.md = %q", got)
	}
	if got := gitRun(t, repo, "rev-parse", "feature/stale"); got != feature {
		t.Fatalf("feature/stale moved %s -> %s", feature, got)
	}
}

func TestMergeIntoMainlineFromADetachedProjectDirectoryLandsOnMain(t *testing.T) {
	repo := newTestRepo(t)
	gitRun(t, repo, "checkout", "--detach")
	wt := addTaskWorktree(t, repo, "agent/chat/review", "main")
	commitIn(t, wt, "CONTEXT.md", "terms\n", "Chat review: terms")

	res, err := MergeIntoMainline(wt, "Chat review: sediment", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mainline != "main" || gitRun(t, repo, "rev-parse", "main") != res.Tip {
		t.Fatalf("res = %+v", res)
	}
}

func TestMergeIntoMainlineRefusesWhenTheMainlineIsAmbiguous(t *testing.T) {
	repo := newTestRepo(t)
	gitRun(t, repo, "branch", "master")
	before := gitRun(t, repo, "rev-parse", "main")
	wt := addTaskWorktree(t, repo, "agent/chat/review", "main")
	commitIn(t, wt, "CONTEXT.md", "terms\n", "Chat review: terms")

	_, err := MergeIntoMainline(wt, "Chat review: sediment", "")
	if !errors.Is(err, ErrDeliveryMergeRefused) || !strings.Contains(err.Error(), MainlineConfigKey) {
		t.Fatalf("err = %v; want a refusal naming %s", err, MainlineConfigKey)
	}
	if gitRun(t, repo, "rev-parse", "main") != before || gitRun(t, repo, "rev-parse", "master") != before {
		t.Fatal("a refused merge moved a branch")
	}

	gitRun(t, repo, "config", MainlineConfigKey, "master")
	res, err := MergeIntoMainline(wt, "Chat review: sediment", "")
	if err != nil || res.Mainline != "master" || gitRun(t, repo, "rev-parse", "main") != before {
		t.Fatalf("res = %+v err = %v; want the configured master", res, err)
	}
}

// originFor gives repo a bare origin whose default branch is main.
func originFor(t *testing.T, repo string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, repo, "clone", "--bare", "-q", repo, origin)
	gitRun(t, repo, "remote", "add", "origin", origin)
	gitRun(t, repo, "fetch", "-q", "origin")
	return origin
}

func TestMainlineOfARemoteRepositoryIsTheRemoteDefault(t *testing.T) {
	repo := newTestRepo(t)
	origin := originFor(t, repo)
	gitRun(t, repo, "branch", "kun")
	gitRun(t, repo, "push", "-q", "origin", "kun")
	gitRun(t, origin, "symbolic-ref", "HEAD", "refs/heads/kun")
	gitRun(t, repo, "checkout", "-q", "-b", "feature/x")
	if got := Mainline(repo); got != "kun" {
		t.Fatalf("Mainline = %q; want the remote's default kun, not the checkout's feature/x or a stale origin/HEAD", got)
	}
}

func TestCheckRemoteLandingSeesOnlyWhatTheRemoteHolds(t *testing.T) {
	repo := newTestRepo(t)
	originFor(t, repo)
	commitIn(t, repo, "CONTEXT.md", "terms\n", "Chat review: terms")

	res, err := CheckRemoteLanding(repo, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Landed {
		t.Fatalf("res = %+v; an unpushed commit has not landed", res)
	}
	gitRun(t, repo, "push", "-q", "origin", "main")
	if res, err = CheckRemoteLanding(repo, "main", ""); err != nil || !res.Landed || res.RemoteTip != gitRun(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("res = %+v err = %v; a pushed commit has landed", res, err)
	}
	if !strings.Contains(strings.Join(res.Files, ","), "CONTEXT.md") {
		t.Fatalf("files = %v", res.Files)
	}
}

// DENE-1680: the hygiene account of a delivery — size at HEAD, existing
// lines removed or rewritten, added supersede marks, sections.
func TestDeliveredMemoryFilesReadsHygieneFacts(t *testing.T) {
	repo := newTestRepo(t)
	commitIn(t, repo, "AGENTS.md", "# Map\n## 派单\n- 手工派单\n- 先查重\n", "seed map")
	wt := addTaskWorktree(t, repo, "agent/agent/dene-3", "main")
	next := "# Map\n## 派单\n- 手工派单（已被「自动派票」取代）\n## 自动派票\n- 路由决定执行席\n"
	commitIn(t, wt, "AGENTS.md", next, "DENE-3: map")
	commitIn(t, wt, "server.go", "package x\n", "DENE-3: code")

	files, err := DeliveredMemoryFiles(wt)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v; only AGENTS.md is a memory file", files)
	}
	f := files[0]
	if f.Path != "AGENTS.md" || f.Bytes != len(next) || f.Deleted != 2 || f.SupersedeMarks != 1 {
		t.Fatalf("facts = %+v", f)
	}
	if len(f.Sections) != 3 || !f.Sections[1].Superseded || f.Sections[2].Heading != "自动派票" {
		t.Fatalf("sections = %+v", f.Sections)
	}

	head := strings.TrimSpace(gitRun(t, wt, "rev-parse", "HEAD~1"))
	byCommit, err := CommitMemoryFiles(wt, []string{head}, []string{"AGENTS.md"})
	if err != nil || len(byCommit) != 1 || byCommit[0].Deleted != 2 || byCommit[0].SupersedeMarks != 1 {
		t.Fatalf("by commit = %+v err = %v", byCommit, err)
	}
}
