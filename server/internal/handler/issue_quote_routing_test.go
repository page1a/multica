package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1613: a quote that does not check out is an unquoted pick. The ticket
// is handed to routing, never held for the agent to go back to the person.

// routableSeat tags a medium seat and turns routing on, so a routing pass has
// somebody to put on the ticket.
func routableSeat(t *testing.T, name string) string {
	t.Helper()
	seat := createHandlerTestAgent(t, name, []byte("[]"))
	dbfx.Exec(t, `UPDATE agent SET routing_tier = 'medium' WHERE id = $1`, seat)
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	return seat
}

// routeNow runs the pass the create/status hooks would have started, inline.
func routeNow(t *testing.T, issueID string) routing.Outcome {
	t.Helper()
	out, err := testHandler.Routing.Route(context.Background(), testWorkspaceID, issueID)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return out
}

func assertRoutedPastUnverifiedQuote(t *testing.T, issueID string) {
	t.Helper()
	out := routeNow(t, issueID)
	if out.ExecutorWritten == nil {
		t.Fatalf("routing left the slot empty after an unverified quote: %+v", out)
	}
	assignee, source, _, _ := originOf(t, issueID)
	if assignee == nil || source == nil || *source != routing.SourceRouter {
		t.Fatalf("assignee = %v source = %v, want a router fill", assignee, source)
	}
	var body string
	dbfx.QueryRow(t, `SELECT string_agg(content, E'\n') FROM comment WHERE issue_id = $1 AND author_type = 'system'`, issueID).Scan(&body)
	if !strings.Contains(body, "附的原话没有点到这个名字") {
		t.Fatalf("routing comment does not say the quote did not name the seat:\n%s", body)
	}
}

func TestUnverifiedQuoteOnCreateIsHandedToRouting(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	routableSeat(t, "Quote Route Seat create")
	f := originRun(t, "quote-route-create", "你先做第二个，去 multica 魔改项目开票", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "quote names nobody", "你先做第二个")
	if resp["assignee_id"] != nil || resp["assignee_ignored"] != true {
		t.Fatalf("assignee_id = %v ignored = %v", resp["assignee_id"], resp["assignee_ignored"])
	}
	reason, _ := resp["assignee_ignored_reason"].(string)
	if reason != routing.ReasonQuoteNotVerified || strings.Contains(reason, "ask the person") {
		t.Fatalf("assignee_ignored_reason = %q, want the routing hand-off", reason)
	}
	id := resp["id"].(string)
	if _, source, _, _ := originOf(t, id); source == nil || *source != routing.SourceAgent {
		t.Fatalf("assignee_source = %v, want agent", source)
	}
	assertRoutedPastUnverifiedQuote(t, id)
}

// The assign request itself starts routing: nothing here calls Route, and the
// request does not touch status, title or description, the writes that
// routed a ticket before (DENE-1613 review F1).
func TestUnverifiedQuoteOnAssignIsHandedToRouting(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	routableSeat(t, "Quote Route Seat assign")
	f := originRun(t, "quote-route-assign", "这张票先放着", "member", testUserID, testUserID)

	for _, tc := range []struct {
		name  string
		batch bool
	}{{"single update", false}, {"batch update", true}} {
		t.Run(tc.name, func(t *testing.T) {
			id := originIssue(t, "empty todo "+tc.name)
			assign := map[string]any{"assignee_type": "agent", "assignee_id": f.target, "assignee_quote": "交给 " + f.name}
			var req *http.Request
			if tc.batch {
				req = newRequest(http.MethodPatch, "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
					"issue_ids": []string{id}, "updates": assign,
				})
			} else {
				req = newRequest(http.MethodPut, "/api/issues/"+id, assign)
			}
			req.Header.Set("X-Agent-ID", f.caller)
			req.Header.Set("X-Task-ID", f.task)
			if tc.batch {
				testutil.Call(t, testHandler.BatchUpdateIssues, req).Want(http.StatusOK)
			} else {
				resp := testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(req, "id", id)).Want(http.StatusOK).Map()
				if resp["assignee_ignored"] != true || resp["assignee_ignored_reason"] != routing.ReasonQuoteNotVerified {
					t.Fatalf("ignored = %v reason = %v", resp["assignee_ignored"], resp["assignee_ignored_reason"])
				}
			}
			waitRouterFill(t, id)
		})
	}
}

// waitRouterFill waits for the detached routing pass a write started.
func waitRouterFill(t *testing.T, issueID string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, source, _, _ := originOf(t, issueID); source != nil && *source == routing.SourceRouter {
			return
		}
	}
	assignee, source, _, _ := originOf(t, issueID)
	t.Fatalf("no routing pass filled the slot after the write: assignee = %v source = %v", assignee, source)
}

