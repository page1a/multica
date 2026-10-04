package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DENE-1301: three false alarms on the sub-issue blocker card, fixed where the
// data is written.

func issueMetaValue(t *testing.T, issueID, key string) (string, bool) {
	t.Helper()
	var v *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>$2 FROM issue WHERE id = $1`, issueID, key).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	if v == nil {
		return "", false
	}
	return *v, true
}

// A member's reply that wakes the executor puts the ticket back to work in
// the same step: status in_progress, the wait dropped, and the blocked close
// marked superseded rather than replaced.
func TestMemberReplyThatWakesExecutorResumesBlockedTicket(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "resume on reply", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, issue.ID)
	})

	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":     "blocked",
		"evidence":    "两个方案都能做，等你挑一个。",
		"summary":     "要人拍板：A 还是 B",
		"needs_human": testUserID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("close: %d %s", w.Code, w.Body.String())
	}
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status after close = %s, want blocked", got)
	}
	closeAt, _ := issueMetaValue(t, issue.ID, "close.at")
	if closeAt == "" {
		t.Fatal("close did not record close.at")
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID); err != nil {
		t.Fatalf("finish task: %v", err)
	}

	postMemberComment(t, testUserID, issue.ID, "选 A。")

	if got := summonCount(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued','dispatched','running')`, issue.ID, agentID); got != 1 {
		t.Fatalf("executor runs = %d, want 1", got)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status after reply = %s, want in_progress", got)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.superseded"); got != closeAt {
		t.Fatalf("close.superseded = %q, want close.at %q", got, closeAt)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.conclusion"); got != "blocked" {
		t.Fatalf("close.conclusion = %q, the record must not be forged", got)
	}
	for _, key := range []string{"block.needs_human", "close.block_kind", "close.block_action"} {
		if v, ok := issueMetaValue(t, issue.ID, key); ok && v != "" {
			t.Fatalf("%s survived the resume: %q", key, v)
		}
	}
}

// A comment that starts no run is a remark, not an answer: the ticket stays
// blocked.
func TestMemberCommentWithoutRunKeepsTicketBlocked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "casual comment", "blocked")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	peer := summonSecondMember(t)

	postMemberComment(t, testUserID, issue.ID, fmt.Sprintf("[@Peer](mention://member/%s) 看一眼", peer))

	if got := summonCount(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status IN ('queued','dispatched','running')`, issue.ID); got != 0 {
		t.Fatalf("casual comment started %d runs; the test needs a comment that starts none", got)
	}
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
	if v, ok := issueMetaValue(t, issue.ID, "close.superseded"); ok && v != "" {
		t.Fatalf("close.superseded = %q, nothing was answered", v)
	}
}

func agentBlockRequest(t *testing.T, issueID, agentID, taskID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, body)
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.UpdateIssue(w, req)
	return w
}

// An agent that blocks through the status path names what kind of block it is
// and the one next step, same as the close path records.
func TestAgentStatusBlockedNeedsKindAndAction(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "block attribution", "in_progress")
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	w := agentBlockRequest(t, issue.ID, agentID, taskID, map[string]any{
		"status":     "blocked",
		"blocked_by": "DENE-806",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing kind: status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "--block-kind") || !strings.Contains(w.Body.String(), "multica issue close --outcome blocked") {
		t.Fatalf("rejection should name the flags and the close command, got %s", w.Body.String())
	}

	w = agentBlockRequest(t, issue.ID, agentID, taskID, map[string]any{
		"status":       "blocked",
		"blocked_by":   "DENE-806",
		"block_kind":   "nonsense",
		"block_action": "等 DENE-806 修好",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: status = %d, want 400: %s", w.Code, w.Body.String())
	}

	w = agentBlockRequest(t, issue.ID, agentID, taskID, map[string]any{
		"status":       "blocked",
		"blocked_by":   "DENE-806",
		"block_kind":   "dependency",
		"block_action": "等 DENE-806 修好",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("with kind: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_kind"); got != "dependency" {
		t.Fatalf("close.block_kind = %q", got)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_action"); got != "等 DENE-806 修好" {
		t.Fatalf("close.block_action = %q", got)
	}
}

// Members dragging a card are not asked for anything.
func TestMemberStatusBlockedStaysUnrestricted(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "member drag", "in_progress")
	updateIssueStatusHTTP(t, issue.ID, "blocked")
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
}

func TestBatchAgentBlockedNeedsKindAndAction(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "batch block attribution", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	batch := func(updates map[string]any) string {
		req := newRequest("POST", "/api/issues/batch-update", map[string]any{
			"issue_ids": []string{issue.ID},
			"updates":   updates,
		})
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		w := httptest.NewRecorder()
		testHandler.BatchUpdateIssues(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("batch: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	body := batch(map[string]any{"status": "blocked", "blocked_by": "DENE-806"})
	if !strings.Contains(body, "--block-kind") {
		t.Fatalf("batch without kind should be rejected with guidance, got %s", body)
	}
	if got := issueStatusDirect(t, issue.ID); got != "in_progress" {
		t.Fatalf("status = %s after rejection", got)
	}

	batch(map[string]any{"status": "blocked", "blocked_by": "DENE-806", "block_kind": "external", "block_action": "等供应商回信"})
	if got := issueStatusDirect(t, issue.ID); got != "blocked" {
		t.Fatalf("status = %s, want blocked", got)
	}
	if got, _ := issueMetaValue(t, issue.ID, "close.block_kind"); got != "external" {
		t.Fatalf("close.block_kind = %q", got)
	}
}

// A cross-family wake consumes the waiter's close.waiting_on so the card no
// longer reports "finished, not woken".
func TestWaitingOnWakeConsumesThePointer(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_progress", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if v, ok := issueMetaValue(t, fx.waiter.ID, "close.waiting_on"); !ok || v != "" {
		t.Fatalf("close.waiting_on = %q (present %v), want present and empty", v, ok)
	}
	if got, _ := issueMetaValue(t, fx.waiter.ID, "block.woken_by"); !strings.Contains(got, fx.waited.Identifier) {
		t.Fatalf("block.woken_by = %q, want it to name %s", got, fx.waited.Identifier)
	}
}

// The stage barrier wakes a parent that also wrote close.waiting_on to its
// child; the pointer is consumed there too (the DENE-1232 shape).
func TestChildDoneConsumesParentWaitingOn(t *testing.T) {
	fx := newChildDoneFixture(t, "in_progress")
	setCloseWaitingOn(t, fx.parent.ID, fx.child.Identifier)

	updateChildStatus(t, fx.child.ID, "done")

	if v, ok := issueMetaValue(t, fx.parent.ID, "close.waiting_on"); !ok || v != "" {
		t.Fatalf("parent close.waiting_on = %q (present %v), want present and empty", v, ok)
	}
}
