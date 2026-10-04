package routing

import (
	"context"
	"strings"
	"testing"
)

func contSeat(id, name, tier, label, direction string) ContinuationSeat {
	return ContinuationSeat{
		Seat:         Seat{ID: id, Name: name, TierKey: tier, TierLabel: label, Direction: direction},
		OnRoster:     true,
		Availability: AvailabilityAvailable,
	}
}

func TestPickContinuation(t *testing.T) {
	order := []string{"strongest", "strong", "medium", "weak"}
	prev := RelatedTicket{Identifier: "DENE-1", Relation: RelationPreviousStage, ExecutorID: "a-goku"}
	parent := RelatedTicket{Identifier: "DENE-0", Relation: RelationParent, ExecutorID: "a-bulma"}
	batch := RelatedTicket{Identifier: "DENE-2", Relation: RelationSameBatch, ExecutorID: "a-vegeta"}
	seats := func(mut func(map[string]ContinuationSeat)) map[string]ContinuationSeat {
		m := map[string]ContinuationSeat{
			"a-goku":   contSeat("a-goku", "孙悟空", "strong", "强", ""),
			"a-bulma":  contSeat("a-bulma", "布尔玛", "strongest", "最强", ""),
			"a-vegeta": contSeat("a-vegeta", "贝吉塔", "medium", "中", ""),
		}
		if mut != nil {
			mut(m)
		}
		return m
	}
	cases := []struct {
		name     string
		related  []RelatedTicket
		seats    map[string]ContinuationSeat
		required string
		dir      string
		want     string // agent id, "" for no pick
		skipWord string // a word the skip reason must carry
	}{
		{name: "no related ticket", related: nil, seats: seats(nil), required: "strong"},
		{name: "related ticket held by nobody", related: []RelatedTicket{{Identifier: "DENE-1", Relation: RelationPreviousStage}}, seats: seats(nil), required: "strong"},
		{name: "same rung continues", related: []RelatedTicket{prev}, seats: seats(nil), required: "strong", want: "a-goku"},
		{name: "stronger rung continues", related: []RelatedTicket{prev}, seats: seats(nil), required: "medium", want: "a-goku"},
		{name: "tier too low", related: []RelatedTicket{batch}, seats: seats(nil), required: "strong", skipWord: "档位不够"},
		{name: "offline", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityUnreachable
			m["a-goku"] = s
		}), required: "strong", skipWord: "不在线"},
		{name: "runtime dead", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityDead
			m["a-goku"] = s
		}), required: "strong", skipWord: "不在线"},
		{name: "disabled", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityDisabled
			m["a-goku"] = s
		}), required: "strong", skipWord: "停用"},
		{name: "off the roster", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.OnRoster = false
			m["a-goku"] = s
		}), required: "strong", skipWord: "停用"},
		{name: "quota exhausted", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityQuotaExhausted
			m["a-goku"] = s
		}), required: "strong", skipWord: "额度耗尽"},
		{name: "unknown availability still continues", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityUnknown
			m["a-goku"] = s
		}), required: "strong", want: "a-goku"},
		{name: "wrong direction", related: []RelatedTicket{prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Seat.Direction = "出海"
			m["a-goku"] = s
		}), required: "strong", dir: "游戏", skipWord: "方向不对"},
		{name: "generic seat serves a directed ticket", related: []RelatedTicket{prev}, seats: seats(nil), required: "strong", dir: "游戏", want: "a-goku"},
		{name: "several candidates: previous stage wins over parent and batch", related: []RelatedTicket{batch, parent, prev}, seats: seats(nil), required: "medium", want: "a-goku"},
		{name: "several candidates: first one out, next relation takes it", related: []RelatedTicket{batch, parent, prev}, seats: seats(func(m map[string]ContinuationSeat) {
			s := m["a-goku"]
			s.Availability = AvailabilityQuotaExhausted
			m["a-goku"] = s
		}), required: "strong", want: "a-bulma", skipWord: "额度耗尽"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PickContinuation(ContinuationSnapshot{
				Related: tc.related, Seats: tc.seats, Direction: tc.dir,
				RequiredTier: tc.required, TierOrder: order,
			})
			if tc.want == "" {
				if got.OK {
					t.Fatalf("picked %s, want no pick", got.Seat.ID)
				}
			} else if !got.OK || got.Seat.ID != tc.want {
				t.Fatalf("pick = %+v, want %s", got, tc.want)
			}
			if tc.skipWord != "" && !strings.Contains(strings.Join(got.Skipped, "；"), tc.skipWord) {
				t.Fatalf("skipped = %v, want a reason with %q", got.Skipped, tc.skipWord)
			}
			if got.OK && !strings.Contains(got.Detail(), got.From.Identifier) {
				t.Fatalf("detail %q does not name %s", got.Detail(), got.From.Identifier)
			}
		})
	}
}

// The confident verdict picks 孙悟空游戏 (strong). The previous stage was
// done by 布尔玛游戏 (strongest), which qualifies.
func continuationStore(enabled bool) *fakeStore {
	store := newFakeStore()
	store.settings.PreferContinuation = enabled
	store.issue.ParentIssueID = "parent-1"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	store.issue.Related = []RelatedTicket{{Identifier: "DENE-7", Relation: RelationPreviousStage, ExecutorID: "a-bulma-g"}}
	return store
}

func TestContinuationShadowKeepsTheLadderPick(t *testing.T) {
	store := continuationStore(false)
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("shadow mode changed the pick: %v", store.assigns)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "**为什么是他**：档位") {
		t.Fatalf("shadow pick should still be 档位:\n%s", body)
	}
	if !strings.Contains(body, "影子运行") || !strings.Contains(body, "按新规则会选 布尔玛游戏（接着做：DENE-7（上一阶段）的执行人）") {
		t.Fatalf("shadow line missing:\n%s", body)
	}
}

func TestContinuationOnWritesThePreviousExecutor(t *testing.T) {
	store := continuationStore(true)
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "布尔玛游戏" {
		t.Fatalf("switch on should continue with the previous executor: %v", store.assigns)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "**为什么是他**：接着做 —— DENE-7（上一阶段）的执行人") {
		t.Fatalf("pick reason should be 接着做:\n%s", body)
	}
	if strings.Contains(body, "影子运行") {
		t.Fatalf("switch on must not print the shadow line:\n%s", body)
	}
}

func TestContinuationOnFallsBackWhenThePreviousSeatIsOut(t *testing.T) {
	store := continuationStore(true)
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
		"a-bulma-g": {AgentID: "a-bulma-g", Availability: AvailabilityQuotaExhausted},
	}}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("an exhausted previous seat must not be picked: %v", store.assigns)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "额度耗尽") || !strings.Contains(body, "**为什么是他**：档位") {
		t.Fatalf("comment should say why continuation was skipped:\n%s", body)
	}
}

func TestContinuationNeverOverridesAPersonsTierLabel(t *testing.T) {
	store := continuationStore(true)
	store.issue.Labels = []string{"中"}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "贝吉塔游戏" {
		t.Fatalf("a person's tier label must win: %v", store.assigns)
	}
}
