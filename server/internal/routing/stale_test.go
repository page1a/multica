package routing

import (
	"context"
	"strings"
	"testing"
	"time"
)

// DENE-712 B1. A ticket awaiting acceptance whose reviewer slot was never
// filled is the case the blanket human-assignee rule froze: the in-review
// handoff makes the assignee a person, and every later pass then skipped the
// whole ticket, so the slot could never be filled and nobody was ever told to
// check the work. Eighteen tickets in one workspace sat like that.
//
// The carve-out fills the slot and hands the ticket on. It must still leave
// the status alone — this row has never written one and does not start now.
func TestHumanHeldInReviewFillsTheReviewerSlot(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "member"
	store.issue.AssigneeID = "user-1"
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	out, err := r.Route(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.Action != ActionHandedOff {
		t.Fatalf("action = %q / %q, want handed off", out.Action, out.Reason)
	}
	if len(store.reviewer) != 1 {
		t.Fatalf("reviewer slot writes = %v, want exactly one", store.reviewer)
	}
	if len(store.handoffs) != 1 {
		t.Fatalf("handoffs = %v, want exactly one", store.handoffs)
	}
	if len(store.statusWritten) != 0 {
		t.Fatalf("this row wrote a status: %v", store.statusWritten)
	}
}

func TestChildInReviewIsIgnoredByStaleAcceptanceSweep(t *testing.T) {
	store := newFakeStore()
	store.issue.ParentIssueID = "parent-1"
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku-g"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-bulma-g", Name: "布尔玛游戏"}
	store.issue.LastActivityAt = time.Now().Add(-48 * time.Hour)
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionNoop || out.Reason != "sub-issue has no acceptance route" {
		t.Fatalf("stale child route = %+v, want acceptance no-op", out)
	}
	if store.wrote() || store.commentCount() != 0 || judge.callCount() != 0 {
		t.Fatalf("stale child produced independent acceptance work: writes=%v comments=%d calls=%d", store.wrote(), store.commentCount(), judge.callCount())
	}
}

// The other side of the same carve-out: it is as narrow as the bug. A person
// holding a ticket whose reviewer slot already holds a value has nothing left
// to fill, so the ticket is untouched exactly as before.
func TestHumanHeldInReviewWithAReviewerIsStillUntouched(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "member"
	store.issue.AssigneeID = "user-1"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-goku", Name: "孙悟空"}
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	out, err := r.Route(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.Action != ActionSkipped || out.Reason != "assignee is a person" {
		t.Fatalf("action = %q / %q, want skipped / assignee is a person", out.Action, out.Reason)
	}
	if store.wrote() || judge.callCount() != 0 {
		t.Fatalf("a human-held ticket was touched: writes=%v calls=%d", store.wrote(), judge.callCount())
	}
}

// DENE-712 acceptance #2. Narrowing rule three by status must not widen it by
// accident: a person doing the work is still a person doing the work.
func TestHumanAssigneeOutsideReviewIsStillSkipped(t *testing.T) {
	for _, status := range []string{"todo", "in_progress", "blocked", "backlog"} {
		t.Run(status, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = status
			store.issue.AssigneeType = "member"
			store.issue.AssigneeID = "user-1"
			judge := &fakeJudge{verdict: confidentVerdict()}
			r := newRouter(store, judge)

			out, err := r.Route(context.Background(), "ws-1", "issue-1")
			if err != nil {
				t.Fatalf("route: %v", err)
			}
			if out.Action != ActionSkipped || out.Reason != "assignee is a person" {
				t.Fatalf("action = %q / %q, want skipped / assignee is a person", out.Action, out.Reason)
			}
			if store.wrote() || store.commentCount() != 0 || judge.callCount() != 0 {
				t.Fatalf("a human-held %s ticket was touched", status)
			}
		})
	}
}

