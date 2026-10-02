package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1093: a run that failed on a full model waits in place as a deferred
// child. The execution log row and issue detail both carry which retry it is
// and when it fires, so the UI and `multica issue get/runs --output json`
// can say "model full, retry N, next at HH:MM".
func TestCapacityRetryIsVisibleOnRunsAndIssueDetail(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "CapacityRetryAgent", []byte("[]"))
	issueID := dbfx.Issue(t, "capacity retry visible")
	runtimeID := handlerTestRuntimeID(t)
	fireAt := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)

	parentID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": runtimeID, "status": "failed",
		"attempt": 3, "max_attempts": 3,
		"failure_reason": "agent_error.provider_capacity_or_rate_limit",
	})
	childID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": runtimeID, "status": "deferred",
		"attempt": 4, "max_attempts": 4, "parent_task_id": parentID, "fire_at": fireAt,
	})

	var child *AgentTaskResponse
	runs := runsRequest(t, issueID, "")
	for i := range runs {
		if runs[i].ID == parentID && runs[i].CapacityRetry != nil {
			t.Fatalf("the failed parent must not carry the retry line")
		}
		if runs[i].ID == childID {
			child = &runs[i]
		}
	}
	if child == nil || child.CapacityRetry == nil {
		t.Fatalf("deferred capacity child has no capacity_retry: %+v", child)
	}
	if child.CapacityRetry.Retry != 3 || child.CapacityRetry.TaskID != childID {
		t.Fatalf("capacity_retry = %+v, want retry 3 on %s", child.CapacityRetry, childID)
	}
	if got, err := time.Parse(time.RFC3339, child.CapacityRetry.NextAt); err != nil || !got.Equal(fireAt) {
		t.Fatalf("next_at = %q, want %s", child.CapacityRetry.NextAt, fireAt)
	}

	var issue IssueResponse
	testutil.Call(t, testHandler.GetIssue,
		withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID),
	).Want(http.StatusOK).JSON(&issue)
	if issue.CapacityRetry == nil || issue.CapacityRetry.Retry != 3 || issue.CapacityRetry.TaskID != childID {
		t.Fatalf("issue capacity_retry = %+v, want retry 3 on %s", issue.CapacityRetry, childID)
	}

	// Any other deferred retry (a runtime-offline wait, say) is not a full model.
	otherIssue := dbfx.Issue(t, "offline retry is not capacity")
	otherParent := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": otherIssue, "runtime_id": runtimeID, "status": "failed",
		"failure_reason": "runtime_offline",
	})
	dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": otherIssue, "runtime_id": runtimeID, "status": "deferred",
		"attempt": 2, "parent_task_id": otherParent, "fire_at": fireAt,
	})
	for _, r := range runsRequest(t, otherIssue, "") {
		if r.CapacityRetry != nil {
			t.Fatalf("runtime_offline retry carries capacity_retry: %+v", r.CapacityRetry)
		}
	}
	var other IssueResponse
	testutil.Call(t, testHandler.GetIssue,
		withURLParam(newRequest(http.MethodGet, "/api/issues/"+otherIssue, nil), "id", otherIssue),
	).Want(http.StatusOK).JSON(&other)
	if other.CapacityRetry != nil {
		t.Fatalf("issue detail shows capacity_retry for a non-capacity wait")
	}
}
