package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DENE-1721 acceptance on the server: a weak seat working a ticket consults
// the strongest seat; the 4th ask on the ticket is refused with a reason; an
// answered consult lands its answer, a timeline line and the goal budget.
func TestIssueConsult_LimitAnswerActivityAndBudget(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	ctx := context.Background()
	var stored []byte
	if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, stored)
	})

	runtime := dbfx.Runtime(t, "consult-runtime", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"metadata":     testutil.Raw(`'{"capabilities":["` + protocol.DaemonCapabilityConsultV1 + `"]}'::jsonb`),
	})
	asker := dbfx.Agent(t, "consult-asker", runtime, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID, "routing_tier": "weak"})
	advisor := dbfx.Agent(t, "consult-advisor", runtime, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID, "routing_tier": "strongest"})
	issue := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "consult ticket", "status": "in_progress", "priority": "none",
		"creator_type": "member", "creator_id": testUserID, "number": nextTestIssueNumber(t),
	})
	dbfx.Insert(t, "issue_goal", testutil.Cols{"issue_id": issue, "workspace_id": testWorkspaceID, "status": "active"})
	askerTask := dbfx.Task(t, asker, testutil.Cols{
		"runtime_id": runtime, "status": "running", "issue_id": issue,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id IN (SELECT advisor_task_id FROM issue_consult WHERE issue_id = $1)`, issue)
		testPool.Exec(ctx, `DELETE FROM issue_consult WHERE issue_id = $1`, issue)
	})

	ask := func(question string) (*httptest.ResponseRecorder, map[string]any) {
		r := newRequest(http.MethodPost, "/api/issues/"+issue+"/consults", map[string]any{"question": question})
		r.Header.Set("X-Actor-Source", "task_token")
		r.Header.Set("X-Agent-ID", asker)
		r.Header.Set("X-Task-ID", askerTask)
		r = withURLParams(r, "id", issue)
		w := httptest.NewRecorder()
		testHandler.CreateIssueConsult(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	var first map[string]any
	for i := 1; i <= 3; i++ {
		w, out := ask("Which index should this use?")
		if w.Code != http.StatusAccepted {
			t.Fatalf("consult %d: status %d body=%s", i, w.Code, w.Body.String())
		}
		if out["advisor_agent_id"] == asker {
			t.Fatalf("consult %d went to the asker", i)
		}
		if i == 1 {
			first = out
		}
	}
	w, out := ask("One more?")
	if w.Code != http.StatusConflict || out["code"] != ConsultRefusedLimit || out["error"] == "" {
		t.Fatalf("4th consult: status %d body=%s", w.Code, w.Body.String())
	}

	// The advisor answers the first one.
	taskID := first["advisor_task_id"].(string)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() - interval '30 seconds' WHERE id = $1`, taskID); err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "task_usage", testutil.Cols{"task_id": taskID, "provider": "test", "model": "m", "input_tokens": 1000, "output_tokens": 200})
	result, _ := json.Marshal(protocol.TaskCompletedPayload{TaskID: taskID, Output: "Use the composite index."})
	if _, err := testHandler.TaskService.CompleteTask(ctx, util.MustParseUUID(taskID), result, "", "", "", false, "", ""); err != nil {
		t.Fatalf("complete advisor run: %v", err)
	}

	r := withURLParams(newRequest(http.MethodGet, "/api/issues/"+issue+"/consults/"+first["id"].(string), nil), "id", issue, "consultId", first["id"].(string))
	gw := httptest.NewRecorder()
	testHandler.GetIssueConsult(gw, r)
	var got ConsultResponse
	_ = json.Unmarshal(gw.Body.Bytes(), &got)
	if got.Status != "answered" || got.Answer != "Use the composite index." || got.TokensUsed != 1200 {
		t.Fatalf("consult after answer = %+v", got)
	}

	var details []byte
	if err := testPool.QueryRow(ctx, `SELECT details FROM activity_log WHERE issue_id = $1 AND action = 'consult_answered'`, issue).Scan(&details); err != nil {
		t.Fatalf("activity: %v", err)
	}
	var d map[string]any
	_ = json.Unmarshal(details, &d)
	if d["answer"] != "Use the composite index." || d["advisor_id"] != advisor || d["question"] == "" {
		t.Fatalf("activity details = %v", d)
	}
	var tokens int64
	var runs int
	if err := testPool.QueryRow(ctx, `SELECT tokens_used, runs_used FROM issue_goal WHERE issue_id = $1`, issue).Scan(&tokens, &runs); err != nil {
		t.Fatal(err)
	}
	if tokens != 1200 || runs != 0 {
		t.Fatalf("goal usage tokens=%d runs=%d, want 1200 tokens and no extra round", tokens, runs)
	}
}
