package service

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestTaskTimeLimitContinuesSameSessionThenBlocks is the DENE-857 regression:
// a run stopped with task_time_limit keeps its CLI session and workdir for
// the next attempt, and once that budget is spent the issue leaves todo.
func TestTaskTimeLimitContinuesSameSessionThenBlocks(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, _, agentID, issueID := seedAttributionFixture(t, pool)

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
			agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts,
			session_id, work_dir, failure_reason, error
		)
		VALUES ($1, $2, $3, 'failed', 0, 1, 2, 'limit-session', '/tmp/limit-workdir',
			'task_time_limit', 'task time limit reached')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
		t.Fatalf("insert stopped task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	parent, err := q.GetAgentTask(ctx, parentID)
	if err != nil {
		t.Fatalf("load stopped task: %v", err)
	}
	svc := NewTaskService(q, pool, nil, events.New())
	if retried := svc.HandleFailedTasks(ctx, []db.AgentTaskQueue{parent}); retried != 1 {
		t.Fatalf("retried = %d, want 1", retried)
	}

	var childSession, childWorkDir, childStatus string
	var childAttempt, childMax int32
	if err := pool.QueryRow(ctx, `
		SELECT session_id, work_dir, status, attempt, max_attempts
		FROM agent_task_queue WHERE parent_task_id = $1
	`, parentID).Scan(&childSession, &childWorkDir, &childStatus, &childAttempt, &childMax); err != nil {
		t.Fatalf("expected continuation: %v", err)
	}
	if childSession != "limit-session" || childWorkDir != "/tmp/limit-workdir" {
		t.Fatalf("continuation kept session %q workdir %q, want limit-session /tmp/limit-workdir", childSession, childWorkDir)
	}
	if childStatus != "queued" || childAttempt != 2 || childMax != 2 {
		t.Fatalf("continuation = %s attempt %d/%d, want queued 2/2", childStatus, childAttempt, childMax)
	}
	var issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if issueStatus != "in_progress" {
		t.Fatalf("issue status after continuation = %q, want in_progress", issueStatus)
	}
	notice := latestComment(t, pool, issueID)
	for _, want := range []string{"已自动安排续跑", "沿用原来的会话和工作目录", "收口", "拆小"} {
		if !strings.Contains(notice, want) {
			t.Errorf("continuation notice %q does not contain %q", notice, want)
		}
	}

	var childID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE parent_task_id = $1`, parentID).Scan(&childID); err != nil {
		t.Fatalf("load continuation id: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'failed', failure_reason = 'task_time_limit', error = 'task time limit reached', completed_at = now()
		WHERE id = $1
	`, childID); err != nil {
		t.Fatalf("exhaust continuation: %v", err)
	}
	child, err := q.GetAgentTask(ctx, childID)
	if err != nil {
		t.Fatalf("reload continuation: %v", err)
	}
	if retried := svc.HandleFailedTasks(ctx, []db.AgentTaskQueue{child}); retried != 0 {
		t.Fatalf("exhausted continuation retried %d, want 0", retried)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue after exhaustion: %v", err)
	}
	if issueStatus != "blocked" {
		t.Fatalf("issue status after exhaustion = %q, want blocked", issueStatus)
	}
	finalNotice := latestComment(t, pool, issueID)
	for _, want := range []string{"自动续跑没有再排", "blocked", "mention://member/"} {
		if !strings.Contains(finalNotice, want) {
			t.Errorf("give-up notice %q does not contain %q", finalNotice, want)
		}
	}
}
