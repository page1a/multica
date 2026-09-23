// @vitest-environment is a TS notion; this is the Go canonical layer for the
// routing state table. Every rule the feature states is checked here, against
// fakes, with no database: the rules are about which writes happen, and a
// fake store can prove a write did NOT happen in a way a live one cannot.
package routing

import (
	"context"
	"strings"
	"testing"
)

func TestDisabledAndIncompleteLeaveNoTrace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		s     Settings
		state State
	}{
		{"off", Settings{}, StateOff},
		{"off with model chosen", Settings{Model: "m"}, StateOff},
		{"incomplete", Settings{Enabled: true}, StateIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.settings = tc.s
			judge := &fakeJudge{verdict: confidentVerdict()}
			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.State != tc.state {
				t.Errorf("state = %q, want %q", out.State, tc.state)
			}
			if out.Action != ActionSkipped {
				t.Errorf("action = %q, want %q", out.Action, ActionSkipped)
			}
			// The whole point of these two states: the product behaves exactly
			// as it did before routing existed.
			if judge.callCount() != 0 {
				t.Errorf("asked the model %d times while not enabled", judge.callCount())
			}
			if store.wrote() {
				t.Error("wrote a value while not enabled")
			}
			if store.commentCount() != 0 {
				t.Error("commented while not enabled")
			}
			if out.Mentioned {
				t.Error("mentioned somebody while not enabled")
			}
		})
	}
}

func TestHumanAssigneeIssueIsNeverTouched(t *testing.T) {
	for _, status := range []string{"todo", "in_review", "blocked"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			store.issue.AssigneeType = "member"
			store.issue.AssigneeID = "user-9"
			store.issue.Reviewer = ReviewerRef{Kind: ReviewerMember, ID: "user-1", Name: "Kun"}
			judge := &fakeJudge{verdict: confidentVerdict()}

			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Action != ActionSkipped {
				t.Errorf("action = %q, want %q", out.Action, ActionSkipped)
			}
			if store.wrote() || store.commentCount() != 0 || out.Mentioned || judge.callCount() != 0 {
				t.Errorf("a person's issue was touched: writes=%v comments=%d mentioned=%v calls=%d",
					store.wrote(), store.commentCount(), out.Mentioned, judge.callCount())
			}
		})
	}
}

func TestQuietStatusesDoNothing(t *testing.T) {
	for _, status := range []string{"in_progress", "done", "cancelled", "backlog"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			judge := &fakeJudge{verdict: confidentVerdict()}

			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Action != ActionNoop {
				t.Errorf("action = %q, want %q", out.Action, ActionNoop)
			}
			if store.wrote() || store.commentCount() != 0 || out.Mentioned {
				t.Error("a quiet status produced an effect")
			}
			if judge.callCount() != 0 {
				t.Error("a quiet status cost a model call")
			}
		})
	}
}

func TestTodoFillsBothSlotsAndDoesNotMention(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want %q", out.Action, ActionAssigned)
	}
	// Project "game" maps to 游戏, so the direction-specialised seat is picked.
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want [孙悟空游戏]", store.assigns)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "布尔玛游戏" {
		t.Errorf("reviewer writes = %v, want [布尔玛游戏]", store.reviewer)
	}
	// Dispatched to an agent: somebody is on it, so an @ would be noise.
	if out.Mentioned {
		t.Error("mentioned somebody on a successfully dispatched issue")
	}
	if len(store.subs) != 0 {
		t.Errorf("subscribed %v with no mention to deliver", store.subs)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"孙悟空游戏", "布尔玛游戏", "游戏", "路由没有改过状态"} {
		if !strings.Contains(body, want) {
			t.Errorf("decision comment is missing %q:\n%s", want, body)
		}
	}
}

