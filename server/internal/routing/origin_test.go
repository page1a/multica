package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// DENE-1033: an agent's own pick of executor or tier carries no weight; a
// person's does, and says whose it was.

func TestQuoteHolds(t *testing.T) {
	msg := "这张票请让  孙悟饭 来做，别的先放一放"
	cases := []struct {
		name  string
		msg   string
		quote string
		agent string
		want  bool
	}{
		{"verbatim with the name", msg, "请让 孙悟饭 来做", "孙悟饭", true},
		{"whitespace and case are forgiven", "Ask Bulma to do it", "ask   BULMA to do it", "bulma", true},
		{"fabricated words are not in the message", msg, "请让孙悟饭立刻来做这个", "孙悟饭", false},
		{"quote does not name the agent", msg, "别的先放一放", "孙悟饭", false},
		{"names a different agent than the one assigned", msg, "请让 孙悟饭 来做", "布尔玛", false},
		{"empty quote", msg, "  ", "孙悟饭", false},
		{"empty agent name", msg, "请让 孙悟饭 来做", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QuoteHolds(tc.msg, tc.quote, tc.agent); got != tc.want {
				t.Fatalf("QuoteHolds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgentTierLabelIsIgnoredAndSaidSo(t *testing.T) {
	store := newFakeStore()
	store.issue.Labels = []string{"最强"}
	store.issue.AgentLabels = []string{"最强"}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if judge.callCount() == 0 {
		t.Fatal("the judge was skipped although the only tier label was an agent's")
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "strong" {
		t.Fatalf("executor = %+v, want the judge's strong rung, not the label's strongest", out.ExecutorWritten)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, NoticeAgentTierLabelIgnored) {
		t.Errorf("comment does not say the agent's tier label was ignored:\n%s", body)
	}
}

func TestHumanTierLabelStillWinsBesideAnAgentOne(t *testing.T) {
	store := newFakeStore()
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	store.issue.Labels = []string{"弱", "最强"}
	store.issue.AgentLabels = []string{"最强"}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ExecutorWritten == nil || out.ExecutorWritten.TierKey != "weak" {
		t.Fatalf("executor = %+v, want the person's weak label", out.ExecutorWritten)
	}
	if body := store.comments[KindAssignment][0]; strings.Contains(body, NoticeAgentTierLabelIgnored) {
		t.Errorf("said an agent label was ignored although a person's label decided:\n%s", body)
	}
}

func TestIgnoredAgentPickIsSaidOnceWithoutNamingWhoWasPicked(t *testing.T) {
	store := newFakeStore()
	store.issue.AssigneeSource = SourceAgent // an attempt: the slot is still empty
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ExecutorWritten == nil {
		t.Fatal("routing did not fill the executor slot an agent's pick left empty")
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, NoticeAgentPickIgnored) {
		t.Errorf("comment does not say the agent's pick was ignored:\n%s", body)
	}
	if strings.Count(body, "Agent 给这张票指定过执行人") != 1 {
		t.Errorf("the notice should appear exactly once:\n%s", body)
	}
}

func TestRejectedQuoteStaysUnassignedWithoutRerouting(t *testing.T) {
	store := newFakeStore()
	store.issue.AssigneeSource = SourceQuoteRejected
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop || len(store.assigns) != 0 || judge.callCount() != 0 {
		t.Fatalf("rejected quote was rerouted: outcome=%+v assigns=%v judge_calls=%d", out, store.assigns, judge.callCount())
	}
}

func TestQuotedExecutorIsKeptAndCreditedToTheSpeaker(t *testing.T) {
	store := newFakeStore()
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-bulma"
	store.issue.AssigneeSource = SourceQuote
	store.issue.AssigneeSourceUser = "小明"
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ExecutorWritten != nil || len(store.assigns) != 0 {
		t.Fatalf("routing overwrote an executor a person named: %+v", out.ExecutorWritten)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "按 小明 原话指派") {
		t.Errorf("comment does not credit the speaker:\n%s", body)
	}
	if strings.Contains(body, NoticeAgentPickIgnored) {
		t.Errorf("a verified quote was reported as ignored:\n%s", body)
	}
}

func TestEscalateMovesUpAndCannotNameAnybody(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g" // 比克游戏, weak rung
	// The judge asks for a rung nobody can take: escalation still moves one up.
	judge := &fakeJudge{verdict: Verdict{ExecutorTier: "nonexistent", ExecutorConfidence: 0.9}}

	esc, err := newRouter(store, judge).Escalate(context.Background(), "ws", "issue-1", "要改三个服务")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !esc.Changed || esc.From != "比克游戏" {
		t.Fatalf("escalation = %+v, want a move away from 比克游戏", esc)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-vegeta-g" {
		t.Fatalf("handoffs = %v, want one handoff to 贝吉塔游戏", store.handoffs)
	}
	if got := judge.assignedState().EscalationReason; got != "要改三个服务" {
		t.Errorf("the judge was not shown the reason, got %q", got)
	}
	// One rung up from weak is medium (贝吉塔游戏).
	if esc.To != "贝吉塔游戏" {
		t.Errorf("moved to %q, want the next rung up (贝吉塔游戏)", esc.To)
	}
}

func TestEscalateFromTheTopMovesNothing(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_progress"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-bulma-g" // 布尔玛游戏, strongest
	judge := &fakeJudge{verdict: confidentVerdict()}

	esc, err := newRouter(store, judge).Escalate(context.Background(), "ws", "issue-1", "太难了")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if esc.Changed || !esc.AtTop || len(store.handoffs) != 0 {
		t.Fatalf("escalation from the top = %+v handoffs=%v, want nothing moved", esc, store.handoffs)
	}
}

func TestEscalateNeedsAnAgentExecutor(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_progress"
	_, err := newRouter(store, &fakeJudge{}).Escalate(context.Background(), "ws", "issue-1", "x")
	if !errors.Is(err, ErrNotEscalatable) {
		t.Fatalf("err = %v, want ErrNotEscalatable", err)
	}
}
