package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/internal/delivery"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// closeIssueHTTP posts to /api/issues/{id}/close as the given agent task (or
// as the test member when agentID is empty) and returns the recorder.
func closeIssueHTTP(t *testing.T, issueID, agentID, taskID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	// Closes that predate the knowledge audit keep their original assertions.
	// omit_knowledge_audit asks for the rejection path; every other body gets
	// an explicit 无够格知识 declaration when it did not name an audit.
	if body != nil {
		omit, _ := body["omit_knowledge_audit"].(bool)
		delete(body, "omit_knowledge_audit")
		if omit {
			delete(body, "knowledge_audit")
		} else if _, ok := body["knowledge_audit"]; !ok {
			body["knowledge_audit"] = map[string]any{"none": true}
		}
	}
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/close", body)
	req = withURLParam(req, "id", issueID)
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
	}
	testHandler.CloseIssue(w, req)
	// The transcript is the PR-body evidence for DENE-859's four scenarios:
	// what was sent, and the exact status + message the server answered.
	reqJSON, _ := json.Marshal(body)
	t.Logf("POST /api/issues/%s/close %s\n-> %d %s", issueID, reqJSON, w.Code, strings.TrimSpace(w.Body.String()))
	return w
}

func issueMetaString(t *testing.T, issueID, key string) string {
	t.Helper()
	var value *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>$2 FROM issue WHERE id = $1`, issueID, key).Scan(&value); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if value == nil {
		return ""
	}
	return *value
}

func issueStatusDirect(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

// Scene 1: done without evidence is refused and nothing is written.
func TestCloseDoneWithoutEvidenceIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close no evidence", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "--evidence") {
		t.Fatalf("rejection should name the missing --evidence, got %s", w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status changed on a rejected close: %s", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != "" {
		t.Fatalf("close record written on a rejected close: %s", got)
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM comment WHERE issue_id = $1`, issue.ID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Fatalf("comment posted on a rejected close: %d", n)
	}
}

// Scene 2: blocked without a wait is refused with the DENE-850 hint.
func TestCloseBlockedWithoutWaitIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close blocked bare", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "blocked",
		"evidence": "买入卡住了，等 DENE-806 修好。",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"--blocked-by", "--wake-at", "--needs-human"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rejection should list %s, got %s", want, body)
		}
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status changed on a rejected close: %s", got)
	}

	// The same call with a blocker lands: status, comment, close.* and the
	// DENE-850 wait record, all from one request.
	w = closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":    "blocked",
		"evidence":   "买入卡住了，等 DENE-806 修好。",
		"blocked_by": "DENE-806",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("blocked close: status = %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionBlocked {
		t.Fatalf("close.conclusion = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyBlockKind); got != closeprotocol.BlockDependency {
		t.Fatalf("close.block_kind = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyWaitingOn); got != "DENE-806" {
		t.Fatalf("close.waiting_on = %q", got)
	}
	if got := issueMetaString(t, issue.ID, "block.blocked_by"); got != "DENE-806" {
		t.Fatalf("block.blocked_by = %q", got)
	}
}

// Scene 3: a normal in_review close writes the comment, the status and the
// close record together; the wake is routing, not a mention.
func TestCloseInReviewWritesCommentStatusAndRecord(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close in_review", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	// The DENE-869 review gate: an agent's in_review needs a linked PR.
	seedOpenPullForIssue(t, issue.ID, 1)

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "in_review",
		"summary":  "PR #1 已开，本地测试通过",
		"evidence": "PR: https://example.test/pr/1\ngo test ./internal/... 全绿。",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "in_review" || !resp.StatusChanged || resp.PrevStatus != "in_progress" {
		t.Fatalf("response status = %+v", resp)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_review" {
		t.Fatalf("status = %s, want in_review", got)
	}
	var content, authorType string
	if err := testPool.QueryRow(context.Background(),
		`SELECT content, author_type FROM comment WHERE id = $1`, resp.Comment.ID).Scan(&content, &authorType); err != nil {
		t.Fatalf("read comment: %v", err)
	}
	if authorType != "agent" || !strings.Contains(content, "PR #1 已开") || !strings.Contains(content, "全绿") {
		t.Fatalf("comment = %q by %s", content, authorType)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyEvidenceCommentID); got != resp.Comment.ID {
		t.Fatalf("close.evidence_comment_id = %q, want %q", got, resp.Comment.ID)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionAwaitingReview {
		t.Fatalf("close.conclusion = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyWakeAction); got != closeprotocol.WakeRoute {
		t.Fatalf("close.wake_action = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyStatus); got != "in_review" {
		t.Fatalf("close.status = %q", got)
	}
	if len(resp.Woken) == 0 || !strings.Contains(strings.Join(resp.Woken, "\n"), "路由") {
		t.Fatalf("woken should explain the routing hand-off, got %v", resp.Woken)
	}
}

// A routing verdict of reviewer_type=none means this ticket should use the
// done path. An agent close must not leave an unowned in_review card behind.
func TestCloseInReviewRoutingNoneIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close routed without review", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	seedOpenPullForIssue(t, issue.ID, 1156)
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET reviewer_type = 'none', reviewer_id = NULL WHERE id = $1`, issue.ID); err != nil {
		t.Fatalf("set no-review routing verdict: %v", err)
	}

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "in_review",
		"evidence": "MR 已提交，等验收",
	})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "--outcome done") {
		t.Fatalf("status = %d, want a done-path refusal: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s, want in_progress", got)
	}
}

// A linked GitLab MR may be merged after its last webhook. The close gate
// refreshes the provider state and must accept the merged result as done.
func TestCloseDoneRefreshesMergedGitLabMR(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"iid":490,"title":"Fix DENE-1156","state":"merged","web_url":%q,"sha":"27b88db00","source_branch":"agent/dene-1156"}`, api.URL+"/acme/game/-/merge_requests/490")
	}))
	defer api.Close()
	box := withVCSBox(t)
	prevHTTP := testHandler.deliveryHTTP
	testHandler.deliveryHTTP = api.Client()
	t.Cleanup(func() { testHandler.deliveryHTTP = prevHTTP })
	connID := seedVCSConnection(t, ctx, box, "gitlab", api.URL)
	issue := newVCSIssue(t, "GitLab merged close")
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	pr, err := testHandler.Queries.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID), ConnectionID: parseUUID(connID),
		Provider: "gitlab", RepoOwner: "acme", RepoName: "game", PrNumber: 490,
		Title: "Fix " + issue.Identifier, State: "open",
		HtmlUrl:     api.URL + "/acme/game/-/merge_requests/490",
		PrCreatedAt: now, PrUpdatedAt: now, HeadSha: "oldsha",
	})
	if err != nil {
		t.Fatalf("UpsertVCSPullRequest: %v", err)
	}
	if _, err := testHandler.Queries.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
		IssueID: parseUUID(issue.ID), PullRequestID: pr.ID,
	}); err != nil {
		t.Fatalf("LinkIssueToVCSPullRequest: %v", err)
	}
	t.Cleanup(func() { cleanupVCS(ctx, issue.ID) })

	w := closeIssueHTTP(t, issue.ID, "", "", map[string]any{
		"outcome":  "done",
		"evidence": "MR 已合并：" + api.URL + "/acme/game/-/merge_requests/490",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("status = %s, want done", got)
	}
}

