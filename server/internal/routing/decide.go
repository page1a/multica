package routing

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Who picked the tier, for the decision comment.
const (
	DeciderNone = "none"
	// DeciderRule — the rule table decided, possibly raised one rung by the
	// judge (Trace.Judge says).
	DeciderRule = "rule"
)

// decision is the todo row's answer before any seat is looked up: a verdict,
// who gave it, and the facts it was given, when there were any.
type decision struct {
	Verdict Verdict
	Mode    Mode
	Decider string
	// Facts is nil only in ModeNone.
	Facts *Facts
	// FactsSource is FactsFromAnalysis or FactsFromCreator, empty when the
	// facts are all Unknown because nobody answered; Cached reports that the
	// analysis call was skipped because the record was fresh.
	FactsSource string
	Cached      bool
	// RaisedFrom is the judge's own executor tier when it answered below the
	// rule table and was overruled (DENE-1648); empty otherwise.
	RaisedFrom string
	// Trace is the decision log the comment and `issue route` print.
	Trace *Trace
}

// Trace is one decision, step by step (DENE-1677): every question and how its
// answer was read, the row that matched, what the judge said about it, and
// where the ticket went. The decision comment and `issue route --output json`
// print the same object.
type Trace struct {
	Questions []Answer `json:"questions"`
	// FactsSource is analysis, creator, or unanswered.
	FactsSource string `json:"facts_source"`
	Cached      bool   `json:"cached,omitempty"`
	// AnalysisError says why every question is unanswered, when one failed.
	AnalysisError string      `json:"analysis_error,omitempty"`
	Rule          TraceRule   `json:"rule"`
	Judge         *TraceJudge `json:"judge,omitempty"`
	// Tier is the executor tier after the judge; Seat and Reviewer are
	// filled once the slots are written.
	Tier     string `json:"tier"`
	Seat     string `json:"seat,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
}

// TraceRule is the matched row.
type TraceRule struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Label    string       `json:"label"`
	Tier     string       `json:"tier"`
	Reviewer ReviewerKind `json:"reviewer"`
}

// What the judge's answer did to the table's tier.
const (
	JudgeRaised  = "raised"  // one rung up, with its reason
	JudgeAgreed  = "agreed"  // it named the table's tier
	JudgeIgnored = "ignored" // below the table, unsure, no reason, or unknown tier
	JudgeFailed  = "failed"  // no answer
)

// TraceJudge is the judge's part. It can only ever raise.
type TraceJudge struct {
	Tier       string  `json:"tier,omitempty"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
	Effect     string  `json:"effect"`
	// Why explains an ignored or failed answer.
	Why string `json:"why,omitempty"`
}

// decide answers the todo row: facts from the analysis model's
// multiple-choice answers (or the creator's), the tier and reviewer from the
// rule table, and at most one rung more from the judge. An
// analysis model that fails never stalls the ticket: its questions are
// Unknown, and the table routes Unknown conservatively. The error is the
// judge's alone, when it is on and could not be reached.
func (r *Router) decide(ctx context.Context, workspaceID string, settings Settings, issue Issue, state JudgeState) (decision, error) {
	d := decision{Mode: settings.Mode()}
	if d.Mode == ModeNone {
		// No model is called. The zero verdict names no tier and carries no
		// confidence, so both slots take the ladder's fallback rung.
		d.Decider = DeciderNone
		return d, nil
	}
	rules := r.rules()
	trace := &Trace{FactsSource: "unanswered"}
	facts := UnknownFacts()

	rec, cached, err := r.analysis(ctx, workspaceID, settings, issue, state)
	switch {
	case err != nil:
		r.log().Warn("routing: analysis unanswered, routing on the rule table's unknown row",
			"workspace_id", workspaceID, "issue_id", issue.ID, "error", err)
		trace.AnalysisError = clipRunes(err.Error(), 160)
		trace.Questions = rules.UnknownAnswers()
	case rec != nil:
		facts = rec.Facts
		d.FactsSource, d.Cached = rec.Source, cached
		trace.FactsSource, trace.Cached = rec.Source, cached
		trace.Questions = rec.Answers
		if len(trace.Questions) == 0 {
			trace.Questions = rules.AnswersFor(facts)
		}
	default:
		// Judge-only mode with no creator facts: nobody was asked.
		trace.Questions = rules.UnknownAnswers()
	}
	d.Facts = &facts

	index, row := rules.Match(facts)
	d.Verdict, d.Decider = row.Verdict(facts), DeciderRule
	trace.Rule = TraceRule{Index: index, ID: row.ID, Label: row.Label, Tier: row.Tier, Reviewer: row.Reviewer}

	if settings.JudgeOn() && r.Judge != nil {
		if rec != nil {
			state.Facts = d.Facts
			if d.Mode == ModeBoth {
				// The judge sees the facts and not the prose: that is the split.
				state.DescriptionSummary = ""
			}
		}
		state.RuleTier = row.Tier
		v, jerr := r.Judge.Assign(ctx, settings.Target(), state)
		d = d.withJudge(v, jerr, settings.Threshold(), r.ladder(), trace)
		if jerr != nil {
			// A judge the workspace turned on and cannot reach keeps its
			// old contract: the breaker counts it and the ticket says so
			// once. Only the analysis side degrades to the unknown row.
			trace.Tier = d.Verdict.ExecutorTier
			d.Trace = trace
			return d, jerr
		}
	}
	trace.Tier = d.Verdict.ExecutorTier
	d.Trace = trace
	return d, nil
}

