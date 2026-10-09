package routing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// A runtime analysis failure leaves every question unknown: the table's
// unknown row routes the ticket, and the judge is still asked.
func TestRuntimeAnalysisFailureStillRoutes(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	store.settings.Analysis.Source = AnalysisSourceRuntimeSubscription
	store.settings.Analysis.RuntimeID = "runtime-1"
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{err: errors.New("runtime offline")}
	r := newRouter(store, judge)
	r.Analyst = analyst
	d, err := r.decide(context.Background(), "ws", store.settings, store.issue, JudgeState{})
	if err != nil {
		t.Fatalf("runtime failure stalled the decision: %v", err)
	}
	if judge.callCount() != 1 {
		t.Fatalf("judge calls = %d, want 1", judge.callCount())
	}
	if d.Trace.Rule.ID != "unknown" || judge.assignedState().RuleTier != "medium" {
		t.Fatalf("rule = %q, judge rule tier = %q; want the unknown row at medium", d.Trace.Rule.ID, judge.assignedState().RuleTier)
	}
}

// A gateway failure, a timeout or a garbled reply never stalls the ticket and
// never lands it on the weakest rung: the questions are unknown, the table
// routes unknown to medium with a seat reviewer.
func TestAnalysisFailureRoutesAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"gateway", errors.New("gateway unavailable")},
		{"timeout", context.DeadlineExceeded},
		{"garbled", ErrJudgeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newCachingStore(analysisOnly())
			out := routeWith(t, store, &fakeJudge{}, &fakeAnalyst{err: tc.err})
			if out.Action != ActionAssigned {
				t.Fatalf("action = %q, want assigned", out.Action)
			}
			if len(store.assigns) != 1 || store.assigns[0] != "贝吉塔游戏" {
				t.Fatalf("assigns = %v, want the medium seat 贝吉塔游戏", store.assigns)
			}
			if len(store.reviewer) != 1 {
				t.Fatalf("reviewer slots = %v, want a seat", store.reviewer)
			}
			if out.Trace == nil || out.Trace.Tier != "medium" || out.Trace.AnalysisError == "" {
				t.Fatalf("trace = %+v, want medium with the analysis error", out.Trace)
			}
			if len(store.saved) != 0 {
				t.Error("a failed analysis was cached")
			}
			body := store.comments[KindAssignment][0]
			for _, want := range []string{"分析模型没答上", "改动范围、需求、出错代价、要人拍板：答不出", "「有一题答不出」→ 中档"} {
				if !strings.Contains(body, want) {
					t.Errorf("comment is missing %q:\n%s", want, body)
				}
			}
		})
	}
}

// fakeAnalyst is the analysis role's double. Like fakeJudge it counts calls,
// because "this combination did not call the model" is half of what the
// four-mode tests pin.
type fakeAnalyst struct {
	mu       sync.Mutex
	record   AnalysisRecord
	stuck    Advice
	err      error
	analyzes int
	stucks   int
	lastSt   AnalysisState
}

func (a *fakeAnalyst) Analyze(_ context.Context, _ Target, st AnalysisState) (AnalysisRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.analyzes++
	a.lastSt = st
	return a.record, a.err
}

func (a *fakeAnalyst) Stuck(_ context.Context, _ Target, st AnalysisState) (Advice, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stucks++
	a.lastSt = st
	return a.stuck, a.err
}

// cachingStore adds the optional analysis cache and discussion reader.
type cachingStore struct {
	*fakeStore
	saved      []AnalysisRecord
	discussion []string
}

func (s *cachingStore) SaveAnalysis(_ context.Context, _, _ string, rec AnalysisRecord) error {
	s.saved = append(s.saved, rec)
	// Mirrors the handler: the next read of the issue sees the record.
	s.issue.Analysis = &rec
	return nil
}

func (s *cachingStore) Discussion(context.Context, string, string) ([]string, error) {
	return s.discussion, nil
}

func boolPtr(b bool) *bool { return &b }

func analysisOnly() Settings {
	return Settings{
		Enabled: true, ConfidenceThreshold: 0.7,
		Analysis: &AnalysisSettings{Enabled: true, Model: "analysis-model"},
	}
}

func analysisAndJudge() Settings {
	s := analysisOnly()
	s.Model = "judge-model"
	s.JudgeEnabled = boolPtr(true)
	return s
}