// An agent's in_review close runs the DENE-869 review gate `issue status`
// runs: no linked PR and no declared no-code reason is a 409 that names the
// exit, and the issue stays where it was. The same close with
// no_code_reason goes through and the reason lands in the evidence trail.
func TestCloseInReviewWithoutPullRequestReusesReviewGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close in_review no pr", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "in_review",
		"evidence": "改动都在本地，还没推分支",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "PR") || !strings.Contains(body, issue.Identifier) || !strings.Contains(body, "--no-code") {
		t.Fatalf("refusal must name the PR, the identifier and the exit, got %s", body)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s, want the issue left in in_progress", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyStatus); got != "" {
		t.Fatalf("a refused close must not leave a close record, got close.status=%q", got)
	}

	t.Run("a declared no-code reason is the exit", func(t *testing.T) {
		docs := createIssueHTTP(t, "close in_review docs only", "in_progress")
		taskID := insertIssueTaskWithStatus(t, agentID, docs.ID, "running")
		w := closeIssueHTTP(t, docs.ID, agentID, taskID, map[string]any{
			"outcome":        "in_review",
			"evidence":       "docs/kun 下两篇文档已改，没有代码",
			"no_code_reason": "纯文档票，改动在 docs/ 里已直接合入",
		})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		var resp CloseIssueResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Status != "in_review" {
			t.Fatalf("status = %s, want in_review", resp.Status)
		}
		if got := issueStatusDirect(t, docs.ID); got != "in_review" {
			t.Fatalf("status = %s, want in_review", got)
		}
		if !strings.Contains(strings.Join(resp.Woken, "\n"), "纯文档票") {
			t.Fatalf("the reply should carry the declared no-code reason, got %v", resp.Woken)
		}
	})
}

