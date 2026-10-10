package execenv

import (
	"strings"
	"testing"
)

// A consult run (DENE-1721) is told it only advises: its credential reads,
// its final message is the answer, and it neither comments nor hands off.
func TestConsultBriefIsReadOnlyAdvice(t *testing.T) {
	brief := buildMetaSkillContentSlim("claude", TaskContextForEnv{IsConsult: true, AgentID: "a-1", AgentName: "Agent"})
	for _, want := range []string{"**This is a consult.**", "read-only", "This is a consult run."} {
		if !strings.Contains(brief, want) {
			t.Fatalf("consult brief misses %q", want)
		}
	}
	if strings.Contains(brief, "Deliver through a comment on the issue") {
		t.Fatal("consult brief must not ask for an issue comment")
	}
}
