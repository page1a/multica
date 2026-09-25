package routing

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPolicyPromptMatchesTheSettingsPage(t *testing.T) {
	raw, err := os.ReadFile("../../../packages/core/workspace/routing-policy-prompt.ts")
	if err != nil {
		t.Fatalf("read shared prompt: %v", err)
	}
	const start = "export const DEFAULT_ROUTING_POLICY_PROMPT = `"
	text := string(raw)
	from := strings.Index(text, start)
	if from < 0 {
		t.Fatal("shared prompt constant not found")
	}
	body := text[from+len(start):]
	end := strings.Index(body, "`")
	if end < 0 {
		t.Fatal("shared prompt constant is not closed")
	}
	if got := body[:end]; got != DefaultPolicyPrompt {
		t.Fatalf("settings page prompt drifted from the server prompt\n--- page ---\n%s\n--- server ---\n%s", got, DefaultPolicyPrompt)
	}
}

func TestPolicyPromptForbidsStatusWritesAndNamesThePreference(t *testing.T) {
	for _, phrase := range []string{
		"medium or strong",
		"Simple, explicit, low-risk work: weak",
		"strong or strongest",
		"do not change status",
		"do not take an action",
		"unknown means the fact is missing",
		"Provider quota never decides whether a seat is alive",
	} {
		if !strings.Contains(DefaultPolicyPrompt, phrase) {
			t.Errorf("default prompt missing %q", phrase)
		}
	}
	if !strings.Contains(assignSystemPrompt, "do not change status") || !strings.Contains(assignSystemPrompt, "policy_prompt") {
		t.Error("assign prompt does not point at the policy or does not forbid status writes")
	}
	if !strings.Contains(executorInstruction, "do not change status") || !strings.Contains(executorInstruction, "medium or strong") {
		t.Error("system one instruction does not carry the tier preference")
	}
	if !strings.Contains(reviewerTierInstr, "policy_prompt") || !strings.Contains(reviewerTierInstr, "do not change status") {
		t.Error("reviewer tier instruction does not follow policy_prompt")
	}
	if strings.Contains(reviewerTierInstr, "at least as demanding") {
		t.Error("reviewer tier instruction still pushes the tier up")
	}
	if !strings.Contains(assignSystemPrompt, "reviewer_tier") {
		t.Error("assign prompt does not apply policy_prompt to the reviewer tier")
	}
}

func TestSnapshotOmitsUnobservedNumbers(t *testing.T) {
	snap := InterpretSeat(SeatFacts{AgentID: "a-1", Tier: "strong"}, time.Now())
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["availability"] != AvailabilityUnknown {
		t.Fatalf("availability = %v, want unknown", body["availability"])
	}
	if body["model"] != "unknown" {
		t.Fatalf("model = %v, want unknown", body["model"])
	}
	if body["latency_ms"] != nil {
		t.Fatalf("latency_ms = %v, want null", body["latency_ms"])
	}
	quota, _ := body["quota"].(map[string]any)
	if quota["remaining"] != nil || quota["reset_at"] != nil {
		t.Fatalf("quota invented a number: %#v", quota)
	}
	if strings.Contains(string(raw), `"p50":0`) || strings.Contains(string(raw), `"p95":0`) {
		t.Fatalf("snapshot forged a zero latency: %s", raw)
	}
}

func TestSnapshotKeepsObservedLatency(t *testing.T) {
	snap := InterpretSeat(SeatFacts{
		AgentID:   "a-1",
		Model:     "grok-4.7",
		Tier:      "strong",
		Runtime:   "online",
		Latencies: []int64{100, 200, 300, 400},
	}, time.Now())
	if snap.Availability != AvailabilityAvailable {
		t.Fatalf("availability = %s", snap.Availability)
	}
	if snap.Latency == nil || snap.Latency.P50 == nil || *snap.Latency.P50 != 200 {
		t.Fatalf("p50 = %#v, want 200", snap.Latency)
	}
	if snap.Latency.P95 == nil || *snap.Latency.P95 != 400 {
		t.Fatalf("p95 = %#v, want 400", snap.Latency)
	}
}

func TestUnselectableReasonsAreFilteredAndUnknownIsKept(t *testing.T) {
	candidates := []Seat{
		{ID: "alive", TierKey: "medium"},
		{ID: "dead", TierKey: "strong"},
		{ID: "blank", TierKey: "weak"},
		{ID: "quota", TierKey: "strongest"},
	}
	snaps := map[string]SeatSnapshot{
		"alive": {AgentID: "alive", Availability: AvailabilityAvailable},
		"dead":  {AgentID: "dead", Availability: AvailabilityDead},
		"quota": {AgentID: "quota", Availability: AvailabilityQuotaExhausted},
	}
	got := EligibleSeats(candidates, snaps)
	if len(got) != 2 || got[0].ID != "alive" || got[1].ID != "blank" {
		t.Fatalf("eligible = %#v", got)
	}
	for _, reason := range []string{
		AvailabilityCancelled, AvailabilityDead, AvailabilityDisabled,
		AvailabilityArchived, AvailabilityUnreachable, AvailabilityQuotaExhausted,
	} {
		if !Unselectable(reason) {
			t.Errorf("%s should be unselectable", reason)
		}
	}
	if Unselectable(AvailabilityUnknown) || Unselectable(AvailabilityAvailable) {
		t.Error("unknown or available was treated as unselectable")
	}
}

