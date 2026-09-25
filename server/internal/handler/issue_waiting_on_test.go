package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// waitingOnFixture is two independent issues: `waited` is the upstream ticket,
// `waiter` has close.waiting_on pointing at it. They do not share a parent —
// that is the DENE-209 / DENE-196 shape the stage barrier cannot see.
type waitingOnFixture struct {
	waited  IssueResponse
	waiter  IssueResponse
	agentID string
}

func createIssueHTTP(t *testing.T, title, status string) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  title + " " + time.Now().Format(time.RFC3339Nano),
		"status": status,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func setCloseWaitingOn(t *testing.T, issueID, value string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		t.Fatalf("marshal waiting_on: %v", err)
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/metadata/close.waiting_on", json.RawMessage(body))
	req = withURLParams(req, "id", issueID, "key", "close.waiting_on")
	testHandler.SetIssueMetadataKey(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set close.waiting_on: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func handlerTestAgentID(t *testing.T) string {
	t.Helper()
	var agentID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM agent WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, "Handler Test Agent",
	).Scan(&agentID); err != nil {
		t.Fatalf("locate test agent: %v", err)
	}
	return agentID
}

func newWaitingOnFixture(t *testing.T, waiterStatus, waitingOnValue string) waitingOnFixture {
	t.Helper()
	waited := createIssueHTTP(t, "waiting-on upstream", "in_progress")
	waiter := createIssueHTTP(t, "waiting-on waiter", waiterStatus)
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, waiter.ID, "agent", agentID)
	value := waitingOnValue
	if value == "" {
		value = waited.Identifier
	}
	setCloseWaitingOn(t, waiter.ID, value)
	return waitingOnFixture{waited: waited, waiter: waiter, agentID: agentID}
}

func updateIssueStatusHTTP(t *testing.T, issueID, status string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue status=%q: expected 200, got %d: %s", status, w.Code, w.Body.String())
	}
}

func assertWaiterWoken(t *testing.T, fx waitingOnFixture) {
	t.Helper()
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 1 {
		t.Fatalf("expected 1 system comment on waiter, got %d", got)
	}
	content, _, _, _ := systemCommentOn(t, fx.waiter.ID)
	if !strings.Contains(content, fx.waited.Identifier) {
		t.Errorf("waiter comment missing waited-on identifier %q, got: %s", fx.waited.Identifier, content)
	}
	if !strings.Contains(content, "mention://issue/"+fx.waited.ID) {
		t.Errorf("waiter comment missing mention://issue/%s, got: %s", fx.waited.ID, content)
	}
	if !strings.Contains(content, "mention://agent/"+fx.agentID) {
		t.Errorf("waiter comment missing agent mention, got: %s", content)
	}
	if !strings.Contains(content, "可以继续了") {
		t.Errorf("waiter comment missing resolved marker, got: %s", content)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, fx.agentID); got != 1 {
		t.Fatalf("expected 1 pending task for waiter agent, got %d", got)
	}
}

func TestWaitingOnWakesWaiterWhenDone(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	assertWaiterWoken(t, fx)
}

func TestWaitingOnWakesWaiterWhenCancelled(t *testing.T) {
	fx := newWaitingOnFixture(t, "blocked", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "cancelled")
	assertWaiterWoken(t, fx)
}

func TestWaitingOnMatchesIssueUUID(t *testing.T) {
	waited := createIssueHTTP(t, "waiting-on uuid upstream", "in_progress")
	fx := newWaitingOnFixture(t, "in_review", waited.ID)
	// newWaitingOnFixture created its own waited issue; point this waiter at
	// the UUID of `waited` instead, then finish that one.
	fx.waited = waited
	updateIssueStatusHTTP(t, waited.ID, "done")
	assertWaiterWoken(t, fx)
}

func TestWaitingOnDoesNotWakeWhenWaitedOnStaysOpen(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "in_review")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 0 {
		t.Fatalf("in_review on waited-on must not wake waiter, got %d comments", got)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, fx.agentID); got != 0 {
		t.Fatalf("in_review on waited-on must not enqueue, got %d tasks", got)
	}
}

func TestWaitingOnSkipsEnqueueWhenAgentAlreadyRunning(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_progress", "")
	insertIssueTaskWithStatus(t, fx.agentID, fx.waiter.ID, "running")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 1 {
		t.Fatalf("expected the observable wake comment even when skipping enqueue, got %d", got)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, fx.agentID); got != 1 {
		t.Fatalf("running (issue, agent) must skip enqueue, got %d tasks", got)
	}
}

