package quotarelay

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestPlanForSeparatesWeeklyModelAndTransient(t *testing.T) {
	now := time.Date(2026, 9, 23, 15, 0, 0, 0, time.UTC) // Wednesday
	own := Binding{Model: "gpt-special", OwnModel: true}
	shared := Binding{Model: "gpt-shared", OwnModel: false}

	weekly, ok := PlanFor(string(taskfailure.ReasonAgentProviderQuotaLimit), "Weekly usage limit reached", own, now)
	if !ok || weekly.Kind != KindWeeklyAgent || weekly.Scope() != ScopeAgent || weekly.ModelKey != "" {
		t.Fatalf("weekly plan = %+v ok=%v", weekly, ok)
	}
	if weekly.Condition != ConditionWeekly || !weekly.RecoverAt.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("weekly recovery = %s %s", weekly.Condition, weekly.RecoverAt)
	}

	modelText := "model quota exceeded for gpt-special"
	special, ok := PlanFor(string(taskfailure.ReasonAgentProviderQuotaLimit), modelText, own, now)
	if !ok || special.Kind != KindSpecializedModel || special.ModelKey != "gpt-special" || special.Scope() != ScopeModel {
		t.Fatalf("specialized plan = %+v ok=%v", special, ok)
	}
	if !special.RecoverAt.Equal(now.Add(time.Hour)) || special.Condition != ConditionModelWindow {
		t.Fatalf("model recovery = %s %s", special.Condition, special.RecoverAt)
	}

	// The same model words on an inherited or base-role model stay an
	// agent-scoped breaker. The shared model string is not the subject.
	inherited, ok := PlanFor(string(taskfailure.ReasonAgentProviderQuotaLimit), "model quota exceeded for gpt-shared", shared, now)
	if !ok || inherited.Kind != KindWeeklyAgent || inherited.ModelKey != "" {
		t.Fatalf("inherited model must stay agent-scoped, got %+v", inherited)
	}

	parsed, ok := PlanFor("agent_error", "weekly usage limit, resets in 2d3h", own, now)
	if !ok || parsed.Kind != KindWeeklyAgent || parsed.Condition != ConditionParsedReset {
		t.Fatalf("coarse weekly text = %+v ok=%v", parsed, ok)
	}
	if !parsed.RecoverAt.Equal(now.Add(2*24*time.Hour + 3*time.Hour)) {
		t.Fatalf("parsed reset = %s", parsed.RecoverAt)
	}

	capacity, ok := PlanFor(string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "quota exceeded but API Error: 429 Too Many Requests", own, now)
	if !ok || capacity.Kind != KindProviderCapacity || capacity.Scope() != ScopeAgent || capacity.ModelKey != "" {
		t.Fatalf("capacity plan = %+v ok=%v, want a capacity cooldown, not a quota breaker", capacity, ok)
	}
	if capacity.Condition != ConditionCapacityCooldown || !capacity.RecoverAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("capacity recovery = %s %s", capacity.Condition, capacity.RecoverAt)
	}
	if _, ok := PlanFor(string(taskfailure.ReasonAgentProviderNetwork), "connection reset by peer", own, now); ok {
		t.Fatal("network failure must not open a breaker")
	}
	if ShouldInspect(string(taskfailure.ReasonAgentProviderQuotaLimit), "weekly usage limit") != true {
		t.Fatal("quota reason must be inspected")
	}
	if !ShouldInspect(string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "Selected model is at capacity. Please try a different model.") {
		t.Fatal("capacity reason must be inspected")
	}
	if ShouldInspect(string(taskfailure.ReasonTimeout), "weekly usage limit") {
		t.Fatal("a named non-quota reason must not be inspected")
	}
	coarse, ok := PlanFor("agent_error", "Selected model is at capacity. Please try a different model.", own, now)
	if !ok || coarse.Kind != KindProviderCapacity {
		t.Fatalf("coarse capacity text = %+v ok=%v", coarse, ok)
	}
}

func TestNextWeeklyResetLandsOnTheFollowingMonday(t *testing.T) {
	monday := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	if got := nextWeeklyReset(monday); !got.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Monday hit recovers %s", got)
	}
	sunday := time.Date(2026, 9, 27, 23, 0, 0, 0, time.UTC)
	if got := nextWeeklyReset(sunday); !got.Equal(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Sunday hit recovers %s", got)
	}
}

func TestPickSameTierThenExactlyOneDown(t *testing.T) {
	ladder := []string{"strongest", "strong", "medium", "weak"}
	failed := Seat{ID: "a", Name: "孙悟空", Tier: "strong", Direction: "游戏"}
	roster := []Seat{
		failed,
		{ID: "b", Name: "孙悟天", Tier: "strong", Direction: "游戏", Eligible: true},
		{ID: "c", Name: "克林", Tier: "strong", Direction: "", Eligible: true},
		{ID: "d", Name: "贝吉塔", Tier: "medium", Eligible: true},
		{ID: "e", Name: "布尔玛", Tier: "strongest", Eligible: true},
		{ID: "broken", Name: "孙悟饭", Tier: "strong", Direction: "游戏", Eligible: false},
	}
	got, ok := Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "b" || got.SteppedDown {
		t.Fatalf("same tier = %+v ok=%v, want b same tier", got, ok)
	}

	roster[1].Eligible = false
	got, ok = Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "c" || got.SteppedDown {
		t.Fatalf("same tier other direction = %+v ok=%v, want c", got, ok)
	}

	roster[2].Eligible = false
	got, ok = Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "d" || !got.SteppedDown {
		t.Fatalf("one down = %+v ok=%v, want d stepped down", got, ok)
	}

	roster[3].Eligible = false
	if _, ok := Pick(failed, roster, ladder); ok {
		t.Fatal("must not skip two tiers down to strongest or weak")
	}

	untagged := failed
	untagged.Tier = ""
	if _, ok := Pick(untagged, roster, ladder); ok {
		t.Fatal("a seat with no tier has no same-tier or one-down replacement")
	}
}