func TestChildTodoDispatchesExecutorWithoutAcceptanceSeat(t *testing.T) {
	store := newFakeStore()
	store.issue.ParentIssueID = "parent-1"
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want %q", out.Action, ActionAssigned)
	}
	if len(store.assigns) != 1 {
		t.Fatalf("executor writes = %v, want one child dispatch", store.assigns)
	}
	if len(store.reviewer) != 0 || !out.ReviewerWritten.Empty() {
		t.Fatalf("child received an acceptance seat: writes=%v outcome=%+v", store.reviewer, out.ReviewerWritten)
	}
	if judge.callCount() != 1 {
		t.Fatalf("judge calls = %d, want one executor-only decision", judge.callCount())
	}
}

func TestChildInReviewDoesNotStartAcceptanceHandoff(t *testing.T) {
	store := newFakeStore()
	store.issue.ParentIssueID = "parent-1"
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop || out.Reason != "sub-issue has no acceptance route" {
		t.Fatalf("child in_review route = %+v, want acceptance no-op", out)
	}
	if store.wrote() || store.commentCount() != 0 || judge.callCount() != 0 {
		t.Fatalf("child in_review produced independent acceptance work: writes=%v comments=%d calls=%d", store.wrote(), store.commentCount(), judge.callCount())
	}
}

func TestTodoDoesNotOverwriteSlotsSomebodyElseFilled(t *testing.T) {
	store := newFakeStore()
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop {
		t.Errorf("action = %q, want %q", out.Action, ActionNoop)
	}
	if store.wrote() {
		t.Error("overwrote a slot that already held a value")
	}
	// Both slots full means there is nothing left to be unsure about, so the
	// model is not asked at all. This is also what makes repeated status
	// flips free rather than merely idempotent.
	if judge.callCount() != 0 {
		t.Errorf("asked the model %d times with both slots already filled", judge.callCount())
	}
	if store.commentCount() != 0 {
		t.Error("commented with nothing to say")
	}
}

func TestSecondRouteCallWritesNothingMore(t *testing.T) {
	// Creation and the status change that immediately follows both call Route.
	// The second call must find the slots taken and stop.
	store := newFakeStore()
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("first route: %v", err)
	}
	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("second route: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Errorf("assigned %d times, want 1: %v", len(store.assigns), store.assigns)
	}
	if len(store.reviewer) != 1 {
		t.Errorf("wrote the reviewer slot %d times, want 1", len(store.reviewer))
	}
	if got := len(store.comments[KindAssignment]); got != 1 {
		t.Errorf("posted %d decision comments, want 1", got)
	}
}

func TestLowConfidenceStillDispatchesToTheFallbackRung(t *testing.T) {
	// Routing never parks a ticket for lack of confidence: an unconfident
	// verdict lands on the ladder's fallback rung, both slots come out filled,
	// and nobody is pinged — somebody is working on it.
	store := newFakeStore()
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "weak", ExecutorConfidence: 0.4,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.2,
	}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want the fallback rung (孙悟空游戏), not the unconfident pick", store.assigns)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "布尔玛游戏" {
		t.Errorf("reviewer = %v, want the rung above the executor", store.reviewer)
	}
	if out.Action != ActionAssigned {
		t.Errorf("action = %q, want %q", out.Action, ActionAssigned)
	}
	if out.Mentioned {
		t.Error("pinged a person about a ticket that was dispatched")
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "兜底") {
		t.Errorf("comment does not say the pick was a fallback:\n%s", body)
	}
}

func TestReviewerIsNeverTheSeatThatDidTheWork(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "strong"
	v.ReviewerTier = "strong" // same rung as the executor
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.reviewer) != 1 {
		t.Fatalf("reviewer writes = %v", store.reviewer)
	}
	// Promoted one rung up rather than accepting a self-review.
	if store.reviewer[0] != "布尔玛游戏" {
		t.Errorf("reviewer = %q, want the rung above the executor (布尔玛游戏)", store.reviewer[0])
	}
}

