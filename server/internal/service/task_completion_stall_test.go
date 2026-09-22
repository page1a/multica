package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// newCompletionStallFixture builds one workspace with an agent-assigned issue
// and a finished run on it. The pool helper skips the suite when no local
// Postgres is reachable, the same way every other DB-backed service test does.
func newCompletionStallFixture(t *testing.T) (*testutil.Fixture, *TaskService, *events.Bus, string, string) {
	t.Helper()
	fx := testutil.New(newResolveOriginatorPool(t), "", "")
	suffix := time.Now().UnixNano()
	fx.UserID = fx.User(t, "Completion stall", fmt.Sprintf("completion-stall-%d@multica.test", suffix))
	fx.WorkspaceID = fx.Workspace(t, "Completion stall", fmt.Sprintf("completion-stall-%d", suffix))
	fx.Member(t, fx.WorkspaceID, fx.UserID, "owner")
	runtimeID := fx.Runtime(t, "Completion stall runtime")
	agentID := fx.Agent(t, "Stalled executor", runtimeID)

	bus := events.New()
	svc := &TaskService{Queries: db.New(fx.Pool), Bus: bus}
	return fx, svc, bus, agentID, runtimeID
}

func stallTestUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	parsed, err := util.ParseUUID(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return parsed
}

// completedTaskFor queues a run that already finished — the shape the /complete
// callback hands to HandleCompletedTasks. The row is read back so the test
// passes the same struct the production caller does.
func completedTaskFor(t *testing.T, fx *testutil.Fixture, agentID, runtimeID, issueID string) db.AgentTaskQueue {
	t.Helper()
	taskID := fx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": runtimeID,
		"status":     "completed",
	})
	task, err := db.New(fx.Pool).GetAgentTask(context.Background(), stallTestUUID(t, taskID))
	if err != nil {
		t.Fatalf("load completed task: %v", err)
	}
	return task
}

func completionStallComments(t *testing.T, fx *testutil.Fixture, issueID string) []string {
	t.Helper()
	rows, err := fx.Pool.Query(context.Background(),
		`SELECT content FROM comment WHERE issue_id = $1 AND content LIKE '%' || $2 || '%' ORDER BY created_at`,
		issueID, CompletionStallMarker)
	if err != nil {
		t.Fatalf("list signal comments: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan signal comment: %v", err)
		}
		out = append(out, content)
	}
	return out
}

func issueStatusOf(t *testing.T, fx *testutil.Fixture, issueID string) string {
	t.Helper()
	var status string
	fx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	return status
}

// TestHandleCompletedTasksSignalsStalledIssue is the acceptance case: a run
// completed, the issue is still in_progress, and nothing is queued behind it.
func TestHandleCompletedTasksSignalsStalledIssue(t *testing.T) {
	ctx := context.Background()
	fx, svc, bus, agentID, runtimeID := newCompletionStallFixture(t)
	parentID := fx.Issue(t, "Parent orchestration issue", testutil.Cols{"status": "in_progress"})
	issueID := fx.Issue(t, "Partly delivered child", testutil.Cols{
		"status":          "in_progress",
		"assignee_type":   "agent",
		"assignee_id":     agentID,
		"parent_issue_id": parentID,
	})

	var commentEvents []events.Event
	bus.Subscribe(protocol.EventCommentCreated, func(e events.Event) { commentEvents = append(commentEvents, e) })

	completed := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != 1 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 1", got)
	}

	comments := completionStallComments(t, fx, issueID)
	if len(comments) != 1 {
		t.Fatalf("signal comments = %d, want 1", len(comments))
	}
	body := comments[0]
	if !strings.Contains(body, CompletionStallMarker) {
		t.Errorf("signal body is missing the marker:\n%s", body)
	}
	if !strings.Contains(body, "mention://agent/"+agentID) {
		t.Errorf("signal body does not name the current assignee:\n%s", body)
	}
	if !strings.Contains(body, "mention://issue/"+parentID) {
		t.Errorf("signal body does not link the parent issue:\n%s", body)
	}
	if !strings.Contains(body, "in_progress") {
		t.Errorf("signal body does not state the status:\n%s", body)
	}

	// The signal is mention-only: the issue must not be moved, because
	// completion is neither todo nor done.
	if got := issueStatusOf(t, fx, issueID); got != "in_progress" {
		t.Errorf("issue status = %q, want in_progress (the signal must not write status)", got)
	}
	// Nothing was queued either — re-waking the executor is the dispatcher's
	// call, not a side effect of detection.
	var active int
	fx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1
		AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')`, issueID).Scan(&active)
	if active != 0 {
		t.Errorf("tasks enqueued by the signal = %d, want 0", active)
	}

	if len(commentEvents) != 1 {
		t.Fatalf("comment:created events = %d, want 1", len(commentEvents))
	}
	if commentEvents[0].WorkspaceID != fx.WorkspaceID {
		t.Errorf("comment:created workspace = %q, want %q", commentEvents[0].WorkspaceID, fx.WorkspaceID)
	}
	payload, ok := commentEvents[0].Payload.(map[string]any)
	if !ok {
		t.Fatalf("comment:created payload is not map[string]any: %T", commentEvents[0].Payload)
	}
	comment, ok := payload["comment"].(map[string]any)
	if !ok {
		t.Fatalf("comment field is not map[string]any: %T", payload["comment"])
	}
	if comment["content"] != body {
		t.Errorf("broadcast content = %v, want the stored comment body", comment["content"])
	}
	if payload["issue_status"] != "in_progress" {
		t.Errorf("broadcast issue_status = %v, want in_progress", payload["issue_status"])
	}
}

