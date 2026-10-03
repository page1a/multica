package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDismissInboxClearsOnlyTheOriginatorWaitingReminder(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	fixture := newBoardAgentFixture(t, "direct_human")
	issue := createIssueHTTP(t, "dismiss stale waiting", "in_progress")
	boardCleanup(t, issue.ID)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET visibility = 'workspace' WHERE id = $1`, issue.ID); err != nil {
		t.Fatalf("make issue visible: %v", err)
	}
	var beforeStatus, beforeAssigneeType, beforeAssigneeID string
	if err := testPool.QueryRow(ctx, `
		SELECT status, COALESCE(assignee_type, ''), COALESCE(assignee_id::text, '')
		FROM issue WHERE id = $1`, issue.ID).Scan(&beforeStatus, &beforeAssigneeType, &beforeAssigneeID); err != nil {
		t.Fatalf("read issue before: %v", err)
	}
	var summonID, inboxID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, body, details)
		VALUES ($1, 'member', $2, 'needs_you', 'action_required', $3, 'stale waiting', 'already decided', '{}'::jsonb)
		RETURNING id`, testWorkspaceID, fixture.originator, issue.ID).Scan(&inboxID); err != nil {
		t.Fatalf("insert inbox row: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue_summon (workspace_id, issue_id, recipient_id, caller_type, caller_id, source, reason, inbox_item_id)
		VALUES ($1, $2, $3, 'agent', $4, 'manual', 'already decided', $5)
		RETURNING id`, testWorkspaceID, issue.ID, fixture.originator, fixture.agent, inboxID).Scan(&summonID); err != nil {
		t.Fatalf("insert summon: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM inbox_item WHERE id = $1`, inboxID)
		testPool.Exec(ctx, `DELETE FROM issue_summon WHERE id = $1`, summonID)
	})

	beforeTasks := countActiveTasksForIssue(t, issue.ID)
	req := boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/issues/"+issue.ID+"/dismiss")
	req = withURLParam(req, "issueId", issue.ID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", fixture.agent)
	req.Header.Set("X-Task-ID", fixture.task)
	req.Body = newRequestAs(testUserID, http.MethodPost, "/api/inbox/issues/"+issue.ID+"/dismiss", map[string]any{"reason": "五种语言已经定下并已合入代码"}).Body
	w := httptest.NewRecorder()
	testHandler.DismissInbox(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss: %d %s", w.Code, w.Body.String())
	}
	var out DismissInboxResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode dismiss: %v", err)
	}
	if out.SummonID != summonID || out.InboxItemID != inboxID || out.Reason == "" {
		t.Fatalf("dismiss response: %+v", out)
	}
	var answered, archived bool
	if err := testPool.QueryRow(ctx, `
		SELECT s.answered_at IS NOT NULL, i.archived
		FROM issue_summon s JOIN inbox_item i ON i.id = s.inbox_item_id
		WHERE s.id = $1`, summonID).Scan(&answered, &archived); err != nil {
		t.Fatalf("read dismissed rows: %v", err)
	}
	if !answered || !archived {
		t.Fatalf("dismissed rows answered=%v archived=%v, want true/true", answered, archived)
	}
	var comment string
	if err := testPool.QueryRow(ctx, `SELECT content FROM comment WHERE id = $1`, out.CommentID).Scan(&comment); err != nil {
		t.Fatalf("read audit comment: %v", err)
	}
	if !strings.Contains(comment, "五种语言已经定下并已合入代码") || !strings.Contains(comment, "智能体") {
		t.Fatalf("audit comment = %q", comment)
	}
	var afterStatus, afterAssigneeType, afterAssigneeID string
	if err := testPool.QueryRow(ctx, `
		SELECT status, COALESCE(assignee_type, ''), COALESCE(assignee_id::text, '')
		FROM issue WHERE id = $1`, issue.ID).Scan(&afterStatus, &afterAssigneeType, &afterAssigneeID); err != nil {
		t.Fatalf("read issue after: %v", err)
	}
	if afterStatus != beforeStatus || afterAssigneeType != beforeAssigneeType || afterAssigneeID != beforeAssigneeID {
		t.Fatalf("issue changed: before=%s/%s/%s after=%s/%s/%s", beforeStatus, beforeAssigneeType, beforeAssigneeID, afterStatus, afterAssigneeType, afterAssigneeID)
	}
	if got := countActiveTasksForIssue(t, issue.ID); got != beforeTasks {
		t.Fatalf("active tasks after dismiss = %d, want %d", got, beforeTasks)
	}
	waiting := waitingSummons(t, fixture.originator)
	for _, row := range waiting {
		if row.IssueID == issue.ID {
			t.Fatalf("dismissed summon still waiting: %+v", row)
		}
	}
}

func TestDismissInboxRejectsAutomationRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fixture := newBoardAgentFixture(t, "trigger_owner")
	issue := createIssueHTTP(t, "dismiss automation", "in_progress")
	boardCleanup(t, issue.ID)
	req := boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/issues/"+issue.ID+"/dismiss")
	req = withURLParam(req, "issueId", issue.ID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", fixture.agent)
	req.Header.Set("X-Task-ID", fixture.task)
	req.Body = newRequestAs(testUserID, http.MethodPost, "/api/inbox/issues/"+issue.ID+"/dismiss", map[string]any{"reason": "过期"}).Body
	w := httptest.NewRecorder()
	testHandler.DismissInbox(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("automation dismiss: %d %s, want 403", w.Code, w.Body.String())
	}
}

func countActiveTasksForIssue(t *testing.T, issueID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM agent_task_queue
		WHERE issue_id = $1 AND status IN ('queued', 'dispatched', 'running')`, issueID).Scan(&count); err != nil {
		t.Fatalf("count active tasks: %v", err)
	}
	return count
}
