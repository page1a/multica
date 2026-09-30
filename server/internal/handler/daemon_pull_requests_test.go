package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "下一步：") || !strings.Contains(w.Body.String(), "--pr") {
		t.Fatalf("close = %d: %s, want 409 naming the delivery lookup reason and next command", w.Code, w.Body.String())
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

// DENE-906: a workspace with no GitHub App still closes from the gh snapshot.
// Clean and green is merged by the caller's gh before this request, so the
// report arrives already merged and done sticks. Dirty or red blocks with
// that reason instead of the old "还没确认能干净合并".
func TestDaemonSnapshotCloseWithoutApp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	prev := testHandler.PRMerger
	testHandler.PRMerger = nil
	t.Cleanup(func() { testHandler.PRMerger = prev })

	t.Run("clean green merges once gh reports it merged", func(t *testing.T) {
		issue, agentID, taskID := daemonSnapshotIssue(t)
		pr := daemonSnapshotPR(issue.Identifier, 9989061, "open", "clean", "success", nil, 0)
		cleanupDaemonSnapshotPR(t, pr.Number)
		reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr})
		assertSnapshotColumns(t, pr.Number, "clean", "success")

		w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "gh pr merge") || strings.Contains(w.Body.String(), "还没确认能干净合并") {
			t.Fatalf("open clean PR = %d: %s, want 409 telling the agent to gh pr merge", w.Code, w.Body.String())
		}

		merged := time.Now().UTC()
		pr.State, pr.MergedAt = "merged", &merged
		reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr})
		w = closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL + " merged"})
		if w.Code != http.StatusOK {
			t.Fatalf("close after gh merge = %d: %s", w.Code, w.Body.String())
		}
		if got := issueStatusDirect(t, issue.ID); got != "done" {
			t.Fatalf("status = %s, want done", got)
		}
	})

	t.Run("dirty blocks with the conflict", func(t *testing.T) {
		issue, agentID, taskID := daemonSnapshotIssue(t)
		pr := daemonSnapshotPR(issue.Identifier, 9989062, "open", "dirty", "success", nil, 0)
		cleanupDaemonSnapshotPR(t, pr.Number)
		reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr})
		w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL})
		assertBlockedClose(t, issue.ID, w, "合并冲突")
	})

	t.Run("red check blocks with the check name", func(t *testing.T) {
		issue, agentID, taskID := daemonSnapshotIssue(t)
		pr := daemonSnapshotPR(issue.Identifier, 9989063, "open", "clean", "failure", []string{"backend"}, 0)
		cleanupDaemonSnapshotPR(t, pr.Number)
		reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{pr})
		w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + pr.URL})
		assertBlockedClose(t, issue.ID, w, "检查是红的")
		if cond := issueMetaString(t, issue.ID, "block.wait_condition"); !strings.Contains(cond, "backend") {
			t.Fatalf("wait condition = %q, want the check name", cond)
		}
	})
}

func daemonSnapshotIssue(t *testing.T) (IssueResponse, string, string) {
	t.Helper()
	issue := createIssueHTTP(t, "daemon snapshot close", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	return issue, agentID, taskID
}

func daemonSnapshotPR(ident string, number int32, state, mergeable, rollup string, failed []string, running int) DaemonPullRequest {
	return DaemonPullRequest{
		Owner: "jeff-kunkun", Repo: "multica", Number: number, Title: ident + ": gate", State: state,
		URL: "https://github.com/jeff-kunkun/multica/pull/" + strconv.Itoa(int(number)), Branch: "agent/agent/dene-906", SHA: "sha-" + strconv.Itoa(int(number)),
		MergeableState: &mergeable, ChecksRollup: &rollup, FailedCheckNames: failed, ChecksRunning: running,
	}
}

func assertSnapshotColumns(t *testing.T, number int32, mergeable, rollup string) {
	t.Helper()
	var gotMergeable, gotRollup string
	if err := testPool.QueryRow(context.Background(), `
		SELECT mergeable_state, checks_rollup_state FROM github_pull_request
		WHERE workspace_id = $1 AND pr_number = $2`, testWorkspaceID, number).Scan(&gotMergeable, &gotRollup); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if gotMergeable != mergeable || gotRollup != rollup {
		t.Fatalf("snapshot = %s / %s, want %s / %s", gotMergeable, gotRollup, mergeable, rollup)
	}
}

func TestDaemonReportKeepsAppInstallation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ws := parseUUID(testWorkspaceID)
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	row, err := testHandler.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID: ws, InstallationID: 4242, RepoOwner: "acme", RepoName: "widget", PrNumber: 9061,
		Title: "keep installation", State: "open", HtmlUrl: "https://github.com/acme/widget/pull/9061",
		PrCreatedAt: now, PrUpdatedAt: now, HeadSha: "oldsha",
		MergeableState: pgtype.Text{String: "clean", Valid: true},
		Source:         pgtype.Text{String: "github_app", Valid: true},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cleanupDaemonSnapshotPR(t, row.PrNumber)
	if err := testHandler.persistReportedPullRequests(ctx, ws, []DaemonPullRequest{{
		Owner: "acme", Repo: "widget", Number: 9061, Title: "keep installation",
		State: "open", URL: "https://github.com/acme/widget/pull/9061", SHA: "newsha",
	}}, ""); err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := testHandler.Queries.GetGitHubPullRequest(ctx, db.GetGitHubPullRequestParams{
		WorkspaceID: ws, RepoOwner: "acme", RepoName: "widget", PrNumber: 9061,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.InstallationID != 4242 || got.Source != "github_app" || got.MergeableState.String != "clean" || got.HeadSha != "newsha" {
		t.Fatalf("installation=%d source=%s mergeable=%s sha=%s", got.InstallationID, got.Source, got.MergeableState.String, got.HeadSha)
	}
}

func cleanupDaemonSnapshotPR(t *testing.T, number int32) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id IN (
			SELECT id FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2)`, testWorkspaceID, number)
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id IN (
			SELECT id FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2)`, testWorkspaceID, number)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE workspace_id = $1 AND pr_number = $2`, testWorkspaceID, number)
	})
}

func assertBlockedClose(t *testing.T, issueID string, w *httptest.ResponseRecorder, reason string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("close = %d: %s, want 200 rewritten to blocked", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "blocked" {
		t.Fatalf("status = %q, want blocked: %s", resp.Status, w.Body.String())
	}
	if got := issueStatusDirect(t, issueID); got != "blocked" {
		t.Fatalf("db status = %s, want blocked", got)
	}
	cond := issueMetaString(t, issueID, "block.wait_condition")
	if !strings.Contains(cond, reason) {
		t.Fatalf("wait condition = %q, want %q", cond, reason)
	}
}