// TestHandleCompletedTasksSkipsStatusesThatAreNotInProgress pins the guard
// carried over from the failure path: only in_progress is detected, so
// in_review and blocked stay with whoever owns them and every terminal /
// not-started status is silent.
func TestHandleCompletedTasksSkipsStatusesThatAreNotInProgress(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"todo", "backlog", "in_review", "blocked", "done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
			issueID := fx.Issue(t, "Issue in "+status, testutil.Cols{
				"status":        status,
				"assignee_type": "agent",
				"assignee_id":   agentID,
			})
			completed := completedTaskFor(t, fx, agentID, runtimeID, issueID)
			if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != 0 {
				t.Fatalf("HandleCompletedTasks signalled = %d, want 0 for status %q", got, status)
			}
			if comments := completionStallComments(t, fx, issueID); len(comments) != 0 {
				t.Fatalf("signal comments = %d, want 0 for status %q", len(comments), status)
			}
			if got := issueStatusOf(t, fx, issueID); got != status {
				t.Errorf("issue status = %q, want %q", got, status)
			}
		})
	}
}

// TestHandleCompletedTasksHonoursCustomStatusCategory pins the issuestatus
// guard for custom rows.
//
// MUL-7365 replaced the seven-key category table with four lifecycle
// categories and made issuestatus.Effective inherit ONLY terminal semantics:
// a custom row in `done` reads as Done and one in `closed` reads as Cancelled,
// while every nonterminal key stays distinct. There is therefore no longer a
// category that means "in progress", so a custom active status can never read
// as InProgress and never signals — the fork's old table (in_progress →
// signals, in_review → does not) is not expressible in the new vocabulary.
// This test pins the merged rule rather than the pre-merge one.
func TestHandleCompletedTasksHonoursCustomStatusCategory(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		key      string
		category string
		want     int
	}{
		// A nonterminal custom key stays distinct, so it is not "in progress".
		{key: "qa_gate", category: "started", want: 0},
		{key: "building", category: "started", want: 0},
		// A terminal custom key inherits Done, which is not a stall either.
		{key: "shipped", category: "done", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
			fx.Insert(t, "issue_status", testutil.Cols{
				"workspace_id": fx.WorkspaceID,
				"key":          tc.key,
				"name":         tc.key,
				"category":     tc.category,
				"color":        "#123456",
				"is_system":    false,
			})
			issueID := fx.Issue(t, "Issue on custom status", testutil.Cols{
				"status":        tc.key,
				"assignee_type": "agent",
				"assignee_id":   agentID,
			})
			completed := completedTaskFor(t, fx, agentID, runtimeID, issueID)
			if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != tc.want {
				t.Fatalf("HandleCompletedTasks signalled = %d, want %d for category %q", got, tc.want, tc.category)
			}
		})
	}
}

// TestHandleCompletedTasksSkipsIssuesWithActiveWork covers both halves of "no
// one is working on it": a still-running successor and an auto-retry already
// queued for the same issue. A retry child is a queued row, so the single
// active-task probe covers both — this pins that.
func TestHandleCompletedTasksSkipsIssuesWithActiveWork(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"running", "queued"} {
		t.Run(status, func(t *testing.T) {
			fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
			issueID := fx.Issue(t, "Issue with active work", testutil.Cols{
				"status":        "in_progress",
				"assignee_type": "agent",
				"assignee_id":   agentID,
			})
			completed := completedTaskFor(t, fx, agentID, runtimeID, issueID)
			fx.Task(t, agentID, testutil.Cols{
				"issue_id":       issueID,
				"runtime_id":     runtimeID,
				"status":         status,
				"parent_task_id": completed.ID,
			})

			if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != 0 {
				t.Fatalf("HandleCompletedTasks signalled = %d, want 0 with an active task", got)
			}
			if comments := completionStallComments(t, fx, issueID); len(comments) != 0 {
				t.Fatalf("signal comments = %d, want 0 with an active task", len(comments))
			}
		})
	}
}

