package routing

import (
	"context"
	"strings"
	"testing"
)

// DENE-812: an alignment confirm creates the root in_progress and later-stage
// sub-issues in backlog — two statuses Route leaves alone. RouteGroupNode is
// what seats them, and each node kind differs only in what the seat does next.

func TestGroupRootIsSeatedAsCoordinatorWithoutARun(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "in_progress"
	store.issue.HasChildren = true
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).RouteGroupNode(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned {
		t.Fatalf("action = %q, want %q (reason %q)", out.Action, ActionAssigned, out.Reason)
	}
	if len(store.assigns) != 1 || len(store.quietAssigns) != 1 {
		t.Errorf("assigns = %v quiet = %v, want one seat written without a run", store.assigns, store.quietAssigns)
	}
	if len(store.reviewer) != 1 {
		t.Errorf("reviewer writes = %v, want the root's acceptance seat filled", store.reviewer)
	}
	body := store.comments[KindAssignment][0]
	for _, want := range []string{"协调席", "run 没启动", "父票不替子票干活"} {
		if !strings.Contains(body, want) {
			t.Errorf("decision comment is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "run 已启动") {
		t.Errorf("coordinator comment claims a run started:\n%s", body)
	}
}

func TestGroupParkedChildIsSeatedWithoutReviewerOrMention(t *testing.T) {
	store := newFakeStore()
	store.issue.Status = "backlog"
	store.issue.ParentIssueID = "parent-1"
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).RouteGroupNode(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || len(store.assigns) != 1 {
		t.Fatalf("action = %q assigns = %v, want the parked child seated", out.Action, store.assigns)
	}
	if len(store.reviewer) != 0 {
		t.Errorf("reviewer writes = %v: a sub-issue has no acceptance seat", store.reviewer)
	}
	if len(store.quietAssigns) != 1 {
		t.Errorf("quiet assigns = %v: a parked child's write must not start a run", store.quietAssigns)
	}
	if body := store.comments[KindAssignment][0]; !strings.Contains(body, "阶段提到待办时才开跑") {
		t.Errorf("parked comment does not say when it runs:\n%s", body)
	}
}

func TestGroupTodoChildRunsLikeRoute(t *testing.T) {
	store := newFakeStore()
	store.issue.ParentIssueID = "parent-1"
	judge := &fakeJudge{verdict: confidentVerdict()}

	out, err := newRouter(store, judge).RouteGroupNode(context.Background(), "ws", "issue-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Action != ActionAssigned || len(store.assigns) != 1 || len(store.quietAssigns) != 0 {
		t.Fatalf("action = %q assigns = %v quiet = %v, want a started stage-1 seat", out.Action, store.assigns, store.quietAssigns)
	}
}

func TestGroupNodeKeepsRouteQuietWhereItShould(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Issue)
	}{
		// A top-level in_progress issue with no sub-issues is somebody's work
		// in flight, not a coordinator.
		{"in_progress leaf", func(i *Issue) { i.Status = "in_progress" }},
		// A top-level backlog issue is nobody's work yet.
		{"top-level backlog", func(i *Issue) { i.Status = "backlog" }},
		{"done child", func(i *Issue) { i.Status = "done"; i.ParentIssueID = "parent-1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			tc.setup(&store.issue)
			judge := &fakeJudge{verdict: confidentVerdict()}
			out, err := newRouter(store, judge).RouteGroupNode(context.Background(), "ws", "issue-1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Action != ActionNoop || store.wrote() || judge.callCount() != 0 {
				t.Errorf("action = %q wrote = %v calls = %d, want a quiet noop", out.Action, store.wrote(), judge.callCount())
			}
		})
	}
}