func TestSeatReasons(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	reset := now.Add(time.Hour)
	used := 100.0
	remaining := 0.0
	cases := []struct {
		name  string
		facts SeatFacts
		want  string
	}{
		{"archived", SeatFacts{Archived: true, Runtime: "online"}, AvailabilityArchived},
		{"disabled", SeatFacts{WorkKnown: true, WorkEnabled: false, Runtime: "online"}, AvailabilityDisabled},
		{"cancelled", SeatFacts{Cancelled: true, Runtime: "online", WorkKnown: true, WorkEnabled: true}, AvailabilityCancelled},
		{"dead", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "dead"}, AvailabilityDead},
		{"unreachable", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "unreachable"}, AvailabilityUnreachable},
		{"online", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online"}, AvailabilityAvailable},
		{"quota exhausted", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online", Quota: QuotaObservation{Status: "exhausted", ObservedAt: fresh, ResetAt: &reset}}, AvailabilityQuotaExhausted},
		{"used up", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online", Quota: QuotaObservation{Status: "available", ObservedAt: fresh, UsedPercent: &used}}, AvailabilityQuotaExhausted},
		{"balance empty", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online", Quota: QuotaObservation{Status: "available", ObservedAt: fresh, Remaining: &remaining}}, AvailabilityQuotaExhausted},
		{"stale exhaustion is unknown quota", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online", Quota: QuotaObservation{Status: "exhausted", ObservedAt: now.Add(-48 * time.Hour)}}, AvailabilityAvailable},
		{"reset already passed", SeatFacts{WorkKnown: true, WorkEnabled: true, Runtime: "online", Quota: QuotaObservation{Status: "exhausted", ObservedAt: fresh, ResetAt: timePtr(now.Add(-time.Minute))}}, AvailabilityAvailable},
		{"unread", SeatFacts{}, AvailabilityUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InterpretSeat(tc.facts, now).Availability; got != tc.want {
				t.Fatalf("availability = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStaleProviderQuotaIsUnknownNotExhausted(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	got := InterpretProvider("claude", QuotaObservation{Status: "exhausted", ObservedAt: now.Add(-48 * time.Hour)}, now)
	if got.Status != AvailabilityUnknown || got.UsedPercent != nil || got.Remaining != nil {
		t.Fatalf("stale provider = %#v", got)
	}
	fresh := InterpretProvider("codex", QuotaObservation{}, now)
	if fresh.Status != AvailabilityUnknown {
		t.Fatalf("missing provider = %#v", fresh)
	}
}

func TestEnsureProviderQuotasFillsTheSubscription(t *testing.T) {
	got := EnsureProviderQuotas(nil, DefaultWatchedProviders)
	if len(got) != 3 || got[0].Provider != "claude" || got[2].Provider != "grok" {
		t.Fatalf("providers = %#v", got)
	}
	for _, quota := range got {
		if quota.Status != AvailabilityUnknown || quota.UsedPercent != nil {
			t.Fatalf("filled a number for %s: %#v", quota.Provider, quota)
		}
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestRouteSkipsASeatTheSnapshotAlreadySaysIsDead(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = ""
	store.facts = RoutingFacts{Seats: map[string]SeatSnapshot{
		"a-goku": {AgentID: "a-goku", Availability: AvailabilityDead},
	}}
	judge := &fakeJudge{verdict: confidentVerdict()}
	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range store.assigns {
		if name == "孙悟空" {
			t.Fatal("assigned the seat the snapshot said was dead")
		}
	}
	state := judge.assignedState()
	if state.PolicyPrompt != DefaultPolicyPrompt {
		t.Fatal("judge request did not carry the policy prompt")
	}
	if state.RoutingPolicy.DefaultTier != "medium_or_strong" {
		t.Fatalf("policy = %#v", state.RoutingPolicy)
	}
	if len(state.ProviderQuotas) != 3 {
		t.Fatalf("provider quotas = %#v", state.ProviderQuotas)
	}
	var deadSeen bool
	for _, tier := range state.Candidates {
		if tier == "strong" {
			t.Fatal("dead strong seat stayed in candidate_tiers")
		}
	}
	for _, seat := range state.Seats {
		if seat.AgentID == "a-goku" {
			deadSeen = true
			if seat.Availability != AvailabilityDead {
				t.Fatalf("dead seat rendered as %s", seat.Availability)
			}
		}
	}
	if !deadSeen {
		t.Fatal("request hid the dead seat instead of showing why it was excluded")
	}
	if out.ExecutorWritten != nil && out.ExecutorWritten.ID == "a-goku" {
		t.Fatal("wrote the dead seat")
	}
}

func TestRouteRechecksBeforeWriting(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = ""
	after := RoutingFacts{Seats: map[string]SeatSnapshot{
		"a-goku": {AgentID: "a-goku", Availability: AvailabilityQuotaExhausted},
	}}
	store.factsAfter = &after
	judge := &fakeJudge{verdict: confidentVerdict()}
	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatal(err)
	}
	if store.factReads < 2 {
		t.Fatalf("recheck did not run: %d fact reads", store.factReads)
	}
	for _, name := range store.assigns {
		if name == "孙悟空" {
			t.Fatal("wrote the seat that exhausted its quota between the call and the write")
		}
	}
	if out.ExecutorWritten != nil && out.ExecutorWritten.ID == "a-goku" {
		t.Fatal("executor written is the exhausted seat")
	}
	if len(store.statusWritten) != 0 {
		t.Fatal("recheck wrote a status")
	}
}

func TestIncompleteContextWritesNothing(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = ""
	store.errOn["facts"] = errUpstream
	judge := &fakeJudge{verdict: confidentVerdict()}
	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err == nil {
		t.Fatal("expected the facts error")
	}
	if out.Action != ActionSkipped {
		t.Fatalf("action = %s", out.Action)
	}
	if judge.callCount() != 0 || store.wrote() {
		t.Fatalf("incomplete context still judged or wrote: calls=%d wrote=%v", judge.callCount(), store.wrote())
	}
}