// withJudge folds the judge's answer into the table's verdict. The judge can
// raise the executor tier by one rung, and only with a confident answer and a
// reason; a stronger pick is capped at one rung, a weaker one is overruled.
// The reviewer seat follows the executor's seat, so it needs no raise of
// its own.
func (d decision) withJudge(v Verdict, err error, threshold float64, l Ladder, trace *Trace) decision {
	if err != nil {
		trace.Judge = &TraceJudge{Effect: JudgeFailed, Why: clipRunes(err.Error(), 160)}
		return d
	}
	got := strings.ToLower(strings.TrimSpace(v.ExecutorTier))
	reason := strings.TrimSpace(v.Reason)
	tj := &TraceJudge{Tier: got, Confidence: v.ExecutorConfidence, Reason: reason}
	trace.Judge = tj
	ruleTier := d.Verdict.ExecutorTier
	ruleRank, gotRank := tierRank(l, ruleTier), tierRank(l, got)
	switch {
	case gotRank < 0:
		tj.Effect, tj.Why = JudgeIgnored, "它点的档位不在阶梯上"
	case gotRank == ruleRank:
		tj.Effect = JudgeAgreed
	case gotRank > ruleRank:
		tj.Effect, tj.Why = JudgeIgnored, "判断模型只能往上调"
		d.RaisedFrom = got
	case v.ExecutorConfidence < threshold:
		tj.Effect, tj.Why = JudgeIgnored, "置信度 "+pct(v.ExecutorConfidence)+" 低于阈值 "+pct(threshold)
	case reason == "":
		tj.Effect, tj.Why = JudgeIgnored, "没写理由"
	case ruleRank <= 0:
		tj.Effect, tj.Why = JudgeIgnored, "已经是最强档"
	default:
		up := l.Tiers[ruleRank-1].Key
		tj.Effect = JudgeRaised
		d.Verdict.ExecutorTier = up
	}
	return d
}

// floorLine is the decision comment's line for a judge answer below the
// table.
func floorLine(d decision, l Ladder) string {
	if d.RaisedFrom == "" || d.Trace == nil {
		return ""
	}
	return "- **抬档**：判断模型给的是" + tierLabel(l, d.RaisedFrom) + "档，按规则（" + floorWhy(d.Trace.Rule, l) + "）抬到" + tierLabel(l, d.Verdict.ExecutorTier) + "档\n"
}

// floorWhy names the row that set the tier.
func floorWhy(r TraceRule, l Ladder) string {
	if r.Tier == l.Tiers[len(l.Tiers)-1].Key || r.ID == "default" {
		return "只有小改动、低风险、需求清楚才用弱档"
	}
	return r.Label + "至少" + tierLabel(l, r.Tier) + "档"
}

func tierLabel(l Ladder, key string) string {
	if t, ok := l.TierByKey(key); ok && t.Label != "" {
		return t.Label
	}
	return key
}

func (r *Router) rules() RuleTable {
	if len(r.Rules.Rules) == 0 {
		return DefaultRules
	}
	return r.Rules
}

func (r *Router) ladder() Ladder {
	if len(r.Ladder.Tiers) == 0 {
		return DefaultLadder
	}
	return r.Ladder
}