// A sub-issue never enters in_review: acceptance belongs to the parent.
func TestCloseSubIssueInReviewIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := createIssueHTTP(t, "close parent", "in_progress")
	child := createIssueHTTP(t, "close child", "in_progress")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET parent_issue_id = $2 WHERE id = $1`, child.ID, parent.ID); err != nil {
		t.Fatalf("attach child: %v", err)
	}
	w := closeIssueHTTP(t, child.ID, "", "", map[string]any{"outcome": "in_review", "evidence": "done"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "--outcome done") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	// done on the child is the right close: stage_done wake, parent notified.
	w = closeIssueHTTP(t, child.ID, "", "", map[string]any{"outcome": "done", "evidence": "PR 已合。"})
	if w.Code != http.StatusOK {
		t.Fatalf("child done: %d: %s", w.Code, w.Body.String())
	}
	if got := issueMetaString(t, child.ID, closeprotocol.KeyWakeAction); got != closeprotocol.WakeStageDone {
		t.Fatalf("close.wake_action = %q", got)
	}
	if got := countSystemCommentsOn(t, parent.ID); got != 1 {
		t.Fatalf("parent should get one child-done note, got %d", got)
	}
}

// Scene 4: the acceptance seat's pass merges the open PR through the same
// chain `comment add --verdict pass` uses, and the ticket ends done.
func TestCloseVerdictPassMergesAndCloses(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "close verdict pass", "in_review")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	// The executor is somebody else: a pass from a non-reviewer is refused.
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "看过了", "verdict": "pass"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-reviewer pass: status = %d: %s", w.Code, w.Body.String())
	}

	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	var prID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO github_pull_request (workspace_id, installation_id, repo_owner, repo_name, pr_number, title, state, html_url, pr_created_at, pr_updated_at, head_sha, mergeable_state)
		VALUES ($1, 1, 'multica-ai', 'multica', 999859, 'close PR', 'open', 'https://example.test/pr/859', now(), now(), 'abc859', 'clean')
		RETURNING id
	`, testWorkspaceID).Scan(&prID); err != nil {
		t.Fatalf("seed PR: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_pull_request (issue_id, pull_request_id) VALUES ($1, $2)`, issue.ID, prID); err != nil {
		t.Fatalf("link PR: %v", err)
	}
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	w = closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "done",
		"evidence": "验收：四个场景都对，PR 可以合。",
		"verdict":  "pass",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Merged || resp.PRURL != "https://example.test/pr/859" {
		t.Fatalf("merged = %v url = %q, want merged through the PR chain", resp.Merged, resp.PRURL)
	}
	if resp.Status != "done" {
		t.Fatalf("status = %q, want done", resp.Status)
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("db status = %s, want done", got)
	}
	var content string
	if err := testPool.QueryRow(ctx, `SELECT content FROM comment WHERE id = $1`, resp.Comment.ID).Scan(&content); err != nil {
		t.Fatalf("read comment: %v", err)
	}
	if !strings.Contains(content, "\nverdict: pass") {
		t.Fatalf("verdict line missing from the comment: %q", content)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionDelivered {
		t.Fatalf("close.conclusion = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyStatus); got != "done" {
		t.Fatalf("close.status = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyEvidenceCommentID); got != resp.Comment.ID {
		t.Fatalf("close.evidence_comment_id = %q", got)
	}
}

// A pass whose merge still fails after the retry keeps the ticket in review
// (never blocked) and the response tells the seat how to finish (DENE-1219).
func TestCloseVerdictPassMergeFailureStaysInReview(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "close verdict merge fail", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	seedOpenPullForIssue(t, issue.ID, 999860)
	calls := 0
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{err: errPullNotMergeable, calls: &calls}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "可以合。", "verdict": "pass"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Merged || resp.Status != "in_review" {
		t.Fatalf("merged = %v status = %q, want unmerged + in_review", resp.Merged, resp.Status)
	}
	if calls != 2 {
		t.Fatalf("merge attempts = %d, want one retry", calls)
	}
	if resp.Hold == nil || resp.Hold.Kind != blockwait.HoldMergeFailed || !strings.Contains(resp.Hold.Next, "gh pr merge --squash") {
		t.Fatalf("hold = %+v, want merge_failed with the local merge hint", resp.Hold)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_review" {
		t.Fatalf("db status = %s, want in_review", got)
	}
	if got := issueMetaString(t, issue.ID, blockwait.KeyReleased); got != "" {
		t.Fatalf("block.released = %q, want cleared so the next pass retries", got)
	}
	if got := issueMetaString(t, issue.ID, blockwait.KeyWakeAt); got != "" {
		t.Fatalf("block.wake_at = %q, want no clock", got)
	}
}

// A pass whose PR checks are still running is answered before the pass is
// posted: 409 with the hold code, no comment, no status (DENE-1219).
func TestCloseVerdictPassWithPendingChecksIsAnsweredFirst(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "close verdict pending", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	seedOpenPullForIssue(t, issue.ID, 999864)
	var prID string
	if err := testPool.QueryRow(ctx, `UPDATE github_pull_request SET checks_rollup_state = 'PENDING', snapshot_head_sha = head_sha WHERE workspace_id = $1 AND pr_number = 999864 RETURNING id`, testWorkspaceID).Scan(&prID); err != nil {
		t.Fatalf("set checks: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id = $1`, prID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO github_pull_request_check_run (pr_id, head_sha, ordinal, name, status) VALUES ($1, 'sha999864', 0, 'backend', 'in_progress')`, prID); err != nil {
		t.Fatalf("seed check run: %v", err)
	}
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "可以合。", "verdict": "pass"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"close_checks_pending"`) {
		t.Fatalf("status = %d: %s, want 409 close_checks_pending", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_review" {
		t.Fatalf("db status = %s, want in_review", got)
	}
	var comments int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%verdict: pass%'`, issue.ID).Scan(&comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if comments != 0 {
		t.Fatalf("pass comments = %d, want none before the PR can merge", comments)
	}
}

// seedOpenPullForIssue links one open, clean PR to the issue and returns its
// URL. Cleanup removes both rows.
func seedOpenPullForIssue(t *testing.T, issueID string, number int) string {
	t.Helper()
	ctx := context.Background()
	url := "https://example.test/pr/" + fmtInt(number)
	var prID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO github_pull_request (workspace_id, installation_id, repo_owner, repo_name, pr_number, title, state, html_url, pr_created_at, pr_updated_at, head_sha, mergeable_state)
		VALUES ($1, 1, 'multica-ai', 'multica', $2, 'close PR', 'open', $3, now(), now(), $4, 'clean')
		RETURNING id
	`, testWorkspaceID, number, url, "sha"+fmtInt(number)).Scan(&prID); err != nil {
		t.Fatalf("seed PR: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO issue_pull_request (issue_id, pull_request_id) VALUES ($1, $2)`, issueID, prID); err != nil {
		t.Fatalf("link PR: %v", err)
	}
	return url
}

func fmtInt(n int) string { return fmt.Sprintf("%d", n) }

// An executor's plain `--outcome done` with an open linked PR goes through the
// same gate as `issue status done` (DENE-857): the PR is merged first and the
// response says so.
func TestCloseDoneWithOpenPullMergesFirst(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close done open PR", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	url := seedOpenPullForIssue(t, issue.ID, 999861)
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR " + url + "，本地测试全绿。"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Merged || resp.PRURL != url || resp.Status != "done" {
		t.Fatalf("merged = %v url = %q status = %q, want merged + done", resp.Merged, resp.PRURL, resp.Status)
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("db status = %s, want done", got)
	}
	if !strings.Contains(strings.Join(resp.Woken, "\n"), "已先合并") {
		t.Fatalf("woken should report the merge, got %v", resp.Woken)
	}
}

// The same close when the merge fails: the gate retries once, then refuses
// with the local merge command and writes nothing — no blocked, no clock
// (DENE-1219).
func TestCloseDoneWithUnmergeablePullIsRefusedWithHint(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close done PR merge fails", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	url := seedOpenPullForIssue(t, issue.ID, 999862)
	calls := 0
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{err: errPullNotMergeable, calls: &calls}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR 开着，测试全绿。"})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s, want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"close_merge_failed"`) || !strings.Contains(w.Body.String(), "gh pr merge --squash "+url) {
		t.Fatalf("refusal = %s, want close_merge_failed with the local merge command", w.Body.String())
	}
	if calls != 2 {
		t.Fatalf("merge attempts = %d, want one retry", calls)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("db status = %s, want in_progress untouched", got)
	}
	for _, key := range []string{blockwait.KeyWakeAt, blockwait.KeyWaitCondition, blockwait.KeyWatched, blockwait.KeyNeedsHuman} {
		if got := issueMetaString(t, issue.ID, key); got != "" {
			t.Fatalf("%s = %q, want nothing written", key, got)
		}
	}
}

// A merge that fails once and goes through on the retry closes the ticket.
func TestCloseDoneMergeRetrySucceeds(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close done merge retry", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	seedOpenPullForIssue(t, issue.ID, 999865)
	calls := 0
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{err: errPullNotMergeable, calls: &calls, failFirst: true}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR 开着，测试全绿。"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if calls != 2 || issueStatusDirect(t, issue.ID) != "done" {
		t.Fatalf("calls = %d status = %s, want merged on the retry and done", calls, issueStatusDirect(t, issue.ID))
	}
}

