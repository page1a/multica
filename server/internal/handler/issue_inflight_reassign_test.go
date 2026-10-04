package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// heldIssue is a ticket already in flight, held by holder.
func heldIssue(t *testing.T, title, status, holder string) string {
	t.Helper()
	id := originIssue(t, title)
	dbfx.Exec(t, `UPDATE issue SET status = $2, assignee_type = 'agent', assignee_id = $3 WHERE id = $1`, id, status, holder)
	return id
}

func heldAssignee(t *testing.T, issueID string) string {
	t.Helper()
	var assignee *string
	dbfx.QueryRow(t, `SELECT assignee_id::text FROM issue WHERE id = $1`, issueID).Scan(&assignee)
	if assignee == nil {
		return ""
	}
	return *assignee
}

// TestAgentCannotReassignAnIssueInFlight is the iron law of DENE-1201: with
// routing on, an agent cannot move a ticket past todo to another executor on
// its own say. Everything that legitimately changes an in-flight executor is
// listed beside it and must keep working.
func TestAgentCannotReassignAnIssueInFlight(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "inflight", "这张交给 Origin Target inflight 做", "member", testUserID, testUserID)

	cases := []struct {
		name       string
		status     string
		asAgent    bool
		body       func() map[string]any
		wantHolder func() string
		rejected   bool
	}{
		{"in_progress agent to another agent", "in_progress", true,
			func() map[string]any { return map[string]any{"assignee_type": "agent", "assignee_id": f.target} },
			func() string { return f.caller }, true},
		{"in_review agent to another agent", "in_review", true,
			func() map[string]any { return map[string]any{"assignee_type": "agent", "assignee_id": f.target} },
			func() string { return f.caller }, true},
		{"blocked agent to another agent", "blocked", true,
			func() map[string]any { return map[string]any{"assignee_type": "agent", "assignee_id": f.target} },
			func() string { return f.caller }, true},
		{"agent moves to in_progress and reassigns in one write", "todo", true,
			func() map[string]any {
				return map[string]any{"status": "in_progress", "assignee_type": "agent", "assignee_id": f.target}
			},
			func() string { return f.caller }, true},
		{"agent re-sends the executor already there", "in_progress", true,
			func() map[string]any { return map[string]any{"assignee_type": "agent", "assignee_id": f.caller} },
			func() string { return f.caller }, false},
		{"agent hands it to a person", "in_progress", true,
			func() map[string]any { return map[string]any{"assignee_type": "member", "assignee_id": testUserID} },
			func() string { return testUserID }, false},
		{"agent carries the person's verified words", "in_progress", true,
			func() map[string]any {
				return map[string]any{"assignee_type": "agent", "assignee_id": f.target, "assignee_quote": "交给 Origin Target inflight 做"}
			},
			func() string { return f.target }, false},
		{"person reassigns by hand", "in_review", false,
			func() map[string]any { return map[string]any{"assignee_type": "agent", "assignee_id": f.target} },
			func() string { return f.target }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := heldIssue(t, "inflight "+tc.name, tc.status, f.caller)
			req := newRequest(http.MethodPut, "/api/issues/"+id, tc.body())
			if tc.asAgent {
				req.Header.Set("X-Agent-ID", f.caller)
				req.Header.Set("X-Task-ID", f.task)
			}
			req = req.WithContext(withSkipIssueRouting(req.Context()))
			resp := testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(req, "id", id)).Want(http.StatusOK).Map()

			if got := heldAssignee(t, id); got != tc.wantHolder() {
				t.Fatalf("assignee = %q, want %q", got, tc.wantHolder())
			}
			if (resp["assignee_ignored"] == true) != tc.rejected {
				t.Fatalf("assignee_ignored = %v, want %v", resp["assignee_ignored"], tc.rejected)
			}
			if tc.rejected {
				reason, _ := resp["assignee_ignored_reason"].(string)
				if reason != routing.ReasonAgentReassignInFlight ||
					!strings.Contains(reason, "multica issue escalate") || !strings.Contains(reason, "--outcome blocked") {
					t.Fatalf("assignee_ignored_reason = %q, want the way out named", reason)
				}
			}
		})
	}
}

// The batch path rules the same way: one write cannot launder a reassignment
// the single-issue path refuses.
func TestAgentCannotReassignInFlightThroughBatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "inflight-batch", "随便", "member", testUserID, testUserID)
	id := heldIssue(t, "inflight batch", "in_progress", f.caller)

	req := newRequest(http.MethodPatch, "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
		"issue_ids": []string{id},
		"updates":   map[string]any{"assignee_type": "agent", "assignee_id": f.target},
	})
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	testutil.Call(t, testHandler.BatchUpdateIssues, req).Want(http.StatusOK)
	if got := heldAssignee(t, id); got != f.caller {
		t.Fatalf("assignee = %q, batch reassigned an in-flight ticket", got)
	}
}

// With routing off nobody else would ever fill or move the slot, so the agent
// keeps the old freedom.
func TestAgentReassignInFlightStandsWhenRoutingIsOff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := originRun(t, "inflight-off", "随便", "member", testUserID, testUserID)
	id := heldIssue(t, "inflight routing off", "in_progress", f.caller)

	req := newRequest(http.MethodPut, "/api/issues/"+id, map[string]any{"assignee_type": "agent", "assignee_id": f.target})
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(req, "id", id)).Want(http.StatusOK).Map()
	if heldAssignee(t, id) != f.target || resp["assignee_ignored"] == true {
		t.Fatalf("routing off: assignee = %q ignored = %v", heldAssignee(t, id), resp["assignee_ignored"])
	}
}

// Flows that move an in-flight ticket without naming an executor through the
// update path — a status flip, a handoff to a named agent — are untouched.
func TestInFlightFlowsWithoutAnExecutorWriteStillWork(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "inflight-flows", "随便", "member", testUserID, testUserID)
	id := heldIssue(t, "inflight flows", "in_progress", f.caller)

	req := newRequest(http.MethodPost, "/api/issues/"+id+"/handoff", map[string]any{"to": f.name})
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	resp := testutil.Call(t, testHandler.HandoffIssue, testutil.WithURLParams(req, "id", id)).Want(http.StatusOK).Map()
	if resp["target_name"] != f.name {
		t.Fatalf("handoff target = %v, want %s", resp["target_name"], f.name)
	}
	if got := heldAssignee(t, id); got != f.caller {
		t.Fatalf("handoff changed the executor to %q", got)
	}
}
