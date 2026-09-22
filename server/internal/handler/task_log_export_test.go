package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/logexport"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// exportFixture builds one issue with an agent and returns both ids.
func exportFixture(t *testing.T) (agentID, issueID string) {
	t.Helper()
	agentID = dbfx.Agent(t, "log-export-agent", handlerTestRuntimeID(t), testutil.Cols{
		"custom_env": testutil.Raw(`'{"DEPLOY_TOKEN":"super-secret-value-123"}'::jsonb`),
	})
	issueID = dbfx.Issue(t, "log export fixture")
	return agentID, issueID
}

func insertExportMessage(t *testing.T, taskID string, seq int, typ, content, output string) {
	t.Helper()
	cols := testutil.Cols{
		"task_id": taskID,
		"seq":     seq,
		"type":    typ,
	}
	if content != "" {
		cols["content"] = content
	}
	if output != "" {
		cols["output"] = output
	}
	dbfx.Insert(t, "task_message", cols)
}

func exportLogsRequest(t *testing.T, taskID, query string) *testutil.Response {
	t.Helper()
	path := "/api/tasks/" + taskID + "/logs/export"
	if query = strings.TrimPrefix(query, "?"); query != "" {
		path += "?" + query
	}
	req := withURLParam(newRequest(http.MethodGet, path, nil), "taskId", taskID)
	// The handler reads the workspace from the middleware context, which a
	// direct handler call does not get; inject the same member row the router's
	// workspace middleware would have set.
	return testutil.Call(t, testHandler.ExportTaskLogs, withChatTestWorkspaceCtx(t, req))
}

// TestExportTaskLogsRunScope pins the artifact's identity fields and the two
// guarantees the whole feature rests on: the transcript is present, and a
// secret that would otherwise be readable is gone.
func TestExportTaskLogsRunScope(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceIssuePrefixForTest(t, "DENE")

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":     issueID,
		"status":       "failed",
		"runtime_id":   handlerTestRuntimeID(t),
		"started_at":   testutil.Raw("now() - interval '30 seconds'"),
		"completed_at": testutil.Raw("now()"),
	})
	insertExportMessage(t, taskID, 1, "text", "worker started", "")
	insertExportMessage(t, taskID, 2, "tool_result", "", "GET /api/sync 返回 504 timeout")
	insertExportMessage(t, taskID, 3, "text", "deploying with token super-secret-value-123", "")

	body := exportLogsRequest(t, taskID, "").Want(http.StatusOK).Text()

	var bundle logexport.Bundle
	exportLogsRequest(t, taskID, "").Want(http.StatusOK).JSON(&bundle)

	if bundle.Format != logexport.FormatName || bundle.Version != logexport.FormatVersion {
		t.Fatalf("format = %s/%d", bundle.Format, bundle.Version)
	}
	if bundle.Task.ID != taskID {
		t.Fatalf("task id = %q, want %q", bundle.Task.ID, taskID)
	}
	if bundle.Task.IssueID != issueID {
		t.Fatalf("issue id = %q, want %q", bundle.Task.IssueID, issueID)
	}
	if !strings.HasPrefix(bundle.Task.IssueIdentifier, "DENE-") {
		t.Fatalf("issue identifier = %q, want a DENE- prefix", bundle.Task.IssueIdentifier)
	}
	if bundle.Task.AgentName != "log-export-agent" {
		t.Fatalf("agent name = %q", bundle.Task.AgentName)
	}
	if bundle.Task.Status != "failed" || bundle.Task.ExitCode == nil || *bundle.Task.ExitCode != 1 {
		t.Fatalf("status/exit = %q/%v, want failed/1", bundle.Task.Status, bundle.Task.ExitCode)
	}
	if bundle.EntryCount != 3 {
		t.Fatalf("entry count = %d, want 3", bundle.EntryCount)
	}
	if !strings.Contains(bundle.SummaryMarkdown, "GET /api/sync 返回 504") {
		t.Fatalf("summary lost the failure clue:\n%s", bundle.SummaryMarkdown)
	}

	if strings.Contains(body, "super-secret-value-123") {
		t.Fatalf("artifact leaked the agent env value:\n%s", body)
	}
}

