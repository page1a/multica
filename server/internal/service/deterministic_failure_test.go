package service

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTaskFailureInputVersionPrefersNarrowestOwner(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	commentID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	chatID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	if got := taskFailureInputVersion(db.AgentTaskQueue{IssueID: issueID}); got != "issue:"+util.UUIDToString(issueID) {
		t.Fatalf("issue fallback = %q", got)
	}
	if got := taskFailureInputVersion(db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID}); got != "comment:"+util.UUIDToString(commentID) {
		t.Fatalf("comment version = %q", got)
	}
	if got := taskFailureInputVersion(db.AgentTaskQueue{IssueID: issueID, TriggerCommentID: commentID, ChatInputTaskID: chatID}); got != "chat:"+util.UUIDToString(chatID) {
		t.Fatalf("chat version = %q", got)
	}
	if got := taskFailureInputVersion(db.AgentTaskQueue{}); got != "" {
		t.Fatalf("no owner should give empty version, got %q", got)
	}
}

func seedRetryableRun(t *testing.T, pool *pgxpool.Pool, agentID, issueID string, maxAttempts int) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("mark issue in progress: %v", err)
	}
	var parentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts, session_id, work_dir
		)
		VALUES ($1, $2, $3, 'running', 0, 1, $4, 'det-session', '/tmp/det-workdir')
		RETURNING id
	`, agentID, runtimeID, issueID, maxAttempts).Scan(&parentID); err != nil {
		t.Fatalf("insert parent task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})
	return parentID
}

func failAndTakeChild(t *testing.T, pool *pgxpool.Pool, svc *TaskService, taskID pgtype.UUID, errText, reason string) (pgtype.UUID, bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := svc.FailTask(ctx, taskID, errText, "det-session", "/tmp/det-workdir", "", reason, false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	var childID pgtype.UUID
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, taskID).Scan(&n); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if n == 0 {
		return childID, false
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE parent_task_id = $1`, taskID).Scan(&childID); err != nil {
		t.Fatalf("read child: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, childID); err != nil {
		t.Fatalf("mark child running: %v", err)
	}
	return childID, true
}

// TestDeterministicFailureStopsRetryAndHandsToHuman pins DENE-819: the second
// consecutive hit of the same deterministic error on the same input stops
// the auto-retry chain early, records the fingerprint, blocks the issue with
// a structured needs_human wait, and tells the issue what to do next.
func TestDeterministicFailureStopsRetryAndHandsToHuman(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, userID, agentID, issueID := seedAttributionFixture(t, pool)
	parentID := seedRetryableRun(t, pool, agentID, issueID, 4)
	svc := NewTaskService(q, pool, nil, events.New())

	first := "prepare worktree: mkdir /work/" + issueID + "/wt-1: permission denied"
	childID, retried := failAndTakeChild(t, pool, svc, parentID, first, "runtime_recovery")
	if !retried {
		t.Fatal("first deterministic hit should still retry (only one witness so far)")
	}
	var version, fingerprint string
	if err := pool.QueryRow(ctx, `SELECT failure_input_version, failure_fingerprint FROM agent_task_queue WHERE id = $1`, parentID).Scan(&version, &fingerprint); err != nil {
		t.Fatalf("read failure record: %v", err)
	}
	if version != "issue:"+issueID || fingerprint == "" {
		t.Fatalf("failure record = (%q, %q), want issue version and a fingerprint", version, fingerprint)
	}

	second := "prepare worktree: mkdir /work/" + issueID + "/wt-2: permission denied"
	if _, retriedAgain := failAndTakeChild(t, pool, svc, childID, second, "runtime_recovery"); retriedAgain {
		t.Fatal("second consecutive deterministic hit must not enqueue another retry")
	}
	var status string
	var metadata map[string]any
	if err := pool.QueryRow(ctx, `SELECT status, metadata FROM issue WHERE id = $1`, issueID).Scan(&status, &metadata); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if status != "blocked" {
		t.Fatalf("issue status = %q, want blocked", status)
	}
	if got := blockwait.MetaString(metadata, blockwait.KeyNeedsHuman); got != userID {
		t.Fatalf("needs_human = %q, want issue creator %s", got, userID)
	}
	if blockwait.MetaString(metadata, blockwait.KeyWaitCondition) == "" {
		t.Fatal("blocked issue must say what it waits on")
	}
	notice := latestComment(t, pool, issueID)
	for _, want := range []string{"自动重试已停止", fingerprint, "blocked", "重新运行"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q does not contain %q", notice, want)
		}
	}
}

// TestTransientFailureKeepsRetryBudget is the control: an identical transient
// error twice in a row still follows the existing provider_network budget.
func TestTransientFailureKeepsRetryBudget(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)
	parentID := seedRetryableRun(t, pool, agentID, issueID, 4)
	svc := NewTaskService(q, pool, nil, events.New())

	errText := "API Error: connection reset by peer"
	childID, retried := failAndTakeChild(t, pool, svc, parentID, errText, "agent_error.provider_network")
	if !retried {
		t.Fatal("transient failure should retry")
	}
	if _, retriedAgain := failAndTakeChild(t, pool, svc, childID, errText, "agent_error.provider_network"); !retriedAgain {
		t.Fatal("repeated transient failure must keep its retry budget")
	}
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress while retries continue", status)
	}
}