// staleStore builds a ticket that is genuinely stalled: awaiting acceptance,
// quiet for a week, with a reviewer seat that was decided long ago.
func staleStore() *fakeStore {
	store := newFakeStore()
	store.issue.Status = "in_review"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-goku"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerAgent, ID: "a-vegeta", Name: "贝吉塔"}
	store.issue.LastActivityAt = time.Now().Add(-7 * 24 * time.Hour)
	store.workspaces = []string{"ws-1"}
	store.staleIDs = []string{"issue-1"}
	return store
}

// DENE-712 acceptance #3, default half. Waking is the default and it writes no
// status — including when the judge answers "complete" confidently but the
// reviewer never actually said anything on the ticket. That gate is
// deterministic on purpose: with no remark there is no acceptance for a status
// to be aligned to, whatever the model believes.
func TestStaleSweepDefaultsToWakingWithoutTouchingStatus(t *testing.T) {
	for _, name := range []string{"judge says wake", "judge says complete but nobody spoke"} {
		t.Run(name, func(t *testing.T) {
			store := staleStore()
			judge := &fakeJudge{stale: StaleDecision{Action: StaleWake, Confidence: 0.95}}
			if strings.Contains(name, "complete") {
				judge.stale = StaleDecision{Action: StaleComplete, Confidence: 0.99}
				store.remarks = nil
			}
			r := newRouter(store, judge)

			out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
			if err != nil {
				t.Fatalf("route stale: %v", err)
			}
			if out.Action != ActionWoken {
				t.Fatalf("action = %q / %q, want woken", out.Action, out.Reason)
			}
			if len(store.statusWritten) != 0 {
				t.Fatalf("the sweep wrote a status: %v", store.statusWritten)
			}
			if len(store.handoffs) != 1 || store.handoffs[0] != "agent:a-vegeta" {
				t.Fatalf("handoffs = %v, want one wake of the reviewer seat", store.handoffs)
			}
		})
	}
}

// A below-threshold completion is a weak answer to the only question in this
// package whose mistake is invisible on the board, so it falls to the default
// like every other weak answer here.
func TestStaleCompletionBelowThresholdFallsBackToWaking(t *testing.T) {
	store := staleStore()
	store.remarks = []string{"看过了，没问题，通过"}
	judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.5}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionWoken {
		t.Fatalf("action = %q, want woken", out.Action)
	}
	if len(store.statusWritten) != 0 {
		t.Fatalf("a 0.5 answer wrote a status: %v", store.statusWritten)
	}
}

// DENE-712 acceptance #3, completion half. Both gates satisfied — the reviewer
// spoke on the ticket, and the judge reads that as an acceptance at or above
// the threshold — is the one path that reaches a status write.
func TestStaleCompletionNeedsAVerdictAlreadyOnTheTicket(t *testing.T) {
	store := staleStore()
	store.remarks = []string{"看过了，没问题，通过"}
	judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.9, Reason: "验收席已明确通过"}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionCompleted {
		t.Fatalf("action = %q / %q, want completed", out.Action, out.Reason)
	}
	if len(store.statusWritten) != 1 {
		t.Fatalf("status writes = %v, want exactly one", store.statusWritten)
	}
	if len(store.handoffs) != 0 || len(store.reviewer) != 0 || len(store.assigns) != 0 {
		t.Fatalf("completion changed something other than the status: %+v", store)
	}
	if len(store.comments[KindCompleted]) != 1 {
		t.Fatalf("completion was not recorded on the ticket")
	}
	if len(store.subs) != 0 {
		t.Fatalf("completion mentioned somebody: %v", store.subs)
	}
}

