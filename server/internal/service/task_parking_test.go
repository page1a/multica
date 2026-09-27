package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/parking"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type parkingRow struct {
	state, category, stuckKind, summary, source, ownerType, ownerID string
	unexplained                                                     bool
	timeline                                                        []parking.Event
}

func readParking(t *testing.T, fx *testutil.Fixture, issueID string) parkingRow {
	t.Helper()
	var r parkingRow
	var raw []byte
	fx.QueryRow(t, `SELECT state, category, stuck_kind, summary, summary_source, next_owner_type, next_owner_id, unexplained, timeline
		FROM issue_parking_record WHERE issue_id = $1`, issueID).
		Scan(&r.state, &r.category, &r.stuckKind, &r.summary, &r.source, &r.ownerType, &r.ownerID, &r.unexplained, &raw)
	if err := json.Unmarshal(raw, &r.timeline); err != nil {
		t.Fatalf("timeline: %v", err)
	}
	return r
}

func parkingInboxCount(t *testing.T, fx *testutil.Fixture, issueID string) int {
	t.Helper()
	var n int
	fx.QueryRow(t, `SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND type = $2 AND recipient_id = $3`,
		issueID, ParkingInboxType, fx.UserID).Scan(&n)
	return n
}

type fakeSummarizer struct {
	calls int
	facts routing.SummaryFacts
	err   error
}

func (f *fakeSummarizer) Summarize(_ context.Context, _ string, facts routing.SummaryFacts) (string, error) {
	f.calls++
	f.facts = facts
	if f.err != nil {
		return "", f.err
	}
	return "运行失败，分支没记上，送审被拒。", nil
}

// DENE-707: run finished, PR merged, ticket still in progress, no close.
// The record says so, and the owner hears about it once — not once per run.
func TestParkingDoneButNotClosed(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	issueID := fx.Issue(t, "Merged but open", testutil.Cols{
		"status": "in_progress", "assignee_type": "agent", "assignee_id": agentID,
	})
	sum := &fakeSummarizer{err: routing.ErrJudgeUnavailable}
	svc.ParkingSummarizer = sum

	task := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{task})

	r := readParking(t, fx, issueID)
	if r.category != parking.CategoryStalledUnclosed || !r.unexplained || r.state != parking.StateParked {
		t.Fatalf("record = %+v", r)
	}
	if r.ownerType != "agent" || r.ownerID != agentID {
		t.Errorf("next owner = %s/%s", r.ownerType, r.ownerID)
	}
	if r.source != "template" || r.summary == "" {
		t.Errorf("summary = %q (%s), want the fixed wording when the model is off", r.summary, r.source)
	}
	if sum.calls != 1 {
		t.Errorf("summarizer calls = %d, want 1 (no agent words)", sum.calls)
	}
	if got := parkingInboxCount(t, fx, issueID); got != 1 {
		t.Fatalf("inbox items = %d, want 1", got)
	}

	// The stall signal queued a recovery run; it finishes the same way.
	again := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	fx.Exec(t, `UPDATE agent_task_queue SET status = 'cancelled' WHERE issue_id = $1 AND status IN ('queued','dispatched','running')`, issueID)
	svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{again})
	if got := parkingInboxCount(t, fx, issueID); got != 1 {
		t.Fatalf("inbox items after a second identical stop = %d, want still 1", got)
	}
}

