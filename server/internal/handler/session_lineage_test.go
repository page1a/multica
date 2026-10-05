package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestClassifySessionBreak(t *testing.T) {
	rt := parseUUID("11111111-1111-1111-1111-111111111111")
	task := db.AgentTaskQueue{RuntimeID: rt}
	cases := []struct {
		name string
		task db.AgentTaskQueue
		bc   sessionBreakContext
		want string
	}{
		{"nobody ran before", task, sessionBreakContext{}, sessionBreakFirstRun},
		{"only another agent ran", task, sessionBreakContext{otherAgentRan: true}, sessionBreakAgentChanged},
		{"own run on another machine", task, sessionBreakContext{ownLastRuntimeID: "22222222-2222-2222-2222-222222222222"}, sessionBreakRuntimeChanged},
		{"own run here, nothing resumable", task, sessionBreakContext{ownLastRuntimeID: uuidToString(rt), otherAgentRan: true}, sessionBreakSessionLost},
		{"fresh asked for", db.AgentTaskQueue{RuntimeID: rt, ForceFreshSession: true}, sessionBreakContext{ownLastRuntimeID: uuidToString(rt)}, sessionBreakFreshRequested},
		{"poisoned retry is a lost session", db.AgentTaskQueue{RuntimeID: rt, ForceFreshSession: true, RetryOfTaskID: parseUUID("33333333-3333-3333-3333-333333333333")}, sessionBreakContext{ownLastRuntimeID: uuidToString(rt)}, sessionBreakSessionLost},
	}
	for _, tc := range cases {
		if got := classifySessionBreak(tc.task, tc.bc); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

type lineageRow struct {
	mode, resumedFrom, reason sql.NullString
}

func readLineage(t *testing.T, taskID string) lineageRow {
	t.Helper()
	var r lineageRow
	dbfx.QueryRow(t, `
		SELECT session_mode, resumed_from_task_id::text, session_break_reason
		FROM agent_task_queue WHERE id = $1
	`, taskID).Scan(&r.mode, &r.resumedFrom, &r.reason)
	return r
}

func queueIssueTask(t *testing.T, agentID, runtimeID, issueID string) string {
	t.Helper()
	var id string
	dbfx.QueryRow(t, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0) RETURNING id
	`, agentID, runtimeID, issueID).Scan(&id)
	return id
}

// The ticket's acceptance, at the claim: a first run is new, the same agent
// again resumes run 1, and a run that comes after another agent's is new
// because the agent changed.
func TestClaimTask_RecordsSessionLineage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
	issueID := dbfx.Issue(t, "session lineage fixture", testutil.Cols{"status": "in_progress", "number": 91345})

	first := queueIssueTask(t, agentID, runtimeID, issueID)
	claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if r := readLineage(t, first); r.mode.String != "new" || r.reason.String != sessionBreakFirstRun {
		t.Fatalf("first run lineage = %+v, want new/first_run", r)
	}
	dbfx.Exec(t, `
		UPDATE agent_task_queue SET status = 'completed', started_at = now() - interval '2 minutes',
		       completed_at = now() - interval '1 minute', session_id = 'lineage-session', work_dir = '/tmp/lineage'
		WHERE id = $1
	`, first)

	second := queueIssueTask(t, agentID, runtimeID, issueID)
	task := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if task.PriorSessionID != "lineage-session" {
		t.Fatalf("PriorSessionID = %q, want lineage-session", task.PriorSessionID)
	}
	if r := readLineage(t, second); r.mode.String != "resumed" || r.resumedFrom.String != first || r.reason.Valid {
		t.Fatalf("second run lineage = %+v, want resumed from %s", r, first)
	}

	// The daemon could not restore that session and ran a fresh one.
	w := httptest.NewRecorder()
	req := newDaemonTokenRequest("POST", "/api/daemon/tasks/"+second+"/complete",
		map[string]any{"output": "done", "session_resume_dropped": true}, testWorkspaceID, daemonID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", second)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, second)
	testHandler.CompleteTask(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CompleteTask: %d %s", w.Code, w.Body.String())
	}
	if r := readLineage(t, second); r.mode.String != "new" || r.resumedFrom.Valid || r.reason.String != sessionBreakSessionLost {
		t.Fatalf("dropped resume lineage = %+v, want new/session_lost", r)
	}

	// Another agent on the same issue starts fresh because the agent changed.
	var otherAgentID string
	dbfx.QueryRow(t, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, $2, 'local', '{}'::jsonb, $3, 'workspace', 3, $4) RETURNING id
	`, testWorkspaceID, "Lineage Other Agent "+t.Name(), runtimeID, testUserID).Scan(&otherAgentID)
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, otherAgentID) })
	third := queueIssueTask(t, otherAgentID, runtimeID, issueID)
	claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if r := readLineage(t, third); r.mode.String != "new" || r.reason.String != sessionBreakAgentChanged {
		t.Fatalf("other agent lineage = %+v, want new/agent_changed", r)
	}
}