// A person cannot be woken by being assigned a ticket they already hold, so
// the member half of the wake is a comment plus an @ — and the one-comment-per
// -kind index makes it happen exactly once, whatever the sweep's cadence.
func TestStaleWakeTellsAMemberReviewerOnce(t *testing.T) {
	store := staleStore()
	store.issue.AssigneeType = "member"
	store.issue.AssigneeID = "user-1"
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerMember, ID: "user-1", Name: "Kun"}
	judge := &fakeJudge{stale: StaleDecision{Action: StaleWake, Confidence: 0.9}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionWoken {
		t.Fatalf("action = %q / %q, want woken", out.Action, out.Reason)
	}
	if len(store.comments[KindStalled]) != 1 || len(store.subs) != 1 {
		t.Fatalf("member wake = %d comments / %d subscribers, want one of each",
			len(store.comments[KindStalled]), len(store.subs))
	}
	if store.wrote() {
		t.Fatalf("waking a person changed a value")
	}

	// Second sweep over the same ticket.
	out, err = r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("second route stale: %v", err)
	}
	if out.Action != ActionNoop {
		t.Fatalf("second sweep action = %q, want noop", out.Action)
	}
	if len(store.comments[KindStalled]) != 1 || len(store.subs) != 1 {
		t.Fatalf("the second sweep said it again: %d comments / %d subscribers",
			len(store.comments[KindStalled]), len(store.subs))
	}
}

// The completion callback may already have queued the reviewer between the
// list query and this pass. Waking again would be a second run.
func TestStaleSweepSkipsAnActiveRun(t *testing.T) {
	store := staleStore()
	store.activeRun = true
	judge := &fakeJudge{stale: StaleDecision{Action: StaleWake, Confidence: 0.95}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionNoop || out.Reason != "active run in progress" {
		t.Fatalf("action = %q / %q, want noop / active run in progress", out.Action, out.Reason)
	}
	if judge.callCount() != 0 || len(store.handoffs) != 0 || len(store.statusWritten) != 0 {
		t.Fatalf("an active run was judged or woken: calls=%d handoffs=%v status=%v",
			judge.callCount(), store.handoffs, store.statusWritten)
	}
}

// A ticket that has not been quiet long enough is not stalled, even when the
// single-issue entry point is called on it by hand.
func TestStaleRowIgnoresTicketsThatAreNotQuietYet(t *testing.T) {
	store := staleStore()
	store.issue.LastActivityAt = time.Now().Add(-time.Hour)
	judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.99}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionNoop {
		t.Fatalf("action = %q / %q, want noop", out.Action, out.Reason)
	}
	if judge.callCount() != 0 || store.wrote() {
		t.Fatalf("a fresh ticket was judged or written")
	}
}

// Only the awaiting-acceptance category is ever swept. The list query filters
// by status too, but the row must hold on its own: a ticket can leave review
// between the listing and the pass.
func TestStaleRowNeverTouchesOtherStatuses(t *testing.T) {
	for _, status := range []string{"todo", "in_progress", "backlog", "blocked", "done"} {
		t.Run(status, func(t *testing.T) {
			store := staleStore()
			store.issue.Status = status
			judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.99}}
			r := newRouter(store, judge)

			out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
			if err != nil {
				t.Fatalf("route stale: %v", err)
			}
			if out.Action != ActionNoop {
				t.Fatalf("action = %q / %q, want noop", out.Action, out.Reason)
			}
			if judge.callCount() != 0 || store.wrote() || store.commentCount() != 0 {
				t.Fatalf("the sweep touched a %s ticket", status)
			}
		})
	}
}

// A stalled ticket whose reviewer slot was never decided is the B1 case seen
// from the sweep's side: filling the slot and handing the ticket on IS the
// wake, so the row delegates rather than inventing a second way to do it.
func TestStaleRowFillsAnUndecidedReviewerSlot(t *testing.T) {
	store := staleStore()
	store.issue.Reviewer = ReviewerRef{}
	judge := &fakeJudge{verdict: confidentVerdict()}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionHandedOff {
		t.Fatalf("action = %q / %q, want handed off", out.Action, out.Reason)
	}
	if len(store.reviewer) != 1 || len(store.statusWritten) != 0 {
		t.Fatalf("reviewer writes = %v, status writes = %v", store.reviewer, store.statusWritten)
	}
}

// A ticket judged not to need an acceptance pass at all has nobody to wake and
// nothing to align.
func TestStaleRowSkipsTicketsThatNeedNoReview(t *testing.T) {
	store := staleStore()
	store.issue.Reviewer = ReviewerRef{Kind: ReviewerNoReview}
	judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.99}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionNoop || judge.callCount() != 0 || store.wrote() {
		t.Fatalf("action = %q / %q, calls = %d", out.Action, out.Reason, judge.callCount())
	}
}

