package routing

import (
	"context"
	"strings"
	"testing"
)

// DENE-1255: a blocked ticket nobody holds is seated with the todo row's
// ladder, parked — no run starts, and the status is not touched.
func TestBlockedWithEmptyExecutorIsSeatedWithoutStarting(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.Wait = BlockWait{Registered: true, BlockedBy: []string{"DENE-1236"}}
	judge := &fakeJudge{
		verdict: Verdict{ExecutorTier: "strong", ExecutorConfidence: 0.9, Reviewer: ReviewerSeat, ReviewerTier: "medium", ReviewerConfidence: 0.9},
		advice:  Advice{Cause: "human"},
	}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.Action != ActionAssigned || out.ExecutorWritten == nil {
		t.Fatalf("action = %q executor = %v, want the empty slot filled", out.Action, out.ExecutorWritten)
	}
	if len(store.assigns) != 1 || len(store.quietAssigns) != 1 {
		t.Fatalf("assigns = %v quiet = %v, want one write that starts no run", store.assigns, store.quietAssigns)
	}
	if len(store.statusWritten) != 0 {
		t.Errorf("the blocked row wrote a status: %v", store.statusWritten)
	}
	body := store.comments[KindAssignment][0]
	if !strings.Contains(body, "票还在阻塞") {
		t.Errorf("assignment comment does not say the seat is parked:\n%s", body)
	}
	if strings.Contains(body, "run 已启动") {
		t.Errorf("assignment comment claims a run started:\n%s", body)
	}
}

func TestBlockedWithHeldExecutorKeepsIt(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	judge := &fakeJudge{advice: Advice{Cause: "human"}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.Action != ActionAdvised || store.wrote() {
		t.Fatalf("action = %q assigns = %v, want advice only", out.Action, store.assigns)
	}
	if len(store.comments[KindAssignment]) != 0 {
		t.Error("a held ticket got a decision comment")
	}
}

// When the slot cannot be filled, the advice says so from the record — it
// does not ask a model and does not answer "其他".
func TestBlockedAdviceNamesTheEmptyExecutorWithoutAModel(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.Wait = BlockWait{Registered: true, BlockedBy: []string{"DENE-1236"}}
	store.roster = map[string]Agent{} // no seat on the ladder
	judge := &fakeJudge{advice: Advice{Cause: "other", Reason: "The blocker is probably outside this ticket"}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if judge.callCount() != 0 {
		t.Errorf("asked a model %d times for a fact the record holds", judge.callCount())
	}
	if !out.Mentioned {
		t.Error("nobody was told the ticket has no executor")
	}
	body := store.comments[KindAdvice][0]
	for _, want := range []string{"缺执行人", "multica issue assign", "没有问模型"} {
		if !strings.Contains(body, want) {
			t.Errorf("advice is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "不像档位问题") || strings.Contains(body, "outside this ticket") {
		t.Errorf("advice fell back to the model's vague cause:\n%s", body)
	}
}

func TestBlockedAdviceFacts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wait        BlockWait
		want        string
		wantMention bool
	}{
		{"nothing on record", BlockWait{}, "没登记在等什么", true},
		{"blocker ended", BlockWait{Registered: true, BlockedBy: []string{"DENE-1236"}, Ended: []string{"DENE-1236"}}, "挡路的票已结束", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.issue.Status = "blocked"
			store.issue.AssigneeType = "agent"
			store.issue.AssigneeID = "a-piccolo-g"
			store.issue.Wait = tc.wait
			judge := &fakeJudge{advice: Advice{Cause: "other"}}

			out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("route: %v", err)
			}
			if judge.callCount() != 0 {
				t.Errorf("asked a model %d times", judge.callCount())
			}
			if out.Mentioned != tc.wantMention {
				t.Errorf("mentioned = %v, want %v", out.Mentioned, tc.wantMention)
			}
			if body := store.comments[KindAdvice][0]; !strings.Contains(body, tc.want) {
				t.Errorf("advice is missing %q:\n%s", tc.want, body)
			}
		})
	}
}

// A seat that was filled on an earlier pass is not re-advised, but a later
// pass still fills an executor slot that emptied since.
func TestBlockedSeatsEvenAfterAdvice(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.comments[KindAdvice] = []string{"earlier advice"}
	judge := &fakeJudge{verdict: Verdict{ExecutorTier: "strong", ExecutorConfidence: 0.9, Reviewer: ReviewerNone, ReviewerConfidence: 0.9}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if out.Action != ActionAssigned || len(store.quietAssigns) != 1 {
		t.Fatalf("action = %q quiet = %v, want the slot filled", out.Action, store.quietAssigns)
	}
	if len(store.comments[KindAdvice]) != 1 {
		t.Errorf("advised again: %v", store.comments[KindAdvice])
	}
}

func TestSeatExecutorFillsWithoutStarting(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	judge := &fakeJudge{verdict: Verdict{ExecutorTier: "strong", ExecutorConfidence: 0.9, Reviewer: ReviewerNone, ReviewerConfidence: 0.9}}
	r := newRouter(store, judge)

	out, err := r.SeatExecutor(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("seat: %v", err)
	}
	if out.ExecutorWritten == nil || len(store.quietAssigns) != 1 {
		t.Fatalf("executor = %v quiet = %v, want a parked seat", out.ExecutorWritten, store.quietAssigns)
	}
	if len(store.comments[KindAdvice]) != 0 {
		t.Error("seating posted blocked advice")
	}

	store.issue.AssigneeType = "agent"
	out, err = r.SeatExecutor(context.Background(), "ws", "issue-1")
	if err != nil || out.Action != ActionNoop {
		t.Fatalf("held slot: action = %q err = %v, want noop", out.Action, err)
	}
}

// DENE-1395/1396/1397: a held ticket waiting on a ticket that is still open
// is waiting, not stuck. No model, no comment, nobody @'d.
func TestBlockedWaitingOnOpenTicketIsLeftAlone(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "blocked"
	store.issue.AssigneeType = "agent"
	store.issue.AssigneeID = "a-piccolo-g"
	store.issue.Wait = BlockWait{Registered: true, BlockedBy: []string{"DENE-1393", "DENE-1394"}, Ended: []string{"DENE-1394"}}
	judge := &fakeJudge{advice: Advice{Cause: "human"}}

	out, err := newRouter(store, judge).Route(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if judge.callCount() != 0 {
		t.Errorf("asked a model %d times about a ticket that is only waiting", judge.callCount())
	}
	if out.Action != ActionNoop || out.Mentioned || len(store.comments[KindAdvice]) != 0 {
		t.Fatalf("action = %q mentioned = %v advice = %v, want a silent noop", out.Action, out.Mentioned, store.comments[KindAdvice])
	}
	if !strings.Contains(out.Reason, "DENE-1393") || strings.Contains(out.Reason, "DENE-1394") {
		t.Errorf("reason = %q, want only the open blocker named", out.Reason)
	}
}
