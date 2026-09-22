package routing

import (
	"context"
	"testing"
)

func TestSuggestNamesTheDirectionSeatAndWritesNothing(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	got, err := r.Suggest(context.Background(), "ws-1", "game", []SuggestRow{
		{Title: "parent", HasChildren: true},
		{Title: "child"},
	})
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("suggestions = %d, want one per row", len(got))
	}
	for i, s := range got {
		if s.Seat == nil || s.Seat.ID != "a-goku-g" {
			t.Fatalf("row %d seat = %+v, want the strong seat specialised for 游戏", i, s.Seat)
		}
	}
	if judge.callCount() != 2 {
		t.Fatalf("judge calls = %d, want one per row", judge.callCount())
	}
	if store.wrote() || store.commentCount() != 0 {
		t.Fatalf("Suggest must not write an assignee or a comment")
	}
}

// A draft row is on screen in front of the person who will pick, so an
// unconfident or failed verdict leaves it empty instead of landing on the
// fallback rung the way routeTodo does.
func TestSuggestLeavesTheRowEmptyWhenRoutingHasNoAnswer(t *testing.T) {
	low := confidentVerdict()
	low.ExecutorConfidence = 0.2
	missing := confidentVerdict()
	missing.ExecutorTier = "galactic"

	for name, tc := range map[string]struct {
		settings  *Settings
		judge     *fakeJudge
		wantCalls int
	}{
		"routing disabled":      {settings: &Settings{}, judge: &fakeJudge{verdict: confidentVerdict()}},
		"low confidence":        {judge: &fakeJudge{verdict: low}, wantCalls: 1},
		"tier without a seat":   {judge: &fakeJudge{verdict: missing}, wantCalls: 1},
		"judge cannot be asked": {judge: &fakeJudge{err: errUpstream}, wantCalls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			if tc.settings != nil {
				store.settings = *tc.settings
			}
			got, err := newRouter(store, tc.judge).Suggest(context.Background(), "ws-1", "game", []SuggestRow{{Title: "row"}})
			if err != nil {
				t.Fatalf("Suggest: %v", err)
			}
			if len(got) != 1 || got[0].Seat != nil {
				t.Fatalf("suggestions = %+v, want one empty row", got)
			}
			if got[0].Reason == "" {
				t.Fatalf("an empty suggestion must say why")
			}
			if tc.judge.callCount() != tc.wantCalls {
				t.Fatalf("judge calls = %d, want %d", tc.judge.callCount(), tc.wantCalls)
			}
			if store.wrote() || store.commentCount() != 0 {
				t.Fatalf("Suggest must not write")
			}
		})
	}
}
