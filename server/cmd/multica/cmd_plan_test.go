package main

import (
	"strings"
	"testing"
)

func TestParsePlanFileNewAndExistingParent(t *testing.T) {
	plan, parent, ref, err := parsePlanFile([]byte("key: k\nparent:\n  title: P\n  assignee: someone\nchildren:\n  - title: A\n    stage: 1\n"))
	if err != nil || parent == nil || parent.Title != "P" || ref != "" || len(plan.Children) != 1 || *plan.Children[0].Stage != 1 {
		t.Fatalf("new-parent plan = %+v parent=%+v ref=%q err=%v", plan, parent, ref, err)
	}
	_, parent, ref, err = parsePlanFile([]byte("key: k\nparent: DENE-858\nchildren:\n  - title: A\n    stage: 1\n"))
	if err != nil || parent != nil || ref != "DENE-858" {
		t.Fatalf("existing-parent plan parent=%+v ref=%q err=%v", parent, ref, err)
	}
	if _, _, _, err := parsePlanFile([]byte("parent: DENE-1\n")); err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("plan without key err = %v, want it to ask for a key", err)
	}
}

func TestPlanAndStageCommandsRegistered(t *testing.T) {
	if cmd, _, err := rootCmd.Find([]string{"plan", "apply"}); err != nil || cmd != planApplyCmd {
		t.Fatalf("plan apply not registered: %v", err)
	}
	if cmd, _, err := issueCmd.Find([]string{"stage", "advance"}); err != nil || cmd != issueStageAdvanceCmd {
		t.Fatalf("issue stage advance not registered: %v", err)
	}
}