func TestReviewerCollisionOnTopRungFallsToTheRungBelow(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "strongest"
	v.ReviewerTier = "strongest"
	judge := &fakeJudge{verdict: v}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nothing above the top rung can check it, and the slot may never name a
	// person: a person in this slot is handed the ticket at 待验收 and routing
	// never touches it again. The rung below checks, merges and closes.
	if len(store.reviewer) != 1 || store.reviewer[0] != "孙悟空游戏" {
		t.Errorf("reviewer = %v, want [孙悟空游戏] — the rung below the top rung", store.reviewer)
	}
}

// The judge answering "this acceptance needs a person" must not put a person
// in the slot. This is the regression DENE-633 was reopened for: a reviewer
// slot naming a member freezes the ticket, because a ticket a person holds is
// one routing never touches again.
func TestJudgeAskingForAPersonStillWritesASeat(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.Reviewer = ReviewerHuman
	judge := &fakeJudge{verdict: v}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten.Kind != ReviewerAgent {
		t.Fatalf("reviewer kind = %q, want %q — the slot may never name a person",
			out.ReviewerWritten.Kind, ReviewerAgent)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "布尔玛游戏" {
		t.Errorf("reviewer = %v, want [布尔玛游戏] — the rung above the executor", store.reviewer)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "需要人拍板") {
		t.Errorf("comment hides that the judge asked for a person:\n%s", body)
	}
	if !strings.Contains(body, "@ 对应的人") {
		t.Errorf("comment does not tell the seat to ping the person:\n%s", body)
	}
}

func TestNoReviewNeededWritesAValueRatherThanLeavingTheSlotEmpty(t *testing.T) {
	// An empty slot is re-judged on every later status change. "No review
	// needed" has to be written down for the rule to ever close.
	store := newFakeStore()
	v := confidentVerdict()
	v.Reviewer = ReviewerNone
	judge := &fakeJudge{verdict: v}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten.Kind != ReviewerNoReview {
		t.Errorf("ReviewerWritten = %q, want %q", out.ReviewerWritten.Label(), LabelNoReview)
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != LabelNoReview {
		t.Errorf("reviewer = %v, want [不需要验收]", store.reviewer)
	}
	if out.Mentioned {
		t.Error("mentioned somebody on an issue that will run to completion by itself")
	}
}

func TestInReviewHandsOffToAgentWithoutMentioning(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
	judge := &fakeJudge{}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionHandedOff {
		t.Fatalf("action = %q, want %q", out.Action, ActionHandedOff)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-bulma-g" {
		t.Errorf("handoffs = %v, want [agent:a-bulma-g]", store.handoffs)
	}
	if out.Mentioned {
		t.Error("mentioned a person when assignment already wakes the seat")
	}
	// The handoff row needs no judgement: the reviewer slot already says who.
	if judge.callCount() != 0 {
		t.Errorf("handoff cost %d model calls", judge.callCount())
	}
}

// A person can still be put in the slot by hand, and old tickets already hold
// one. That person gets pinged — and keeps their hands free: reassigning the
// ticket to them is what made the status unmovable, because every later
// routing row skips an issue a person holds.
func TestInReviewWithAPersonInTheSlotNotifiesWithoutReassigning(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerMember, ID: "user-1", Name: "Kun"}
	judge := &fakeJudge{}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.handoffs) != 0 {
		t.Errorf("handoffs = %v, want none — the ticket must not be reassigned to a person", store.handoffs)
	}
	if out.Action != ActionAdvised {
		t.Errorf("action = %q, want %q", out.Action, ActionAdvised)
	}
	if !out.Mentioned {
		t.Error("the person named in the slot was not notified")
	}
	if len(store.subs) != 1 {
		t.Errorf("subs = %v — a mention without a subscription does not notify", store.subs)
	}
	body := store.comments[KindHandoff][0]
	if !strings.Contains(body, "票没有被改派") {
		t.Errorf("handoff comment does not say the ticket stayed put:\n%s", body)
	}
}

func TestInReviewWithNoReviewNeededChangesNothing(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}

	out, err := newRouter(store, &fakeJudge{}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionNoop || store.wrote() || store.commentCount() != 0 || out.Mentioned {
		t.Errorf("an issue marked as needing no review was still handled: %+v", out)
	}
}

