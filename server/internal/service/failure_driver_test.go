package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParkableAfterFailure(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	agentOwned := db.Issue{
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
	}
	personOwned := agentOwned
	personOwned.AssigneeType = pgtype.Text{String: "member", Valid: true}
	withMeta := func(meta string) db.Issue {
		issue := agentOwned
		issue.Metadata = []byte(meta)
		return issue
	}
	cases := []struct {
		name   string
		issue  db.Issue
		status string
		want   bool
	}{
		{"agent-owned todo", agentOwned, issuestatus.Todo, true},
		{"agent-owned in_progress", agentOwned, issuestatus.InProgress, true},
		{"person owns it", personOwned, issuestatus.InProgress, false},
		{"unassigned belongs to routing", db.Issue{}, issuestatus.Todo, false},
		{"in_review has its seat", agentOwned, issuestatus.InReview, false},
		{"done stays done", agentOwned, issuestatus.Done, false},
		{"blocked with no wait", withMeta(`{}`), issuestatus.Blocked, true},
		{"blocked with a spent clock", withMeta(`{"block.wake_at":"2026-10-04T11:00:00Z"}`), issuestatus.Blocked, true},
		{"blocked with a clock still ahead", withMeta(`{"block.wake_at":"2026-10-04T13:00:00Z"}`), issuestatus.Blocked, false},
		{"blocked on another issue", withMeta(`{"block.blocked_by":"DENE-1"}`), issuestatus.Blocked, false},
		{"blocked on a person", withMeta(`{"block.needs_human":"someone"}`), issuestatus.Blocked, false},
	}
	for _, tc := range cases {
		if got := parkableAfterFailure(tc.issue, tc.status, now); got != tc.want {
			t.Errorf("%s: parkable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// seedChildOf makes issueID a sub-issue of a fresh parent and returns it.
func seedChildOf(t *testing.T, pool *pgxpool.Pool, workspaceID, userID, issueID string) string {
	t.Helper()
	ctx := context.Background()
	var parentID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id, priority, number)
		VALUES ($1, 'parent issue', 'member', $2, 'medium',
			(SELECT COALESCE(max(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id`, workspaceID, userID).Scan(&parentID); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET parent_issue_id = $1 WHERE id = $2`, parentID, issueID); err != nil {
		t.Fatalf("link child: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, parentID) })
	return parentID
}

func readIssueBlock(t *testing.T, pool *pgxpool.Pool, issueID string) (string, map[string]any) {
	t.Helper()
	var status string
	var metadata map[string]any
	if err := pool.QueryRow(context.Background(), `SELECT status, metadata FROM issue WHERE id = $1`, issueID).Scan(&status, &metadata); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	return status, metadata
}

// TestFailedRunWithNoRetryLeavesADriver pins DENE-1339 for a router-direct
// run: a failure that queues no retry parks the issue as blocked with a
// failure clock the patrol will act on, and tells the parent the truth.
func TestFailedRunWithNoRetryLeavesADriver(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	q := db.New(pool)
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	parentID := seedChildOf(t, pool, workspaceID, userID, issueID)
	runID := seedRetryableRun(t, pool, agentID, issueID, 1)
	svc := NewTaskService(q, pool, nil, events.New())

	before := time.Now()
	if _, retried := failAndTakeChild(t, pool, svc, runID, "agent crashed", "agent_error"); retried {
		t.Fatal("a one-attempt run must not retry")
	}
	status, meta := readIssueBlock(t, pool, issueID)
	if status != issuestatus.Blocked {
		t.Fatalf("issue status = %q, want blocked", status)
	}
	rec := blockwait.ParseMetadata(meta)
	if !rec.HasWakeAt || rec.WakeAt.After(time.Now().Add(time.Second)) || rec.WakeAt.Before(before.Add(-time.Second)) {
		t.Fatalf("first failure should leave a clock that is already due, got %+v", rec)
	}
	if blockwait.MetaString(meta, blockwait.KeyWatched) != blockwait.WatchedYes {
		t.Fatal("the patrol cannot see an unwatched block")
	}
	notice := latestComment(t, pool, parentID)
	if !strings.Contains(notice, "已把它转为 blocked") || strings.Contains(notice, "mention://agent/") {
		t.Fatalf("parent notice = %q", notice)
	}
}

// TestChatDelegatedFailureStillLeavesADriver is DENE-1312 itself: the run was
// delegated from a chat, so delegated recovery had no issue to return to and
// the parent note skipped it for being delegated. Nothing drove the issue.
func TestChatDelegatedFailureStillLeavesADriver(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	parentID := seedChildOf(t, pool, workspaceID, userID, issueID)
	runID := seedRetryableRun(t, pool, agentID, issueID, 1)

	var sourceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, attempt, max_attempts)
		SELECT id, runtime_id, 'completed', 0, 1, 1 FROM agent WHERE id = $1
		RETURNING id`, agentID).Scan(&sourceID); err != nil {
		t.Fatalf("seed chat source task: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, sourceID) })
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET delegated_from_task_id = $1 WHERE id = $2`, sourceID, runID); err != nil {
		t.Fatalf("mark delegated: %v", err)
	}

	svc := NewTaskService(q, pool, nil, events.New())
	if _, retried := failAndTakeChild(t, pool, svc, runID, "execenv: git worktree add: fatal: not a git repository", "environment_prepare_failed"); retried {
		t.Fatal("a deterministic prepare failure must not retry")
	}
	if status, _ := readIssueBlock(t, pool, issueID); status != issuestatus.Blocked {
		t.Fatalf("issue status = %q, want blocked", status)
	}
	if notice := latestComment(t, pool, parentID); !strings.Contains(notice, "已把它转为 blocked") {
		t.Fatalf("parent never heard about the failure: %q", notice)
	}
}

// A person-owned issue already has its driver. The run's failure leaves the
// status alone and the parent notice promises nothing.
func TestFailedRunOnPersonOwnedIssueIsNotParked(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	parentID := seedChildOf(t, pool, workspaceID, userID, issueID)
	runID := seedRetryableRun(t, pool, agentID, issueID, 1)
	if _, err := pool.Exec(ctx, `UPDATE issue SET assignee_type = 'member', assignee_id = $1 WHERE id = $2`, userID, issueID); err != nil {
		t.Fatalf("hand to person: %v", err)
	}

	svc := NewTaskService(q, pool, nil, events.New())
	failAndTakeChild(t, pool, svc, runID, "agent crashed", "agent_error")
	if status, _ := readIssueBlock(t, pool, issueID); status == issuestatus.Blocked {
		t.Fatal("a person-owned issue must not be parked by the platform")
	}
	notice := latestComment(t, pool, parentID)
	if strings.Contains(notice, "到点会") || !strings.Contains(notice, "没有为它排上") {
		t.Fatalf("parent notice = %q", notice)
	}
}

// TestTransientCheckoutFailureIsRetried: an environment_prepare_failed whose
// text is git losing a passing write race gets one server retry after a pause,
// even from a daemon that has not upgraded. A deterministic one stays final.
func TestTransientCheckoutFailureIsRetried(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	runID := seedRetryableRun(t, pool, agentID, issueID, 2)
	svc := NewTaskService(q, pool, nil, events.New())

	text := "execenv: git worktree add: error: unable to write file public/a.png\nfatal: Could not reset index file to revision 'HEAD'.: exit status 128"
	if _, err := svc.FailTask(ctx, runID, text, "", "", "", "environment_prepare_failed", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	var fireAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `SELECT fire_at FROM agent_task_queue WHERE parent_task_id = $1`, runID).Scan(&fireAt); err != nil {
		t.Fatalf("passing checkout fault was not retried: %v", err)
	}
	if !fireAt.Valid || time.Until(fireAt.Time) < 20*time.Second {
		t.Fatalf("retry should wait about %s, fire_at = %+v", environmentPrepareRetryWait, fireAt)
	}
	if status, _ := readIssueBlock(t, pool, issueID); status == issuestatus.Blocked {
		t.Fatal("a queued retry is the driver; the issue must not be parked")
	}
}
