package routing

import (
	"context"
	"strings"
	"testing"
)

// DENE-1647: a failed acceptance run on a seat that is still open is retried
// on the same seat first.
func TestCoverFailedReviewRetriesAnOpenSeat(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-piccolo", Name: "比克"}

	out, err := newRouter(store, &fakeJudge{}).CoverFailedReview(context.Background(), "ws", "issue-1", false)
	if err != nil {
		t.Fatalf("cover: %v", err)
	}
	if out.Action != ActionWoken || len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-piccolo" {
		t.Fatalf("out = %+v handoffs = %v, want the same seat woken", out, store.handoffs)
	}
}

// A switched-off reviewer is covered by another seat, never one that worked
// on the ticket, and the note says the acceptance seat failed.
func TestCoverFailedReviewSkipsSeatsThatWorkedOnTheTicket(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-gohan"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-gohan", Name: "孙悟饭"}
	store.issue.Workers = []string{"a-bulma", "a-bulma-g"}
	store.offRoster = map[string]Agent{
		"a-gohan": {ID: "a-gohan", Name: "孙悟饭", Tier: "strongest"},
	}

	out, err := newRouter(store, &fakeJudge{}).CoverFailedReview(context.Background(), "ws", "issue-1", false)
	if err != nil {
		t.Fatalf("cover: %v", err)
	}
	for _, h := range store.handoffs {
		if h == "agent:a-bulma" || h == "agent:a-bulma-g" {
			t.Fatalf("handoffs = %v, acceptance went to a seat that worked on the ticket", store.handoffs)
		}
	}
	switch out.Action {
	case ActionHandedOff:
		if body := strings.Join(store.comments[KindHandoff], "\n"); !strings.Contains(body, "验收席失败") {
			t.Fatalf("handoff comment does not say the seat failed:\n%s", body)
		}
	case ActionAdvised:
	default:
		t.Fatalf("out = %+v, want handed off or advised", out)
	}
}

func TestCoverFailedReviewLeavesAPersonAlone(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerMember, ID: "user-1", Name: "Kun"}

	out, err := newRouter(store, &fakeJudge{}).CoverFailedReview(context.Background(), "ws", "issue-1", true)
	if err != nil || out.Action != ActionNoop || len(store.handoffs) != 0 {
		t.Fatalf("out = %+v err = %v handoffs = %v, want untouched", out, err, store.handoffs)
	}
}