// --needs-human is the one close that still writes blocked (DENE-1219).
func TestCloseBlockedNeedsHumanStillBlocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close needs human", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "blocked", "evidence": "要 Kun 决定用哪个方案。", "needs_human": testUserID})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("db status = %s, want blocked", got)
	}
	if got := issueMetaString(t, issue.ID, blockwait.KeyNeedsHuman); got != testUserID {
		t.Fatalf("block.needs_human = %q", got)
	}
}

// DENE-928/931: a child with an acceptance seat could neither enter in_review
// (children do not) nor close done (the seat was someone else). The child's
// executor now closes done and the parent's seat accepts the tree.
func TestCloseChildWithSeatDoneByExecutor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	parent := createIssueHTTP(t, "seat parent", "in_progress")
	child := createIssueHTTP(t, "seat child", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, child.ID, "running")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET parent_issue_id = $2, reviewer_type = 'member', reviewer_id = $3 WHERE id = $1`, child.ID, parent.ID, testUserID); err != nil {
		t.Fatalf("attach child + seat: %v", err)
	}
	w := closeIssueHTTP(t, child.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "文档已更新。", "no_code_reason": "纯文档"})
	if w.Code != http.StatusOK {
		t.Fatalf("child done by executor: %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, child.ID); got != "done" {
		t.Fatalf("db status = %s, want done", got)
	}

	// A top-level ticket with a seat still goes through the seat.
	top := createIssueHTTP(t, "seat top", "in_progress")
	topTask := insertIssueTaskWithStatus(t, agentID, top.ID, "running")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'member', reviewer_id = $2 WHERE id = $1`, top.ID, testUserID); err != nil {
		t.Fatalf("set seat: %v", err)
	}
	w = closeIssueHTTP(t, top.ID, agentID, topTask, map[string]any{"outcome": "done", "evidence": "做完了", "no_code_reason": "纯文档"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "in_review") {
		t.Fatalf("top-level self-close: %d: %s", w.Code, w.Body.String())
	}
}

