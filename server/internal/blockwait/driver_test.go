package blockwait

import (
	"testing"
	"time"
)

func TestDriverOf(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	clock := Record{HasWakeAt: true, WakeAt: now.Add(time.Hour), WaitCondition: "到点"}
	cases := []struct {
		name string
		in   DriverFacts
		ok   bool
		kind string
	}{
		{"done needs none", DriverFacts{Status: "done"}, false, ""},
		{"custom closed needs none", DriverFacts{Status: "shipped", Closed: true}, false, ""},
		{"backlog is parked", DriverFacts{Status: "backlog", AssigneeType: "agent"}, false, ""},
		{"active run", DriverFacts{Status: "todo", AssigneeType: "agent", ActiveRun: true}, true, DriverRun},
		{"review by person", DriverFacts{Status: "in_review", ReviewerType: "member"}, true, DriverPerson},
		{"review by agent", DriverFacts{Status: "in_review", ReviewerType: "agent"}, true, DriverWait},
		{"needs human", DriverFacts{Status: "blocked", AssigneeType: "agent", Record: Record{NeedsHuman: "u"}}, true, DriverPerson},
		{"member executor", DriverFacts{Status: "todo", AssigneeType: "member"}, true, DriverPerson},
		{"blocked with clock", DriverFacts{Status: "blocked", AssigneeType: "agent", Watched: true, Record: clock}, true, DriverWait},
		{"blocked on another issue", DriverFacts{Status: "blocked", AssigneeType: "agent", Watched: true, Record: Record{BlockedBy: []string{"DENE-1"}}}, true, DriverWait},
		{"watched unstructured block", DriverFacts{Status: "blocked", AssigneeType: "agent", Watched: true}, true, DriverWait},
		{"unwatched block", DriverFacts{Status: "blocked", AssigneeType: "agent", Record: clock}, true, DriverNone},
		{"in_progress pause", DriverFacts{Status: "in_progress", AssigneeType: "agent", Watched: true, Record: clock}, true, DriverWait},
		{"in_progress nothing", DriverFacts{Status: "in_progress", AssigneeType: "agent"}, true, DriverNone},
		{"todo with wakeup", DriverFacts{Status: "todo", AssigneeType: "agent", Wakeup: true}, true, DriverWait},
		{"parent waits on children", DriverFacts{Status: "in_progress", AssigneeType: "agent", OpenChildren: true}, true, DriverWait},
		{"todo agent nothing", DriverFacts{Status: "todo", AssigneeType: "agent"}, true, DriverNone},
		{"todo unassigned", DriverFacts{Status: "todo"}, true, DriverNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = now
			got, ok := DriverOf(tc.in)
			if ok != tc.ok || got.Kind != tc.kind {
				t.Fatalf("DriverOf = %+v ok=%v, want kind %q ok=%v", got, ok, tc.kind, tc.ok)
			}
			if ok && got.Reason == "" {
				t.Fatal("a driver must say why")
			}
		})
	}
}

func TestDriverOfReportsWhatThePatrolTried(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	got, _ := DriverOf(DriverFacts{
		Status: "todo", AssigneeType: "agent", Now: now,
		Undriven: UndrivenState{Revives: 2, LastRevive: now.Add(-time.Hour), EscalatedAt: now.Add(-time.Minute), HasEscalated: true},
	})
	if got.Revives != 2 || !got.Escalated {
		t.Fatalf("driver = %+v, want revives 2 escalated", got)
	}
	stale, _ := DriverOf(DriverFacts{
		Status: "todo", AssigneeType: "agent", Now: now,
		Undriven: UndrivenState{Revives: 2, LastRevive: now.Add(-48 * time.Hour), EscalatedAt: now.Add(-48 * time.Hour), HasEscalated: true},
	})
	if stale.Revives != 0 || stale.Escalated {
		t.Fatalf("stale bookkeeping should reset, got %+v", stale)
	}
}

