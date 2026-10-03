package projectboard

import (
	"testing"
	"time"
)

func TestBuildUsesParkingFactsAndAgeThreshold(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	issues := []Issue{
		{ID: "waiting", Identifier: "DENE-1", Title: "waiting", Status: "in_review", LastActivityAt: now.Add(-time.Hour)},
		{ID: "stalled", Identifier: "DENE-2", Title: "stalled", Status: "in_progress", LastActivityAt: now.Add(-time.Hour)},
		{ID: "running", Identifier: "DENE-3", Title: "running", Status: "in_progress", LastActivityAt: now.Add(-2 * time.Hour)},
		{ID: "todo", Identifier: "DENE-4", Title: "todo", Status: "todo", LastActivityAt: now.Add(-48 * time.Hour)},
		{ID: "stale", Identifier: "DENE-5", Title: "stale", Status: "in_progress", LastActivityAt: now.Add(-37 * time.Hour)},
	}
	board := Build(issues, []Parking{
		{IssueID: "waiting", CurrentStatus: "in_review", RecordedStatus: "in_review", Category: "awaiting_review", NextOwner: Owner{Type: "member", ID: "u1"}},
		{IssueID: "stalled", CurrentStatus: "in_progress", RecordedStatus: "in_progress", Category: "stalled_delivery", Unexplained: true, StuckKind: "run_failed", Summary: "run failed"},
	}, []Task{{IssueID: "running", AgentID: "a1", Status: "running", StartedAt: now.Add(-time.Minute)}}, now)
	if len(board.Waiting) != 1 || len(board.Stalled) != 1 || len(board.Running) != 1 || len(board.Todo) != 1 || len(board.Stale) != 1 {
		t.Fatalf("unexpected lanes: waiting=%d stalled=%d running=%d todo=%d stale=%d", len(board.Waiting), len(board.Stalled), len(board.Running), len(board.Todo), len(board.Stale))
	}
	if board.Stale[0].LastActivityAt != now.Add(-37*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("stale last activity = %s", board.Stale[0].LastActivityAt)
	}
}

func TestBuildExcludesIntentionalPauseFromStale(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	board := Build([]Issue{{ID: "paused", Identifier: "DENE-9", Title: "paused", Status: "in_progress", LastActivityAt: now.Add(-72 * time.Hour), Metadata: map[string]any{"block.wake_at": "2026-10-04T00:00:00Z"}}}, nil, nil, now)
	if len(board.Stale) != 0 || len(board.Todo) != 1 || !board.Todo[0].Paused {
		t.Fatalf("intentional pause was not kept out of stale: %#v", board)
	}
}

func TestBuildKeepsUnclassifiedOpenIssueVisible(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	board := Build([]Issue{{ID: "new", Identifier: "DENE-10", Title: "new", Status: "in_progress", LastActivityAt: now.Add(-time.Hour)}}, nil, nil, now)
	if len(board.Todo) != 1 || board.Todo[0].IssueID != "new" {
		t.Fatalf("unclassified open issue was dropped: %#v", board)
	}
}
