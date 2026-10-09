package routing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// This file is the rule table (DENE-1677).
//
// DENE-1647 and DENE-1648 had the same facts and the judge gave one weak 100%
// and the other strong 99%: a prompt is a suggestion, and a model that holds
// the final say can always ignore it. The tier is therefore decided here, by
// data: the analysis model answers numbered multiple-choice questions, only
// the number is read, and the first matching row of rules.json names the
// tier. An answer that cannot be read is 未知, never a guess, and 未知 never
// lands on the weakest rung. The judge, when it is on, may only raise the
// table's tier by one rung, with a reason.

//go:embed rules.json
var rulesJSON []byte

// Unknown is the value of a question nobody could answer: no number, a number
// outside the options, a timeout, or a reply that was not JSON at all.
const Unknown = "unknown"

// unknownLabel is how an Unknown answer reads. Not 未知: on a decision
// comment 未知 is the direction word, and a direction is never unknown
// (DENE-1477).
const unknownLabel = "答不出"

// Question is one closed question the analysis model is asked.
type Question struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

// Option is one numbered choice. Its number is its position, from 1.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

// Rule is one row of the table. A condition lists the values a fact may hold
// for the row to match; a fact the row does not name matches anything. A row
// with no conditions is the catch-all.
type Rule struct {
	ID         string              `json:"id"`
	Label      string              `json:"label"`
	When       map[string][]string `json:"when,omitempty"`
	AnyUnknown bool                `json:"any_unknown,omitempty"`
	Tier       string              `json:"tier"`
	// Reviewer is "seat" or "none". A person is never a row's answer: the
	// 要人拍板 question turns any row into a seat that pings the person.
	Reviewer ReviewerKind `json:"reviewer"`
}

// RuleTable is rules.json: the questions and the rows.
type RuleTable struct {
	Questions []Question `json:"questions"`
	Rules     []Rule     `json:"rules"`
}

// DefaultRules is the shipped table. A malformed rules.json is a programming
// error, like a malformed ladder.json, so it panics at init.
var DefaultRules = mustLoadRules(rulesJSON, DefaultLadder)

func mustLoadRules(raw []byte, l Ladder) RuleTable {
	var t RuleTable
	if err := json.Unmarshal(raw, &t); err != nil {
		panic(fmt.Sprintf("routing: rules.json is malformed: %v", err))
	}
	if err := t.validate(l); err != nil {
		panic("routing: rules.json: " + err.Error())
	}
	return t
}

// factValues is the closed set each question must map onto: Facts is a
// struct, so the table can relabel and reorder options but not invent facts.
var factValues = map[string][]string{
	"scope":       {ScopeSmall, ScopeModule, ScopeCrossModule},
	"clarity":     {ClarityClear, ClarityVague},
	"risk":        {RiskLow, RiskMedium, RiskHigh},
	"needs_human": {"no", "yes"},
}

func (t RuleTable) validate(l Ladder) error {
	if len(t.Questions) != len(factValues) {
		return fmt.Errorf("want %d questions, got %d", len(factValues), len(t.Questions))
	}
	for _, q := range t.Questions {
		allowed, ok := factValues[q.Key]
		if !ok {
			return fmt.Errorf("unknown question %q", q.Key)
		}
		if len(q.Options) != len(allowed) {
			return fmt.Errorf("question %s must offer exactly %v", q.Key, allowed)
		}
		for _, o := range q.Options {
			if !contains(allowed, o.Value) || o.Label == "" {
				return fmt.Errorf("question %s has option %q outside %v or without a label", q.Key, o.Value, allowed)
			}
		}
	}
	if len(t.Rules) == 0 {
		return fmt.Errorf("no rules")
	}
	for i, r := range t.Rules {
		if r.ID == "" || r.Label == "" {
			return fmt.Errorf("rule %d has no id or label", i+1)
		}
		if _, ok := l.TierByKey(r.Tier); !ok {
			return fmt.Errorf("rule %s names tier %q, which the ladder does not have", r.ID, r.Tier)
		}
		if r.Reviewer != ReviewerSeat && r.Reviewer != ReviewerNone {
			return fmt.Errorf("rule %s reviewer must be seat or none", r.ID)
		}
		for key, values := range r.When {
			allowed, ok := factValues[key]
			if !ok || key == "needs_human" {
				return fmt.Errorf("rule %s conditions on %q", r.ID, key)
			}
			for _, v := range values {
				if !contains(allowed, v) {
					return fmt.Errorf("rule %s: %s cannot be %q", r.ID, key, v)
				}
			}
		}
	}
	if last := t.Rules[len(t.Rules)-1]; len(last.When) > 0 || last.AnyUnknown {
		return fmt.Errorf("the last rule must be the catch-all")
	}
	// The two promises this table exists to keep, checked on every
	// combination rather than trusted to the row order: an unanswered
	// question never lands on the weakest rung, and neither does a
	// cross-module change.
	weakest := l.Tiers[len(l.Tiers)-1].Key
	for _, scope := range append(factValues["scope"], Unknown) {
		for _, clarity := range append(factValues["clarity"], Unknown) {
			for _, risk := range append(factValues["risk"], Unknown) {
				for _, human := range []string{"no", "yes", Unknown} {
					f := Facts{Scope: scope, Clarity: clarity, Risk: risk, NeedsHuman: human == "yes"}
					if human == Unknown {
						f.Unanswered = []string{"needs_human"}
					}
					_, r := t.Match(f)
					if r.Tier == weakest && (f.AnyUnknown() || scope == ScopeCrossModule) {
						return fmt.Errorf("scope=%s clarity=%s risk=%s needs_human=%s lands on the weakest rung via rule %s", scope, clarity, risk, human, r.ID)
					}
				}
			}
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Match returns the first row these facts satisfy, and its 1-based position.
// The catch-all guarantees a row.
func (t RuleTable) Match(f Facts) (int, Rule) {
	for i, r := range t.Rules {
		if r.matches(f) {
			return i + 1, r
		}
	}
	last := t.Rules[len(t.Rules)-1]
	return len(t.Rules), last
}

func (r Rule) matches(f Facts) bool {
	if r.AnyUnknown && !f.AnyUnknown() {
		return false
	}
	for key, values := range r.When {
		if !contains(values, f.value(key)) {
			return false
		}
	}
	return true
}

// Verdict is the row's answer for these facts. Confidence is 1: a rule is
// not a guess, and the threshold exists for answers that are.
func (r Rule) Verdict(f Facts) Verdict {
	v := Verdict{
		ExecutorTier:       r.Tier,
		ExecutorConfidence: 1,
		Reviewer:           r.Reviewer,
		ReviewerConfidence: 1,
	}
	// A seat row names no rung for the reviewer: the ladder picks it next to
	// the executor, so a raised executor carries its check along.
	if f.NeedsHuman {
		v.Reviewer, v.ReviewerTier = ReviewerHuman, ""
	}
	return v
}

// questionsPrompt renders the questions for the analysis system prompt.
func (t RuleTable) questionsPrompt() string {
	var b strings.Builder
	for _, q := range t.Questions {
		b.WriteString(q.Key + ": " + q.Question + "\n")
		for i, o := range q.Options {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, o.Text))
		}
	}
	return b.String()
}

// Answer is one question's trace: what was asked, what came back, and what
// it was read as.
type Answer struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Raw is the reply as given, clipped. Empty when nothing came back.
	Raw string `json:"raw,omitempty"`
	// Choice is the option number read from Raw; 0 when none could be.
	Choice int `json:"choice"`
	// Value is the option's value, or Unknown.
	Value      string `json:"value"`
	ValueLabel string `json:"value_label"`
}