// TestDecidePatrolUndriven is the DENE-1342 table: a waiter whose blocker has
// no driver reruns the blocker on its own seat, escalates once the count is
// spent, and keeps holding while somebody drives the blocker.
func TestDecidePatrolUndriven(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	quiet := 2 * QuietAfter
	waitOnA := Record{BlockedBy: []string{"DENE-1"}}
	undriven := func(st UndrivenState) []BlockerView {
		return []BlockerView{{Ref: "DENE-1", Status: "todo", Undriven: true, UndrivenWhy: "有执行人，但没有运行", State: st}}
	}
	cases := []struct {
		name   string
		in     PatrolInput
		action string
		target string
	}{
		{
			name:   "blocker driven holds",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: []BlockerView{{Ref: "DENE-1", Status: "in_progress"}}},
			action: ActionHold,
		},
		{
			name:   "blocker undriven first time reruns it",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{})},
			action: ActionRevive, target: "DENE-1",
		},
		{
			name:   "one rerun spent still reruns",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{Revives: 1, LastRevive: now.Add(-time.Hour)})},
			action: ActionRevive, target: "DENE-1",
		},
		{
			name:   "rerun too recent holds",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{Revives: 1, LastRevive: now.Add(-5 * time.Minute)})},
			action: ActionHold,
		},
		{
			name:   "reruns spent escalates",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{Revives: ReviveLimit, LastRevive: now.Add(-time.Hour)})},
			action: ActionEscalate, target: "DENE-1",
		},
		{
			name: "escalated holds",
			in: PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{
				Revives: ReviveLimit, LastRevive: now.Add(-time.Hour), EscalatedAt: now.Add(-time.Hour), HasEscalated: true,
			})},
			action: ActionHold,
		},
		{
			name:   "old count resets",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: undriven(UndrivenState{Revives: ReviveLimit, LastRevive: now.Add(-2 * ReviveWindow)})},
			action: ActionRevive, target: "DENE-1",
		},
		{
			name:   "cleared blocker wins",
			in:     PatrolInput{Status: "blocked", Quiet: quiet, Record: waitOnA, Blockers: []BlockerView{{Ref: "DENE-1", Status: "done", Undriven: true}}},
			action: ActionWake,
		},
		{
			name: "own due clock first",
			in: PatrolInput{Status: "blocked", Quiet: quiet, Blockers: undriven(UndrivenState{}),
				Record: Record{BlockedBy: []string{"DENE-1"}, HasWakeAt: true, WakeAt: now.Add(-time.Minute)}},
			action: ActionWake,
		},
		{
			name:   "todo undriven quiet reruns itself",
			in:     PatrolInput{Status: "todo", Quiet: quiet, Undriven: true, UndrivenWhy: "有执行人，但没有运行"},
			action: ActionRevive,
		},
		{
			name:   "todo undriven not yet quiet holds",
			in:     PatrolInput{Status: "todo", Quiet: time.Minute, Undriven: true},
			action: ActionHold,
		},
		{
			name:   "todo driven holds",
			in:     PatrolInput{Status: "todo", Quiet: quiet},
			action: ActionHold,
		},
		{
			name:   "todo reruns spent escalates",
			in:     PatrolInput{Status: "todo", Quiet: quiet, Undriven: true, UndrivenState: UndrivenState{Revives: ReviveLimit, LastRevive: now.Add(-time.Hour)}},
			action: ActionEscalate,
		},
		{
			name:   "recent patrol holds todo",
			in:     PatrolInput{Status: "todo", Quiet: quiet, Undriven: true, HasLastPatrol: true, LastPatrol: now.Add(-time.Minute)},
			action: ActionHold,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = now
			got := DecidePatrol(tc.in)
			if got.Action != tc.action || got.Target != tc.target {
				t.Fatalf("DecidePatrol = %q target %q (%s), want %q target %q", got.Action, got.Target, got.Reason, tc.action, tc.target)
			}
			if (got.Action == ActionRevive || got.Action == ActionEscalate) && got.Reason == "" {
				t.Fatal("revive and escalate must say why")
			}
		})
	}
}

func TestUndrivenFollowUp(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	st := UndrivenState{Revives: 1, LastRevive: now.Add(-time.Hour)}
	set := Decision{Action: ActionRevive}.UndrivenFollowUp(st, now)
	if set[KeyRevives] != "2" || set[KeyRevivedAt] != "2026-10-04T12:00:00Z" {
		t.Fatalf("revive follow-up = %v", set)
	}
	set = Decision{Action: ActionEscalate}.UndrivenFollowUp(st, now)
	if set[KeyEscalatedAt] == "" || set[KeyRevives] != "" {
		t.Fatalf("escalate follow-up = %v", set)
	}
	back := ParseUndriven(map[string]any{KeyRevives: "2", KeyRevivedAt: "2026-10-04T11:00:00Z", KeyEscalatedAt: "2026-10-04T11:30:00Z"})
	if back.Revives != 2 || !back.HasEscalated || back.LastRevive.IsZero() {
		t.Fatalf("ParseUndriven = %+v", back)
	}
	set, _ = Decision{Action: ActionRevive}.FollowUp(now, "", "", "")
	if set[KeyPatrolAt] == "" {
		t.Fatal("a revive stamps the patrol time on the waiter")
	}
}
