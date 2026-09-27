package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func reportIssuePRsHTTP(t *testing.T, issueID string, prs []DaemonPullRequest) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/pull-requests/report", map[string]any{"pull_requests": prs})
	req = withURLParam(req, "id", issueID)
	testHandler.ReportIssuePullRequests(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("report = %d: %s", w.Code, w.Body.String())
	}
}

// DENE-875 on a workspace with no GitHub App: the reported open PR cannot be
// merged by the server, so done is refused back to the closing agent (a block
// would wait forever, DENE-899); once the caller's gh reports the merge, the
// same close goes through.
func TestReportedPullRequestDrivesDoneGateWithoutApp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "zero app close", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	prev := testHandler.PRMerger
	testHandler.PRMerger = nil
	t.Cleanup(func() {
		testHandler.PRMerger = prev
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE workspace_id = $1 AND pr_number = 998751`, testWorkspaceID)
	})
	pr := DaemonPullRequest{Owner: "jeff-kunkun", Repo: "multica", Number: 998751, Title: issue.Identifier + ": gate", State: "open",
		URL: "https://github.com/jeff-kunkun/multica/pull/998751", Branch: "agent/agent/x", SHA: "abc"}
	unrelated := pr
	unrelated.Number, unrelated.Title = 998752, "unrelated work"
	reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr, unrelated})

	var linked int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue_pull_request WHERE issue_id = $1`, issue.ID).Scan(&linked)
	if linked != 1 {
		t.Fatalf("linked PRs = %d, want 1 (the unrelated one is skipped)", linked)
	}
	var source string
	testPool.QueryRow(context.Background(), `SELECT source FROM github_pull_request WHERE workspace_id = $1 AND pr_number = 998751`, testWorkspaceID).Scan(&source)
	if source != "daemon" {
		t.Fatalf("source = %q, want daemon", source)
	}

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "gh pr merge") {
		t.Fatalf("close with open PR = %d: %s, want 409 naming gh pr merge", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status with open PR = %s, want unchanged", got)
	}

	merged := time.Now().UTC()
	pr.State, pr.MergedAt = "merged", &merged
	reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr})
	w = closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL + " merged"})
	if w.Code != http.StatusOK {
		t.Fatalf("close after merge = %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("status after merge report = %s, want done", got)
	}
}

// DENE-899: a delivery branch with no PR the platform can see is refused back
// to the closing agent instead of parked in a block nobody would ever release.
func TestAgentCloseWithBranchButNoPullIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "branch without PR", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO issue_delivery_branch (issue_id, workspace_id, branch_name, role, agent_id)
		VALUES ($1, $2, 'agent/agent/no-pr', 'canonical', $3)`, issue.ID, testWorkspaceID, agentID); err != nil {
		t.Fatalf("seed delivery branch: %v", err)
	}
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "推上去了"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "gh pr create") {
		t.Fatalf("close = %d: %s, want 409 naming gh pr create", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s, want unchanged", got)
	}
}

// An executor cannot close past someone else's acceptance seat.
func TestAgentCannotCloseOverAcceptanceSeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "seat close", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET reviewer_type = 'member', reviewer_id = $2 WHERE id = $1`, issue.ID, testUserID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "done", "no_code_reason": "docs"})
	if w.Code != http.StatusConflict {
		t.Fatalf("close = %d: %s, want 409", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s, want unchanged", got)
	}
}
