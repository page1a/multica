package routing

import (
	"context"
	"strings"
	"testing"
)

// The iron laws of executor picking (DENE-1201). Each row is a rule that must
// never break; whoever changes routing and breaks one turns this red. The
// handler half — an agent cannot reassign a ticket in flight — lives in
// handler/issue_inflight_reassign_test.go.

func TestIronLawRoutingNeverChangesAHeldSlot(t *testing.T) {
	for _, status := range []string{"todo", "backlog", "in_progress", "in_review", "blocked"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			store.issue.AssigneeType = "agent"
			store.issue.AssigneeID = "a-piccolo-g"
			store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
			if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
				t.Fatalf("route: %v", err)
			}
			if len(store.assigns) != 0 {
				t.Fatalf("routing re-picked a held executor: %v", store.assigns)
			}
			// The one move routing makes on a held slot is the acceptance
			// handoff: in_review, to the reviewer seat already on the ticket.
			for _, h := range store.handoffs {
				if status != "in_review" || h != "agent:a-bulma-g" {
					t.Fatalf("routing moved a held executor: handoffs=%v", store.handoffs)
				}
			}
		})
	}
}

func TestIronLawRoutingNeverPicksADisabledOrOfflineSeat(t *testing.T) {
	// The confident verdict names the strong rung, whose game seat is
	// 孙悟空游戏 (a-goku-g). Take that seat out every way a seat can be out.
	for _, availability := range []string{
		AvailabilityDisabled, AvailabilityUnreachable, AvailabilityDead,
		AvailabilityArchived, AvailabilityCancelled, AvailabilityQuotaExhausted,
	} {
		t.Run(availability, func(t *testing.T) {
			store := newFakeStore()
			store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
				"a-goku-g": {AgentID: "a-goku-g", Availability: availability},
			}}
			if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
				t.Fatalf("route: %v", err)
			}
			for _, name := range append(append([]string{}, store.assigns...), store.reviewer...) {
				if name == "孙悟空游戏" {
					t.Fatalf("picked a %s seat: assigns=%v reviewer=%v", availability, store.assigns, store.reviewer)
				}
			}
		})
	}
}

func TestIronLawEveryPickSaysWhy(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeStore, *fakeJudge)
		want  PickReason
	}{
		{"confident verdict", func(*fakeStore, *fakeJudge) {}, PickReasonTier},
		{"tier label from a person", func(s *fakeStore, _ *fakeJudge) { s.issue.Labels = []string{"强"} }, PickReasonTier},
		// A judge that does not clear the bar leaves the rule table's tier.
		{"weak verdict", func(_ *fakeStore, j *fakeJudge) {
			j.verdict.ExecutorConfidence = 0.1
		}, PickReasonTier},
		{"no tier answer", func(_ *fakeStore, j *fakeJudge) { j.verdict.ExecutorTier = "" }, PickReasonTier},
		{"no model on", func(s *fakeStore, _ *fakeJudge) { s.settings = noModels() }, PickReasonFallback},
		{"held by a person, reviewer filled", func(s *fakeStore, _ *fakeJudge) {
			s.issue.AssigneeType, s.issue.AssigneeID, s.issue.AssigneeSource = "agent", "a-piccolo-g", SourceHuman
		}, PickReasonHuman},
		{"held on the person's words, reviewer filled", func(s *fakeStore, _ *fakeJudge) {
			s.issue.AssigneeType, s.issue.AssigneeID, s.issue.AssigneeSource = "agent", "a-piccolo-g", SourceQuote
		}, PickReasonQuote},
		{"held by routing, reviewer filled", func(s *fakeStore, _ *fakeJudge) {
			s.issue.AssigneeType, s.issue.AssigneeID, s.issue.AssigneeSource = "agent", "a-piccolo-g", SourceRouter
		}, PickReasonTier},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			judge := &fakeJudge{verdict: confidentVerdict()}
			tc.setup(store, judge)
			if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
				t.Fatalf("route: %v", err)
			}
			bodies := store.comments[KindAssignment]
			if len(bodies) != 1 {
				t.Fatalf("assignment comments = %d, want 1", len(bodies))
			}
			want := "## 自动选派\n\n**为什么是他**：" + string(tc.want)
			if !strings.HasPrefix(bodies[0], want) {
				t.Fatalf("comment does not open with %q:\n%s", want, bodies[0])
			}
		})
	}
}
