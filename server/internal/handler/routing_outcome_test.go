package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
)

// DENE-1722: a hold verdict on a ticket routing tiered is recorded against
// its class; the escalate signal goes through the store; the stats count
// each ticket once whatever it carries.
func TestRoutingOutcomeSignalsAndStats(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	store := testHandler.RoutingStore()
	class := routing.OutcomeClass{Direction: "测试方向-1722", Tier: "medium", Scope: routing.ScopeModule, Clarity: routing.ClarityClear, Risk: routing.RiskLow}

	held := dbfx.Issue(t, "outcome held")
	escalated := dbfx.Issue(t, "outcome escalated")
	both := dbfx.Issue(t, "outcome both")
	quiet := dbfx.Issue(t, "outcome quiet")
	untiered := dbfx.Issue(t, "never tiered")
	for _, id := range []string{held, escalated, both, quiet} {
		if err := store.RecordOutcome(ctx, testWorkspaceID, id, class); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// A second record keeps the first class.
	other := class
	other.Tier = "weak"
	if err := store.RecordOutcome(ctx, testWorkspaceID, quiet, other); err != nil {
		t.Fatalf("re-record: %v", err)
	}

	postHold := func(issueID string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "还差一步", "verdict": "hold"}), "id", issueID)
		testHandler.CreateComment(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateComment: %d %s", w.Code, w.Body.String())
		}
	}
	postHold(held)
	postHold(held) // a second hold is the same ticket
	postHold(both)
	postHold(untiered)
	for _, id := range []string{escalated, both, escalated} {
		if err := store.MarkUnderjudged(ctx, testWorkspaceID, id, routing.SignalEscalated); err != nil {
			t.Fatalf("mark escalated: %v", err)
		}
	}

	stats, err := store.OutcomeStats(ctx, testWorkspaceID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	var got *routing.ClassStats
	for i := range stats {
		if stats[i].Class == class {
			got = &stats[i]
		}
		if stats[i].Class == other {
			t.Errorf("a re-routed ticket was recorded twice: %+v", stats[i])
		}
	}
	if got == nil {
		t.Fatalf("class missing from stats: %+v", stats)
	}
	if got.Total != 4 || got.Held != 2 || got.Escalated != 2 || got.Low != 3 {
		t.Errorf("stats = %+v, want total 4, held 2, escalated 2, low 3", *got)
	}
	var untieredRows int
	dbfx.QueryRow(t, `SELECT count(*) FROM issue_routing_outcome WHERE issue_id = $1`, untiered).Scan(&untieredRows)
	if untieredRows != 0 {
		t.Errorf("a hold on a ticket routing never tiered created a row")
	}
}