// analysis returns the facts for this ticket: the cached record while it is
// fresh, a creator's record in any mode, or a new analysis call when the
// analysis role is on. Nil means no facts take part — only possible in the
// judge-only mode. cached reports that a record was reused.
func (r *Router) analysis(ctx context.Context, workspaceID string, settings Settings, issue Issue, state JudgeState) (*AnalysisRecord, bool, error) {
	model := settings.AnalysisTarget().Model
	if rec := issue.Analysis; rec != nil && rec.Fresh(issue, model) {
		// A creator's record always counts; an analysis record only in a
		// mode that would have made the call.
		if rec.Source == FactsFromCreator || settings.AnalysisOn() {
			return rec, true, nil
		}
	}
	if !settings.AnalysisOn() {
		return nil, false, nil
	}
	if r.Analyst == nil {
		return nil, false, ErrJudgeUnavailable
	}
	failureKey := workspaceID + ":" + issue.ID + ":" + issue.ContentHash + ":" + model
	// A gateway failure keeps its historical behaviour: the next routing pass
	// may try it again.  The short suppression window is only for a runtime
	// probe, where an offline/empty-quota daemon would otherwise be hammered by
	// every status hook while its late result is still in flight.
	if settings.AnalysisTarget().UsesRuntime() {
		if at, ok := r.analysisFailures.Load(failureKey); ok && time.Since(at.(time.Time)) < 20*time.Second {
			return nil, false, ErrJudgeUnavailable
		}
	}
	requestCtx := WithAnalysisRequest(ctx, workspaceID, issue.ID, issue.ContentHash)
	// The questions are about the work, not about who does it: the seat,
	// quota and tier context stays with the judge.
	state.Candidates, state.Seats, state.ProviderQuotas = nil, nil, nil
	state.RoutingPolicy, state.PolicyPrompt = RoutingPolicy{}, ""
	rec, err := r.Analyst.Analyze(requestCtx, settings.AnalysisTarget(), AnalysisState{
		JudgeState:  state,
		Description: issue.Description,
	})
	if err != nil {
		if settings.AnalysisTarget().UsesRuntime() {
			r.analysisFailures.Store(failureKey, time.Now())
		}
		return nil, false, err
	}
	r.analysisFailures.Delete(failureKey)
	rec.Hash = issue.ContentHash
	rec.Model = model
	if cache, ok := r.Store.(AnalysisCache); ok && rec.Hash != "" {
		// A cache that cannot be written only costs the next call; it never
		// turns a good answer into a failed route.
		if err := cache.SaveAnalysis(ctx, workspaceID, issue.ID, rec); err != nil {
			r.log().Warn("routing: analysis cache write failed",
				"workspace_id", workspaceID, "issue_id", issue.ID, "error", err)
		}
	}
	return &rec, false, nil
}

// PreAnalyze warms the per-issue facts cache without dispatching the issue.
// Handlers call it after content writes, including backlog writes; a later
// route reuses the same record and therefore does not add model latency.
func (r *Router) PreAnalyze(ctx context.Context, workspaceID string, issueID string) error {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return err
	}
	if !settings.AnalysisOn() {
		return nil
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return err
	}
	_, _, err = r.analysis(ctx, workspaceID, settings, issue, JudgeState{Title: issue.Title, DescriptionSummary: issue.DescriptionSummary, Status: issue.Status})
	return err
}

// advise asks whichever roles are on for the blocked row's advice. ok is
// false in ModeNone: nothing to ask, and an advice comment with no advice in
// it is noise.
func (r *Router) advise(ctx context.Context, workspaceID string, settings Settings, issue Issue, state JudgeState) (Advice, bool, error) {
	mode := settings.Mode()
	if mode == ModeNone {
		return Advice{}, false, nil
	}
	if mode == ModeJudge {
		a, err := r.Judge.Unblock(ctx, settings.Target(), state)
		return a, true, err
	}
	if r.Analyst == nil {
		return Advice{}, true, ErrJudgeUnavailable
	}
	var discussion []string
	if reader, ok := r.Store.(DiscussionReader); ok {
		// Unreadable discussion is missing context, not a failed route.
		discussion, _ = reader.Discussion(ctx, workspaceID, issue.ID)
	}
	stuck, err := r.Analyst.Stuck(ctx, settings.AnalysisTarget(), AnalysisState{
		JudgeState:  state,
		Description: issue.Description,
		Discussion:  discussion,
	})
	if err != nil || mode == ModeAnalysis {
		return stuck, true, err
	}
	// Both roles: the analysis model says where it is stuck, the judge says
	// what that means for the tier. The judge's reading wins on the branch;
	// the summary is the analysis model's.
	if rec := issue.Analysis; rec != nil && rec.Fresh(issue, settings.AnalysisTarget().Model) {
		facts := rec.Facts
		state.Facts = &facts
		state.DescriptionSummary = ""
	}
	state.Stuck = stuck.Stuck
	a, err := r.Judge.Unblock(ctx, settings.Target(), state)
	if err != nil {
		return a, true, err
	}
	a.Stuck = stuck.Stuck
	return a, true, nil
}

