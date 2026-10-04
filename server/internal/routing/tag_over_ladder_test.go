package routing

import (
	"context"
	"testing"
)

// A seat's tag is its rung even when ladder.json lists the same name on
// another rung. 特兰克斯 is tagged 中 here but sits on 强 in ladder.json; an
// escalation from it must land on 强, not skip to the strongest rung.
func TestEscalateReadsTheTagNotTheLadderName(t *testing.T) {
	store := newFakeStore()
	store.roster = map[string]Agent{
		"布尔玛":  {ID: "a-bulma", Name: "布尔玛", Tier: "strongest", Usage: UsageTight},
		"孙悟空":  {ID: "a-goku", Name: "孙悟空", Tier: "strong"},
		"特兰克斯": {ID: "a-trunks", Name: "特兰克斯", Tier: "medium", Usage: UsageAmple},
		"比克":   {ID: "a-piccolo", Name: "比克", Tier: "weak"},
	}
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-trunks"
	judge := &fakeJudge{verdict: Verdict{ExecutorTier: "nonexistent", ExecutorConfidence: 0.9}}

	esc, err := newRouter(store, judge).Escalate(context.Background(), "ws", "issue-1", "太难了")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !esc.Changed || esc.To != "孙悟空" {
		t.Fatalf("escalation = %+v, want one rung up to 孙悟空 — the tag says 特兰克斯 is 中", esc)
	}
}

// The model family comes from the seat's configured model, so a seat that
// ladder.json never heard of still counts as another house on its rung.
func TestSameTierAlternateReadsTheConfiguredModel(t *testing.T) {
	roster := map[string]Agent{
		"特兰克斯": {ID: "a-trunks", Name: "特兰克斯", Tier: "medium", Model: "gpt-6.1-sol"},
		"天津饭":  {ID: "a-tien", Name: "天津饭", Tier: "medium", Model: "claude-sonnet-5-5"},
	}
	holder := Seat{ID: "a-trunks", Name: "特兰克斯", TierKey: "medium"}
	alt, ok := DefaultLadder.SameTierAlternate(holder, "", roster)
	if !ok || alt.Name != "天津饭" {
		t.Fatalf("alternate = %+v ok=%v, want 天津饭 (anthropic vs openai)", alt, ok)
	}
}

func TestModelProvider(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":       "anthropic",
		"gpt-6.1-sol":           "openai",
		"grok-4.7":              "xai",
		"gemini-3.8-flash-high": "google",
		"command-code/deepseek%2Fdeepseek-v4.1-flash": "deepseek",
		"nexus-coder": "",
		"":            "",
	}
	for model, want := range cases {
		if got := ModelProvider(model); got != want {
			t.Errorf("ModelProvider(%q) = %q, want %q", model, got, want)
		}
	}
}
