package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dispatch"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestWorkThreadActionForbidsUninvocablePrivateAgent: the work-thread panel
// must not be a back door around canInvokeAgent (MUL-4525). testUserID (the
// workspace owner) can VIEW an issue assigned to someone else's private agent,
// but neither "queue" nor "continue" may enqueue a run for it — and a blocked
// action writes nothing.
func TestWorkThreadActionForbidsUninvocablePrivateAgent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, ownerID, _ := privateAgentTestFixture(t)

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, priority, visibility, status)
		VALUES ($1, 'work thread private agent', 'member', $2, 'agent', $3, 'medium', 'workspace', 'in_progress')
		RETURNING id`, testWorkspaceID, ownerID, agentID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO work_thread (id, agent_id, issue_id, last_session_id) VALUES ($1,$2,$3,$4)`, threadID, agentID, issueID, sessionID); err != nil {
		t.Fatal(err)
	}
	// A resumable (cancelled, session-bearing) turn so "continue" has a target.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority, session_id, completed_at)
		VALUES ($1,$2,$3,$4,$5,'cancelled',0,$6,now())`, uuid.NewString(), agentID, handlerTestRuntimeID(t), issueID, threadID, sessionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM work_thread WHERE id = $1`, threadID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	taskCount := func() int {
		var n int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := taskCount()

	for _, action := range []string{"queue", "continue"} {
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": action}), "id", issueID)
		w := testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusForbidden)
		if code := readReasonCode(t, w.Body.Bytes()); code != string(dispatch.ReasonInvocationNotAllowed) {
			t.Errorf("%s reason_code = %q, want invocation_not_allowed", action, code)
		}
		if got := taskCount(); got != before {
			t.Errorf("blocked %s changed task count: got %d, want %d", action, got, before)
		}
	}

	// The owner passes the same gate and continue resumes the thread.
	req := withURLParam(newRequestAs(ownerID, http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "continue"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	if got := taskCount(); got != before+1 {
		t.Fatalf("owner continue task count: got %d, want %d", got, before+1)
	}
}

// TestWorkThreadActionRefusesDerivedRunInTriage: continue/queue are derived
// runs (nobody named the agent), so like RerunIssue they are refused while the
// issue waits in Triage.
func TestWorkThreadActionRefusesDerivedRunInTriage(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Work Thread Triage Agent", []byte("[]"))
	issueID := dbfx.Issue(t, "work thread in triage", testutil.Cols{"status": "todo", "assignee_type": "agent", "assignee_id": agentID, "triage_state": "pending"})
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id, last_session_id) VALUES ($1,$2,$3,$4)`, threadID, agentID, issueID, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority, session_id, completed_at)
		VALUES ($1,$2,$3,$4,$5,'cancelled',0,$6,now())`, uuid.NewString(), agentID, handlerTestRuntimeID(t), issueID, threadID, sessionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM work_thread WHERE id = $1`, threadID)
	})
	for _, action := range []string{"queue", "continue"} {
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": action}), "id", issueID)
		w := testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusForbidden)
		if code := readReasonCode(t, w.Body.Bytes()); code != string(dispatch.ReasonIssueInTriage) {
			t.Errorf("%s reason_code = %q, want issue_in_triage", action, code)
		}
	}
}

// TestWorkThreadActionInterruptRecordsCancellingUser: interrupt is an explicit
// user cancellation (CancelTaskByUser), so the row names who stopped it and is
// marked user-initiated — the delegated-failure sweeper must not rebuild it.
func TestWorkThreadActionInterruptRecordsCancellingUser(t *testing.T) {
	issueID := dbfx.Issue(t, "work thread interrupt actor", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Work Thread Interrupt Actor Agent", []byte("[]"))
	threadID := uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, threadID, agentID, issueID); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, taskID, agentID, handlerTestRuntimeID(t), issueID, threadID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(context.Background(), `DELETE FROM work_thread WHERE id = $1`, threadID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "interrupt"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	var status, byType, byID string
	if err := testPool.QueryRow(t.Context(), `SELECT status, COALESCE(cancelled_by_type,''), COALESCE(cancelled_by_id::text,'') FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status, &byType, &byID); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || byType != "member" || byID != testUserID {
		t.Fatalf("status=%q cancelled_by=%s/%s, want cancelled by member %s", status, byType, byID, testUserID)
	}
}

