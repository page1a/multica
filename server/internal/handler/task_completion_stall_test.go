package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestCompleteTask_SignalsRunCompletedWithoutTerminalStatus proves the
// completion-path fallback is wired into the daemon's terminal callback, not
// merely available: a run that /complete ends cleanly on an issue still sitting
// in_progress, with nothing queued behind it, must leave the observable stall
// signal and must not move the issue.
func TestCompleteTask_SignalsRunCompletedWithoutTerminalStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var agentID, runtimeID string
	dbfx.QueryRow(t,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND runtime_id IS NOT NULL LIMIT 1`,
		testWorkspaceID).Scan(&agentID, &runtimeID)

	issueID := dbfx.Issue(t, "completion-stall e2e fixture", testutil.Cols{
		"status":        "in_progress",
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueID,
		"status":     "running",
	})

	if w := completeTaskViaHandler(t, taskID, "delivered half of the acceptance criteria"); w.Code != http.StatusOK {
		t.Fatalf("CompleteTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var signals int
	dbfx.QueryRow(t,
		`SELECT count(*) FROM comment WHERE issue_id = $1 AND author_type = 'system'
		   AND content LIKE '%' || $2 || '%'`,
		issueID, service.CompletionStallMarker).Scan(&signals)
	if signals != 1 {
		t.Fatalf("completion-stall signal comments = %d, want 1", signals)
	}

	// The signal must not write the issue. It does queue one bounded closeout
	// run for the assignee — the same contract the service-level stall test
	// pins. That run is the recovery, not a comment reconcile.
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	if status != "in_progress" {
		t.Errorf("issue status = %q, want in_progress", status)
	}
	if n := pendingTaskCountForAgentIssue(t, issueID, agentID); n != 1 {
		t.Errorf("follow-up tasks enqueued by the signal = %d, want 1", n)
	}
	var handoff string
	dbfx.QueryRow(t, `SELECT COALESCE(handoff_note, '') FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, agentID).Scan(&handoff)
	if !strings.Contains(handoff, "close protocol") {
		t.Errorf("recovery handoff = %q, want the closeout instruction", handoff)
	}
}

// TestCompleteTask_NoStallSignalWhenIssueIsTerminal is the negative half of the
// wiring: an issue the executor already moved to in_review stays silent.
func TestCompleteTask_NoStallSignalWhenIssueIsTerminal(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	var agentID, runtimeID string
	dbfx.QueryRow(t,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND runtime_id IS NOT NULL LIMIT 1`,
		testWorkspaceID).Scan(&agentID, &runtimeID)

	issueID := dbfx.Issue(t, "completion-stall in_review fixture", testutil.Cols{
		"status":        "in_review",
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueID,
		"status":     "running",
	})

	if w := completeTaskViaHandler(t, taskID, "handed to review"); w.Code != http.StatusOK {
		t.Fatalf("CompleteTask: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var signals int
	dbfx.QueryRow(t,
		`SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%' || $2 || '%'`,
		issueID, service.CompletionStallMarker).Scan(&signals)
	if signals != 0 {
		t.Fatalf("completion-stall signal comments = %d, want 0 for an in_review issue", signals)
	}
}
