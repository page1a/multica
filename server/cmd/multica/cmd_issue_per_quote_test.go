package main

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
)

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

// DENE-1201: an in-flight reassignment refused by the server keeps the old
// executor, and the agent must read the way out, not "routing will pick".
func TestNoteIgnoredAssigneeInFlightKeepsExecutorAndNamesAlternatives(t *testing.T) {
	c := captureStderr(t)
	noteIgnoredAssignee(map[string]any{
		"identifier":              "DENE-1",
		"assignee_id":             "a-1",
		"assignee_ignored":        true,
		"assignee_ignored_reason": routing.ReasonAgentReassignInFlight,
	})
	got := c.read()
	for _, want := range []string{"keeps its current assignee", "multica issue escalate", "--outcome blocked"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "routing will pick") {
		t.Errorf("an in-flight ticket is not left to routing:\n%s", got)
	}
}

// DENE-1613: a quote that did not check out hands the slot to routing, and
// the agent is not sent back to the person.
func TestNoteIgnoredAssigneeUnverifiedQuoteSaysRoutingPicks(t *testing.T) {
	c := captureStderr(t)
	noteIgnoredAssignee(map[string]any{
		"identifier":              "DENE-1",
		"assignee_ignored":        true,
		"assignee_ignored_reason": routing.ReasonQuoteNotVerified,
	})
	got := c.read()
	if !strings.Contains(got, "routing will pick the executor") || !strings.Contains(got, "reassign with --per-quote") {
		t.Errorf("stderr does not say routing picks:\n%s", got)
	}
	if strings.Contains(got, "ask the person") {
		t.Errorf("stderr still sends the agent back to the person:\n%s", got)
	}
}