func TestWaitingOnSkipsEnqueueWhenAgentAlreadyQueued(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_progress", "")
	insertIssueTaskWithStatus(t, fx.agentID, fx.waiter.ID, "queued")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 1 {
		t.Fatalf("expected the observable wake comment even when skipping enqueue, got %d", got)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, fx.agentID); got != 1 {
		t.Fatalf("queued (issue, agent) must skip enqueue, got %d tasks", got)
	}
}

func TestWaitingOnSkipsMemberWaiter(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	var userID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT user_id FROM member WHERE workspace_id = $1 LIMIT 1`,
		testWorkspaceID,
	).Scan(&userID); err != nil {
		t.Fatalf("locate workspace member: %v", err)
	}
	setIssueAssigneeDirect(t, fx.waiter.ID, "member", userID)
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 0 {
		t.Fatalf("member waiter must stay silent, got %d comments", got)
	}
}

func TestWaitingOnSkipsBacklogWaiter(t *testing.T) {
	fx := newWaitingOnFixture(t, "backlog", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 0 {
		t.Fatalf("backlog waiter must stay parked, got %d comments", got)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, fx.agentID); got != 0 {
		t.Fatalf("backlog waiter must not enqueue, got %d tasks", got)
	}
}

func TestWaitingOnSkipsTerminalWaiter(t *testing.T) {
	fx := newWaitingOnFixture(t, "done", "")
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 0 {
		t.Fatalf("already-done waiter must not be woken, got %d comments", got)
	}
}

func TestWaitingOnDoesNotWakeUnrelatedIssue(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	other := createIssueHTTP(t, "unrelated open issue", "in_progress")
	setIssueAssigneeDirect(t, other.ID, "agent", fx.agentID)
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, other.ID); got != 0 {
		t.Fatalf("unrelated issue must not receive waiting_on wake, got %d comments", got)
	}
	assertWaiterWoken(t, fx)
}

func TestWaitingOnDoesNotDoubleWakeParent(t *testing.T) {
	// Same-family wait: parent already gets the stage-barrier comment.
	// close.waiting_on on the parent must not add a second wake.
	parent := createIssueHTTP(t, "waiting-on parent", "in_progress")
	child := createIssueHTTP(t, "waiting-on child", "in_progress")
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET parent_issue_id = $2 WHERE id = $1`,
		child.ID, parent.ID,
	); err != nil {
		t.Fatalf("set parent_issue_id: %v", err)
	}
	setCloseWaitingOn(t, parent.ID, child.Identifier)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, parent.ID)
	})

	updateIssueStatusHTTP(t, child.ID, "done")

	if got := countSystemCommentsOn(t, parent.ID); got != 1 {
		t.Fatalf("parent must get only the child-done comment, got %d", got)
	}
	content, _, _, _ := systemCommentOn(t, parent.ID)
	if strings.Contains(content, "close.waiting_on is resolved") {
		t.Errorf("parent must not get a waiting_on comment on top of child-done, got: %s", content)
	}
	if got := countPendingTasksForAgent(t, parent.ID, agentID); got != 1 {
		t.Fatalf("parent must get exactly one enqueue (child-done), got %d", got)
	}
}

func TestWaitingOnBatchWakesWaiter(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	batchSetStatus(t, []string{fx.waited.ID}, "done")
	assertWaiterWoken(t, fx)
}

func TestWaitingOnWakesSquadWaiter(t *testing.T) {
	fx := newWaitingOnFixture(t, "in_review", "")
	sq := newSquadCommentTriggerFixture(t)
	setIssueAssigneeDirect(t, fx.waiter.ID, "squad", sq.SquadID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, fx.waiter.ID)
	})
	updateIssueStatusHTTP(t, fx.waited.ID, "done")
	if got := countSystemCommentsOn(t, fx.waiter.ID); got != 1 {
		t.Fatalf("expected 1 system comment on squad waiter, got %d", got)
	}
	content, _, _, _ := systemCommentOn(t, fx.waiter.ID)
	if !strings.Contains(content, "mention://squad/"+sq.SquadID) {
		t.Errorf("squad waiter comment missing squad mention, got: %s", content)
	}
	if got := countPendingTasksForAgent(t, fx.waiter.ID, sq.LeaderID); got != 1 {
		t.Fatalf("expected 1 pending leader task for squad waiter, got %d", got)
	}
}
