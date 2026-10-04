package routing

import (
	"context"
	"strings"
	"testing"
)

func loadSeat(id, name string, running int) LoadSeat {
	return LoadSeat{
		Seat:         Seat{ID: id, Name: name, TierKey: "strong", TierLabel: "强"},
		Availability: AvailabilityAvailable,
		Running:      running,
	}
}

func TestPickLoad(t *testing.T) {
	base := Seat{ID: "a-goku", Name: "孙悟空", TierKey: "strong", TierLabel: "强"}
	cases := []struct {
		name  string
		base  Seat
		seats []LoadSeat
		want  string // the seat id the rule lands on
		moved bool
	}{
		{name: "only one seat", base: base, seats: []LoadSeat{loadSeat("a-goku", "孙悟空", 3)}, want: "a-goku"},
		{name: "all idle keeps the ladder's pick", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 0), loadSeat("a-krillin", "克林", 0), loadSeat("a-gohan", "孙悟饭", 0),
		}, want: "a-goku"},
		{name: "all equally busy keeps the ladder's pick", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 2), loadSeat("a-krillin", "克林", 2),
		}, want: "a-goku"},
		{name: "busy base, idle sibling takes it", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1), loadSeat("a-krillin", "克林", 0),
		}, want: "a-krillin", moved: true},
		{name: "all busy, the least busy takes it", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 3), loadSeat("a-krillin", "克林", 2), loadSeat("a-gohan", "孙悟饭", 1),
		}, want: "a-gohan", moved: true},
		{name: "tie among the idle goes by the old order", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1), loadSeat("a-gohan", "孙悟饭", 0), loadSeat("a-krillin", "克林", 0),
		}, want: "a-krillin", moved: true},
		{name: "idle sibling offline", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat {
				s := loadSeat("a-krillin", "克林", 0)
				s.Availability = AvailabilityUnreachable
				return s
			}(),
		}, want: "a-goku"},
		{name: "idle sibling out of quota", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat {
				s := loadSeat("a-krillin", "克林", 0)
				s.Availability = AvailabilityQuotaExhausted
				return s
			}(),
		}, want: "a-goku"},
		{name: "unknown availability still counts", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat {
				s := loadSeat("a-krillin", "克林", 0)
				s.Availability = AvailabilityUnknown
				return s
			}(),
		}, want: "a-krillin", moved: true},
		{name: "idle seat on another rung is not in the cell", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat { s := loadSeat("a-krillin", "克林", 0); s.Seat.TierKey = "medium"; return s }(),
		}, want: "a-goku"},
		{name: "idle seat of another direction is not in the cell", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat { s := loadSeat("a-goten-g", "悟天游戏", 0); s.Seat.Direction = "游戏"; return s }(),
		}, want: "a-goku"},
		{name: "a demoted idle seat stays behind a busy one", base: base, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1),
			func() LoadSeat { s := loadSeat("a-krillin", "克林", 0); s.Demoted = true; return s }(),
		}, want: "a-goku"},
		{name: "an idle tight seat does not take work from a busy normal one", base: base, seats: []LoadSeat{
			func() LoadSeat { s := loadSeat("a-goku", "孙悟空", 2); s.UsageRank = 1; return s }(),
			func() LoadSeat { s := loadSeat("a-gohan", "孙悟饭", 0); s.UsageRank = 2; return s }(),
		}, want: "a-goku"},
		{name: "an idle normal seat shares with a busy ample pick", base: base, seats: []LoadSeat{
			func() LoadSeat { s := loadSeat("a-goku", "孙悟空", 2); s.UsageRank = 0; return s }(),
			func() LoadSeat { s := loadSeat("a-trunks", "特兰克斯", 0); s.UsageRank = 1; return s }(),
			func() LoadSeat { s := loadSeat("a-gohan", "孙悟饭", 0); s.UsageRank = 2; return s }(),
		}, want: "a-trunks", moved: true},
		{name: "all tight seats share among themselves", base: base, seats: []LoadSeat{
			func() LoadSeat { s := loadSeat("a-goku", "孙悟空", 1); s.UsageRank = 2; return s }(),
			func() LoadSeat { s := loadSeat("a-gohan", "孙悟饭", 0); s.UsageRank = 2; return s }(),
		}, want: "a-gohan", moved: true},
		{name: "upshifted base is left alone", base: Seat{ID: "a-goku", Name: "孙悟空", TierKey: "strong", Upshifted: true}, seats: []LoadSeat{
			loadSeat("a-goku", "孙悟空", 1), loadSeat("a-krillin", "克林", 0),
		}, want: "a-goku"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PickLoad(LoadSnapshot{Base: tc.base, Seats: tc.seats})
			if got.Seat.ID != tc.want || got.Moved != tc.moved {
				t.Fatalf("pick = %+v, want %s moved=%v", got, tc.want, tc.moved)
			}
			if got.Moved && !strings.Contains(got.Detail(), tc.base.Name+" 正在跑") {
				t.Fatalf("detail %q does not name the busy seat", got.Detail())
			}
		})
	}
}

// The confident verdict picks 孙悟空游戏 (strong). A second strong 游戏 seat,
// 悟天游戏, is idle while 孙悟空游戏 holds two runs.
func loadStore(enabled bool) *fakeStore {
	store := newFakeStore()
	store.settings.PreferIdle = enabled
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	store.roster["悟天游戏"] = Agent{ID: "a-goten-g", Name: "悟天游戏", Tier: "strong"}
	store.roster["孙悟空游戏"] = Agent{ID: "a-goku-g", Name: "孙悟空游戏", Tier: "strong"}
	store.facts = RoutingFacts{Running: map[string]int{"a-goku-g": 2}}
	return store
}

func TestLoadShadowKeepsTheLadderPick(t *testing.T) {
	store := loadStore(false)
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
	if !strings.Contains(body, "影子运行") || !strings.Contains(body, "按新规则会选 悟天游戏（负载：孙悟空游戏 正在跑 2 个活，悟天游戏 空着）") {
		t.Fatalf("shadow line missing:\n%s", body)
	}
}

func TestLoadOnWritesTheIdleSeat(t *testing.T) {
	store := loadStore(true)
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "悟天游戏" {
		t.Fatalf("switch on should pick the idle seat: %v", store.assigns)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if !strings.Contains(body, "**为什么是他**：负载 —— 孙悟空游戏 正在跑 2 个活，悟天游戏 空着") {
		t.Fatalf("pick reason should be 负载:\n%s", body)
	}
	if strings.Contains(body, "影子运行") {
		t.Fatalf("switch on must not print the shadow line:\n%s", body)
	}
}

func TestLoadIdleEverywhereSaysNothing(t *testing.T) {
	store := loadStore(false)
	store.facts = RoutingFacts{}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if strings.Contains(body, "负载") {
		t.Fatalf("an idle cell must not add a 负载 line:\n%s", body)
	}
}

// With both switches on, 接着做 wins and 负载 stays quiet.
func TestContinuationBeatsLoad(t *testing.T) {
	store := loadStore(true)
	store.settings.PreferContinuation = true
	store.issue.ParentIssueID = "parent-1"
	store.issue.Related = []RelatedTicket{{Identifier: "DENE-7", Relation: RelationPreviousStage, ExecutorID: "a-bulma-g"}}
	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "布尔玛游戏" {
		t.Fatalf("接着做 should win over 负载: %v", store.assigns)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	if strings.Contains(body, "负载") {
		t.Fatalf("负载 must stay quiet when 接着做 placed the seat:\n%s", body)
	}
}
