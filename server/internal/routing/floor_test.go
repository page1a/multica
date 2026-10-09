package routing

import (
	"strings"
	"testing"
)

// DENE-1648: a model answering below the rule's reading of the same facts is
// raised to the rule, and the decision comment says so.
func TestModelTierBelowRuleFloorIsRaised(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	analyst := &fakeAnalyst{record: AnalysisRecord{
		Facts:  Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskMedium},
		Source: FactsFromAnalysis,
	}}
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "weak", ExecutorConfidence: 1,
		Reviewer: ReviewerSeat, ReviewerTier: "medium", ReviewerConfidence: 1,
	}}
	out := routeWith(t, store, judge, analyst)
	if out.Tier != "strong" || out.JudgedTier != "weak" {
		t.Fatalf("tier = %q judged = %q, want strong raised from weak", out.Tier, out.JudgedTier)
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "strong" {
		t.Fatalf("executor = %+v, want a strong seat", out.ExecutorWritten)
	}
	body := strings.Join(store.comments[KindAssignment], "\n")
	for _, want := range []string{"判断模型给的是弱档", "跨模块至少强档", "抬到强档", "按规则下限抬到的强档"} {
		if !strings.Contains(body, want) {
			t.Fatalf("decision comment lacks %q:\n%s", want, body)
		}
	}
}

// The table's tier is the floor and the judge's ceiling is one rung above it:
// strong on small/low/clear facts is capped at medium, weak on module facts
// stays at the table's medium.
func TestModelTierAtOrAboveRuleFloorStands(t *testing.T) {
	small := Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow}
	_, row := DefaultRules.Match(small)
	for tier, want := range map[string]string{"weak": "weak", "strong": "medium"} {
		trace := &Trace{}
		d := decision{Decider: DeciderRule, Facts: &small, Verdict: row.Verdict(small)}.
			withJudge(Verdict{ExecutorTier: tier, ExecutorConfidence: 1, Reason: "看过"}, nil, 0.7, DefaultLadder, trace)
		if d.Verdict.ExecutorTier != want || d.RaisedFrom != "" {
			t.Fatalf("%s on small/low/clear facts became %+v", tier, d)
		}
	}
	module := Facts{Scope: ScopeModule, Clarity: ClarityClear, Risk: RiskLow}
	index, mrow := DefaultRules.Match(module)
	trace := &Trace{Rule: TraceRule{Index: index, ID: mrow.ID, Label: mrow.Label, Tier: mrow.Tier}}
	d := decision{Decider: DeciderRule, Facts: &module, Verdict: mrow.Verdict(module), Trace: trace}.
		withJudge(Verdict{ExecutorTier: "weak", ExecutorConfidence: 1}, nil, 0.7, DefaultLadder, trace)
	if d.Verdict.ExecutorTier != "medium" || d.RaisedFrom != "weak" {
		t.Fatalf("weak on module facts = %+v, want held at medium", d)
	}
	if got := floorLine(d, DefaultLadder); !strings.Contains(got, "只有小改动、低风险、需求清楚才用弱档") {
		t.Fatalf("floor line = %q", got)
	}
}