func noModels() Settings {
	return Settings{Enabled: true, ConfidenceThreshold: 0.7, JudgeEnabled: boolPtr(false)}
}

// analysedFacts is a cross-module answer: the table's second row, strong
// with a seat reviewer.
func analysedFacts() AnalysisRecord {
	reply := map[string]json.RawMessage{
		"scope": json.RawMessage(`"3"`), "clarity": json.RawMessage(`1`),
		"risk": json.RawMessage(`"2"`), "needs_human": json.RawMessage(`"1"`),
	}
	f, answers := DefaultRules.read(reply)
	f.Summary = "改两个模块"
	return AnalysisRecord{Facts: f, Answers: answers, Source: FactsFromAnalysis}
}

func newCachingStore(settings Settings) *cachingStore {
	s := &cachingStore{fakeStore: newFakeStore()}
	s.settings = settings
	s.issue.Description = "完整的票面内容，判断模型看不到"
	s.issue.DescriptionSummary = "票面摘要"
	s.issue.ContentHash = ContentHash(s.issue.Title, s.issue.Description)
	return s
}

func routeWith(t *testing.T, store Store, judge Judge, analyst Analyst) Outcome {
	t.Helper()
	r := newRouter(store, judge)
	r.Analyst = analyst
	out, err := r.Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return out
}

func TestSettingsModeCoversFourCombinations(t *testing.T) {
	legacy := Settings{Enabled: true, Model: "m"}
	cases := []struct {
		name string
		s    Settings
		want Mode
	}{
		{"legacy block is judge-only", legacy, ModeJudge},
		{"nothing on", noModels(), ModeNone},
		{"analysis only", analysisOnly(), ModeAnalysis},
		{"both", analysisAndJudge(), ModeBoth},
	}
	for _, c := range cases {
		if got := c.s.Mode(); got != c.want {
			t.Errorf("%s: Mode() = %q, want %q", c.name, got, c.want)
		}
		if got := c.s.State(); got != StateEnabled {
			t.Errorf("%s: State() = %q, want enabled", c.name, got)
		}
	}
	// A role that is on without a model is incomplete, whichever it is.
	missing := analysisOnly()
	missing.Analysis.Model = ""
	if got := missing.State(); got != StateIncomplete {
		t.Errorf("analysis on without a model: State() = %q, want incomplete", got)
	}
	both := analysisAndJudge()
	both.Model = ""
	if got := both.State(); got != StateIncomplete {
		t.Errorf("judge on without a model: State() = %q, want incomplete", got)
	}
}

// 都不选: no model is called and both slots take the fallback rung.
func TestModeNoneCallsNoModelAndTakesTheFallback(t *testing.T) {
	store := newCachingStore(noModels())
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{record: analysedFacts()}

	out := routeWith(t, store, judge, analyst)
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want assigned", out.Action)
	}
	if judge.callCount() != 0 || analyst.analyzes != 0 {
		t.Fatalf("called a model with none on: judge=%d analyst=%d", judge.callCount(), analyst.analyzes)
	}
	if len(store.assigns) != 1 || len(store.reviewer) != 1 {
		t.Fatalf("fallback did not fill both slots: assigns=%v reviewer=%v", store.assigns, store.reviewer)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "没有启用分析或判断模型") {
		t.Errorf("comment does not say no model was on:\n%s", body)
	}
}

// 只分析: the analysis model answers the questions, the rule table picks the
// tier, the judge is never asked, and the answers are cached.
func TestModeAnalysisRuleTablePicksTheTier(t *testing.T) {
	store := newCachingStore(analysisOnly())
	judge := &fakeJudge{}
	analyst := &fakeAnalyst{record: analysedFacts()}

	out := routeWith(t, store, judge, analyst)
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want assigned", out.Action)
	}
	if judge.callCount() != 0 {
		t.Errorf("asked the judge %d times in analysis-only mode", judge.callCount())
	}
	if analyst.analyzes != 1 {
		t.Errorf("analysis calls = %d, want 1", analyst.analyzes)
	}
	if analyst.lastSt.Description != store.issue.Description {
		t.Error("the analysis model did not read the full body")
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want the strong seat 孙悟空游戏", store.assigns)
	}
	if len(store.saved) != 1 || store.saved[0].Hash != store.issue.ContentHash || store.saved[0].Model != "analysis-model" {
		t.Errorf("analysis not cached against content and model: %+v", store.saved)
	}
	if out.Trace == nil || out.Trace.Rule.ID != "cross_module" || out.Trace.Tier != "strong" || out.Trace.Seat != "孙悟空游戏" {
		t.Fatalf("trace = %+v, want the cross_module row, strong, 孙悟空游戏", out.Trace)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"分析模型 `analysis-model`", "改动范围：跨模块（答 3）", "第 2 行「跨模块」→ 强档，要验收", "改两个模块"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q:\n%s", want, body)
		}
	}
}

