package routing

import (
	"context"
	"math"
	"strconv"
	"time"
)

// 从结果里学 (DENE-1722): the rule table tiers a ticket from its text and never
// hears whether it was right. Two real events say it was too low — the
// executor escalated, or acceptance held the work — and both are recorded
// against the class the ticket was tiered in: direction × tier × the
// analysis facts. When enough of a class's recent tickets were judged low,
// the next ticket of that class goes one rung higher.
//
// Only events count, never prose: an escalation is the escalate call, a hold
// is a comment carrying `verdict: hold`.
//
// The rule ships behind a workspace switch that is off by default. While it
// is off the table's pick is written unchanged and the assignment comment
// says what the rule would have done, like 接着做 and 负载.

// Defaults for the two settings, and the sample a class needs before its
// rate means anything: two escalations out of three tickets is noise.
const (
	DefaultLearnLowRate    = 0.3
	DefaultLearnWindowDays = 30
	LearnMinSample         = 5
	maxLearnWindowDays     = 365
)

// LowRate is the share of a class's tickets that must have been judged low
// before the class is raised. Out of range falls back to the default.
func (s Settings) LowRate() float64 {
	r := s.LearnLowRate
	if math.IsNaN(r) || r <= 0 || r > 1 {
		return DefaultLearnLowRate
	}
	return r
}

// LearnWindowDaysOrDefault is how far back the rate looks, in days.
func (s Settings) LearnWindowDaysOrDefault() int {
	d := s.LearnWindowDays
	if math.IsNaN(d) || d < 1 || d > maxLearnWindowDays {
		return DefaultLearnWindowDays
	}
	return int(d)
}

// Signals a ticket can carry.
const (
	SignalEscalated = "escalated"
	SignalHeld      = "held"
)

// OutcomeClass is what "the same kind of ticket" means.
type OutcomeClass struct {
	Direction string `json:"direction"`
	Tier      string `json:"tier"`
	Scope     string `json:"scope"`
	Clarity   string `json:"clarity"`
	Risk      string `json:"risk"`
}

// ClassOf is the class of a ticket tiered by the rule table.
func ClassOf(scene Scene, tier string, f Facts) OutcomeClass {
	return OutcomeClass{Direction: scene.Label(), Tier: tier, Scope: f.Scope, Clarity: f.Clarity, Risk: f.Risk}
}

// ClassStats is one class over the window.
type ClassStats struct {
	Class     OutcomeClass `json:"class"`
	Total     int          `json:"total"`
	Escalated int          `json:"escalated"`
	Held      int          `json:"held"`
	// Low is the tickets with either signal.
	Low int `json:"low"`
}

// LearnPick is the rule's answer for one ticket.
type LearnPick struct {
	// Raise is true when the class clears the sample and the rate.
	Raise      bool       `json:"raise"`
	Stats      ClassStats `json:"stats"`
	From       string     `json:"from"`
	To         string     `json:"to,omitempty"`
	ToLabel    string     `json:"to_label,omitempty"`
	WindowDays int        `json:"window_days"`
}

// PickLearned applies the rule to one class's stats. It is pure. A class on
// the strongest rung has nowhere to go and is never raised.
func PickLearned(st ClassStats, l Ladder, rate float64, windowDays int) LearnPick {
	p := LearnPick{Stats: st, From: st.Class.Tier, WindowDays: windowDays}
	rank := tierRank(l, st.Class.Tier)
	if rank <= 0 || st.Total < LearnMinSample || float64(st.Low) < rate*float64(st.Total) {
		return p
	}
	up := l.Tiers[rank-1]
	p.Raise, p.To, p.ToLabel = true, up.Key, tierLabel(l, up.Key)
	return p
}

// Detail is the readable reason, e.g.
// "同类票近 30 天 4/10 张判低（升档 3、打回 1），上调一档到强档".
func (p LearnPick) Detail() string {
	if !p.Raise {
		return ""
	}
	return "同类票近 " + strconv.Itoa(p.WindowDays) + " 天 " + strconv.Itoa(p.Stats.Low) + "/" + strconv.Itoa(p.Stats.Total) +
		" 张判低（升档 " + strconv.Itoa(p.Stats.Escalated) + "、打回 " + strconv.Itoa(p.Stats.Held) + "），上调一档到" + p.ToLabel + "档"
}

// LearnLine is the assignment comment's line about the rule, or "" when there
// is nothing to add: the class is not raised, or the raised seat was written
// and the first line already says why. applied is whether the raise moved
// the pick; explained is whether the written seat came from it.
func LearnLine(p LearnPick, enabled, applied, explained bool) string {
	switch {
	case !p.Raise || (applied && explained):
		return ""
	case !enabled:
		return "**从结果里学（影子运行：开关没开，实际选择没改）**：按新规则会上调一档——" + p.Detail() + "。"
	case applied:
		return "**从结果里学**：已上调一档——" + p.Detail() + "。"
	}
	return "**从结果里学**：该上调一档（" + p.Detail() + "），但" + p.ToLabel + "档这里没有能接活的席位，按原档派。"
}

// learned reads the class's stats and applies the rule. A read failure is
// not a routing failure: the table's pick stands and nothing is said.
func (r *Router) learned(ctx context.Context, workspaceID string, settings Settings, ladder Ladder, class OutcomeClass) LearnPick {
	days := settings.LearnWindowDaysOrDefault()
	stats, err := r.Store.OutcomeStats(ctx, workspaceID, time.Now().AddDate(0, 0, -days))
	if err != nil {
		r.log().Warn("routing: outcome stats unreadable, table pick stands", "workspace_id", workspaceID, "error", err)
		return LearnPick{From: class.Tier, WindowDays: days}
	}
	st := ClassStats{Class: class}
	for _, s := range stats {
		if s.Class == class {
			st = s
			break
		}
	}
	return PickLearned(st, ladder, settings.LowRate(), days)
}

// LearningReport is what `multica workspace routing learning` and the
// settings page read: the switch, its thresholds, and every class seen in
// the window with what the rule would do to its next ticket.
type LearningReport struct {
	Enabled    bool        `json:"enabled"`
	Mode       string      `json:"mode"`
	LowRate    float64     `json:"low_rate"`
	WindowDays int         `json:"window_days"`
	MinSample  int         `json:"min_sample"`
	Classes    []LearnPick `json:"classes"`
}

// Learning builds the report for a workspace.
func (r *Router) Learning(ctx context.Context, workspaceID string) (LearningReport, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return LearningReport{}, err
	}
	days := settings.LearnWindowDaysOrDefault()
	rep := LearningReport{
		Enabled: settings.LearnFromOutcomes, Mode: "shadow",
		LowRate: settings.LowRate(), WindowDays: days, MinSample: LearnMinSample,
		Classes: []LearnPick{},
	}
	if rep.Enabled {
		rep.Mode = "on"
	}
	stats, err := r.Store.OutcomeStats(ctx, workspaceID, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return rep, err
	}
	ladder := r.Ladder.For(settings)
	for _, st := range stats {
		rep.Classes = append(rep.Classes, PickLearned(st, ladder, rep.LowRate, days))
	}
	return rep, nil
}

// recordOutcome keeps the class a ticket was tiered in. Losing the record
// only means this ticket does not count; routing goes on.
func (r *Router) recordOutcome(ctx context.Context, workspaceID, issueID string, class OutcomeClass) {
	if err := r.Store.RecordOutcome(ctx, workspaceID, issueID, class); err != nil {
		r.log().Warn("routing: outcome class not recorded", "workspace_id", workspaceID, "issue_id", issueID, "error", err)
	}
}