// routerSeatedWithRuns is a ticket routing put on seat, with the run that
// assignment started and a run a mention gave the same seat.
func routerSeatedWithRuns(t *testing.T, title, seat string) (issue, assignRun, mentionRun string) {
	t.Helper()
	issue = originIssue(t, title)
	dbfx.Exec(t, `UPDATE issue SET assignee_type = 'agent', assignee_id = $2, assignee_source = 'router' WHERE id = $1`, issue, seat)
	assignRun = dbfx.Task(t, seat, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t), "status": "running", "issue_id": issue,
		"started_at": testutil.Raw("now()"), "trigger_evidence_kind": "issue_assignment", "trigger_evidence_ref_id": issue,
	})
	mention := dbfx.Comment(t, issue, "看一下", testutil.Cols{"author_type": "member", "author_id": testUserID})
	mentionRun = dbfx.Task(t, seat, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t), "status": "queued", "issue_id": issue,
		"trigger_comment_id": mention, "trigger_evidence_kind": "comment", "trigger_evidence_ref_id": mention,
	})
	return issue, assignRun, mentionRun
}

func TestPersonReplacingARouterSeatCancelsOnlyItsAssignmentRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "router-replaced", "这张交给 Origin Target router-replaced 做", "member", testUserID, testUserID)
	routerSeat := createHandlerTestAgent(t, "Router Seat replaced", []byte("[]"))

	cases := []struct {
		name    string
		asAgent bool
		body    map[string]any
	}{
		{"agent carries the person's verified words", true,
			map[string]any{"assignee_type": "agent", "assignee_id": f.target, "assignee_quote": "交给 Origin Target router-replaced 做"}},
		{"person reassigns by hand", false,
			map[string]any{"assignee_type": "agent", "assignee_id": f.target}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue, assignRun, mentionRun := routerSeatedWithRuns(t, "router replaced "+tc.name, routerSeat)
			req := newRequest(http.MethodPut, "/api/issues/"+issue, tc.body)
			if tc.asAgent {
				req.Header.Set("X-Agent-ID", f.caller)
				req.Header.Set("X-Task-ID", f.task)
			}
			req = req.WithContext(withSkipIssueRouting(req.Context()))
			testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(req, "id", issue)).Want(http.StatusOK)

			if got := heldAssignee(t, issue); got != f.target {
				t.Fatalf("assignee = %q, want %q", got, f.target)
			}
			if got := taskStatus(t, assignRun); got != "cancelled" {
				t.Fatalf("the router seat's assignment run is %q, want cancelled", got)
			}
			if got := taskStatus(t, mentionRun); got != "queued" {
				t.Fatalf("the router seat's mention run is %q, want it untouched", got)
			}
			if got := countPendingTasksForAgent(t, issue, f.target); got != 1 {
				t.Fatalf("runs for the new seat = %d, want 1", got)
			}
			if got := taskStatus(t, f.task); got != "running" {
				t.Fatalf("the requesting run is %q, it must never cancel itself", got)
			}
		})
	}
}

// Only routing's stand-in is voided. A seat a person chose keeps its runs when
// it is changed, exactly as before (MUL-4113).
func TestReplacingAPersonsSeatCancelsNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	seat := createHandlerTestAgent(t, "Human Seat kept", []byte("[]"))
	next := createHandlerTestAgent(t, "Human Seat next", []byte("[]"))
	issue, assignRun, _ := routerSeatedWithRuns(t, "human seat replaced", seat)
	dbfx.Exec(t, `UPDATE issue SET assignee_source = 'human' WHERE id = $1`, issue)

	req := newRequest(http.MethodPut, "/api/issues/"+issue, map[string]any{"assignee_type": "agent", "assignee_id": next})
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(req, "id", issue)).Want(http.StatusOK)
	if got := taskStatus(t, assignRun); got != "running" {
		t.Fatalf("a person's seat lost its run on reassignment: %q", got)
	}
}

func TestBatchReplacingARouterSeatCancelsItsAssignmentRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	seat := createHandlerTestAgent(t, "Router Seat batch", []byte("[]"))
	next := createHandlerTestAgent(t, "Router Seat batch next", []byte("[]"))
	issue, assignRun, mentionRun := routerSeatedWithRuns(t, "router replaced batch", seat)

	req := newRequest(http.MethodPatch, "/api/issues/batch?workspace_id="+testWorkspaceID, map[string]any{
		"issue_ids": []string{issue},
		"updates":   map[string]any{"assignee_type": "agent", "assignee_id": next},
	})
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	testutil.Call(t, testHandler.BatchUpdateIssues, req).Want(http.StatusOK)
	if taskStatus(t, assignRun) != "cancelled" || taskStatus(t, mentionRun) != "queued" {
		t.Fatalf("assignment run %q mention run %q, want cancelled / queued", taskStatus(t, assignRun), taskStatus(t, mentionRun))
	}
}
