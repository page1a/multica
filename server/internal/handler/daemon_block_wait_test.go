package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
)

func reportBlockWaitProbeHTTP(t *testing.T, issueID string, exitCode int, output string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/daemon/issues/"+issueID+"/block-wait", map[string]any{
		"exit_code": exitCode,
		"output":    output,
	})
	req = withURLParam(req, "issueId", issueID)
	testHandler.ReportDaemonBlockWait(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("report probe: status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return resp
}

// TestBlockWaitProbeWakesOncePerResult is DENE-1083's acceptance in one place:
// pending stays silent, a result wakes the executor exactly once, and a
// pending past the wait deadline escalates to one wake.
func TestBlockWaitProbeWakesOncePerResult(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)

	failing := createIssueHTTP(t, "probe failing", "blocked")
	setIssueAssigneeDirect(t, failing.ID, "agent", agentID)
	setIssueMetadataString(t, failing.ID, blockwait.KeyWaitProbe, "gh pr checks 1")

	if resp := reportBlockWaitProbeHTTP(t, failing.ID, blockwait.ProbePendingGitHubExitCode, "pending"); resp["notified"] != false {
		t.Fatalf("pending report = %v, want silent", resp)
	}
	if got := countPendingTasksForAgent(t, failing.ID, agentID); got != 0 {
		t.Fatalf("pending probe started %d tasks, want 0", got)
	}
	if resp := reportBlockWaitProbeHTTP(t, failing.ID, 1, "lint failed"); resp["notified"] != true {
		t.Fatalf("first failure = %v, want notified", resp)
	}
	if resp := reportBlockWaitProbeHTTP(t, failing.ID, 1, "lint failed"); resp["notified"] != false {
		t.Fatalf("second failure = %v, want silent", resp)
	}
	if got := countPendingTasksForAgent(t, failing.ID, agentID); got != 1 {
		t.Fatalf("failing probe tasks = %d, want 1", got)
	}
	body, _, _, _ := systemCommentOn(t, failing.ID)
	if !strings.Contains(body, "lint failed") {
		t.Fatalf("failure wake must carry the output summary: %s", body)
	}

	overdue := createIssueHTTP(t, "probe overdue", "blocked")
	setIssueAssigneeDirect(t, overdue.ID, "agent", agentID)
	setIssueMetadataString(t, overdue.ID, blockwait.KeyWaitProbe, "test -f /nonexistent")
	setIssueMetadataString(t, overdue.ID, blockwait.KeyWaitTimeout, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	if resp := reportBlockWaitProbeHTTP(t, overdue.ID, blockwait.ProbePendingExitCode, ""); resp["notified"] != true || resp["status"] != string(blockwait.ProbeFailed) {
		t.Fatalf("overdue pending = %v, want escalated failure", resp)
	}
	if resp := reportBlockWaitProbeHTTP(t, overdue.ID, blockwait.ProbePendingExitCode, ""); resp["notified"] != false {
		t.Fatalf("second overdue pending = %v, want silent", resp)
	}
	if got := countPendingTasksForAgent(t, overdue.ID, agentID); got != 1 {
		t.Fatalf("overdue probe tasks = %d, want 1", got)
	}
}