// 分析+判断: the judge decides from the facts and never sees the prose, and
// it cannot lower the table's tier.
func TestModeBothJudgeSeesFactsNotTheBody(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	weak := confidentVerdict()
	weak.ExecutorTier, weak.ExecutorConfidence = "weak", 1
	judge := &fakeJudge{verdict: weak}
	analyst := &fakeAnalyst{record: analysedFacts()}

	out := routeWith(t, store, judge, analyst)
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want assigned", out.Action)
	}
	if analyst.analyzes != 1 || judge.callCount() != 1 {
		t.Fatalf("calls: analyst=%d judge=%d, want 1 and 1", analyst.analyzes, judge.callCount())
	}
	st := judge.assignedState()
	if st.Facts == nil || st.Facts.Scope != ScopeCrossModule || st.RuleTier != "strong" {
		t.Fatalf("judge did not receive the facts and the rule tier: %+v %q", st.Facts, st.RuleTier)
	}
	if st.DescriptionSummary != "" {
		t.Errorf("judge saw the description in both-roles mode: %q", st.DescriptionSummary)
	}
	if out.Trace.Tier != "strong" || out.Trace.Judge == nil || out.Trace.Judge.Effect != JudgeIgnored {
		t.Fatalf("trace = %+v judge=%+v, want strong with the judge ignored", out.Trace, out.Trace.Judge)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "判断模型** `judge-model`：给的是弱档，没采用（判断模型只能往上调）") {
		t.Errorf("comment does not say the judge was overruled:\n%s", body)
	}
}

// 只判断 (old configs): nobody answers the questions, so the table's unknown
// row sets medium, and the judge reads the summary and may raise one rung.
func TestModeJudgeRoutesOnTheUnknownRow(t *testing.T) {
	store := newCachingStore(newFakeStore().settings)
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{record: analysedFacts()}

	out := routeWith(t, store, judge, analyst)
	if analyst.analyzes != 0 {
		t.Errorf("judge-only mode called the analysis model %d times", analyst.analyzes)
	}
	st := judge.assignedState()
	if st.Facts != nil {
		t.Errorf("judge-only mode passed facts nobody supplied: %+v", st.Facts)
	}
	if st.DescriptionSummary != "票面摘要" || st.RuleTier != "medium" {
		t.Errorf("judge state: summary %q rule tier %q", st.DescriptionSummary, st.RuleTier)
	}
	if len(store.saved) != 0 {
		t.Error("judge-only mode wrote an analysis cache")
	}
	// confidentVerdict asks for strong: one rung above medium, with a reason.
	if out.Trace.Rule.ID != "unknown" || out.Trace.Tier != "strong" || out.Trace.Judge.Effect != JudgeRaised {
		t.Fatalf("trace = %+v judge=%+v, want unknown row raised to strong", out.Trace, out.Trace.Judge)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want 孙悟空游戏", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "上调一档到强档（置信度 90%）：中等复杂度") {
		t.Errorf("comment does not show the raise:\n%s", body)
	}
}