func TestInReviewHandsOffAtMostOnce(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
	r := newRouter(store, &fakeJudge{})

	for i := 0; i < 3; i++ {
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if len(store.handoffs) != 1 {
		t.Errorf("handed off %d times, want 1: %v", len(store.handoffs), store.handoffs)
	}
	if got := len(store.comments[KindHandoff]); got != 1 {
		t.Errorf("posted %d handoff comments, want 1", got)
	}
}

func TestBlockedAdvisesWithoutChangingAnyValue(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
	judge := &fakeJudge{advice: Advice{Cause: "tier", SuggestedTier: "strong", Reason: "这活比看上去重"}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAdvised {
		t.Fatalf("action = %q, want %q", out.Action, ActionAdvised)
	}
	if store.wrote() {
		t.Errorf("the blocked row changed a value: %v %v %v", store.assigns, store.reviewer, store.handoffs)
	}
	if !out.Mentioned {
		t.Error("a blocked issue was left without notifying anybody")
	}
	body := store.comments[KindAdvice][0]
	for _, want := range []string{"没有改动任何值", "孙悟空游戏", "这活比看上去重"} {
		if !strings.Contains(body, want) {
			t.Errorf("advice comment is missing %q:\n%s", want, body)
		}
	}
}

func TestBlockedAdvisesAtMostOnce(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	judge := &fakeJudge{advice: Advice{Cause: "human"}}
	r := newRouter(store, judge)

	for i := 0; i < 3; i++ {
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if got := len(store.comments[KindAdvice]); got != 1 {
		t.Errorf("posted %d advice comments, want 1", got)
	}
	if judge.callCount() != 1 {
		t.Errorf("asked the model %d times, want 1 — a repeat flip must not cost a call", judge.callCount())
	}
}

func TestModelFailureWritesNothingAndSaysSoOnce(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{err: errUpstream}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionUnavailable {
		t.Errorf("action = %q, want %q", out.Action, ActionUnavailable)
	}
	if store.wrote() {
		t.Error("wrote a value when the model was unreachable")
	}
	if !out.Mentioned {
		t.Error("nobody was told the issue was not routed")
	}
	if got := len(store.comments[KindUnavailable]); got != 1 {
		t.Errorf("posted %d unavailable comments, want 1", got)
	}
}

func TestBreakerStopsCallingAfterRepeatedFailures(t *testing.T) {
	judge := &fakeJudge{err: errUpstream}
	r := newRouter(newFakeStore(), judge)

	// Each call is a different issue, so per-issue comment de-duplication
	// cannot be what stops the traffic — only the breaker can.
	for i := 0; i < 6; i++ {
		store := newFakeStore()
		r.Store = store
		if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
		if store.wrote() {
			t.Fatalf("route %d wrote a value with a broken model", i)
		}
	}
	if judge.callCount() != DefaultFailuresToTrip {
		t.Errorf("made %d upstream calls, want %d — the breaker must stop the traffic, not just the writes",
			judge.callCount(), DefaultFailuresToTrip)
	}
}

func TestOpenBreakerIsSilentOnTheIssue(t *testing.T) {
	judge := &fakeJudge{err: &FatalStatusError{Code: 401}}
	r := newRouter(newFakeStore(), judge)
	if _, err := r.Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("first route: %v", err)
	}

	// A 401 opens the breaker immediately: retrying a rejected credential on
	// every ticket is how a bad key becomes a traffic problem.
	store := newFakeStore()
	r.Store = store
	out, err := r.Route(context.Background(), "ws", "issue-2")
	if err != nil {
		t.Fatalf("second route: %v", err)
	}
	if out.State != StateIneffective {
		t.Errorf("state = %q, want %q", out.State, StateIneffective)
	}
	if judge.callCount() != 1 {
		t.Errorf("made %d calls, want 1 — no request may be sent while cooling down", judge.callCount())
	}
	if store.commentCount() != 0 || out.Mentioned {
		t.Error("a cooling-down workspace still wrote on a ticket; the reason belongs in settings only")
	}
}

func TestIssueWithoutAProjectRoutesGenericAndSaysTheDirectionIsUnknown(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = ""
	judge := &fakeJudge{verdict: confidentVerdict()}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat [孙悟空]", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "未知") {
		t.Errorf("comment does not say the direction is unknown:\n%s", body)
	}
}

func TestUnknownProjectDoesNotGuessADirection(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "某个没进对照表的 project"
	judge := &fakeJudge{verdict: confidentVerdict()}

	if _, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat", store.assigns)
	}
}

