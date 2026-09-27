package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestHandoffDuplicateReasonIsStructured(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   bool
	}{
		{"already handed off this round", true},
		{"active run in progress", true},
		{"status has no routing behaviour", false},
		{"", false},
	} {
		if got := handoffDuplicateReason(tc.reason); got != tc.want {
			t.Errorf("handoffDuplicateReason(%q) = %v, want %v", tc.reason, got, tc.want)
		}
	}
}

func TestHandoffNamedAgentDuplicateDoesNotEnqueueAgain(t *testing.T) {
	agentID := createHandlerTestAgent(t, "handoff-duplicate-agent", []byte("[]"))
	issueID := dbfx.Issue(t, "handoff duplicate")
	dbfx.Task(t, agentID, testutil.Cols{"issue_id": issueID, "runtime_id": handlerTestRuntimeID(t)})

	var before int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentID).Scan(&before)
	resp := callIssueHandoff(t, issueID, "handoff-duplicate-agent")
	if !resp.Duplicate || resp.RunCreated {
		t.Fatalf("duplicate response = %+v, want duplicate=true and run_created=false", resp)
	}
	var after int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, issueID, agentID).Scan(&after)
	if after != before {
		t.Fatalf("active task count changed from %d to %d", before, after)
	}
}

func TestHandoffReviewerRejectsHumanSeat(t *testing.T) {
	issueID := dbfx.Issue(t, "handoff human reviewer", testutil.Cols{
		"reviewer_type": "member",
		"reviewer_id":   testUserID,
	})
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/handoff", map[string]string{"to": "reviewer"})
	req = withURLParam(req, "id", issueID)
	rec := httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s; want 409", rec.Code, rec.Body.String())
	}
}

func TestHandoffResponseMatchesCreatedRun(t *testing.T) {
	agentName := "handoff-result-agent"
	agentID := createHandlerTestAgent(t, agentName, []byte("[]"))
	issueID := dbfx.Issue(t, "handoff result")
	resp := callIssueHandoff(t, issueID, agentName)
	if resp.Duplicate || !resp.Routed || !resp.RunCreated {
		t.Fatalf("handoff response = %+v, want routed=true run_created=true", resp)
	}
	if resp.TargetID != agentID {
		t.Fatalf("target_id = %q, want %q", resp.TargetID, agentID)
	}
	var active bool
	dbfx.QueryRow(t, `SELECT count(*) > 0 FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status IN ('queued','dispatched','running','waiting_local_directory')`, issueID, agentID).Scan(&active)
	if active != resp.RunCreated {
		t.Fatalf("run_created = %v, database active run = %v", resp.RunCreated, active)
	}
}

func callIssueHandoff(t *testing.T, issueID, target string) HandoffIssueResponse {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/handoff", map[string]string{"to": target})
	req = withURLParam(req, "id", issueID)
	rec := httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp HandoffIssueResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode handoff response: %v", err)
	}
	return resp
}