// The judge raises by one rung at most, needs a reason and the threshold,
// and never lowers.
func TestJudgeOnlyRaisesOneRung(t *testing.T) {
	l := DefaultLadder
	base := func(tier string) decision {
		return decision{Verdict: Verdict{ExecutorTier: tier, Reviewer: ReviewerSeat}}
	}
	cases := []struct {
		name, rule, judge string
		conf              float64
		reason            string
		err               error
		want, effect      string
	}{
		{"strongest is capped at one rung", "weak", "strongest", 1, "很难", nil, "medium", JudgeRaised},
		{"one rung up", "medium", "strong", 0.9, "跨端", nil, "strong", JudgeRaised},
		{"lower is ignored", "strong", "weak", 1, "简单", nil, "strong", JudgeIgnored},
		{"equal agrees", "medium", "medium", 0.9, "", nil, "medium", JudgeAgreed},
		{"no reason", "medium", "strong", 0.9, " ", nil, "medium", JudgeIgnored},
		{"low confidence", "medium", "strong", 0.3, "跨端", nil, "medium", JudgeIgnored},
		{"off the ladder", "medium", "godlike", 1, "x", nil, "medium", JudgeIgnored},
		{"already strongest", "strongest", "strongest", 1, "x", nil, "strongest", JudgeAgreed},
		{"failed", "medium", "", 0, "", errUpstream, "medium", JudgeFailed},
	}
	for _, c := range cases {
		trace := &Trace{}
		v := Verdict{ExecutorTier: c.judge, ExecutorConfidence: c.conf, Reason: c.reason}
		d := base(c.rule).withJudge(v, c.err, 0.7, l, trace)
		if d.Verdict.ExecutorTier != c.want || trace.Judge.Effect != c.effect {
			t.Errorf("%s: tier %q effect %q, want %q %q", c.name, d.Verdict.ExecutorTier, trace.Judge.Effect, c.want, c.effect)
		}
	}
}

// A fresh cached analysis is reused; changing the content recomputes it.
func TestAnalysisIsCachedUntilTheContentChanges(t *testing.T) {
	store := newCachingStore(analysisOnly())
	analyst := &fakeAnalyst{record: analysedFacts()}
	r := newRouter(store, &fakeJudge{})
	r.Analyst = analyst
	ctx := context.Background()
	if _, err := r.decide(ctx, "ws", store.settings, store.issue, JudgeState{}); err != nil {
		t.Fatal(err)
	}
	d, err := r.decide(ctx, "ws", store.settings, store.issue, JudgeState{})
	if err != nil {
		t.Fatal(err)
	}
	if analyst.analyzes != 1 || !d.Cached {
		t.Fatalf("second decide re-analysed: calls=%d cached=%v", analyst.analyzes, d.Cached)
	}
	if !strings.Contains(decisionSourceLine(d, store.settings, DefaultLadder), "沿用上次答案") {
		t.Error("comment line does not say the analysis was reused")
	}

	store.issue.Description = "内容改了"
	store.issue.ContentHash = ContentHash(store.issue.Title, store.issue.Description)
	if _, err := r.decide(ctx, "ws", store.settings, store.issue, JudgeState{}); err != nil {
		t.Fatal(err)
	}
	if analyst.analyzes != 2 {
		t.Errorf("changed content did not recompute: calls=%d", analyst.analyzes)
	}

	// Switching the analysis model also recomputes.
	store.settings.Analysis.Model = "another-model"
	if _, err := r.decide(ctx, "ws", store.settings, store.issue, JudgeState{}); err != nil {
		t.Fatal(err)
	}
	if analyst.analyzes != 3 {
		t.Errorf("a different analysis model reused the old record: calls=%d", analyst.analyzes)
	}
}

// Facts supplied at creation skip the analysis call and go straight to the
// table.
func TestCreatorFactsSkipTheAnalysisCall(t *testing.T) {
	store := newCachingStore(analysisOnly())
	store.issue.Analysis = &AnalysisRecord{
		Facts:  Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskHigh},
		Source: FactsFromCreator,
		Hash:   store.issue.ContentHash,
	}
	analyst := &fakeAnalyst{record: analysedFacts()}
	routeWith(t, store, &fakeJudge{}, analyst)
	if analyst.analyzes != 0 {
		t.Fatalf("called the analysis model %d times despite creator facts", analyst.analyzes)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"建票时带上，未调用分析", "出错代价：高", "第 1 行「出错代价高」"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q:\n%s", want, body)
		}
	}

	// In both-roles mode the judge gets the creator's facts and still no
	// analysis call is made.
	both := newCachingStore(analysisAndJudge())
	both.issue.Analysis = store.issue.Analysis
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst2 := &fakeAnalyst{}
	routeWith(t, both, judge, analyst2)
	if analyst2.analyzes != 0 || judge.assignedState().Facts == nil {
		t.Errorf("both-roles with creator facts: analyses=%d facts=%v", analyst2.analyzes, judge.assignedState().Facts)
	}
}