// DENE-872 / 871 / 811: the run failed at the hand-off and the review move
// was refused. The agent's only words are the error, so the model phrases it.
func TestParkingDeliveryStuck(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	issueID := fx.Issue(t, "Delivery stuck", testutil.Cols{
		"status": "todo", "assignee_type": "agent", "assignee_id": agentID,
	})
	start := time.Now().Add(-10 * time.Minute)
	taskID := fx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": runtimeID, "status": "failed",
		"started_at": start, "created_at": start, "completed_at": start.Add(8 * time.Minute),
		"failure_reason": "agent_error",
		"error":          "local_directory worktree: refusing to record branch agent/agent/dene-872",
	})
	fx.Exec(t, `INSERT INTO issue_rejection (id, workspace_id, issue_id, actor_type, action, kind, reason)
		VALUES (gen_random_uuid(), $1, $2, 'agent', 'status:in_review', 'pr_not_linked', '进不了待验收：没有关联的 PR')`,
		fx.WorkspaceID, issueID)
	sum := &fakeSummarizer{}
	svc.ParkingSummarizer = sum

	task, err := svc.Queries.GetAgentTask(ctx, stallTestUUID(t, taskID))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.RecordParking(ctx, task.IssueID, task); !ok {
		t.Fatal("record not written")
	}
	r := readParking(t, fx, issueID)
	if r.category != parking.CategoryStalledDelivery || r.stuckKind != parking.StuckBranchRefused || !r.unexplained {
		t.Fatalf("record = %+v", r)
	}
	if r.source != "model" || sum.facts.Category != parking.CategoryStalledDelivery {
		t.Errorf("summary source = %s facts = %+v", r.source, sum.facts)
	}
	var kinds []string
	for _, e := range r.timeline {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[0] != "run_started" || kinds[2] != "rejected" {
		t.Errorf("timeline = %v", kinds)
	}
}

// DENE-807 / 750: blocked, the agent replied "已完成" and left no wait record.
func TestParkingReplyWithoutCloseOnBlocked(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	issueID := fx.Issue(t, "Blocked, replied", testutil.Cols{
		"status": "blocked", "assignee_type": "agent", "assignee_id": agentID,
	})
	task := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	fx.Exec(t, `INSERT INTO comment (id, workspace_id, issue_id, author_type, author_id, content, source_task_id)
		VALUES (gen_random_uuid(), $1, $2, 'agent', $3, '已完成：四个问题都改好了，PR #171', $4)`,
		fx.WorkspaceID, issueID, agentID, task.ID)
	svc.ParkingSummarizer = &fakeSummarizer{}

	svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{task})
	r := readParking(t, fx, issueID)
	if r.category != parking.CategoryStalledReplyUnclosed || r.stuckKind != parking.StuckNoWaitRecord || !r.unexplained {
		t.Fatalf("record = %+v", r)
	}
	if r.source != "agent" || r.summary != "已完成：四个问题都改好了，PR #171" {
		t.Errorf("summary = %q (%s), want the agent's own words", r.summary, r.source)
	}
	if got := parkingInboxCount(t, fx, issueID); got != 1 {
		t.Fatalf("inbox items = %d, want 1", got)
	}
}

// DENE-822: a structured wait on a person is an explained stop — recorded,
// never pushed to the inbox.
func TestParkingIntentionalWaitIsNotUnexplained(t *testing.T) {
	ctx := context.Background()
	fx, svc, _, agentID, runtimeID := newCompletionStallFixture(t)
	closedAt := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	meta, _ := json.Marshal(map[string]any{
		"block.needs_human": fx.UserID,
		"close.at":          closedAt,
		"close.status":      "blocked",
		"close.conclusion":  "blocked",
	})
	issueID := fx.Issue(t, "Waiting on Kun", testutil.Cols{
		"status": "blocked", "assignee_type": "agent", "assignee_id": agentID, "metadata": string(meta),
	})
	sum := &fakeSummarizer{}
	svc.ParkingSummarizer = sum
	task := completedTaskFor(t, fx, agentID, runtimeID, issueID)
	svc.HandleCompletedTasks(ctx, []db.AgentTaskQueue{task})

	r := readParking(t, fx, issueID)
	if r.category != parking.CategoryWaitingPerson || r.unexplained {
		t.Fatalf("record = %+v", r)
	}
	if r.ownerType != "member" || r.ownerID != fx.UserID {
		t.Errorf("next owner = %s/%s", r.ownerType, r.ownerID)
	}
	if got := parkingInboxCount(t, fx, issueID); got != 0 {
		t.Fatalf("inbox items = %d, want 0 for an intentional wait", got)
	}
}
