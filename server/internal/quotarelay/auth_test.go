package quotarelay

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// DENE-1647: a 401/403 is a broken seat. It opens a breaker that retries on
// its own after an hour, and the relay moves the work to a healthy seat.
func TestPlanForAuthFailureOpensATimedBreaker(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	plan, ok := PlanFor(string(taskfailure.ReasonAgentProviderAuthOrAccess), "API Error: 401 invalid x-api-key", Binding{}, now)
	if !ok || plan.Kind != KindAuthFailure {
		t.Fatalf("401 = %+v ok=%v, want auth_failure", plan, ok)
	}
	if !plan.RecoverAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("recover_at = %v, want an hour later", plan.RecoverAt)
	}
	if IsManualRecovery(plan.Kind) {
		t.Fatal("auth failure must recover on its own clock")
	}
	if !IsAuthFailure(string(taskfailure.ReasonAgentProviderAuthOrAccess), "401") || IsAuthFailure(string(taskfailure.ReasonAgentProviderQuotaLimit), "429") {
		t.Fatal("IsAuthFailure must match only the auth reason")
	}
}

func TestReviewHandoffKeepsTheRole(t *testing.T) {
	h := Handoff{FailedName: "布尔玛", Kind: KindAuthFailure, Review: true, ReplacementName: "比克", RecoverAt: time.Now().Add(time.Hour)}
	note := AgentNote(h)
	for _, want := range []string{"验收接力", "布尔玛", "401/403", "只做验收"} {
		if !strings.Contains(note, want) {
			t.Fatalf("review note missing %q:\n%s", want, note)
		}
	}
	if audit := AuditRelay(h); !strings.Contains(audit, "验收席失败，已转给 比克") {
		t.Fatalf("relay audit must name the new acceptance seat:\n%s", audit)
	}
	h.ReplacementName = ""
	wait := AuditWait(h)
	for _, want := range []string{"没有可接的验收席", "换席位", "我来验", "直接关票"} {
		if !strings.Contains(wait, want) {
			t.Fatalf("wait audit missing %q:\n%s", want, wait)
		}
	}
}
