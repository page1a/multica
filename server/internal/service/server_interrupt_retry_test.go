package service

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestServerInterruptRetriesAndThenBlocks is the DENE-813 regression: a daemon
// that reports failure_reason=cancelled ("task cancelled by server") while the
// row is still running must enqueue the next attempt on the same session and
// workdir, leave a notice, and once the budget is spent move the issue off
// in_progress.
func TestServerInterruptRetriesAndThenBlocks(t *testing.T) {
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
			session_id, work_dir
		)
		VALUES ($1, $2, $3, 'running', 0, 1, 2, 'src-session', '/tmp/src-workdir')
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&parentID); err != nil {
		t.Fatalf("insert parent task: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
	})

	svc := NewTaskService(q, pool, nil, events.New())
	if _, err := svc.FailTask(ctx, parentID, "task cancelled by server", "src-session", "/tmp/src-workdir", "", "cancelled", false, "", ""); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	var childID pgtype.UUID
	var childAttempt, childMax int32
	var childSession, childWorkDir, childStatus string
	if err := pool.QueryRow(ctx, `
		SELECT id, attempt, max_attempts, session_id, work_dir, status
		FROM agent_task_queue WHERE parent_task_id = $1
	`, parentID).Scan(&childID, &childAttempt, &childMax, &childSession, &childWorkDir, &childStatus); err != nil {
		t.Fatalf("expected retry child: %v", err)
	}
	if childAttempt != 2 || childMax != 2 {
		t.Fatalf("retry attempt = %d/%d, want 2/2", childAttempt, childMax)
	}
	if childSession != "src-session" || childWorkDir != "/tmp/src-workdir" {
		t.Fatalf("retry kept session %q workdir %q, want src-session /tmp/src-workdir", childSession, childWorkDir)
	}
	if childStatus != "queued" {
		t.Fatalf("retry status = %q, want queued", childStatus)
	}

	var issueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if issueStatus != "in_progress" {
		t.Fatalf("issue status after retry = %q, want in_progress", issueStatus)
	}
	firstNotice := latestComment(t, pool, issueID)
	for _, want := range []string{"被平台中断", "已自动安排续跑", "第 2 次，共 2 次", "沿用原来的会话和工作目录"} {
		if !strings.Contains(firstNotice, want) {
			t.Errorf("retry notice %q does not contain %q", firstNotice, want)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, childID); err != nil {
		t.Fatalf("mark retry running: %v", err)
	}
	if _, err := svc.FailTask(ctx, childID, "task cancelled by server", "src-session", "/tmp/src-workdir", "", "cancelled", false, "", ""); err != nil {
		t.Fatalf("FailTask exhausted: %v", err)
	}

	var grandchildren int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, childID).Scan(&grandchildren); err != nil {
		t.Fatalf("count grandchildren: %v", err)
	}
	if grandchildren != 0 {
		t.Fatalf("exhausted interrupt created %d more runs, want 0", grandchildren)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("read issue after exhaustion: %v", err)
	}
	if issueStatus != "blocked" {
		t.Fatalf("issue status after exhaustion = %q, want blocked", issueStatus)
	}
	finalNotice := latestComment(t, pool, issueID)
	for _, want := range []string{"被平台中断", "自动续跑没有再排", "第 2 次，共 2 次", "blocked", "重新叫醒这张票的执行人", "不用别人来重派"} {
		if !strings.Contains(finalNotice, want) {
			t.Errorf("give-up notice %q does not contain %q", finalNotice, want)
		}
	}
}

// TestPersonCancelAndHaltDoNotRetryServerInterrupt pins the other half of
// DENE-813: cancel-task and halt stay terminal. They do not enqueue a retry
// and they do not post the platform-interrupt notice.
func TestPersonCancelAndHaltDoNotRetryServerInterrupt(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	_, userID, agentID, issueID := seedAttributionFixture(t, pool)

	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("read agent runtime: %v", err)
	}
	svc := NewTaskService(q, pool, nil, events.New())

	t.Run("cancel-task", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, issueID); err != nil {
			t.Fatalf("mark issue in progress: %v", err)
		}
		var taskID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts)
			VALUES ($1, $2, $3, 'running', 0, 1, 2)
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		t.Cleanup(func() {
			pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
		if _, err := svc.CancelTaskByUser(ctx, taskID, TaskCancellationActor{
			Type: "member",
			ID:   util.MustParseUUID(userID),
			Name: "Attr User",
		}); err != nil {
			t.Fatalf("CancelTaskByUser: %v", err)
		}
		assertNoInterruptRetry(t, pool, taskID, issueID)
	})

	t.Run("halt", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE issue SET status = 'in_progress' WHERE id = $1`, issueID); err != nil {
			t.Fatalf("mark issue in progress: %v", err)
		}
		var taskID pgtype.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, attempt, max_attempts)
			VALUES ($1, $2, $3, 'running', 0, 1, 2)
			RETURNING id
		`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		t.Cleanup(func() {
			pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
		if err := svc.CancelTasksForIssue(ctx, util.MustParseUUID(issueID)); err != nil {
			t.Fatalf("CancelTasksForIssue: %v", err)
		}
		assertNoInterruptRetry(t, pool, taskID, issueID)
	})
}

func assertNoInterruptRetry(t *testing.T, pool *pgxpool.Pool, taskID pgtype.UUID, issueID string) {
	t.Helper()
	ctx := context.Background()
	var children int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, taskID).Scan(&children); err != nil {
		t.Fatalf("count retries: %v", err)
	}
	if children != 0 {
		t.Fatalf("person cancel created %d retries, want 0", children)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("issue status = %q, want in_progress", status)
	}
	var notices int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%被平台中断%'`, issueID).Scan(&notices); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	if notices != 0 {
		t.Fatalf("person cancel posted %d platform-interrupt notices, want 0", notices)
	}
}

func latestComment(t *testing.T, pool *pgxpool.Pool, issueID string) string {
	t.Helper()
	var content string
	if err := pool.QueryRow(context.Background(), `
		SELECT content FROM comment WHERE issue_id = $1 ORDER BY created_at DESC LIMIT 1
	`, issueID).Scan(&content); err != nil {
		t.Fatalf("read latest comment: %v", err)
	}
	return content
}