func TestBrokenVerdictBranchFallsBackRatherThanGuessing(t *testing.T) {
	// A tier the ladder does not have is a broken answer, not a licence to
	// improvise: the ticket goes to the declared fallback rung, and the reason
	// names the tier so the drift is visible.
	store := newFakeStore()
	v := confidentVerdict()
	v.ExecutorTier = "godlike"
	judge := &fakeJudge{verdict: v}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want the fallback rung", store.assigns)
	}
	if !strings.Contains(out.Reason, `"godlike"`) {
		t.Errorf("reason = %q, want it to name the tier the judge invented", out.Reason)
	}
	if out.Mentioned {
		t.Error("pinged a person about a ticket that was dispatched")
	}
}

// Before DENE-633 the reviewer lived in a workspace select property, so a
// workspace that had never provisioned it had NO reviewer slot at all and this
// test covered dispatching without one. The slot is now a column on every
// issue, so the state it was really guarding — "the reviewer question is
// already answered, dispatch the executor anyway and leave the answer alone" —
// is what it checks now.
func TestAnAlreadyAnsweredReviewerSlotStillDispatchesTheExecutor(t *testing.T) {
	store := newFakeStore()
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 {
		t.Errorf("assigns = %v, want one", store.assigns)
	}
	if !out.ReviewerWritten.Empty() {
		t.Errorf("overwrote an answered reviewer slot: %+v", out.ReviewerWritten)
	}
	if len(store.reviewer) != 0 {
		t.Errorf("reviewer writes = %v, want none", store.reviewer)
	}
}

func TestRouteNeverReportsAStatusWrite(t *testing.T) {
	// There is no status write to assert against, and that is the point: the
	// Store interface has no method that could perform one. This test exists
	// to fail loudly if one is ever added.
	var s Store = newFakeStore()
	if _, ok := s.(interface {
		SetStatus(context.Context, string, string, string) error
	}); ok {
		t.Fatal("Store grew a status write; routing must never advance status")
	}
}

func TestLosingTheCommentRaceNotifiesNobody(t *testing.T) {
	// Two Route calls on the same new issue both pass the HasComment filter;
	// only the unique index decides which one posts. The loser must not
	// notify, or somebody gets pinged about a decision comment that is not
	// theirs and is not on the issue.
	store := newFakeStore()
	// Pre-seed the comment WITHOUT letting HasComment see it, which is exactly
	// the window the index closes.
	store.comments[KindAssignment] = []string{"posted by the concurrent call"}

	blind := &blindReadStore{fakeStore: store}

	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.1,
		Reviewer: ReviewerNone, ReviewerConfidence: 0.1,
	}}

	out, err := newRouter(blind, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Mentioned {
		t.Error("the loser of the comment race notified somebody")
	}
	if out.Commented {
		t.Error("the loser of the comment race reported posting a comment")
	}
	if len(store.subs) != 0 {
		t.Errorf("subs = %v, want none from the loser", store.subs)
	}
	if got := len(store.comments[KindAssignment]); got != 1 {
		t.Errorf("issue carries %d assignment comments, want 1", got)
	}
}

// blindReadStore reports no existing comment however many there are, so a test
// can drive the path where only the database's uniqueness check is left.
type blindReadStore struct{ *fakeStore }

func (b *blindReadStore) HasComment(context.Context, string, string, CommentKind) (bool, error) {
	return false, nil
}

