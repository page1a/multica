package routing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestRuntimeAnalysisFailureFallsBackToJudge(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	store.settings.Analysis.Source = AnalysisSourceRuntimeSubscription
	store.settings.Analysis.RuntimeID = "runtime-1"
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{err: errors.New("runtime offline")}
	r := newRouter(store, judge)
	r.Analyst = analyst
	if _, err := r.decide(context.Background(), "ws", store.settings, store.issue, JudgeState{}); err != nil {
		t.Fatalf("runtime failure did not fall back: %v", err)
	}
	if judge.callCount() != 1 {
		t.Fatalf("judge calls = %d, want 1", judge.callCount())
	}
}

func TestGatewayAnalysisFailureIsNotRetriedAsJudge(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{err: errors.New("gateway unavailable")}
	r := newRouter(store, judge)
	r.Analyst = analyst
	if _, err := r.decide(context.Background(), "ws", store.settings, store.issue, JudgeState{}); err == nil {
		t.Fatal("gateway failure unexpectedly fell back to judge")
	}
	if judge.callCount() != 0 {
		t.Fatalf("judge calls = %d, want 0", judge.callCount())
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

func analysedFacts() AnalysisRecord {
	v := confidentVerdict()
	return AnalysisRecord{
		Facts:   Facts{Scope: ScopeModule, Clarity: ClarityClear, Risk: RiskMedium, Summary: "改一个模块"},
		Verdict: &v, Source: FactsFromAnalysis,
	}
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

// 只分析: the analysis model picks the tier, the judge is never asked, and
// the result is cached.
func TestModeAnalysisPicksTheTierWithoutTheJudge(t *testing.T) {
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
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"分析模型 `analysis-model` 读完票直接选档", "改动范围", "改一个模块"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q:\n%s", want, body)
		}
	}
}

// The threshold gates the analysis model's own confidence too.
func TestModeAnalysisLowConfidenceTakesTheFallback(t *testing.T) {
	store := newCachingStore(analysisOnly())
	rec := analysedFacts()
	low := confidentVerdict()
	low.ExecutorConfidence = 0.2
	low.ExecutorTier = "weak"
	rec.Verdict = &low
	routeWith(t, store, &fakeJudge{}, &fakeAnalyst{record: rec})
	// The weak pick is ignored: the executor lands on the fallback rung.
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Fatalf("assigns = %v, want the fallback 孙悟空游戏", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "兜底档") || !strings.Contains(body, "20%") {
		t.Errorf("comment does not report the low confidence:\n%s", body)
	}
}

// 分析+判断: the judge decides from the facts and never sees the prose.
func TestModeBothJudgeSeesFactsNotTheBody(t *testing.T) {
	store := newCachingStore(analysisAndJudge())
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{record: analysedFacts()}

	out := routeWith(t, store, judge, analyst)
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want assigned", out.Action)
	}
	if analyst.analyzes != 1 || judge.callCount() != 1 {
		t.Fatalf("calls: analyst=%d judge=%d, want 1 and 1", analyst.analyzes, judge.callCount())
	}
	st := judge.assignedState()
	if st.Facts == nil || st.Facts.Scope != ScopeModule {
		t.Fatalf("judge did not receive the facts: %+v", st.Facts)
	}
	if st.DescriptionSummary != "" {
		t.Errorf("judge saw the description in both-roles mode: %q", st.DescriptionSummary)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "判断模型 `judge-model` 按整理好的事实定档") {
		t.Errorf("comment does not credit the judge with the facts:\n%s", body)
	}
}

// 只判断: exactly the old behaviour — no analysis call, the judge reads the
// summary.
func TestModeJudgeIsTheOldPath(t *testing.T) {
	store := newCachingStore(newFakeStore().settings)
	judge := &fakeJudge{verdict: confidentVerdict()}
	analyst := &fakeAnalyst{record: analysedFacts()}

	routeWith(t, store, judge, analyst)
	if analyst.analyzes != 0 {
		t.Errorf("judge-only mode called the analysis model %d times", analyst.analyzes)
	}
	st := judge.assignedState()
	if st.Facts != nil {
		t.Errorf("judge-only mode passed facts nobody supplied: %+v", st.Facts)
	}
	if st.DescriptionSummary != "票面摘要" {
		t.Errorf("judge did not read the summary: %q", st.DescriptionSummary)
	}
	if len(store.saved) != 0 {
		t.Error("judge-only mode wrote an analysis cache")
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
	if !strings.Contains(decisionSourceLine(d, store.settings), "沿用上次分析") {
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

// Facts supplied at creation skip the analysis call; in analysis-only mode
// the tier comes from the rule.
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
	for _, want := range []string{"按建票时带的事实套规则选档", "建票时带上，未调用分析"} {
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

func TestRuleVerdictLadder(t *testing.T) {
	cases := []struct {
		f    Facts
		want string
	}{
		{Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow}, "weak"},
		{Facts{Scope: ScopeModule, Clarity: ClarityClear, Risk: RiskMedium}, "medium"},
		{Facts{Scope: ScopeCrossModule, Clarity: ClarityClear, Risk: RiskLow}, "strong"},
		{Facts{Scope: ScopeSmall, Clarity: ClarityVague, Risk: RiskLow}, "strong"},
	}
	for _, c := range cases {
		if got := RuleVerdict(c.f).ExecutorTier; got != c.want {
			t.Errorf("RuleVerdict(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
	if RuleVerdict(Facts{Scope: ScopeSmall, Clarity: ClarityClear, Risk: RiskLow, NeedsHuman: true}).Reviewer != ReviewerHuman {
		t.Error("needs_human facts did not route acceptance to a person")
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