// decisionSourceLine is the decision trace as the comment prints it: each
// question and how it was read, the matched row, and the judge's part.
func decisionSourceLine(d decision, settings Settings, l Ladder) string {
	var b strings.Builder
	if d.Decider == DeciderNone {
		b.WriteString("- **定档**：没有启用分析或判断模型，直接走兜底档\n")
		return b.String()
	}
	t := d.Trace
	if t == nil {
		return ""
	}
	b.WriteString("- **答题**：")
	switch {
	case t.FactsSource == FactsFromCreator:
		b.WriteString("建票时带上，未调用分析")
	case t.FactsSource == FactsFromAnalysis && t.Cached:
		b.WriteString("分析模型 " + modelName(settings.AnalysisTarget().Model) + "，沿用上次答案（票内容没变）")
	case t.FactsSource == FactsFromAnalysis:
		b.WriteString("分析模型 " + modelName(settings.AnalysisTarget().Model))
	case t.AnalysisError != "":
		b.WriteString("分析模型没答上")
	default:
		b.WriteString("没开分析模型")
	}
	b.WriteString("\n")
	if labels, silent := silentAnswers(t.Questions); silent {
		b.WriteString("  - " + strings.Join(labels, "、") + "：" + unknownLabel + "\n")
	} else {
		writeAnswers(&b, t.Questions)
	}
	if d.Facts != nil {
		if s := strings.TrimSpace(d.Facts.Summary); s != "" {
			b.WriteString("- **概要**：" + s + "\n")
		}
	}
	reviewer := map[ReviewerKind]string{ReviewerSeat: "要验收", ReviewerNone: "不需要验收"}[t.Rule.Reviewer]
	b.WriteString(fmt.Sprintf("- **规则**：第 %d 行「%s」→ %s档，%s\n", t.Rule.Index, t.Rule.Label, tierLabel(l, t.Rule.Tier), reviewer))
	if j := t.Judge; j != nil {
		b.WriteString("- **判断模型** " + modelName(settings.Model) + "：")
		switch j.Effect {
		case JudgeRaised:
			b.WriteString(fmt.Sprintf("上调一档到%s档（置信度 %s）：%s", tierLabel(l, t.Tier), pct(j.Confidence), j.Reason))
		case JudgeAgreed:
			b.WriteString("同意" + tierLabel(l, j.Tier) + "档")
		case JudgeFailed:
			b.WriteString("没答上，按规则档走")
		default:
			b.WriteString("给的是" + tierLabel(l, j.Tier) + "档，没采用（" + j.Why + "）")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func modelName(m string) string {
	if strings.TrimSpace(m) == "" {
		return "（未命名）"
	}
	return "`" + strings.TrimSpace(m) + "`"
}

// silentAnswers reports that nothing came back for any question, so the
// comment can say it in one line.
func silentAnswers(answers []Answer) ([]string, bool) {
	labels := make([]string, 0, len(answers))
	for _, a := range answers {
		if a.Choice > 0 || a.Raw != "" {
			return nil, false
		}
		labels = append(labels, a.Label)
	}
	return labels, len(labels) > 0
}

func writeAnswers(b *strings.Builder, answers []Answer) {
	for _, a := range answers {
		b.WriteString("  - " + a.Label + "：" + a.ValueLabel)
		switch {
		case a.Choice > 0 && a.Raw != "":
			b.WriteString("（答 " + a.Raw + "）")
		case a.Raw != "":
			b.WriteString("（答「" + a.Raw + "」，读不出编号）")
		}
		b.WriteString("\n")
	}
}
