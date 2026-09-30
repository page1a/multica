package routing

import (
	"encoding/json"
	"testing"
)

func TestUnwrapRuntimeAnalysis(t *testing.T) {
	facts := `{"scope":"module","clarity":"clear","risk":"low","needs_human":false,"reviewer":"none"}`
	encoded, _ := json.Marshal(facts)
	tests := []struct{ name, in string }{
		{"plain", facts},
		{"claude envelope", `{"type":"result","result":` + string(encoded) + `}`},
		{"codex event", `{"type":"item.completed","item":{"type":"agent_message","text":` + string(encoded) + `}}`},
		{"jsonl events", "{\"type\":\"thread.started\"}\n{" + `"type":"item.completed","item":{"text":` + string(encoded) + `}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unwrapRuntimeAnalysis(tc.in); got != facts {
				t.Fatalf("unwrapRuntimeAnalysis() = %q, want %q", got, facts)
			}
		})
	}
}

func TestUnwrapRuntimeAnalysisMarkdown(t *testing.T) {
	facts := `{"scope":"small","clarity":"vague","risk":"medium","needs_human":true}`
	input := "```json\n" + facts + "\n```"
	if got := unwrapRuntimeAnalysis(input); got != facts {
		t.Fatalf("unwrapRuntimeAnalysis() = %q, want %q", got, facts)
	}
}