// TestChatWorkThreadActionForbidsViewOnlyShare covers DENE-840 on the panel: a
// person the creator shared the chat with as view-only can see it, but may
// neither stop the creator's run nor resume it as the creator's agent.
func TestChatWorkThreadActionForbidsViewOnlyShare(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "WorkThreadShareAgent", []byte("[]"))
	viewerID := insertChatPerson(t, "work-thread-viewer", "member")

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, visibility, created_by)
		VALUES ($1, 'Work thread share project', 'project', $2)
		RETURNING id`, testWorkspaceID, testUserID).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project_member WHERE project_id = $1`, projectID)
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	createReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions", map[string]any{
		"agent_id": agentID, "title": "shared work thread", "project_id": projectID,
	}))
	createW := httptest.NewRecorder()
	testHandler.CreateChatSession(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", createW.Code, createW.Body.String())
	}
	var created ChatSessionResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	sessionID := created.ID
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM resource_share WHERE resource_id = $1`, sessionID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)
		testPool.Exec(context.Background(), `DELETE FROM work_thread WHERE chat_session_id = $1`, sessionID)
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	putReq := withURLParam(chatAs(t, testUserID, newRequest("PUT", "/api/chat/sessions/"+sessionID+"/access", map[string]any{
		"mode":   "extra",
		"shares": []map[string]string{{"user_id": viewerID, "access": "view"}},
	})), "sessionId", sessionID)
	putW := httptest.NewRecorder()
	testHandler.PutChatSessionAccess(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("share view-only: %d %s", putW.Code, putW.Body.String())
	}

	threadID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO work_thread (id, agent_id, chat_session_id) VALUES ($1,$2,$3)`, threadID, agentID, sessionID); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_task_queue (id, agent_id, runtime_id, chat_session_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, taskID, agentID, handlerTestRuntimeID(t), sessionID, threadID); err != nil {
		t.Fatal(err)
	}

	// The viewer can open the thread snapshot…
	getReq := withURLParam(chatAs(t, viewerID, newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/work-thread", nil)), "sessionId", sessionID)
	testutil.Call(t, testHandler.GetChatWorkThread, getReq).Want(http.StatusOK)

	// …but may not interrupt the creator's run, nor continue it.
	for _, action := range []string{"interrupt", "continue"} {
		req := withURLParam(chatAs(t, viewerID, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/work-thread/action", map[string]any{"action": action})), "sessionId", sessionID)
		testutil.Call(t, testHandler.ChatWorkThreadAction, req).Want(http.StatusForbidden)
	}
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("viewer changed the run: status=%q, want running", status)
	}

	// The creator interrupts, and the row records them.
	req := withURLParam(chatAs(t, testUserID, newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/work-thread/action", map[string]any{"action": "interrupt"})), "sessionId", sessionID)
	testutil.Call(t, testHandler.ChatWorkThreadAction, req).Want(http.StatusAccepted)
	var byID string
	if err := testPool.QueryRow(ctx, `SELECT status, COALESCE(cancelled_by_id::text,'') FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status, &byID); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" || byID != testUserID {
		t.Fatalf("creator interrupt: status=%q cancelled_by=%s", status, byID)
	}
}

// TestWorkThreadActionQueueCoalescesOnlyAssigneeRow: when the pending-task
// fence coalesces a "queue" click, the summary lands on the assignee agent's
// own queued row — not on every queued row of the issue. Another agent waiting
// on the same issue keeps its input untouched.
func TestWorkThreadActionQueueCoalescesOnlyAssigneeRow(t *testing.T) {
	assignee := createHandlerTestAgent(t, "Work Thread Queue Assignee", []byte("[]"))
	other := createHandlerTestAgent(t, "Work Thread Queue Other", []byte("[]"))
	issueID := dbfx.Issue(t, "work thread queue coalesce", testutil.Cols{"status": "in_progress", "assignee_type": "agent", "assignee_id": assignee})
	assigneeTask, otherTask := uuid.NewString(), uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, status, priority, trigger_summary)
		VALUES ($1,$2,$4,$5,'queued',0,'assignee input'), ($3,$6,$4,$5,'queued',0,'other agent input')`,
		assigneeTask, assignee, otherTask, handlerTestRuntimeID(t), issueID, other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "queue", "summary": "follow-up"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	var assigneeSummary, otherSummary string
	if err := testPool.QueryRow(t.Context(), `SELECT trigger_summary FROM agent_task_queue WHERE id=$1`, assigneeTask).Scan(&assigneeSummary); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT trigger_summary FROM agent_task_queue WHERE id=$1`, otherTask).Scan(&otherSummary); err != nil {
		t.Fatal(err)
	}
	if assigneeSummary != "assignee input\nfollow-up" {
		t.Fatalf("assignee summary = %q", assigneeSummary)
	}
	if otherSummary != "other agent input" {
		t.Fatalf("other agent's queued row was rewritten: %q", otherSummary)
	}
}