func TestRuleTableLadder(t *testing.T) {
	cases := []struct {
		f        Facts
		want     string
		reviewer ReviewerKind
	}{
		{Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow}, "weak", ReviewerNone},
		{Facts{Scope: ScopeModule, Clarity: ClarityClear, Risk: RiskMedium}, "medium", ReviewerNone},
		{Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskLow}, "strong", ReviewerSeat},
		{Facts{Scope: ScopeSmall, Clarity: ClarityVague, Risk: RiskLow}, "strong", ReviewerSeat},
		{Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskHigh}, "strong", ReviewerSeat},
		{Facts{Scope: ScopeSmall, Clarity: Unknown, Risk: RiskLow}, "medium", ReviewerSeat},
		{UnknownFacts(), "medium", ReviewerSeat},
	}
	for _, c := range cases {
		_, row := DefaultRules.Match(c.f)
		v := row.Verdict(c.f)
		if v.ExecutorTier != c.want || v.Reviewer != c.reviewer {
			t.Errorf("Match(%+v) = %q/%q, want %q/%q", c.f, v.ExecutorTier, v.Reviewer, c.want, c.reviewer)
		}
	}
	human := Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow, NeedsHuman: true}
	if _, row := DefaultRules.Match(human); row.Verdict(human).Reviewer != ReviewerHuman {
		t.Error("needs_human facts did not route acceptance to a person")
	}
}

// Every combination, unknowns included: cross-module and unknown never land
// on the weakest rung. validate checks this at init; the test pins it.
func TestNoUnknownOrCrossModuleIsWeak(t *testing.T) {
	if err := DefaultRules.validate(DefaultLadder); err != nil {
		t.Fatal(err)
	}
	broken := DefaultRules
	broken.Rules = append([]Rule{{ID: "bad", Label: "坏", When: map[string][]string{"scope": {ScopeCrossModule}}, Tier: "weak", Reviewer: ReviewerNone}}, DefaultRules.Rules...)
	if err := broken.validate(DefaultLadder); err == nil {
		t.Error("a table that sends cross-module to weak passed validation")
	}
}

// The same facts give the same tier, every time.
func TestSameFactsSameTierTenTimes(t *testing.T) {
	for i := 0; i < 10; i++ {
		store := newCachingStore(analysisOnly())
		out := routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: analysedFacts()})
		if out.Trace.Tier != "strong" || store.assigns[0] != "孙悟空游戏" {
			t.Fatalf("run %d: tier %q seat %v", i, out.Trace.Tier, store.assigns)
		}
	}
}

// Blocked row: the analysis model writes "where is it stuck"; with both on
// the judge decides the branch and the summary is kept.
func TestBlockedAdviceCarriesTheStuckSummary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		settings    Settings
		judgeCalls  int
		wantComment string
	}{
		{"analysis only", analysisOnly(), 0, "卡在哪：等接口文档"},
		{"both", analysisAndJudge(), 1, "卡在哪：等接口文档"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newCachingStore(tc.settings)
			store.issue.Status = "blocked"
			store.issue.AssigneeType = "agent"
			store.issue.AssigneeID = "a-piccolo-g"
			store.discussion = []string{"member: 接口文档什么时候给？"}
			judge := &fakeJudge{advice: Advice{Cause: "human", Reason: "要人给文档"}}
			analyst := &fakeAnalyst{stuck: Advice{Cause: "human", Stuck: "等接口文档", Reason: "缺文档"}}
			out := routeWith(t, store, judge, analyst)
			if out.Action != ActionAdvised {
				t.Fatalf("action = %q, want advised", out.Action)
			}
			if analyst.stucks != 1 || judge.callCount() != tc.judgeCalls {
				t.Errorf("calls: stuck=%d judge=%d", analyst.stucks, judge.callCount())
			}
			if len(analyst.lastSt.Discussion) != 1 {
				t.Error("the analysis model did not read the discussion")
			}
			if body := store.comments[KindAdvice][0]; !strings.Contains(body, tc.wantComment) {
				t.Errorf("advice comment is missing %q:\n%s", tc.wantComment, body)
			}
		})
	}

	// Nothing on: no advice call, no comment.
	store := newCachingStore(noModels())
	store.issue.Status = "blocked"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	judge := &fakeJudge{}
	out := routeWith(t, store, judge, &fakeAnalyst{})
	if judge.callCount() != 0 || store.commentCount() != 0 || out.Action != ActionNoop {
		t.Errorf("mode none on blocked: action=%q calls=%d comments=%d", out.Action, judge.callCount(), store.commentCount())
	}
}
