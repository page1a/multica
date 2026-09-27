package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

// closeIssueHTTP posts to /api/issues/{id}/close as the given agent task (or
// as the test member when agentID is empty) and returns the recorder.
func closeIssueHTTP(t *testing.T, issueID, agentID, taskID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
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

// A pass whose merge fails ends blocked, and the response says so instead
// of claiming done.
func TestCloseVerdictPassMergeFailureReportsBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "close verdict merge fail", "in_review")
	agentID := handlerTestAgentID(t)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, agentID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
	var prID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO github_pull_request (workspace_id, installation_id, repo_owner, repo_name, pr_number, title, state, html_url, pr_created_at, pr_updated_at, head_sha, mergeable_state)
		VALUES ($1, 1, 'multica-ai', 'multica', 999860, 'close PR', 'open', 'https://example.test/pr/860', now(), now(), 'abc860', 'clean')
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
	testHandler.PRMerger = fakeMerger{err: errPullNotMergeable}
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
	if resp.Merged || resp.Status != "blocked" {
		t.Fatalf("merged = %v status = %q, want unmerged + blocked", resp.Merged, resp.Status)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionBlocked {
		t.Fatalf("close.conclusion = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyBlockKind); got != closeprotocol.BlockExternal {
		t.Fatalf("close.block_kind = %q", got)
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

// The same close when the merge fails: the caller asked for done, the gate
// rewrote it to a structured block, and the close record follows the status
// actually written — the response carries the warning instead of a false done.
func TestCloseDoneWithUnmergeablePullIsRewrittenToBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "close done PR merge fails", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	seedOpenPullForIssue(t, issue.ID, 999862)
	prev := testHandler.PRMerger
	testHandler.PRMerger = fakeMerger{err: errPullNotMergeable}
	t.Cleanup(func() { testHandler.PRMerger = prev })

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{"outcome": "done", "evidence": "PR 开着，测试全绿。"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp CloseIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Merged || resp.Status != "blocked" || resp.PrevStatus != "in_progress" {
		t.Fatalf("merged = %v status = %q prev = %q, want unmerged + blocked", resp.Merged, resp.Status, resp.PrevStatus)
	}
	if len(resp.Warnings) == 0 || !strings.Contains(resp.Warnings[0], "实际落的是 blocked") {
		t.Fatalf("warnings should name the rewrite, got %v", resp.Warnings)
	}
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("db status = %s, want blocked", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyConclusion); got != closeprotocol.ConclusionBlocked {
		t.Fatalf("close.conclusion = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyStatus); got != "blocked" {
		t.Fatalf("close.status = %q", got)
	}
	if got := issueMetaString(t, issue.ID, closeprotocol.KeyBlockKind); got != closeprotocol.BlockExternal {
		t.Fatalf("close.block_kind = %q", got)
	}
	if got := issueMetaString(t, issue.ID, "block.wait_condition"); got == "" {
		t.Fatalf("block.wait_condition should carry the merge failure")
	}
}
