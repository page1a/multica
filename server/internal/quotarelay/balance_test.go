package quotarelay

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// DENE-870: a 402 is a spent account, not a busy provider. It must take a
// different branch from capacity: no timer, a person has to top up.
func TestPlanForSeparatesBalanceFromCapacity(t *testing.T) {
	now := time.Date(2026, 9, 24, 13, 48, 0, 0, time.UTC)
	quota := string(taskfailure.ReasonAgentProviderQuotaLimit)

	balance := []string{
		"API Error: 402 Payment Required",
		"status 402: insufficient_balance",
		"Your credit balance is too low to access the API",
		"You have run out of credits",
	}
	for _, text := range balance {
		plan, ok := PlanFor(quota, text, Binding{}, now)
		if !ok || plan.Kind != KindBalanceExhausted {
			t.Fatalf("%q = %+v ok=%v, want balance_exhausted", text, plan, ok)
		}
		if !IsManualRecovery(plan.Kind) || plan.Condition != ConditionManual {
			t.Fatalf("%q must wait for a person, got %+v", text, plan)
		}
		if plan.RecoverAt.Before(now.Add(50 * 365 * 24 * time.Hour)) {
			t.Fatalf("%q recover_at = %v, must never come due on its own", text, plan.RecoverAt)
		}
	}

	capacity, ok := PlanFor(string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "429 Too Many Requests", Binding{}, now)
	if !ok || capacity.Kind != KindProviderCapacity || IsManualRecovery(capacity.Kind) {
		t.Fatalf("429 = %+v ok=%v, want a self-recovering capacity plan", capacity, ok)
	}
	if !capacity.RecoverAt.Before(now.Add(24 * time.Hour)) {
		t.Fatalf("capacity recover_at = %v, want within the day", capacity.RecoverAt)
	}

	weekly, ok := PlanFor(quota, "You've hit your weekly limit. resets Monday", Binding{}, now)
	if !ok || weekly.Kind == KindBalanceExhausted {
		t.Fatalf("weekly quota = %+v ok=%v, must stay a timed breaker", weekly, ok)
	}

	// A bare 4020 or an id that contains 402 is not a payment status.
	if plan, ok := PlanFor(quota, "request id 94021 failed: usage limit reached", Binding{}, now); ok && plan.Kind == KindBalanceExhausted {
		t.Fatalf("digit run containing 402 classified as balance: %+v", plan)
	}
}

func TestPickBalanceNeverLandsOnTheSameHouseOrExcludedSeat(t *testing.T) {
	ladder := []string{"strongest", "strong", "medium", "weak"}
	failed := Seat{ID: "grok", Name: "孙悟天", Tier: "strong", Provider: "xai", AvoidHouse: "xai", StrictHouse: true}
	roster := []Seat{
		failed,
		{ID: "grok-2", Name: "阿B", Tier: "strong", Provider: "xai", Eligible: true},
		{ID: "claude", Name: "孙悟空", Tier: "strong", Provider: AnthropicHouse, Eligible: true},
		{ID: "gpt", Name: "特兰克斯", Tier: "strong", Provider: OpenAIHouse, Eligible: true},
		{ID: "down-grok", Name: "克林", Tier: "medium", Provider: "xai", Eligible: true},
		{ID: "down-claude", Name: "天津饭", Tier: "medium", Provider: AnthropicHouse, Eligible: true},
	}

	// The ticket's reviewer is the Claude seat: executor and reviewer must
	// not be the same seat, so GPT takes it.
	seat := failed
	seat.Exclude = []string{"claude"}
	got, ok := Pick(seat, roster, ladder)
	if !ok || got.Seat.ID != "gpt" || got.SteppedDown {
		t.Fatalf("excluded reviewer = %+v ok=%v, want the GPT seat", got, ok)
	}

	roster[3].Eligible = false
	got, ok = Pick(seat, roster, ladder)
	if !ok || got.Seat.ID != "down-claude" || !got.SteppedDown {
		t.Fatalf("one down = %+v ok=%v, want another house one tier down, not Grok", got, ok)
	}

	roster[5].Eligible = false
	if got, ok := Pick(seat, roster, ladder); ok {
		t.Fatalf("only Grok seats left = %+v, a spent account must not hand work to its own house", got)
	}
}

func TestBalanceNotesSpeakPlainly(t *testing.T) {
	note := BalanceTransferNote("孙悟天", "孙悟空", "agent/dene-853", true)
	for _, want := range []string{"孙悟天", "agent/dene-853"} {
		if !strings.Contains(note, want) {
			t.Fatalf("transfer note missing %q:\n%s", want, note)
		}
	}
	audit := AuditBalanceTransfer("孙悟天", "孙悟空", "强", false, true, "agent/dene-853")
	if !strings.Contains(audit, "不会") || !strings.Contains(audit, "孙悟空") {
		t.Fatalf("audit must name the new seat and say it will not be pulled back:\n%s", audit)
	}
	alert := BalanceOwnerAlert("孙悟天", "402 Payment Required", 3)
	if !strings.Contains(alert, "孙悟天") || !strings.Contains(alert, "3") {
		t.Fatalf("owner alert must name the seat and how many tickets moved:\n%s", alert)
	}
}
