package main

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// setWorkspaceTaskTimeLimit writes the raw settings value and restores the
// previous settings on cleanup. The value is raw JSON so malformed shapes can
// be exercised too.
func setWorkspaceTaskTimeLimit(t *testing.T, rawValue string) {
	t.Helper()
	ctx := context.Background()
	var previous []byte
	if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous); err != nil {
		t.Fatalf("read workspace settings: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, previous)
	})
	if _, err := testPool.Exec(ctx, `
		UPDATE workspace
		SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), '{agent_task_timeout_minutes}', $2::jsonb)
		WHERE id = $1
	`, testWorkspaceID, rawValue); err != nil {
		t.Fatalf("set workspace time limit: %v", err)
	}
}

func taskStatusAndReason(t *testing.T, taskID string) (string, string) {
	t.Helper()
	var status, reason string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status, COALESCE(failure_reason, '') FROM agent_task_queue WHERE id = $1`, taskID,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("read task: %v", err)
	}
	return status, reason
}

// The fixture task started 3h ago on a runtime with a fresh heartbeat, so the
// liveness sweepers leave it alone. Only the workspace limit can stop it.
func TestTaskTimeLimitStopsHealthyRunPastTheLimit(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	issueID, agentID, taskID := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })
	setWorkspaceTaskTimeLimit(t, "60")

	stopped, err := db.New(testPool).FailTasksOverWorkspaceTimeLimit(context.Background())
	if err != nil {
		t.Fatalf("FailTasksOverWorkspaceTimeLimit: %v", err)
	}
	found := false
	for _, s := range stopped {
		if s.ID.Bytes == parseUUIDBytes(taskID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("task 3h into a 60m limit was not returned as stopped")
	}
	if status, reason := taskStatusAndReason(t, taskID); status != "failed" || reason != "task_time_limit" {
		t.Fatalf("task = (%q, %q), want (failed, task_time_limit)", status, reason)
	}
}

func TestTaskTimeLimitLeavesRunsAloneWhenNoLimitApplies(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	for name, raw := range map[string]string{
		"under the limit":   "600",
		"zero is unlimited": "0",
		"negative ignored":  "-5",
		"oversized ignored": "99999999999",
	} {
		t.Run(name, func(t *testing.T) {
			issueID, agentID, taskID := setupSweeperTestFixture(t, "running")
			t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })
			setWorkspaceTaskTimeLimit(t, raw)

			if _, err := db.New(testPool).FailTasksOverWorkspaceTimeLimit(context.Background()); err != nil {
				t.Fatalf("FailTasksOverWorkspaceTimeLimit: %v", err)
			}
			if status, _ := taskStatusAndReason(t, taskID); status != "running" {
				t.Fatalf("task status = %q, want running", status)
			}
		})
	}
}

func TestTaskTimeLimitNoticeReachesIssueAndParent(t *testing.T) {
	if testPool == nil {
		t.Skip("no database connection")
	}
	ctx := context.Background()
	issueID, agentID, taskID := setupSweeperTestFixture(t, "running")
	t.Cleanup(func() { cleanupSweeperFixture(t, issueID, agentID) })

	parentID := createTestIssue(t, testWorkspaceID, testUserID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = ANY($1::uuid[])`, []string{issueID, parentID})
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = ANY($1::uuid[])`, []string{issueID, parentID})
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, parentID)
	})
	if _, err := testPool.Exec(ctx, `UPDATE issue SET parent_issue_id = $2 WHERE id = $1`, issueID, parentID); err != nil {
		t.Fatalf("link parent: %v", err)
	}
	setWorkspaceTaskTimeLimit(t, "60")

	queries := db.New(testPool)
	taskSvc := service.NewTaskService(queries, testPool, nil, events.New())
	sweepTaskTimeLimits(ctx, taskSvc)

	if status, reason := taskStatusAndReason(t, taskID); status != "failed" || reason != "task_time_limit" {
		t.Fatalf("task = (%q, %q), want (failed, task_time_limit)", status, reason)
	}
	var retries int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND id <> $2`, issueID, taskID).Scan(&retries); err != nil {
		t.Fatalf("count retries: %v", err)
	}
	if retries != 0 {
		t.Fatalf("time-limit stop enqueued %d follow-up task(s); it must stay terminal", retries)
	}

	for label, id := range map[string]string{"issue": issueID, "parent": parentID} {
		var content string
		if err := testPool.QueryRow(ctx,
			`SELECT content FROM comment WHERE issue_id = $1 AND author_type = 'system' ORDER BY created_at DESC LIMIT 1`, id,
		).Scan(&content); err != nil {
			t.Fatalf("%s: no system notice: %v", label, err)
		}
		if !strings.Contains(content, "task time limit") || !strings.Contains(content, "3h") {
			t.Fatalf("%s notice lacks the limit or the measured runtime: %q", label, content)
		}
		if !strings.Contains(content, "mention://member/") {
			t.Fatalf("%s notice does not mention a responsible member: %q", label, content)
		}
	}
}