// DENE-943: a delivery branch with no visible PR is refused with the delivery
// lookup's reason and next step. An explicit --no-code still closes it; once a
// PR is linked, --no-code cannot skip the merge gate.
func TestCloseDoneWithBranchButNoVisiblePull(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "external MR", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue_delivery_branch (issue_id, workspace_id, branch_name, role, agent_id)
		VALUES ($1, $2, 'dene-943-fg109', 'canonical', $3)`, issue.ID, testWorkspaceID, agentID); err != nil {
		t.Fatalf("seed delivery branch: %v", err)
	}

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "MR 已合"})
	body := w.Body.String()
	if w.Code != http.StatusConflict || !strings.Contains(body, "这个仓库还没接上") || !strings.Contains(body, "下一步：") || !strings.Contains(body, "保存令牌") || !strings.Contains(body, "--pr") {
		t.Fatalf("not connected: %d: %s", w.Code, body)
	}
	w = closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "MR 已合", "no_code_reason": "GitLab MR !200 已合 dev"})
	if w.Code != http.StatusOK {
		t.Fatalf("with MR reason: %d: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("db status = %s, want done", got)
	}

	linked := createIssueHTTP(t, "linked PR", "in_progress")
	linkedTask := insertIssueTaskWithStatus(t, agentID, linked.ID, "running")
	seedOpenPullForIssue(t, linked.ID, 999943)
	w = closeIssueHTTP(t, linked.ID, agentID, linkedTask, map[string]any{"outcome": "done", "evidence": "x", "no_code_reason": "想跳过"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "--no-code") {
		t.Fatalf("no-code over a linked PR: %d: %s", w.Code, w.Body.String())
	}
}

func closeError(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error: %v (%s)", err, w.Body.String())
	}
	return payload.Error
}

func assertCloseRejectedClean(t *testing.T, issueID, prevStatus string) {
	t.Helper()
	if got := issueStatusDirect(t, issueID); got != prevStatus {
		t.Fatalf("status changed on a rejected close: %s", got)
	}
	if got := issueMetaString(t, issueID, closeprotocol.KeyConclusion); got != "" {
		t.Fatalf("close record written on a rejected close: %s", got)
	}
	if got := issueMetaString(t, issueID, closeprotocol.KeyKnowledgeAudit); got != "" {
		t.Fatalf("knowledge audit written on a rejected close: %s", got)
	}
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM comment WHERE issue_id = $1`, issueID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Fatalf("comment written on a rejected close: %d", n)
	}
}

func TestCloseWithoutKnowledgeAuditWritesNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close missing audit", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":              "done",
		"evidence":             "文档已更新。",
		"no_code_reason":       "纯文档",
		"omit_knowledge_audit": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if got := closeError(t, w); got != closeprotocol.KnowledgeAuditRequiredMsg {
		t.Fatalf("rejection = %s", got)
	}
	assertCloseRejectedClean(t, issue.ID, "in_progress")
}

func TestCloseKnowledgeNoneSucceeds(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close knowledge none", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":         "done",
		"evidence":        "这次没有够格的项目记忆。",
		"no_code_reason":  "纯文档",
		"knowledge_audit": map[string]any{"none": true},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.KnowledgeAudit == nil || !resp.KnowledgeAudit.None {
		t.Fatalf("knowledge_audit = %#v", resp.KnowledgeAudit)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyKnowledgeAudit); got != `{"none":true}` {
		t.Fatalf("close.knowledge_audit = %q", got)
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("status = %s", got)
	}
}

func TestCloseKnowledgeUnknownLocationWritesNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close bad location", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":        "done",
		"evidence":       "写到了清单外。",
		"no_code_reason": "纯文档",
		"knowledge_audit": map[string]any{
			"changes": []any{map[string]any{"location": "DESIGN.md", "summary": "不在清单里"}},
		},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不在项目记忆清单里") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	assertCloseRejectedClean(t, issue.ID, "in_progress")
}

func TestCloseKnowledgeMixedFormWritesNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close mixed audit", "in_progress")
	w := closeIssueHTTP(t, issue.ID, "", "", map[string]any{
		"outcome":  "cancelled",
		"evidence": "不要了。",
		"knowledge_audit": map[string]any{
			"none":    true,
			"changes": []any{map[string]any{"location": "context", "summary": "又写了一条"}},
		},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "不能同时") {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	assertCloseRejectedClean(t, issue.ID, "in_progress")
}

func TestCloseKnowledgeChangeIsStored(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close knowledge change", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":        "done",
		"evidence":       "补了开张种子。",
		"no_code_reason": "纯文档",
		"knowledge_audit": map[string]any{
			"changes": []any{map[string]any{"location": " agents ", "summary": " 补了开张种子 "}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	want := `{"changes":[{"location":"agents","summary":"补了开张种子"}]}`
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyKnowledgeAudit); got != want {
		t.Fatalf("close.knowledge_audit = %q", got)
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.KnowledgeAudit == nil || len(resp.KnowledgeAudit.Changes) != 1 || resp.KnowledgeAudit.Changes[0].Location != "agents" {
		t.Fatalf("knowledge_audit = %#v", resp.KnowledgeAudit)
	}
	if resp.Close[closeprotocol.KeyKnowledgeAudit] != want {
		t.Fatalf("close map audit = %q", resp.Close[closeprotocol.KeyKnowledgeAudit])
	}
}

func TestCloseVerdictWithoutKnowledgeAuditWritesNoComment(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "verdict missing audit", "in_review")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":              "done",
		"evidence":             "验收通过。",
		"verdict":              "pass",
		"omit_knowledge_audit": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := closeError(t, w); got != closeprotocol.KnowledgeAuditRequiredMsg {
		t.Fatalf("rejection = %s", got)
	}
	assertCloseRejectedClean(t, issue.ID, "in_review")
}

// DENE-1002 (2B): `--outcome backlog` and `--outcome todo` put the ticket back
// on purpose. Each one writes the evidence comment and a `deferred` close
// record, needs no PR, and wakes nobody.
func TestCloseBacklogAndTodoReturnWithACloseRecord(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	for _, outcome := range []string{"backlog", "todo"} {
		t.Run(outcome, func(t *testing.T) {
			issue := createIssueHTTP(t, "close "+outcome, "in_progress")
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome":  outcome,
				"evidence": "这轮先不动，需求还没定；等排期再拉起来。",
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			var resp CloseIssueResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Status != outcome {
				t.Fatalf("status = %s, want %s", resp.Status, outcome)
			}
			if got := issueStatusDirect(t, issue.ID); got != outcome {
				t.Fatalf("issue status = %s, want %s", got, outcome)
			}
			if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionDeferred {
				t.Fatalf("close.conclusion = %q, want deferred", got)
			}
			if got := issueMetaString(t, issue.ID, closeprotocol.KeyStatus); got != outcome {
				t.Fatalf("close.status = %q, want %s", got, outcome)
			}
			if got := issueMetaString(t, issue.ID, closeprotocol.KeyEvidenceCommentID); got == "" {
				t.Fatal("the evidence comment must be recorded")
			}
			if got := issueMetaString(t, issue.ID, closeprotocol.KeyWakeAction); got != closeprotocol.WakeNone {
				t.Fatalf("close.wake_action = %q, want none", got)
			}
			var n int
			if err := testPool.QueryRow(context.Background(),
				`SELECT count(*) FROM comment WHERE issue_id = $1`, issue.ID).Scan(&n); err != nil {
				t.Fatalf("count comments: %v", err)
			}
			if n != 1 {
				t.Fatalf("comments = %d, want the one evidence comment", n)
			}
			woken := strings.Join(resp.Woken, "\n")
			if !strings.Contains(woken, "放回") {
				t.Fatalf("the reply should explain the deliberate return, got %v", resp.Woken)
			}
		})
	}
}

// DENE-1002: `--outcome in_progress` keeps the ticket in progress, records
// who continues, and arms the block-wait patrol on the clock.
func TestCloseInProgressRecordsWhoContinues(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "close in_progress clock", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	if _, err := testPool.Exec(ctx,
		`UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("assign issue: %v", err)
	}
	wakeAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "in_progress",
		"evidence": "网关改造做到一半，先停；等扩容窗口到点继续。",
		"wake_at":  wakeAt,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "in_progress" {
		t.Fatalf("status = %s, want in_progress", resp.Status)
	}
	if resp.StatusChanged {
		t.Fatal("the ticket was already in_progress; no status change is expected")
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("issue status = %s", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionContinuing {
		t.Fatalf("close.conclusion = %q, want continuing", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyWakeAction); got != closeprotocol.WakeClock {
		t.Fatalf("close.wake_action = %q, want clock", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyNextOwnerType); got != closeprotocol.OwnerAgent {
		t.Fatalf("close.next_owner_type = %q, want agent", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyNextOwnerID); got != agentID {
		t.Fatalf("close.next_owner_id = %q, want %q", got, agentID)
	}
	if got := issueMetaString(t, issue.ID, "block.wake_at"); got == "" {
		t.Fatal("the clock must be written to the block-wait record")
	}
	if got := issueMetaString(t, issue.ID, "block.watched"); got != "1" {
		t.Fatalf("block.watched = %q, want 1 so the patrol wakes it", got)
	}
	if woken := strings.Join(resp.Woken, "\n"); !strings.Contains(woken, "叫醒") {
		t.Fatalf("the reply should say who is woken when, got %v", resp.Woken)
	}
}

// DENE-1002: an in_progress close with no continuation is refused with the
// list of ways to say "who continues", and nothing is written.
func TestCloseInProgressWithoutContinuationIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close in_progress bare", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":  "in_progress",
		"evidence": "先停一下，回头再说。",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"--wake-at", "--wait-condition", "--blocked-by", "--needs-human"} {
		if !strings.Contains(body, want) {
			t.Fatalf("rejection should list %s, got %s", want, body)
		}
	}
	assertCloseRejectedClean(t, issue.ID, "in_progress")
}

// DENE-1002: --needs-human on an in_progress close names the person who
// continues instead of the assignee.
func TestCloseInProgressNeedsHumanNamesThePerson(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close in_progress human", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	memberID := testUserID

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":     "in_progress",
		"evidence":    "方案要你拍板后再继续。",
		"needs_human": memberID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyNextOwnerType); got != closeprotocol.OwnerMember {
		t.Fatalf("close.next_owner_type = %q, want member", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyNextOwnerID); got != memberID {
		t.Fatalf("close.next_owner_id = %q, want %q", got, memberID)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s, want in_progress", got)
	}
}

// A local CLI report is the fallback when the server cannot read a GitLab
// token. It must replace a stale open snapshot with the merged state before
// the done gate evaluates it.
func TestCloseDoneAcceptsMergedGitLabMRFromLocalCLIReport(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	box := withVCSBox(t)
	connID := seedVCSConnection(t, ctx, box, "gitlab", "http://gitlab.example")
	localToken, err := box.Seal([]byte("local"))
	if err != nil {
		t.Fatalf("seal local token: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE vcs_connection SET access_token_encrypted = $2 WHERE id = $1`, connID, base64.StdEncoding.EncodeToString(localToken)); err != nil {
		t.Fatalf("set local token: %v", err)
	}
	issue := newVCSIssue(t, "GitLab local CLI close")
	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	mrURL := "http://gitlab.example/acme/game/-/merge_requests/490"
	pr, err := testHandler.Queries.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID: parseUUID(testWorkspaceID), ConnectionID: parseUUID(connID),
		Provider: "gitlab", RepoOwner: "acme", RepoName: "game", PrNumber: 490,
		Title: "Fix " + issue.Identifier, State: "open", HtmlUrl: mrURL,
		PrCreatedAt: now, PrUpdatedAt: now, HeadSha: "oldsha",
	})
	if err != nil {
		t.Fatalf("seed stale MR: %v", err)
	}
	if _, err := testHandler.Queries.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
		IssueID: parseUUID(issue.ID), PullRequestID: pr.ID,
	}); err != nil {
		t.Fatalf("link stale MR: %v", err)
	}
	t.Cleanup(func() { cleanupVCS(ctx, issue.ID) })

	mergedAt := time.Now().UTC()
	reportIssuePRsHTTP(t, issue.ID, []DaemonPullRequest{{
		Provider: "gitlab", Owner: "acme", Repo: "game", Number: 490,
		Title: "Fix " + issue.Identifier, State: "merged", URL: mrURL,
		SHA: "27b88db00", MergedAt: &mergedAt,
	}})
	w := closeIssueHTTP(t, issue.ID, "", "", map[string]any{
		"outcome": "done", "evidence": "MR 已合并：" + mrURL,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "done" {
		t.Fatalf("status = %s, want done", got)
	}
}

// A PR link found only in the timeline is a hint, not a declaration. A related
// open MR cited in review must not be linked to this issue, because a linked
// open PR is what the done gate goes on to merge.
func TestCloseDoneIgnoresCitedPullThatDoesNotNameTheIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"iid":491,"title":"Unrelated refactor","state":"opened","web_url":%q,"sha":"abc","source_branch":"agent/other"}`, api.URL+"/acme/game/-/merge_requests/491")
	}))
	defer api.Close()
	box := withVCSBox(t)
	prevHTTP := testHandler.deliveryHTTP
	testHandler.deliveryHTTP = api.Client()
	t.Cleanup(func() { testHandler.deliveryHTTP = prevHTTP })
	seedVCSConnection(t, ctx, box, "gitlab", api.URL)
	issue := newVCSIssue(t, "close citing another MR")
	t.Cleanup(func() { cleanupVCS(ctx, issue.ID) })

	closeIssueHTTP(t, issue.ID, "", "", map[string]any{
		"outcome":  "done",
		"evidence": "做完了，思路参考 " + api.URL + "/acme/game/-/merge_requests/491",
	})
	var linked int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue_vcs_pull_request WHERE issue_id = $1`, issue.ID).Scan(&linked); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if linked != 0 {
		t.Fatalf("cited MR was linked to the issue (%d rows)", linked)
	}
}

