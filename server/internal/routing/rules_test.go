package routing

import (
	"errors"
	"strings"
	"testing"
)

// Only the option number is read; anything else in the value is ignored, and
// a value with no usable number is 答不出 for that question alone.
func TestParseAnalysisReadsOnlyTheNumber(t *testing.T) {
	rec, err := ParseAnalysisResult(`{"scope":"2 — one module","clarity":1,"risk":"9","needs_human":"cross_module","summary":"改设置页"}`, "m")
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Facts
	if f.Scope != ScopeModule || f.Clarity != ClarityClear || f.Risk != Unknown || f.NeedsHuman {
		t.Fatalf("facts = %+v", f)
	}
	if !f.AnyUnknown() || rec.Facts.Summary != "改设置页" {
		t.Fatalf("facts = %+v, want risk and needs_human unknown", f)
	}
	if len(rec.Answers) != 4 || rec.Answers[0].Choice != 2 || rec.Answers[2].Choice != 0 || rec.Answers[2].Raw != "9" {
		t.Fatalf("answers = %+v", rec.Answers)
	}
	// An out-of-range number lands on the unknown row: medium with a check.
	_, row := DefaultRules.Match(f)
	if row.ID != "unknown" || row.Tier != "medium" || row.Reviewer != ReviewerSeat {
		t.Fatalf("row = %+v, want the unknown row", row)
	}
}

// A word where a number belongs is not a guess either: the old free-text
// answer ("small") reads as 答不出, never as weak.
func TestParseAnalysisWordsAreUnknown(t *testing.T) {
	rec, err := ParseAnalysisResult(`{"scope":"small","clarity":"clear","risk":"low","needs_human":false}`, "m")
	if err != nil {
		t.Fatal(err)
	}
	_, row := DefaultRules.Match(rec.Facts)
	if row.Tier == "weak" {
		t.Fatalf("free-text answers landed on weak via %s", row.ID)
	}
	if !strings.Contains(decisionSourceLine(decision{Decider: DeciderRule, Facts: &rec.Facts, Trace: &Trace{FactsSource: FactsFromAnalysis, Questions: rec.Answers}}, analysisOnly(), DefaultLadder), "（答「small」，读不出编号）") {
		t.Error("trace does not show the unreadable answer")
	}
}

// A reply that is not a JSON object fails the call, so it is retried later
// and never cached; decide turns the failure into all-unknown.
func TestParseAnalysisGarbageIsAnError(t *testing.T) {
	for _, raw := range []string{"", "我觉得是小改动", "[1,2,3]", "null"} {
		if _, err := ParseAnalysisResult(raw, "m"); !errors.Is(err, ErrJudgeUnavailable) {
			t.Errorf("%q: err = %v, want ErrJudgeUnavailable", raw, err)
		}
	}
}

// The table as the settings page and CLI read it.
func TestRuleTableView(t *testing.T) {
	v := DefaultRules.View(DefaultLadder)
	if len(v.Questions) != 4 || len(v.Rules) != len(DefaultRules.Rules) {
		t.Fatalf("view = %+v", v)
	}
	last := v.Rules[len(v.Rules)-1]
	if last.Index != len(v.Rules) || last.TierLabel == "" || len(last.When) != 0 {
		t.Fatalf("catch-all = %+v", last)
	}
}