// A completion that loses its conditional write says nothing: somebody else
// moved the ticket between the decision and the write, and their answer wins.
func TestStaleCompletionThatLosesTheWriteStaysQuiet(t *testing.T) {
	store := staleStore()
	store.remarks = []string{"通过"}
	store.completeLost = true
	judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.9}}
	r := newRouter(store, judge)

	out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
	if err != nil {
		t.Fatalf("route stale: %v", err)
	}
	if out.Action != ActionNoop {
		t.Fatalf("action = %q, want noop", out.Action)
	}
	if store.commentCount() != 0 {
		t.Fatalf("a lost write still commented")
	}
}

// DENE-712 acceptance #4. The stall threshold is not a second on/off switch:
// with routing off, neither the sweep nor the single-issue pass makes a
// request, a write, a comment or a mention.
func TestStaleRowDoesNothingWhileRoutingIsOff(t *testing.T) {
	cases := map[string]func(s *fakeStore){
		"switched off": func(s *fakeStore) { s.settings.Enabled = false },
		"no model":     func(s *fakeStore) { s.settings.Model = "" },
		"breaker cooling": func(s *fakeStore) {
			// Same shape as every other row: the workspace is quiet until the
			// cooldown elapses, and no outbound request is made meanwhile.
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			store := staleStore()
			setup(store)
			judge := &fakeJudge{stale: StaleDecision{Action: StaleComplete, Confidence: 0.99}}
			r := newRouter(store, judge)
			if name == "breaker cooling" {
				r.Breaker.Fail("ws-1", errUpstream)
				r.Breaker.Fail("ws-1", errUpstream)
				r.Breaker.Fail("ws-1", errUpstream)
			}

			report, err := r.Sweep(context.Background())
			if err != nil {
				t.Fatalf("sweep: %v", err)
			}
			out, err := r.RouteStale(context.Background(), "ws-1", "issue-1")
			if err != nil {
				t.Fatalf("route stale: %v", err)
			}
			if out.Action != ActionSkipped {
				t.Fatalf("action = %q / %q, want skipped", out.Action, out.Reason)
			}
			if report.Examined != 0 || report.Woken != 0 || report.Completed != 0 {
				t.Fatalf("the sweep did work while routing was off: %+v", report)
			}
			if store.wrote() || store.commentCount() != 0 || len(store.subs) != 0 || judge.callCount() != 0 {
				t.Fatalf("routing was off and something happened anyway")
			}
		})
	}
}

// The sweep is the timed entry point, so the thing worth asserting about it is
// that it reaches the row at all and reports what it did.
func TestSweepVisitsEnabledWorkspaces(t *testing.T) {
	store := staleStore()
	judge := &fakeJudge{stale: StaleDecision{Action: StaleWake, Confidence: 0.9}}
	r := newRouter(store, judge)

	report, err := r.Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if report.Workspaces != 1 || report.Examined != 1 || report.Woken != 1 {
		t.Fatalf("report = %+v, want one workspace, one examined, one woken", report)
	}
	if len(store.statusWritten) != 0 {
		t.Fatalf("a wake-only sweep wrote a status")
	}
}

// The stall threshold reads like the other three settings fields: absent,
// zero, or nonsense all mean the default rather than "sweep everything".
func TestStaleAfterFallsBackToTheDefault(t *testing.T) {
	want := DefaultStaleReviewHours * time.Hour
	for _, s := range []Settings{
		{},
		{StaleReviewHours: 0},
		{StaleReviewHours: -3},
		{StaleReviewHours: 1e9},
	} {
		if got := s.StaleAfter(); got != want {
			t.Errorf("StaleAfter(%v) = %v, want %v", s.StaleReviewHours, got, want)
		}
	}
	if got := (Settings{StaleReviewHours: 6}).StaleAfter(); got != 6*time.Hour {
		t.Errorf("StaleAfter(6) = %v, want 6h", got)
	}
}
