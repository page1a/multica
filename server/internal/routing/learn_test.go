package routing

import (
	"context"
	"strings"
	"testing"
)

func TestPickLearned(t *testing.T) {
	class := OutcomeClass{Direction: "游戏", Tier: "strong", Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskMedium}
	cases := []struct {
		name       string
		tier       string
		total, low int
		raise      bool
		to         string
	}{
		{"too few tickets", "strong", LearnMinSample - 1, LearnMinSample - 1, false, ""},
		{"rate below threshold", "strong", 10, 2, false, ""},
		{"rate reaches threshold", "strong", 10, 3, true, "strongest"},
		{"strongest has nowhere to go", "strongest", 10, 10, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := class
			c.Tier = tc.tier
			p := PickLearned(ClassStats{Class: c, Total: tc.total, Low: tc.low, Escalated: tc.low}, DefaultLadder, DefaultLearnLowRate, 30)
			if p.Raise != tc.raise || p.To != tc.to {
				t.Fatalf("raise=%v to=%q, want raise=%v to=%q", p.Raise, p.To, tc.raise, tc.to)
			}
		})
	}
	p := PickLearned(ClassStats{Class: class, Total: 10, Low: 4, Escalated: 3, Held: 1}, DefaultLadder, 0.3, 30)
	if got, want := p.Detail(), "同类票近 30 天 4/10 张判低（升档 3、打回 1），上调一档到"; !strings.HasPrefix(got, want) {
		t.Errorf("Detail() = %q, want prefix %q", got, want)
	}
}

func TestLearnSettingsDefaults(t *testing.T) {
	if got := (Settings{}).LowRate(); got != DefaultLearnLowRate {
		t.Errorf("LowRate() = %v", got)
	}
	if got := (Settings{LearnLowRate: 2}).LowRate(); got != DefaultLearnLowRate {
		t.Errorf("out of range LowRate() = %v", got)
	}
	if got := (Settings{LearnWindowDays: 7}).LearnWindowDaysOrDefault(); got != 7 {
		t.Errorf("LearnWindowDaysOrDefault() = %v", got)
	}
	if got := (Settings{LearnWindowDays: 9999}).LearnWindowDaysOrDefault(); got != DefaultLearnWindowDays {
		t.Errorf("out of range window = %v", got)
	}
}

// learningStore is a workspace whose strong-tier cross-module class was
// escalated or held on 4 of its last 10 tickets.
func learningStore(enabled bool) *cachingStore {
	settings := analysisOnly()
	settings.LearnFromOutcomes = enabled
	store := newCachingStore(settings)
	f := analysedFacts().Facts
	store.outcomeStats = []ClassStats{{
		Class: ClassOf(Scene{Domains: []string{"游戏"}}, "strong", f),
		Total: 10, Low: 4, Escalated: 3, Held: 1,
	}}
	return store
}

func TestLearnShadowKeepsTheTablePick(t *testing.T) {
	store := learningStore(false)
	routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: analysedFacts()})
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("assigns = %v, want the table's strong seat 孙悟空游戏", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"**为什么是他**：档位 —— 规则表定的", "从结果里学（影子运行：开关没开，实际选择没改）", "按新规则会上调一档", "4/10 张判低（升档 3、打回 1）"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q:\n%s", want, body)
		}
	}
	if c, ok := store.outcomes["issue-1"]; !ok || c.Tier != "strong" || c.Direction != "游戏" {
		t.Errorf("outcome class not recorded as the table's tier: %+v", store.outcomes)
	}
}

func TestLearnOnRaisesOneRung(t *testing.T) {
	store := learningStore(true)
	routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: analysedFacts()})
	if len(store.assigns) != 1 || store.assigns[0] == "孙悟空游戏" {
		t.Fatalf("assigns = %v, want the seat one rung above 孙悟空游戏", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "**为什么是他**：档位 —— 同类票近 30 天 4/10 张判低") {
		t.Errorf("first line does not give the learned reason:\n%s", body)
	}
	if strings.Contains(body, "影子运行：开关没开，实际选择没改）**：按新规则会上调") {
		t.Errorf("shadow line printed with the switch on:\n%s", body)
	}
	// The class is the one the table judged, not the raised one: the rate
	// measures the table.
	if c := store.outcomes["issue-1"]; c.Tier != "strong" {
		t.Errorf("recorded tier = %q, want the table's strong", c.Tier)
	}
}

func TestLearnStatsFailureLeavesThePick(t *testing.T) {
	store := learningStore(true)
	store.errOn["stats"] = context.DeadlineExceeded
	routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: analysedFacts()})
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("assigns = %v, want the table pick when stats are unreadable", store.assigns)
	}
}

func TestLearnSmallClassSaysNothing(t *testing.T) {
	store := learningStore(false)
	store.outcomeStats[0].Total, store.outcomeStats[0].Low = 3, 3
	routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: analysedFacts()})
	if body := store.comments[KindAssignment][0]; strings.Contains(body, "从结果里学") {
		t.Errorf("a class under the sample still spoke:\n%s", body)
	}
}

// The escalate call is the signal itself, recorded whether or not a stronger
// seat takes the ticket.
func TestEscalateMarksTheTicketUnderjudged(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	if _, err := newRouter(store, &fakeJudge{}).Escalate(context.Background(), "ws", "issue-1", "太难了"); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if len(store.underjudged) != 1 || store.underjudged[0] != "issue-1:"+SignalEscalated {
		t.Fatalf("underjudged = %v, want the escalation", store.underjudged)
	}
}