// --- DENE-706: the reported action is the write, not the attempt -----------

func TestLowConfidenceReportsTheFallbackWithAReadableReason(t *testing.T) {
	store := newFakeStore()
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "weak", ExecutorConfidence: 0.47,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.38,
	}}
	router := newRouter(store, judge)

	out, err := router.Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || out.ExecutorWritten == nil || out.ReviewerWritten.Empty() {
		t.Fatalf("outcome = %+v, want both slots written", out)
	}
	for _, want := range []string{"executor fell back to 孙悟空游戏: confidence 47% < threshold 70%", "reviewer fell back to 布尔玛游戏: confidence 38% < threshold 70%"} {
		if !strings.Contains(out.Reason, want) {
			t.Errorf("reason = %q, want it to contain %q", out.Reason, want)
		}
	}

	// Second pass: the conditional writes see slots that already hold a value,
	// so nothing is written twice and the outcome says so rather than claiming
	// a second dispatch.
	out, err = router.Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("second pass: unexpected error: %v", err)
	}
	if out.Action != ActionDeclined || out.ExecutorWritten != nil || !out.ReviewerWritten.Empty() {
		t.Errorf("second pass: outcome = %+v, want declined with no write", out)
	}
	if len(store.assigns) != 1 || len(store.reviewer) != 1 {
		t.Errorf("second pass wrote again: assigns=%v reviewer=%v", store.assigns, store.reviewer)
	}
}

func TestUnconfidentReviewerIsNamedInTheReasonButStillFilled(t *testing.T) {
	store := newFakeStore()
	v := confidentVerdict()
	v.ReviewerConfidence = 0.38
	out, err := newRouter(store, &fakeJudge{verdict: v}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || out.ExecutorWritten == nil || out.ReviewerWritten.Empty() {
		t.Errorf("outcome = %+v, want assigned with both slots written", out)
	}
	if !strings.Contains(out.Reason, "reviewer fell back") || strings.Contains(out.Reason, "executor") {
		t.Errorf("reason = %q, want only the reviewer slot named", out.Reason)
	}
}

func TestFullFillCarriesNoReason(t *testing.T) {
	store := newFakeStore()
	out, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || out.Reason != "" {
		t.Errorf("outcome = %+v, want assigned with an empty reason", out)
	}
}

func TestBrokenTierAnswerReportsTheFallbackAndNamesTheTier(t *testing.T) {
	store := newFakeStore()
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	v := confidentVerdict()
	v.ExecutorTier = "godlike"
	out, err := newRouter(store, &fakeJudge{verdict: v}).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || !strings.Contains(out.Reason, `"godlike"`) {
		t.Errorf("outcome = %+v, want assigned with a reason naming the tier", out)
	}
}

// --- DENE-706: the project table is workspace data --------------------------

func TestWorkspaceProjectRowSendsWorkToTheDirectionSeat(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "PG 适配"
	store.settings.Projects = map[string]string{"pg 适配": "游戏"}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空游戏" {
		t.Errorf("assigns = %v, want the direction seat [孙悟空游戏]", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "游戏（来自 project「PG 适配」）") {
		t.Errorf("comment does not name the direction source:\n%s", body)
	}
}

func TestProjectMappedToGenericIsKnownNotUnknown(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "Multica 魔改"
	store.settings.Projects = map[string]string{"Multica 魔改": GenericDirection}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat", store.assigns)
	}
	body := store.comments[KindAssignment][0]
	if strings.Contains(body, "未知") || !strings.Contains(body, "归为通用") {
		t.Errorf("a classified-generic project must not read as unknown:\n%s", body)
	}
}

func TestProjectRowNamingNoDirectionIsCalledOut(t *testing.T) {
	store := newFakeStore()
	store.issue.ProjectName = "tarot"
	store.settings.Projects = map[string]string{"tarot": "出海海"}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.assigns) != 1 || store.assigns[0] != "孙悟空" {
		t.Errorf("assigns = %v, want the generic seat", store.assigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "出海海") {
		t.Errorf("comment hides the bad table value:\n%s", body)
	}
}

