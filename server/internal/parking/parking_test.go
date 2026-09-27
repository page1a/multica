package parking

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 5, 48, 0, 0, time.UTC)

func run(status string, start time.Time, reason, errText string) Run {
	return Run{ID: "r", Status: status, CreatedAt: start, StartedAt: start, CompletedAt: start.Add(10 * time.Minute), FailureReason: reason, Error: errText}
}

var executor = Owner{Type: "agent", ID: "exec"}

// DENE-872 / 871 / 811: PR opened, the last step failed — the branch was
// refused and the review move was rejected for a missing PR link.
func TestDeliveryStuck(t *testing.T) {
	rec := Classify(Input{
		Status:   "todo",
		Assignee: executor,
		Runs:     []Run{run("failed", t0, "agent_error", "local_directory worktree: refusing to record branch agent/agent/dene-872: …")},
		Rejections: []Rejection{{
			Action: "status:in_review", Kind: RejectPRNotLinked, Reason: "进不了待验收：DENE-872 没有关联的 PR", At: t0.Add(5 * time.Minute),
		}},
	})
	if rec.Category != CategoryStalledDelivery || !rec.Unexplained {
		t.Fatalf("category = %s unexplained=%v, want stalled_delivery", rec.Category, rec.Unexplained)
	}
	if rec.StuckKind != StuckBranchRefused {
		t.Errorf("stuck kind = %s", rec.StuckKind)
	}
	if got := FallbackSummary(rec); got != "运行失败：平台拒绝记录分支，PR 未关联，送审被拒" {
		t.Errorf("fallback = %q", got)
	}
	var kinds []string
	for _, e := range rec.Timeline {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "run_started,rejected,run_failed" {
		t.Errorf("timeline = %v", kinds)
	}
}

// A completed run whose review move was refused is still a stuck delivery.
func TestDeliveryStuckAfterCompletedRunWithRejection(t *testing.T) {
	rec := Classify(Input{
		Status:     "in_progress",
		Assignee:   executor,
		Runs:       []Run{run("completed", t0, "", "")},
		Rejections: []Rejection{{Action: "status:in_review", Kind: RejectPRNotLinked, At: t0.Add(time.Minute)}},
	})
	if rec.Category != CategoryStalledDelivery || rec.StuckKind != StuckPRNotLinked {
		t.Fatalf("got %s/%s", rec.Category, rec.StuckKind)
	}
	if got := FallbackSummary(rec); got != "交付卡住：PR 未关联，送审被拒" {
		t.Errorf("fallback = %q", got)
	}
}

// DENE-707: PR merged, run done, ticket still in progress, no close.
func TestDoneButNotClosed(t *testing.T) {
	rec := Classify(Input{
		Status:       "in_progress",
		Assignee:     executor,
		Runs:         []Run{run("completed", t0, "", "")},
		PullRequests: []PullRequest{{Number: 373, State: "merged", LinkedAt: t0.Add(2 * time.Minute), MergedAt: t0.Add(4 * time.Minute)}},
	})
	if rec.Category != CategoryStalledUnclosed || !rec.Unexplained {
		t.Fatalf("category = %s", rec.Category)
	}
	if rec.NextOwner != executor {
		t.Errorf("next owner = %+v", rec.NextOwner)
	}
	if got := FallbackSummary(rec); got != "运行已结束：运行结束，票没有收口，PR 已合并" {
		t.Errorf("fallback = %q", got)
	}
}

// DENE-807 / 750: blocked with no wait on record, the agent answered "已完成".
func TestReplyWithoutCloseOnBlockedTicket(t *testing.T) {
	rec := Classify(Input{
		Status:   "blocked",
		Assignee: executor,
		Meta:     map[string]any{},
		Runs:     []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryStalledReplyUnclosed || rec.StuckKind != StuckNoWaitRecord || !rec.Unexplained {
		t.Fatalf("got %s/%s unexplained=%v", rec.Category, rec.StuckKind, rec.Unexplained)
	}
}

// Woken by its blocker, the agent replied and did not close again.
func TestReplyAfterWakeWithoutClose(t *testing.T) {
	closedAt := t0.Add(-time.Hour).Format(time.RFC3339)
	rec := Classify(Input{
		Status:   "blocked",
		Assignee: executor,
		Meta: map[string]any{
			"block.blocked_by": "DENE-1",
			"block.woken_by":   "child-1",
			"close.at":         closedAt,
			"close.status":     "blocked",
			"close.conclusion": "blocked",
		},
		Runs: []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryStalledReplyUnclosed || rec.StuckKind != StuckWokenNoClose {
		t.Fatalf("got %s/%s", rec.Category, rec.StuckKind)
	}
}

// DENE-822 shape A: a structured wait on a named person is an explained stop.
func TestIntentionalWaitOnPersonIsNotUnexplained(t *testing.T) {
	rec := Classify(Input{
		Status:   "blocked",
		Assignee: executor,
		Meta: map[string]any{
			"block.needs_human": "member-kun",
			"close.at":          t0.Add(5 * time.Minute).Format(time.RFC3339),
			"close.status":      "blocked",
			"close.conclusion":  "blocked",
		},
		Runs: []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryWaitingPerson || rec.Unexplained {
		t.Fatalf("got %s unexplained=%v, want waiting_person", rec.Category, rec.Unexplained)
	}
	if rec.NextOwner != (Owner{Type: "member", ID: "member-kun"}) {
		t.Errorf("next owner = %+v", rec.NextOwner)
	}
}

// DENE-822 shape B (the real ticket): parent left in progress with its later
// stages parked in backlog until Kun says go. Not "stopped without reason".
func TestParentHoldingOpenChildrenIsNotUnexplained(t *testing.T) {
	rec := Classify(Input{
		Status:          "in_progress",
		Assignee:        executor,
		HasOpenChildren: true,
		Runs:            []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryDelegated || rec.Unexplained {
		t.Fatalf("got %s", rec.Category)
	}
}

// in_progress with a close that asks a person to decide.
func TestAwaitingHumanCloseIsWaitingPerson(t *testing.T) {
	rec := Classify(Input{
		Status:   "in_progress",
		Assignee: executor,
		Meta: map[string]any{
			"close.at":              t0.Add(5 * time.Minute).Format(time.RFC3339),
			"close.status":          "in_progress",
			"close.conclusion":      "awaiting_human",
			"close.next_owner_type": "member",
			"close.next_owner_id":   "kun",
		},
		Runs: []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryWaitingPerson || !rec.CloseCurrent {
		t.Fatalf("got %s current=%v", rec.Category, rec.CloseCurrent)
	}
	if rec.NextOwner != (Owner{Type: "member", ID: "kun"}) {
		t.Errorf("next owner = %+v", rec.NextOwner)
	}
}

// A close from an EARLIER run does not explain the latest one.
func TestStaleCloseDoesNotExplainNewRun(t *testing.T) {
	rec := Classify(Input{
		Status:   "in_progress",
		Assignee: executor,
		Meta: map[string]any{
			"close.at":         t0.Add(-time.Hour).Format(time.RFC3339),
			"close.status":     "in_progress",
			"close.conclusion": "awaiting_human",
		},
		Runs: []Run{run("completed", t0, "", "")},
	})
	if rec.Category != CategoryStalledUnclosed || rec.CloseCurrent {
		t.Fatalf("got %s current=%v", rec.Category, rec.CloseCurrent)
	}
}

func TestExplainedStates(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"done", Input{Status: "done", Runs: []Run{run("completed", t0, "", "")}}, CategoryDone},
		{"running", Input{Status: "in_progress", HasActiveTask: true}, CategoryRunning},
		{"review", Input{Status: "in_review", Reviewer: Owner{Type: "agent", ID: "rev"}}, CategoryAwaitReview},
		{"blocked on issue", Input{Status: "blocked", Meta: map[string]any{"block.blocked_by": "DENE-9"}, Runs: []Run{run("completed", t0, "", "")}}, CategoryBlocked},
		{"backlog", Input{Status: "backlog", Runs: []Run{run("completed", t0, "", "")}}, CategoryIdle},
		{"never ran", Input{Status: "todo"}, CategoryIdle},
	}
	for _, c := range cases {
		rec := Classify(c.in)
		if rec.Category != c.want || rec.Unexplained {
			t.Errorf("%s: got %s unexplained=%v, want %s", c.name, rec.Category, rec.Unexplained, c.want)
		}
	}
	if rec := Classify(cases[1].in); rec.State != StateRunning {
		t.Errorf("running state = %s", rec.State)
	}
}

func TestErrorOnly(t *testing.T) {
	if !ErrorOnly("local_directory worktree: refusing to record branch x", "") {
		t.Error("daemon hand-off error should count as error-only")
	}
	if !ErrorOnly("", "boom") {
		t.Error("empty is error-only")
	}
	if ErrorOnly("追问四个问题都改好了，PR #171", "boom") {
		t.Error("agent prose is not error-only")
	}
}
