package handler

import (
	"encoding/json"
	"testing"
)

func jsonUnmarshalForTest(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func TestAgentSpawnPolicyConsultCell(t *testing.T) {
	if got := parseAgentSpawnPolicy(nil).Consult; !got.Enabled || got.PerIssue != defaultConsultPerIssue {
		t.Fatalf("default consult cell = %+v", got)
	}
	// A workspace saved before consults existed keeps the default.
	old := parseAgentSpawnPolicy([]byte(`{"agent_spawn":{"chat_chat":{"enabled":false}}}`))
	if !old.Consult.Enabled || old.Consult.PerIssue != defaultConsultPerIssue || old.ChatChat.Enabled {
		t.Fatalf("old settings = %+v", old)
	}
	set := parseAgentSpawnPolicy([]byte(`{"agent_spawn":{"consult":{"enabled":true,"per_issue":5}}}`))
	if set.Consult.PerIssue != 5 {
		t.Fatalf("per_issue = %d", set.Consult.PerIssue)
	}
	for name, patch := range map[string]string{
		"zero":    `{"consult":{"per_issue":0}}`,
		"per_run": `{"consult":{"per_run":2}}`,
		"wrong":   `{"chat_chat":{"per_issue":2}}`,
	} {
		p := defaultAgentSpawnPolicy()
		var body agentSpawnPolicyPatch
		if err := jsonUnmarshalForTest(patch, &body); err != nil {
			t.Fatal(err)
		}
		if err := p.apply(body); err == nil {
			t.Errorf("%s: patch %s should be refused", name, patch)
		}
	}
}
