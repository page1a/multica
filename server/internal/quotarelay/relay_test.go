package quotarelay

import (
	"strings"
	"testing"
	"time"

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

	if _, ok := PlanFor(string(taskfailure.ReasonAgentProviderCapacityOrRateLimit), "quota exceeded but API Error: 429 Too Many Requests", own, now); ok {
		t.Fatal("capacity reason must not open a breaker even if the text mentions quota")
	}
	if _, ok := PlanFor(string(taskfailure.ReasonAgentProviderNetwork), "connection reset by peer", own, now); ok {
		t.Fatal("network failure must not open a breaker")
	}
	if ShouldInspect(string(taskfailure.ReasonAgentProviderQuotaLimit), "weekly usage limit") != true {
		t.Fatal("quota reason must be inspected")
	}
	if ShouldInspect(string(taskfailure.ReasonTimeout), "weekly usage limit") {
		t.Fatal("a named non-quota reason must not be inspected")
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