// The in-review row used to give up when the reviewer slot was empty, which is
// how a workspace accumulates tickets sitting in review that nobody was ever
// told to check: every ticket dispatched by hand, and every ticket that
// predates routing, has an empty slot. It now decides one at this row.
func TestInReviewFillsAnEmptyReviewerSlotAndHandsOff(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionHandedOff {
		t.Fatalf("action = %q, want %q (reason %q)", out.Action, ActionHandedOff, out.Reason)
	}
	if out.ReviewerWritten.Label() != "布尔玛游戏" {
		t.Errorf("reviewer written = %q, want 布尔玛游戏", out.ReviewerWritten.Label())
	}
	if len(store.reviewer) != 1 || store.reviewer[0] != "布尔玛游戏" {
		t.Errorf("reviewer slot = %v, want [布尔玛游戏]", store.reviewer)
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-bulma-g" {
		t.Errorf("handoffs = %v, want [agent:a-bulma-g]", store.handoffs)
	}
	body := store.comments[KindHandoff][0]
	if !strings.Contains(body, "现场定了一个") {
		t.Errorf("handoff comment hides that the reviewer was decided at this row:\n%s", body)
	}
}

// A seat may not accept its own output, including when the reviewer is decided
// at the in-review row, where the executor is read off the issue rather than
// from a write this call just made.
func TestInReviewDecidedReviewerIsNeverTheExecutor(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-bulma-g" // the seat the judge is about to name
	store.issue.Reviewer = ReviewerRef{}
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten.Label() != "孙悟空游戏" {
		t.Errorf("reviewer = %q, want 孙悟空游戏 — the top rung did the work, so the rung below checks it",
			out.ReviewerWritten.Label())
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-goku-g" {
		t.Errorf("handoffs = %v, want [agent:a-goku-g]", store.handoffs)
	}
}

// Routing still never writes status, whichever row filled the reviewer slot.
func TestInReviewDecisionNeverTouchesStatus(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{}

	if _, err := newRouter(store, &fakeJudge{verdict: confidentVerdict()}).
		Route(context.Background(), "ws", "issue-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.issue.Status != "in_review" {
		t.Errorf("status = %q — routing moved a ticket's status", store.issue.Status)
	}
}

// Plenty of work is done by agents that carry no tier label at all. "The
// ladder has no rung above this seat" is then a fact about the ladder, not
// about the ticket, and answering it with a person is how every mechanical
// check ends up queued on somebody's desk. The top rung takes those.
func TestOffLadderExecutorFallsBackToTheTopRungNotAPerson(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-trunks" // not on the ladder: no tier label
	store.issue.Reviewer = ReviewerRef{}
	// An unusable verdict, so the ladder fallback is what answers.
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.9,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.1,
	}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten.Label() != "布尔玛游戏" {
		t.Errorf("reviewer = %q, want 布尔玛游戏 — an unlabelled executor must not force a human check",
			out.ReviewerWritten.Label())
	}
	if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-bulma-g" {
		t.Errorf("handoffs = %v, want [agent:a-bulma-g]", store.handoffs)
	}
}

// When the top rung did the work there is genuinely nothing above it. The
// check goes one rung DOWN rather than to a person: a reviewer checks, merges
// and closes, and a person in the slot takes the ticket out of routing's reach
// for good.
func TestTopRungExecutorFallsBackToTheRungBelow(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-bulma-g"
	store.issue.Reviewer = ReviewerRef{}
	judge := &fakeJudge{verdict: Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.9,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.1,
	}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ReviewerWritten.Kind != ReviewerAgent {
		t.Fatalf("reviewer kind = %q, want %q — the slot may never name a person",
			out.ReviewerWritten.Kind, ReviewerAgent)
	}
	if out.ReviewerWritten.Label() != "孙悟空游戏" {
		t.Errorf("reviewer = %q, want 孙悟空游戏", out.ReviewerWritten.Label())
	}
}
