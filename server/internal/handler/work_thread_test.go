package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestGetIssueWorkThreadReturnsBoundedSnapshot(t *testing.T) {
	issueID := dbfx.Issue(t, "work thread snapshot", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Work Thread Snapshot Agent", []byte("[]"))
	threadID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `
		INSERT INTO work_thread (id, agent_id, issue_id, context_generation, context_message_limit, context_token_budget, continuity_break_reason)
		VALUES ($1, $2, $3, 2, 12, 3456, 'provider session expired')`, threadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id = $1`, threadID) })
	queuedID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority, trigger_summary)
		VALUES ($1, $2, $3, $4, $5, 'queued', 0, 'follow-up input')`, queuedID, agentID, handlerTestRuntimeID(t), issueID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id = $1`, queuedID) })

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/work-thread", nil), "id", issueID)
	w := testutil.Call(t, testHandler.GetIssueWorkThread, req).Want(http.StatusOK)
	var got WorkThreadSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != threadID || !got.Continuous || got.Context.TokenBudget != 3456 || len(got.QueuedInputs) != 1 || got.QueuedInputs[0].Summary != "follow-up input" {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.State != "queued" || got.CanResume {
		t.Fatalf("queued snapshot state = %q can_resume=%v", got.State, got.CanResume)
	}
	if got.Context.SummaryAvailable {
		t.Fatal("summary_available must stay false until a bounded summary source exists")
	}
}

func TestGetIssueWorkThreadMarksCancelledTurnResumable(t *testing.T) {
	issueID := dbfx.Issue(t, "resumable work thread", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Resumable Work Thread Agent", []byte("[]"))
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `
		INSERT INTO work_thread (id, agent_id, issue_id, last_session_id, context_generation)
		VALUES ($1, $2, $3, $4, 1)`, threadID, agentID, issueID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `
		INSERT INTO agent_task_queue (id, agent_id, issue_id, work_thread_id, status, priority, session_id, completed_at)
		VALUES ($1, $2, $3, $4, 'cancelled', 0, $5, now())`, taskID, agentID, issueID, threadID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id = $1`, threadID)
	})

	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/work-thread", nil), "id", issueID)
	w := testutil.Call(t, testHandler.GetIssueWorkThread, req).Want(http.StatusOK)
	var got WorkThreadSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "resumable" || !got.CanResume || got.LastTurn == nil || got.LastTurn.Status != "cancelled" {
		t.Fatalf("resumable snapshot = %+v", got)
	}
}

func TestGetChatWorkThreadEnforcesSessionOwnership(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Work Thread Chat Access Agent", []byte("[]"))
	otherUser := dbfx.User(t, "other chat owner", "work-thread-chat-owner@example.com")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")
	sessionID := insertChatSessionAs(t, agentID, otherUser)
	req := withURLParam(newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/work-thread", nil), "sessionId", sessionID)
	req = withChatTestWorkspaceCtx(t, req)
	// kun hides another person's private chat as 404 (DENE-840), not 403.
	testutil.Call(t, testHandler.GetChatWorkThread, req).Want(http.StatusNotFound)
}

func TestWorkThreadActionRejectsContinueWhileActive(t *testing.T) {
	issueID := dbfx.Issue(t, "work thread action conflict", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Work Thread Action Agent", []byte("[]"))
	threadID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, threadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, oldTaskID, agentID, handlerTestRuntimeID(t), issueID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	latestThreadID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, latestThreadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, taskID, agentID, handlerTestRuntimeID(t), issueID, latestThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id IN ($1,$2)`, oldTaskID, taskID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id IN ($1,$2)`, threadID, latestThreadID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "continue"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusConflict)
}

func TestWorkThreadActionInterruptsActiveTurn(t *testing.T) {
	issueID := dbfx.Issue(t, "work thread action interrupt", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Work Thread Interrupt Agent", []byte("[]"))
	threadID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, threadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, oldTaskID, agentID, handlerTestRuntimeID(t), issueID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	latestThreadID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, latestThreadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$3,$4,$5,'running',0)`, taskID, agentID, handlerTestRuntimeID(t), issueID, latestThreadID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE id IN ($1,$2)`, oldTaskID, taskID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id IN ($1,$2)`, threadID, latestThreadID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "interrupt"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	var status string
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM agent_task_queue WHERE id=$1`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("status=%q, want cancelled", status)
	}
	var oldStatus string
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM agent_task_queue WHERE id=$1`, oldTaskID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "running" {
		t.Fatalf("older thread status=%q, want running", oldStatus)
	}
}

func TestWorkThreadActionContinuePreservesCancelledSession(t *testing.T) {
	issueID := dbfx.Issue(t, "resume cancelled work thread", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Resume Work Thread Agent", []byte("[]"))
	threadID := uuid.NewString()
	sessionID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id, last_session_id) VALUES ($1,$2,$3,$4)`, threadID, agentID, issueID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	parentID := uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority, session_id, completed_at) VALUES ($1,$2,$6,$3,$4,'cancelled',0,$5,now())`, parentID, agentID, issueID, threadID, sessionID, handlerTestRuntimeID(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE work_thread_id=$1`, threadID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id=$1`, threadID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "continue"}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	var got pgtype.UUID
	if err := testPool.QueryRow(t.Context(), `SELECT session_id FROM agent_task_queue WHERE work_thread_id=$1 AND status='queued'`, threadID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String() != sessionID {
		t.Fatalf("session_id=%v, want %s", got, sessionID)
	}
}

func TestWorkThreadActionPrioritizesQueuedInput(t *testing.T) {
	issueID := dbfx.Issue(t, "prioritize work thread", testutil.Cols{"status": "in_progress"})
	agentID := createHandlerTestAgent(t, "Prioritize Work Thread Agent", []byte("[]"))
	threadID := uuid.NewString()
	_, err := testPool.Exec(t.Context(), `INSERT INTO work_thread (id, agent_id, issue_id) VALUES ($1,$2,$3)`, threadID, agentID, issueID)
	if err != nil {
		t.Fatal(err)
	}
	activeID, queuedID := uuid.NewString(), uuid.NewString()
	_, err = testPool.Exec(t.Context(), `INSERT INTO agent_task_queue (id, agent_id, runtime_id, issue_id, work_thread_id, status, priority) VALUES ($1,$2,$6,$3,$4,'running',0),($5,$2,$6,$3,$4,'queued',0)`, activeID, agentID, issueID, threadID, queuedID, handlerTestRuntimeID(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(t.Context(), `DELETE FROM agent_task_queue WHERE work_thread_id=$1`, threadID)
		testPool.Exec(t.Context(), `DELETE FROM work_thread WHERE id=$1`, threadID)
	})
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/work-thread/action", map[string]any{"action": "prioritize", "task_id": queuedID}), "id", issueID)
	testutil.Call(t, testHandler.WorkThreadAction, req).Want(http.StatusAccepted)
	var priority int
	if err := testPool.QueryRow(t.Context(), `SELECT priority FROM agent_task_queue WHERE id=$1`, queuedID).Scan(&priority); err != nil {
		t.Fatal(err)
	}
	if priority != 4 {
		t.Fatalf("priority=%d, want 4", priority)
	}
}
