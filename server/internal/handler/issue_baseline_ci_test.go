package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeBaseChecks struct {
	base *ghsnapshot.BaseChecks
	err  error
}

func (f fakeBaseChecks) FetchBaseChecks(context.Context, int64, string, string, int32) (*ghsnapshot.BaseChecks, error) {
	return f.base, f.err
}

func baselineBranch(red ...string) *ghsnapshot.BaseChecks {
	base := &ghsnapshot.BaseChecks{Branch: "kun-dene892", HeadSHA: "base892"}
	for _, name := range red {
		base.Contexts = append(base.Contexts, ghsnapshot.CheckContext{Name: name, Status: "completed", Conclusion: "failure"})
	}
	base.Contexts = append(base.Contexts, ghsnapshot.CheckContext{Name: "sqlc-check", Status: "completed", Conclusion: "success"})
	return base
}

// seedRedPR links an open PR whose snapshot has the given red checks.
func seedRedPR(t *testing.T, issueID string, number int, failed ...string) {
	t.Helper()
	ctx := context.Background()
	var prID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO github_pull_request (workspace_id, installation_id, repo_owner, repo_name, pr_number, title, state, html_url, pr_created_at, pr_updated_at, head_sha, mergeable_state, checks_rollup_state, snapshot_head_sha)
		VALUES ($1, 1, 'jeff-kunkun', 'multica', $2, 'baseline PR', 'open', $3, now(), now(), 'head892', 'unstable', 'FAILURE', 'head892')
		RETURNING id
	`, testWorkspaceID, number, fmt.Sprintf("https://example.test/pr/%d", number)).Scan(&prID); err != nil {
		t.Fatalf("seed PR: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
	for i, name := range failed {
		if _, err := testPool.Exec(ctx, `INSERT INTO github_pull_request_check_run (pr_id, head_sha, ordinal, name, status, conclusion) VALUES ($1, 'head892', $2, $3, 'completed', 'failure')`, prID, i, name); err != nil {
			t.Fatalf("seed check: %v", err)
		}
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_pull_request (issue_id, pull_request_id) VALUES ($1, $2)`, issueID, prID); err != nil {
		t.Fatalf("link PR: %v", err)
	}
}

func passAcceptance(t *testing.T, title string, number int, failed ...string) (string, releaseOutcome) {
	t.Helper()
	issue := createIssueHTTP(t, title, "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	seedRedPR(t, issue.ID, number, failed...)
	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return issue.ID, testHandler.releaseOnAcceptance(context.Background(), loaded)
}

func useBaseline(t *testing.T, reader BaseCheckReader) {
	prevMerger, prevBase := testHandler.PRMerger, testHandler.PRBaseChecks
	testHandler.PRMerger = fakeMerger{}
	testHandler.PRBaseChecks = reader
	t.Cleanup(func() {
		testHandler.PRMerger, testHandler.PRBaseChecks = prevMerger, prevBase
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND title = 'kun-dene892 基线 CI 红'`, testWorkspaceID)
	})
}

func baselineFixIssues(t *testing.T) []db.Issue {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id, metadata FROM issue WHERE workspace_id = $1 AND title = 'kun-dene892 基线 CI 红'`, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []db.Issue
	for rows.Next() {
		var i db.Issue
		if err := rows.Scan(&i.ID, &i.Metadata); err != nil {
			t.Fatal(err)
		}
		out = append(out, i)
	}
	return out
}

// DENE-892: a red check that is already red on the base branch merges, and
// two tickets let through by the same failure share one fix issue.
func TestBaselineFailureMergesAndSharesOneFixIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	useBaseline(t, fakeBaseChecks{base: baselineBranch("frontend-test", "backend-tests")})

	firstID, first := passAcceptance(t, "baseline first", 998921, "frontend-test")
	if first.Status != "done" || !first.Merged {
		t.Fatalf("first = %+v, want merged and done", first)
	}
	if !strings.Contains(first.Baseline, "因主线原有失败放行：frontend-test") || !strings.Contains(first.Baseline, "修复票：") {
		t.Fatalf("first report = %q", first.Baseline)
	}
	if got := issueStatusDirect(t, firstID); got != "done" {
		t.Fatalf("db status = %s", got)
	}

	_, second := passAcceptance(t, "baseline second", 998922, "frontend-test")
	if second.Status != "done" || !second.Merged {
		t.Fatalf("second = %+v", second)
	}
	fixes := baselineFixIssues(t)
	if len(fixes) != 1 {
		t.Fatalf("fix issues = %d, want 1", len(fixes))
	}

	_, third := passAcceptance(t, "baseline third", 998923, "backend-tests", "frontend-test")
	if third.Status != "done" {
		t.Fatalf("third = %+v", third)
	}
	fixes = baselineFixIssues(t)
	if len(fixes) != 1 {
		t.Fatalf("fix issues after a new name = %d, want 1", len(fixes))
	}
	if !strings.Contains(string(fixes[0].Metadata), "backend-tests,frontend-test") {
		t.Fatalf("fix issue checks = %s", fixes[0].Metadata)
	}
}

func TestNewFailureOrMissingBaselineStillBlocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	useBaseline(t, fakeBaseChecks{base: baselineBranch()})
	_, green := passAcceptance(t, "baseline green", 998924, "frontend-test")
	if green.Status != "blocked" || green.Merged || !strings.Contains(green.Note, "新引入：frontend-test") {
		t.Fatalf("green baseline = %+v", green)
	}

	testHandler.PRBaseChecks = fakeBaseChecks{err: fmt.Errorf("github down")}
	_, unknown := passAcceptance(t, "baseline unknown", 998925, "frontend-test")
	if unknown.Status != "blocked" || !strings.Contains(unknown.Note, "没拿到主线基线") {
		t.Fatalf("missing baseline = %+v", unknown)
	}
	if n := len(baselineFixIssues(t)); n != 0 {
		t.Fatalf("fix issues = %d, want none", n)
	}
}

// The executor's `issue close --outcome done` says in its reply which checks
// the gate let through.
func TestCloseDoneReportsBaselineLetThrough(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	useBaseline(t, fakeBaseChecks{base: baselineBranch("frontend-test")})
	issue := createIssueHTTP(t, "baseline close done", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	seedRedPR(t, issue.ID, 998926, "frontend-test")

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR 已开；frontend-test 和 kun 同一个测试对照过，是主线原有失败。"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Merged || resp.Status != "done" {
		t.Fatalf("merged = %v status = %q warnings = %v", resp.Merged, resp.Status, resp.Warnings)
	}
	if woken := strings.Join(resp.Woken, "\n"); !strings.Contains(woken, "因主线原有失败放行：frontend-test") || !strings.Contains(woken, "修复票：") {
		t.Fatalf("woken = %v", resp.Woken)
	}
	if n := len(baselineFixIssues(t)); n != 1 {
		t.Fatalf("fix issues = %d, want 1", n)
	}
}