// read turns a reply object into facts. Each value is read for its first
// number and nothing else; a missing key, a value without a number, or a
// number outside the options is Unknown.
func (t RuleTable) read(reply map[string]json.RawMessage) (Facts, []Answer) {
	f := Facts{Scope: Unknown, Clarity: Unknown, Risk: Unknown}
	answers := make([]Answer, 0, len(t.Questions))
	for _, q := range t.Questions {
		a := Answer{Key: q.Key, Label: q.Label, Value: Unknown, ValueLabel: unknownLabel}
		raw := rawAnswer(reply[q.Key])
		a.Raw = clipRunes(oneLine(raw), 40)
		if n := firstNumber(raw); n >= 1 && n <= len(q.Options) {
			a.Choice, a.Value, a.ValueLabel = n, q.Options[n-1].Value, q.Options[n-1].Label
		}
		f.set(q.Key, a.Value)
		answers = append(answers, a)
	}
	return f, answers
}

// UnknownAnswers is the trace of an analysis that produced nothing.
func (t RuleTable) UnknownAnswers() []Answer {
	_, answers := t.read(nil)
	return answers
}

// AnswersFor describes facts that did not come from a reply (a creator's, or
// a record cached before answers were kept) in the trace's shape.
func (t RuleTable) AnswersFor(f Facts) []Answer {
	answers := make([]Answer, 0, len(t.Questions))
	for _, q := range t.Questions {
		a := Answer{Key: q.Key, Label: q.Label, Value: f.value(q.Key), ValueLabel: unknownLabel}
		for i, o := range q.Options {
			if o.Value == a.Value {
				a.Choice, a.ValueLabel = i+1, o.Label
			}
		}
		if a.Choice == 0 {
			a.Value = Unknown
		}
		answers = append(answers, a)
	}
	return answers
}

func rawAnswer(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(string(raw))
}

// firstNumber is the first run of ASCII digits in s, or 0.
func firstNumber(s string) int {
	start := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[start:end])
	if err != nil {
		return 0
	}
	return n
}

// oneLine flattens control characters so a raw answer stays on one line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// RuleTableView is the table as the settings page and the CLI read it: the
// questions, and each row with its rung's label.
type RuleTableView struct {
	Questions []Question `json:"questions"`
	Rules     []RuleView `json:"rules"`
}

// RuleView is one row for a reader.
type RuleView struct {
	Index int                 `json:"index"`
	ID    string              `json:"id"`
	Label string              `json:"label"`
	When  map[string][]string `json:"when,omitempty"`
	// AnyUnknown: the row matches when any question was 答不出.
	AnyUnknown bool         `json:"any_unknown,omitempty"`
	Tier       string       `json:"tier"`
	TierLabel  string       `json:"tier_label"`
	Reviewer   ReviewerKind `json:"reviewer"`
}

// View renders the table for a reader.
func (t RuleTable) View(l Ladder) RuleTableView {
	out := RuleTableView{Questions: t.Questions, Rules: make([]RuleView, 0, len(t.Rules))}
	for i, r := range t.Rules {
		out.Rules = append(out.Rules, RuleView{
			Index: i + 1, ID: r.ID, Label: r.Label, When: r.When, AnyUnknown: r.AnyUnknown,
			Tier: r.Tier, TierLabel: tierLabel(l, r.Tier), Reviewer: r.Reviewer,
		})
	}
	return out
}