// TestHandleCompletedTasksReportsOneIssueOncePerRound pins the round dedupe
// (several finished runs on one issue in one batch produce one signal) and the
// repeat window (a row that stays stalled across rounds does not collect one
// comment per run).
func TestHandleCompletedTasksReportsOneIssueOncePerRound(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	issueID := fx.Issue(t, "Issue with two finished runs", testutil.Cols{
		"status":        "in_progress",
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	first := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	second := completedTaskFor(t, fx, agentID, runtimeID, issueID)

	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{first, second}); got != 1 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 1 for one issue in one round", got)
	}
	if comments := completionStallComments(t, fx, issueID); len(comments) != 1 {
		t.Fatalf("signal comments = %d, want 1 after one round", len(comments))
	}

	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{second}); got != 0 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 0 inside the repeat window", got)
	}
	if comments := completionStallComments(t, fx, issueID); len(comments) != 1 {
		t.Fatalf("signal comments = %d, want 1 inside the repeat window", len(comments))
	}
}

// TestHandleCompletedTasksIgnoresIssueLessTasks pins that chat and
// quick-create runs, which have no issue to stall, are inert.
func TestHandleCompletedTasksIgnoresIssueLessTasks(t *testing.T) {
	ctx := context.Background()
	_, svc, _, agentID, _ := newCompletionStallFixture(t)
	task := db.AgentTaskQueue{AgentID: stallTestUUID(t, agentID), Status: "completed"}
	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{task}); got != 0 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 0 for a task with no issue", got)
	}
	if got := svc.HandleCompletedTasks(ctx, nil); got != 0 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 0 for an empty batch", got)
	}
}

// TestCompletionStallEligible covers the pure detection rule on both sides so a
// change to the guard fails here rather than only in the DB suite.
func TestCompletionStallEligible(t *testing.T) {
	cases := []struct {
		status string
		active bool
		want   bool
	}{
		{status: "in_progress", active: false, want: true},
		{status: "in_progress", active: true, want: false},
		{status: "in_review", active: false, want: false},
		{status: "blocked", active: false, want: false},
		{status: "done", active: false, want: false},
		{status: "todo", active: false, want: false},
		{status: "backlog", active: false, want: false},
		{status: "cancelled", active: false, want: false},
		{status: "", active: false, want: false},
	}
	for _, tc := range cases {
		if got := completionStallEligible(tc.status, tc.active); got != tc.want {
			t.Errorf("completionStallEligible(%q, %v) = %v, want %v", tc.status, tc.active, got, tc.want)
		}
	}
}

// TestHandleCompletedTasksSkipsIssuesWithOpenChildren pins the orchestration
// case. Dispatching sub-issues and leaving the parent in_progress is the
// documented way to record "work continues below", so the parent's own run
// completing there is not a stall — the children are the executors.
func TestHandleCompletedTasksSkipsIssuesWithOpenChildren(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	parentID := fx.Issue(t, "Orchestrator issue", testutil.Cols{
		"status":        "in_progress",
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	fx.Issue(t, "Dispatched child", testutil.Cols{
		"status":          "todo",
		"assignee_type":   "agent",
		"assignee_id":     agentID,
		"parent_issue_id": parentID,
	})

	completed := completedTaskFor(t, fx, agentID, runtimeID, parentID)
	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != 0 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 0 while a child is still open", got)
	}
	if comments := completionStallComments(t, fx, parentID); len(comments) != 0 {
		t.Fatalf("signal comments = %d, want 0:\n%s", len(comments), strings.Join(comments, "\n"))
	}
}

// TestHandleCompletedTasksSignalsWhenEveryChildIsTerminal is the other side of
// the same guard: once every child is done or cancelled, the parent sitting in
// in_progress with nothing queued is a real stall again.
func TestHandleCompletedTasksSignalsWhenEveryChildIsTerminal(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	parentID := fx.Issue(t, "Orchestrator issue", testutil.Cols{
		"status":        "in_progress",
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	fx.Issue(t, "Delivered child", testutil.Cols{
		"status":          "done",
		"parent_issue_id": parentID,
	})
	fx.Issue(t, "Dropped child", testutil.Cols{
		"status":          "cancelled",
		"parent_issue_id": parentID,
	})

	completed := completedTaskFor(t, fx, agentID, runtimeID, parentID)
	if got := svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{completed}); got != 1 {
		t.Fatalf("HandleCompletedTasks signalled = %d, want 1 once every child is terminal", got)
	}
	if comments := completionStallComments(t, fx, parentID); len(comments) != 1 {
		t.Fatalf("signal comments = %d, want 1", len(comments))
	}
}
