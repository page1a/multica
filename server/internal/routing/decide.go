package routing

import (
	"context"
	"strings"
)

// Who picked the tier, for the decision comment.
const (
	DeciderNone     = "none"
	DeciderAnalysis = "analysis"
	DeciderRule     = "rule"
	DeciderJudge    = "judge"
)

// decision is the todo row's answer before any seat is looked up: a verdict,
// who gave it, and the facts it was given, when there were any.
type decision struct {
	Verdict Verdict
	Mode    Mode
	Decider string
	// Facts is nil when no facts took part.
	Facts *Facts
	// FactsSource is FactsFromAnalysis or FactsFromCreator; Cached reports
	// that the analysis call was skipped because the record was fresh.
	FactsSource string
	Cached      bool
}

// decide asks whichever roles are on for the todo row's verdict. It is the
// only place the four combinations differ; everything after it — thresholds,
// the ladder, the conditional writes — is the same for all of them.
func (r *Router) decide(ctx context.Context, workspaceID string, settings Settings, issue Issue, state JudgeState) (decision, error) {
	d := decision{Mode: settings.Mode()}
	if d.Mode == ModeNone {
		// No model is called. The zero verdict names no tier and carries no
		// confidence, so both slots take the ladder's fallback rung.
		d.Decider = DeciderNone
		return d, nil
	}

	rec, cached, err := r.analysis(ctx, workspaceID, settings, issue, state)
	if err != nil {
		return d, err
	}
	if rec != nil {
		facts := rec.Facts
		d.Facts, d.FactsSource, d.Cached = &facts, rec.Source, cached
	}

	switch d.Mode {
	case ModeAnalysis:
		if rec.Verdict != nil {
			d.Verdict, d.Decider = *rec.Verdict, DeciderAnalysis
		} else {
			d.Verdict, d.Decider = RuleVerdict(rec.Facts), DeciderRule
		}
		return d, nil
	case ModeBoth:
		// The judge sees the facts and not the prose: that is the split.
		state.Facts = d.Facts
		state.DescriptionSummary = ""
	case ModeJudge:
		// Facts a creator supplied are free to pass along; the judge still
		// reads the summary, exactly as it did before the split.
		state.Facts = d.Facts
	}
	v, err := r.Judge.Assign(ctx, settings.Target(), state)
	if err != nil {
		return d, err
	}
	d.Verdict, d.Decider = v, DeciderJudge
	return d, nil
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
	rec, err := r.Analyst.Analyze(ctx, settings.AnalysisTarget(), AnalysisState{
		JudgeState:  state,
		Description: issue.Description,
	})
	if err != nil {
		return nil, false, err
	}
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

// decisionSourceLine is the comment line saying which model decided and on
// what.
func decisionSourceLine(d decision, settings Settings) string {
	var b strings.Builder
	b.WriteString("- **定档**：")
	switch d.Decider {
	case DeciderNone:
		b.WriteString("没有启用分析或判断模型，直接走兜底档")
	case DeciderAnalysis:
		b.WriteString("分析模型 " + modelName(settings.AnalysisTarget().Model) + " 读完票直接选档")
	case DeciderRule:
		b.WriteString("按建票时带的事实套规则选档，没有调用模型")
	case DeciderJudge:
		if d.Facts != nil && d.Mode == ModeBoth {
			b.WriteString("判断模型 " + modelName(settings.Model) + " 按整理好的事实定档")
		} else {
			b.WriteString("判断模型 " + modelName(settings.Model) + " 读票定档")
		}
	}
	b.WriteString("\n")
	if d.Facts != nil {
		b.WriteString("- **事实**：" + FactsLine(*d.Facts))
		switch {
		case d.FactsSource == FactsFromCreator:
			b.WriteString("（建票时带上，未调用分析）")
		case d.Cached:
			b.WriteString("（沿用上次分析，票内容没变）")
		default:
			b.WriteString("（分析模型整理）")
		}
		b.WriteString("\n")
		if s := strings.TrimSpace(d.Facts.Summary); s != "" {
			b.WriteString("- **概要**：" + s + "\n")
		}
	}
	return b.String()
}

func modelName(m string) string {
	if strings.TrimSpace(m) == "" {
		return "（未命名）"
	}
	return "`" + strings.TrimSpace(m) + "`"
}