func TestPullNamesIssueMatchesWholeIdentifier(t *testing.T) {
	for _, tc := range []struct {
		title, branch string
		want          bool
	}{
		{"DENE-1156: fix close", "", true},
		{"", "agent/dene-1156", true},
		{"DENE-11560 other", "", false},
		{"fix", "agent/xdene-1156", false},
		{"unrelated", "agent/other", false},
	} {
		if got := pullNamesIssue(delivery.Pull{Title: tc.title, Branch: tc.branch}, "DENE-1156"); got != tc.want {
			t.Errorf("pullNamesIssue(%q,%q) = %v, want %v", tc.title, tc.branch, got, tc.want)
		}
	}
	if pullNamesIssue(delivery.Pull{Title: "DENE-1156"}, "DENE-11") {
		t.Error("DENE-11 must not match DENE-1156")
	}
}

// DENE-1183: /close/check is the close endpoint's shape gate with no writes.
// Every refusal it gives is the one /close gives for the same body, so the
// CLI can relay it before a local merge.
func TestCloseCheckAgreesWithCloseAndWritesNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close check parity", "in_progress")
	check := func(body map[string]any) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/close/check", body), "id", issue.ID)
		testHandler.CheckCloseIssue(w, req)
		return w
	}
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"missing outcome", map[string]any{"evidence": "x"}},
		{"unknown outcome", map[string]any{"outcome": "finished", "evidence": "x"}},
		{"missing evidence", map[string]any{"outcome": "done"}},
		{"verdict hold", map[string]any{"outcome": "done", "evidence": "x", "verdict": "hold"}},
		{"in_progress without continuation", map[string]any{"outcome": "in_progress", "evidence": "x"}},
		{"missing audit", map[string]any{"outcome": "cancelled", "evidence": "x", "omit_knowledge_audit": true}},
	} {
		closeBody := map[string]any{}
		checkBody := map[string]any{}
		for k, v := range tc.body {
			closeBody[k] = v
			if k != "omit_knowledge_audit" {
				checkBody[k] = v
			}
		}
		if _, omit := tc.body["omit_knowledge_audit"]; !omit {
			checkBody["knowledge_audit"] = map[string]any{"none": true}
		}
		got := check(checkBody)
		want := closeIssueHTTP(t, issue.ID, "", "", closeBody)
		if got.Code != http.StatusBadRequest || want.Code != http.StatusBadRequest {
			t.Fatalf("%s: check=%d close=%d, want both 400: %s / %s", tc.name, got.Code, want.Code, got.Body.String(), want.Body.String())
		}
		if got.Body.String() != want.Body.String() {
			t.Fatalf("%s: check says %s, close says %s", tc.name, got.Body.String(), want.Body.String())
		}
	}
	ok := check(map[string]any{"outcome": "done", "evidence": "PR #1", "knowledge_audit": map[string]any{"none": true}})
	if ok.Code != http.StatusOK {
		t.Fatalf("acceptable shape: %d %s", ok.Code, ok.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("check changed status to %s", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != "" {
		t.Fatalf("check wrote a close record: %s", got)
	}
}

// DENE-1183: the delivery lookup uses the same whole-identifier rule as an
// inferred link. A search for DENE-11 also returns DENE-110's MR (substring
// match on the provider side); it must not be linked to DENE-11, because a
// linked open PR is what the done gate merges.
func TestDeliveryLookupLinksOnlyPullsNamingTheWholeIdentifier(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := newVCSIssue(t, "lookup whole identifier")
	t.Cleanup(func() { cleanupVCS(ctx, issue.ID) })
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[{"iid":71,"title":"Fix %s0 elsewhere","state":"opened","web_url":%q,"sha":"a","source_branch":"agent/other"},`+
			`{"iid":72,"title":"Fix %s","state":"opened","web_url":%q,"sha":"b","source_branch":"agent/mine"}]`,
			issue.Identifier, api.URL+"/acme/game/-/merge_requests/71", issue.Identifier, api.URL+"/acme/game/-/merge_requests/72")
	}))
	defer api.Close()
	box := withVCSBox(t)
	prevHTTP := testHandler.deliveryHTTP
	testHandler.deliveryHTTP = api.Client()
	t.Cleanup(func() { testHandler.deliveryHTTP = prevHTTP })
	seedVCSConnection(t, ctx, box, "gitlab", api.URL)
	setHandlerTestWorkspaceRepos(t, []map[string]string{{"url": api.URL + "/acme/game"}})

	dbIssue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issue.ID))
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	view, err := testHandler.ensureIssueDeliveries(ctx, dbIssue)
	if err != nil {
		t.Fatalf("ensureIssueDeliveries: %v", err)
	}
	var urls []string
	for _, row := range view.VCS {
		urls = append(urls, row.HtmlUrl)
	}
	if len(urls) != 1 || !strings.HasSuffix(urls[0], "/merge_requests/72") {
		t.Fatalf("linked = %v, want only MR 72 (MR 71 names %s0)", urls, issue.Identifier)
	}
}
