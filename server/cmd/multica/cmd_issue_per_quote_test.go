package main

import "testing"

func TestPerQuoteFlagIsOnCreateUpdateAndAssign(t *testing.T) {
	for name, flags := range map[string]bool{
		"create": issueCreateCmd.Flags().Lookup("per-quote") != nil,
		"update": issueUpdateCmd.Flags().Lookup("per-quote") != nil,
		"assign": issueAssignCmd.Flags().Lookup("per-quote") != nil,
	} {
		if !flags {
			t.Errorf("issue %s has no --per-quote flag", name)
		}
	}
}

func TestAddPerQuoteSendsTheWordsAsAssigneeQuote(t *testing.T) {
	if err := issueAssignCmd.Flags().Set("per-quote", "交给贝吉塔游戏"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = issueAssignCmd.Flags().Set("per-quote", "") })

	body := map[string]any{}
	addPerQuote(issueAssignCmd, body)
	if body["assignee_quote"] != "交给贝吉塔游戏" {
		t.Fatalf("body = %v", body)
	}

	empty := map[string]any{}
	_ = issueAssignCmd.Flags().Set("per-quote", "")
	addPerQuote(issueAssignCmd, empty)
	if _, ok := empty["assignee_quote"]; ok {
		t.Fatalf("an unset flag must not send assignee_quote: %v", empty)
	}
}

func TestNoteIgnoredAssigneeOnlyFiresWhenTheServerSaidSo(t *testing.T) {
	if !noteIgnoredAssignee(map[string]any{"identifier": "DENE-1", "assignee_ignored": true}) {
		t.Fatal("assignee_ignored=true must be reported")
	}
	if noteIgnoredAssignee(map[string]any{"identifier": "DENE-1"}) {
		t.Fatal("no field, no note")
	}
}
