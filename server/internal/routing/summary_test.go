package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type sentenceGen struct {
	out    string
	system string
}

func (g *sentenceGen) GenerateJSON(_ context.Context, _, system, _ string, _ float64, _ int64) (string, error) {
	g.system = system
	return g.out, nil
}

// The parking record's sentence is phrasing only: the judge returns the
// sentence, trimmed, and clips a long one.
func TestLLMJudgeSummarize(t *testing.T) {
	gen := &sentenceGen{out: `{"sentence": "  运行失败：分支没记上，\n送审被拒。 "}`}
	got, err := LLMJudge{Gen: gen}.Summarize(context.Background(), Target{Model: "m"}, SummaryFacts{Category: "stalled_delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "运行失败：分支没记上， 送审被拒。" {
		t.Fatalf("sentence = %q", got)
	}
	if !strings.Contains(gen.system, "must not be changed") {
		t.Errorf("prompt must pin the category")
	}

	gen.out = `{"sentence": "` + strings.Repeat("长", 120) + `"}`
	got, _ = LLMJudge{Gen: gen}.Summarize(context.Background(), Target{Model: "m"}, SummaryFacts{})
	if n := len([]rune(got)); n != 81 {
		t.Fatalf("clipped length = %d", n)
	}

	gen.out = `{"sentence": ""}`
	if _, err := (LLMJudge{Gen: gen}).Summarize(context.Background(), Target{Model: "m"}, SummaryFacts{}); !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("empty sentence err = %v", err)
	}
}

// A router without a store never reaches a model.
func TestRouterSummarizeWithoutStore(t *testing.T) {
	var r *Router
	if _, err := r.Summarize(context.Background(), "ws", SummaryFacts{}); !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