func TestPickCapacityPrefersAnotherHouseThenDropsOneTier(t *testing.T) {
	ladder := []string{"strongest", "strong", "medium", "weak"}
	failed := Seat{ID: "gpt", Name: "特兰克斯", Tier: "strong", Direction: "游戏", Provider: OpenAIHouse, AvoidHouse: OpenAIHouse}
	roster := []Seat{
		failed,
		{ID: "gpt-same", Name: "阿A", Tier: "strong", Direction: "游戏", Provider: OpenAIHouse, Eligible: true},
		{ID: "grok", Name: "孙悟天", Tier: "strong", Provider: "xai", Eligible: true},
		{ID: "claude", Name: "孙悟空", Tier: "strong", Provider: AnthropicHouse, Eligible: true},
		{ID: "down-gpt", Name: "克林", Tier: "medium", Provider: OpenAIHouse, Eligible: true},
		{ID: "two-down", Name: "比克", Tier: "weak", Provider: "deepseek", Eligible: true},
	}

	// Quota, and any failure that does not set AvoidHouse, still prefers the
	// same direction even when that seat is another GPT.
	plain := failed
	plain.AvoidHouse = ""
	got, ok := Pick(plain, roster, ladder)
	if !ok || got.Seat.ID != "gpt-same" || got.SteppedDown {
		t.Fatalf("quota pick = %+v ok=%v, want the same-direction GPT seat", got, ok)
	}

	got, ok = Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "claude" || got.SteppedDown {
		t.Fatalf("cross-house pick = %+v ok=%v, want the Claude seat on the same tier", got, ok)
	}

	roster[3].Eligible = false // claude gone; Grok is still a different house
	got, ok = Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "grok" || got.SteppedDown {
		t.Fatalf("other house = %+v ok=%v, want Grok rather than another GPT or a step down", got, ok)
	}

	roster[2].Eligible = false // same tier is only GPT
	got, ok = Pick(failed, roster, ladder)
	if !ok || got.Seat.ID != "down-gpt" || !got.SteppedDown {
		t.Fatalf("one down = %+v ok=%v, want the GPT seat one tier down", got, ok)
	}

	roster[4].Eligible = false
	if _, ok := Pick(failed, roster, ladder); ok {
		t.Fatal("must not walk two tiers down once the same house is the only same-tier choice")
	}
}

func TestPickFindsTheLiveGPTTiers(t *testing.T) {
	ladder := routing.DefaultLadder.TierKeys()
	for _, name := range []string{"孙悟饭", "特兰克斯", "克林"} {
		tier, ok := routing.DefaultLadder.TierOf(name)
		if !ok {
			t.Fatalf("%s is not on the ladder", name)
		}
		provider, ok := routing.DefaultLadder.ProviderOf(name)
		if !ok || provider != "openai" {
			t.Fatalf("%s provider = %q ok=%v, want openai", name, provider, ok)
		}
		onLadder := false
		for _, key := range ladder {
			if key == tier {
				onLadder = true
				break
			}
		}
		if !onLadder {
			t.Fatalf("%s tier %q is not a ladder key", name, tier)
		}
		failed := Seat{ID: name, Name: name, Tier: tier}
		peer := Seat{ID: "peer-" + name, Name: "同伴", Tier: tier, Eligible: true}
		got, ok := Pick(failed, []Seat{failed, peer}, ladder)
		if !ok || got.Seat.ID != peer.ID || got.SteppedDown {
			t.Fatalf("Pick(%s tier %s) = %+v ok=%v, want the same-tier peer", name, tier, got, ok)
		}
	}
	if _, ok := Pick(Seat{ID: "x", Name: "x", Tier: "not-a-tier"}, nil, ladder); ok {
		t.Fatal("a tier that is not on the ladder must not relay")
	}
}

func TestAgentNoteCarriesThreadReviewerAndIssue(t *testing.T) {
	note := AgentNote(Handoff{
		FailedName:      "孙悟空",
		FailedID:        "agent-1",
		TaskID:          "task-1",
		IssueNumber:     771,
		IssueTitle:      "额度接力",
		ThreadCommentID: "comment-1",
		ReviewerType:    "agent",
		ReviewerID:      "reviewer-1",
		Acceptance:      "定向测试通过",
		Kind:            KindSpecializedModel,
		ModelKey:        "gpt-special",
		RecoverAt:       time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		Condition:       ConditionModelWindow,
		ReplacementName: "孙悟天",
		ReplacementTier: "strong",
	})
	for _, want := range []string{
		"继续",
		"from_task=task-1",
		"thread=comment-1",
		"reviewer_id=reviewer-1",
		"scope=model",
		"model_key=gpt-special",
		"#771",
		"定向测试通过",
		"不要另开任务",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("handoff missing %q\n%s", want, note)
		}
	}
	wait := AuditWait(Handoff{
		FailedName: "孙悟空",
		FailedID:   "agent-1",
		TaskID:     "task-1",
		Kind:       KindWeeklyAgent,
		WaitReason: WaitNoSeat,
		Condition:  ConditionWeekly,
		RecoverAt:  time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(wait, "blocked") || !strings.Contains(wait, WaitNoSeat) || !strings.Contains(wait, "scope=agent") {
		t.Fatalf("wait audit = %s", wait)
	}
}