// TestExportTaskLogsTaskScopeCoversIssueHistory is the "整个任务" range: the
// requested run plus every other run of the same issue.
func TestExportTaskLogsTaskScopeCoversIssueHistory(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, issueID := exportFixture(t)
	older := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"status":     "failed",
		"runtime_id": handlerTestRuntimeID(t),
		"created_at": testutil.Raw("now() - interval '2 hours'"),
	})
	newer := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"status":     "completed",
		"runtime_id": handlerTestRuntimeID(t),
	})
	insertExportMessage(t, older, 1, "text", "older run output", "")
	insertExportMessage(t, newer, 1, "text", "newer run output", "")

	var runScope logexport.Bundle
	exportLogsRequest(t, newer, "?scope=run").Want(http.StatusOK).JSON(&runScope)
	if runScope.RunCount != 1 || runScope.EntryCount != 1 {
		t.Fatalf("run scope = %d runs / %d entries, want 1/1", runScope.RunCount, runScope.EntryCount)
	}

	var taskScope logexport.Bundle
	exportLogsRequest(t, newer, "?scope=task").Want(http.StatusOK).JSON(&taskScope)
	if taskScope.RunCount != 2 {
		t.Fatalf("task scope run count = %d, want 2", taskScope.RunCount)
	}
	if taskScope.EntryCount != 2 {
		t.Fatalf("task scope entry count = %d, want 2", taskScope.EntryCount)
	}
	if taskScope.Task.Scope.Kind != logexport.ScopeTask {
		t.Fatalf("scope kind = %q", taskScope.Task.Scope.Kind)
	}
}

func TestExportTaskLogsHoursScope(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"status":     "failed",
		"runtime_id": handlerTestRuntimeID(t),
	})
	insertExportMessage(t, taskID, 1, "text", "recent line", "")
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id":    taskID,
		"seq":        2,
		"type":       "text",
		"content":    "ancient line",
		"created_at": testutil.Raw("now() - interval '10 hours'"),
	})

	var bundle logexport.Bundle
	exportLogsRequest(t, taskID, "?scope=hours&hours=2").Want(http.StatusOK).JSON(&bundle)
	if bundle.EntryCount != 1 {
		t.Fatalf("entry count = %d, want 1 (the 10h-old line is outside a 2h window)", bundle.EntryCount)
	}
	for _, e := range bundle.Entries {
		if strings.Contains(e.Content, "ancient") {
			t.Fatalf("entry outside the hours window survived: %+v", e)
		}
	}
}

// TestExportTaskLogsMarksRedactionIncompleteWhenEnvUnreadable is F3 at the
// handler boundary. When an agent's stored environment cannot be decoded the
// value deny-list has no input, so the export degrades explicitly: it still
// succeeds, but the bundle records that the deny-list did not run instead of
// carrying the summary's "已自动脱敏" claim for a redaction it never did.
func TestExportTaskLogsMarksRedactionIncompleteWhenEnvUnreadable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A non-string value cannot decode into map[string]string — the shape the
	// deny-list needs — which is the reachable "environment unreadable" case.
	agentID := dbfx.Agent(t, "log-export-bad-env", handlerTestRuntimeID(t), testutil.Cols{
		"custom_env": testutil.Raw(`'{"DATABASE_PASSWORD": 123}'::jsonb`),
	})
	issueID := dbfx.Issue(t, "log export bad env fixture")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"status":     "failed",
		"runtime_id": handlerTestRuntimeID(t),
	})
	insertExportMessage(t, taskID, 1, "text", "DATABASE_PASSWORD=hunter2-correct-horse", "")

	var bundle logexport.Bundle
	exportLogsRequest(t, taskID, "").Want(http.StatusOK).JSON(&bundle)

	if bundle.Redaction.Complete || bundle.Redaction.EnvDenyList {
		t.Fatalf("bundle claims a complete redaction from an unreadable env: %+v", bundle.Redaction)
	}
	if bundle.Redaction.Note == "" {
		t.Fatalf("bundle does not say why redaction was incomplete")
	}
	if !strings.Contains(bundle.SummaryMarkdown, "脱敏不完整") {
		t.Fatalf("summary does not warn about the incomplete redaction:\n%s", bundle.SummaryMarkdown)
	}
}

func TestExportTaskLogsRejectsBadInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"status":     "completed",
		"runtime_id": handlerTestRuntimeID(t),
	})

	// An unknown scope must 400 rather than silently widen to the whole task.
	exportLogsRequest(t, taskID, "?scope=everything").Want(http.StatusBadRequest)
	// hours without a value is the other half of the same contract.
	exportLogsRequest(t, taskID, "?scope=hours").Want(http.StatusBadRequest)
	exportLogsRequest(t, taskID, "?scope=run&hours=6").Want(http.StatusBadRequest)
	// A malformed id is client error, a missing one is not found.
	exportLogsRequest(t, "not-a-uuid", "").Want(http.StatusBadRequest)
	exportLogsRequest(t, "01a0b577-06b6-786e-8df6-3ebf70edf6d2", "").Want(http.StatusNotFound)
}
