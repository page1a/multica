package main

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
)

func TestRuleConditionReadsInQuestionLabels(t *testing.T) {
	view := routing.DefaultRules.View(routing.DefaultLadder)
	labels := map[string]map[string]string{}
	for _, q := range view.Questions {
		labels[q.Key] = map[string]string{"": q.Label}
		for _, o := range q.Options {
			labels[q.Key][o.Value] = o.Label
		}
	}
	var cross, unknown routing.RuleView
	for _, r := range view.Rules {
		switch r.ID {
		case "cross_module":
			cross = r
		case "unknown":
			unknown = r
		}
	}
	if got := ruleCondition(unknown, labels); got != "（任一题答不出）" {
		t.Fatalf("unknown condition = %q", got)
	}
	if got := ruleCondition(cross, labels); got != "（改动范围=跨模块）" {
		t.Fatalf("condition = %q", got)
	}
	if got := ruleCondition(view.Rules[len(view.Rules)-1], labels); got != "" {
		t.Fatalf("catch-all condition = %q, want none", got)
	}
}
