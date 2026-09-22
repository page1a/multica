package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// tierJudge answers every assignment with one rung.
type tierJudge struct {
	tier       string
	confidence float64
}

func (j tierJudge) Assign(context.Context, routing.Target, routing.JudgeState) (routing.Verdict, error) {
	return routing.Verdict{ExecutorTier: j.tier, ExecutorConfidence: j.confidence}, nil
}

func (j tierJudge) Unblock(context.Context, routing.Target, routing.JudgeState) (routing.Advice, error) {
	return routing.Advice{}, nil
}

func (j tierJudge) Stale(context.Context, routing.Target, routing.StaleState) (routing.StaleDecision, error) {
	return routing.StaleDecision{}, nil
}

// enableDraftSuggestRouting turns routing on for the test workspace and swaps
// the judge, restoring both afterwards. The store stays the real one, so the
// roster the suggestion reads is the workspace's actual tagged agents.
func enableDraftSuggestRouting(t *testing.T, judge routing.Judge) {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	previousRouting := testHandler.Routing
	t.Cleanup(func() {
		dbfx.Exec(t, `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, previous)
		testHandler.Routing = previousRouting
	})
	dbfx.Exec(t, `
		UPDATE workspace
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{routing}', '{"enabled": true, "model": "test-model"}'::jsonb)
		WHERE id = $1
	`, testWorkspaceID)
	testHandler.Routing = routing.New(testHandler.RoutingStore(), judge)
}

func suggestDraftAssignees(t *testing.T, sessionID string, rows []map[string]any) SuggestIssueDraftAssigneesResponse {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPost, "/api/issue-drafts/"+sessionID+"/assignee-suggestions", map[string]any{
		"rows": rows,
	}), "sessionId", sessionID)
	var out SuggestIssueDraftAssigneesResponse
	testutil.Call(t, testHandler.SuggestIssueDraftAssignees, req).Want(http.StatusOK).JSON(&out)
	return out
}

func TestSuggestIssueDraftAssigneesUsesTheTaggedRosterAndCreatesNothing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Cleanup(func() { cleanupIssueDraftCarriers(t) })
	agentID := createHandlerTestAgent(t, "Draft Suggest Medium Seat", []byte("[]"))
	dbfx.Exec(t, `UPDATE agent SET routing_tier = 'medium' WHERE id = $1`, agentID)
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	session := startIssueDraftSession(t)

	var issuesBefore int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, testWorkspaceID).Scan(&issuesBefore)

	out := suggestDraftAssignees(t, session.SessionID, []map[string]any{
		{"title": "parent", "description": "group root", "has_children": true},
		{"title": "child", "description": "one piece"},
	})
	if len(out.Suggestions) != 2 {
		t.Fatalf("suggestions = %d, want one per row", len(out.Suggestions))
	}
	for i, s := range out.Suggestions {
		if s == nil || s.AssigneeType != "agent" || s.AssigneeID != agentID || s.Tier != "medium" {
			t.Fatalf("row %d suggestion = %+v, want the seat tagged medium (%s)", i, s, agentID)
		}
	}

	var issuesAfter int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, testWorkspaceID).Scan(&issuesAfter)
	if issuesAfter != issuesBefore {
		t.Fatalf("suggesting created %d issue(s); it must be read-only", issuesAfter-issuesBefore)
	}
}

func TestSuggestIssueDraftAssigneesLeavesRowsEmptyWithoutAConfidentSeat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Cleanup(func() { cleanupIssueDraftCarriers(t) })
	agentID := createHandlerTestAgent(t, "Draft Suggest Unsure Seat", []byte("[]"))
	dbfx.Exec(t, `UPDATE agent SET routing_tier = 'medium' WHERE id = $1`, agentID)
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.1})
	session := startIssueDraftSession(t)

	out := suggestDraftAssignees(t, session.SessionID, []map[string]any{{"title": "vague"}})
	if len(out.Suggestions) != 1 || out.Suggestions[0] != nil {
		t.Fatalf("suggestions = %+v, want a single empty row", out.Suggestions)
	}
}
