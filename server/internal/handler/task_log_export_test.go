package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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

// ---- push endpoint --------------------------------------------------------

// fakeLogExportPusher records what the handler asked it to push. The push
// endpoint's contract is mostly "what did it hand the pusher", so recording is
// the whole assertion surface, and no network is involved.
type fakeLogExportPusher struct {
	requests []logexport.PushRequest
	repos    []logexport.GitRepo
	tokens   []string
	result   logexport.PushResult
	err      error
}

func (f *fakeLogExportPusher) Push(_ context.Context, repo logexport.GitRepo, token string, req logexport.PushRequest) (logexport.PushResult, error) {
	f.repos = append(f.repos, repo)
	f.tokens = append(f.tokens, token)
	f.requests = append(f.requests, req)
	if f.err != nil {
		return logexport.PushResult{}, f.err
	}
	res := f.result
	if res.Path == "" {
		res.Path = repo.Path(req.Filename)
	}
	if res.Branch == "" {
		res.Branch = repo.Branch
	}
	return res, nil
}

func useLogExportPusher(t *testing.T, pusher logexport.Pusher) {
	t.Helper()
	previous := testHandler.LogExportPusher
	testHandler.LogExportPusher = pusher
	t.Cleanup(func() { testHandler.LogExportPusher = previous })
}

func setWorkspaceLogExportSettings(t *testing.T, settings string) {
	t.Helper()
	var previous []byte
	dbfx.QueryRow(t, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previous)
	dbfx.Exec(t, `UPDATE workspace SET settings = $1::jsonb WHERE id = $2`, settings, testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, previous, testWorkspaceID)
	})
}

// freezeLogExportClock pins the clock both handlers stamp a bundle with, so the
// download and the push produce the same bytes (the bundle carries
// generated_at, and the file name is derived from it).
func freezeLogExportClock(t *testing.T) {
	t.Helper()
	at := time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC)
	previous := testHandler.logExportNow
	testHandler.logExportNow = func() time.Time { return at }
	t.Cleanup(func() { testHandler.logExportNow = previous })
}

func pushLogsRequest(t *testing.T, taskID string, body any) *testutil.Response {
	t.Helper()
	path := "/api/tasks/" + taskID + "/logs/export/push"
	req := withURLParam(newRequest(http.MethodPost, path, body), "taskId", taskID)
	return testutil.Call(t, testHandler.PushTaskLogExport, withChatTestWorkspaceCtx(t, req))
}

// TestPushTaskLogExportPushesTheSameArtifact is the guarantee the feature
// rests on: the bytes committed to git are the bytes the download endpoint
// returns, produced by the same Build call under the same redaction rules.
func TestPushTaskLogExportPushesTheSameArtifact(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	freezeLogExportClock(t)
	setWorkspaceIssuePrefixForTest(t, "DENE")
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git","branch":"kun"}}}`)

	pusher := &fakeLogExportPusher{result: logexport.PushResult{
		Path:   "logs/log-export-canned.json",
		URL:    "https://github.com/org/repo/blob/kun/logs/log-export-canned.json",
		Branch: "kun",
	}}
	useLogExportPusher(t, pusher)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":     issueID,
		"status":       "failed",
		"runtime_id":   handlerTestRuntimeID(t),
		"started_at":   testutil.Raw("now() - interval '30 seconds'"),
		"completed_at": testutil.Raw("now()"),
	})
	insertExportMessage(t, taskID, 1, "text", "worker started", "")
	insertExportMessage(t, taskID, 2, "text", "deploying with token super-secret-value-123", "")

	download := exportLogsRequest(t, taskID, "").Want(http.StatusOK).Text()
	var bundle logexport.Bundle
	if err := json.Unmarshal([]byte(download), &bundle); err != nil {
		t.Fatalf("decode download: %v", err)
	}

	response := pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusOK)
	var payload pushTaskLogExportResponse
	response.JSON(&payload)

	if !payload.Pushed {
		t.Fatalf("pushed = false: %s", response.Text())
	}
	if payload.Path != "logs/log-export-canned.json" || payload.URL != "https://github.com/org/repo/blob/kun/logs/log-export-canned.json" {
		t.Fatalf("path/url = %q/%q", payload.Path, payload.URL)
	}
	if payload.Branch != "kun" {
		t.Fatalf("branch = %q", payload.Branch)
	}
	if payload.Repo != "https://github.com/org/repo" {
		t.Fatalf("repo = %q, want the web root without .git", payload.Repo)
	}
	if payload.SummaryMarkdown != bundle.SummaryMarkdown {
		t.Fatalf("summary_markdown is not the bundle's own summary")
	}
	if payload.EntryCount != bundle.EntryCount || payload.RunCount != bundle.RunCount {
		t.Fatalf("counts = %d/%d, want %d/%d", payload.EntryCount, payload.RunCount, bundle.EntryCount, bundle.RunCount)
	}
	if payload.SizeBytes != len(download) {
		t.Fatalf("size_bytes = %d, want %d", payload.SizeBytes, len(download))
	}
	if payload.RedactionComplete != bundle.Redaction.Complete {
		t.Fatalf("redaction_complete = %v, want %v", payload.RedactionComplete, bundle.Redaction.Complete)
	}

	if len(pusher.requests) != 1 {
		t.Fatalf("pusher called %d times, want 1", len(pusher.requests))
	}
	if got := string(pusher.requests[0].Content); got != download {
		t.Fatalf("pushed bytes differ from the download:\n--- pushed ---\n%s\n--- downloaded ---\n%s", got, download)
	}
	if pusher.repos[0].URL != "https://github.com/org/repo.git" || pusher.repos[0].Branch != "kun" {
		t.Fatalf("pusher got repo %+v", pusher.repos[0])
	}
	if strings.Contains(string(pusher.requests[0].Content), "super-secret-value-123") {
		t.Fatal("the pushed artifact leaked the agent env value")
	}
}

// TestPushTaskLogExportWithoutRepoIsConflict — the client falls back to
// uploading the artifact as an attachment, so this is a clean 409 and the
// pusher must never be reached.
func TestPushTaskLogExportWithoutRepoIsConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":""}}}`)
	pusher := &fakeLogExportPusher{}
	useLogExportPusher(t, pusher)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})

	pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusConflict)
	if len(pusher.requests) != 0 {
		t.Fatalf("pusher was called with no usable repo: %+v", pusher.requests)
	}
}

// TestPushTaskLogExportWithoutPusherIsConflict — a deployment that has no
// pusher wired reports "not configured" rather than pretending.
func TestPushTaskLogExportWithoutPusherIsConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git"}}}`)
	useLogExportPusher(t, nil)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})
	pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusConflict)
}

// TestPushTaskLogExportPushFailureIsBadGatewayAndTokenFree — the pusher's
// failure is surfaced so the user knows why the fallback ran, and the body
// carries no part of the credential.
func TestPushTaskLogExportPushFailureIsBadGatewayAndTokenFree(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	const token = "ghp_push_failure_secret"
	box, err := NewLogExportSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewLogExportSecretBox: %v", err)
	}
	previousBox := testHandler.LogExportSecrets
	testHandler.LogExportSecrets = box
	t.Cleanup(func() { testHandler.LogExportSecrets = previousBox })

	sealed, ok := testHandler.sealLogExportToken(token)
	if !ok {
		t.Fatal("sealLogExportToken failed")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git","token_enc":"`+sealed+`"}}}`)

	pusher := &fakeLogExportPusher{err: fmt.Errorf("git clone failed: unable to access 'https://x-access-token:%s@github.com/org/repo.git': could not resolve host", token)}
	useLogExportPusher(t, pusher)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})

	response := pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusBadGateway)
	if strings.Contains(response.Text(), token) {
		t.Fatalf("502 body leaked the token: %s", response.Text())
	}
	if len(pusher.tokens) != 1 || pusher.tokens[0] != token {
		t.Fatalf("pusher token = %v, want the opened stored token", pusher.tokens)
	}
}

// TestPushTaskLogExportUnopenableTokenIsConflict — a repo whose token was
// sealed by a key this deployment does not hold must not silently push
// anonymously.
func TestPushTaskLogExportUnopenableTokenIsConflict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	previousBox := testHandler.LogExportSecrets
	testHandler.LogExportSecrets = nil
	t.Cleanup(func() { testHandler.LogExportSecrets = previousBox })

	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git","token_enc":"c29tZS1jaXBoZXJ0ZXh0"}}}`)
	pusher := &fakeLogExportPusher{}
	useLogExportPusher(t, pusher)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})

	response := pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusConflict)
	if !strings.Contains(response.Text(), "MULTICA_LOG_EXPORT_SECRET_KEY") {
		t.Fatalf("409 does not name the missing deployment secret: %s", response.Text())
	}
	if len(pusher.requests) != 0 {
		t.Fatal("pusher ran with an unopenable token")
	}
}

// TestPushTaskLogExportRejectsBadInput mirrors the download endpoint's input
// contract.
func TestPushTaskLogExportRejectsBadInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git"}}}`)
	pusher := &fakeLogExportPusher{}
	useLogExportPusher(t, pusher)

	agentID, issueID := exportFixture(t)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})

	// An unknown scope must 400 rather than silently widen to the whole task.
	pushLogsRequest(t, taskID, map[string]any{"scope": "everything"}).Want(http.StatusBadRequest)
	// hours without a value is the other half of the same contract.
	pushLogsRequest(t, taskID, map[string]any{"scope": "hours"}).Want(http.StatusBadRequest)
	pushLogsRequest(t, taskID, map[string]any{"scope": "run", "hours": 6}).Want(http.StatusBadRequest)
	// A malformed id is client error, a missing one is not found.
	pushLogsRequest(t, "not-a-uuid", map[string]any{"scope": "run"}).Want(http.StatusBadRequest)
	pushLogsRequest(t, "01a0b577-06b6-786e-8df6-3ebf70edf6d2", map[string]any{"scope": "run"}).Want(http.StatusNotFound)

	if len(pusher.requests) != 0 {
		t.Fatalf("pusher ran for a rejected request: %+v", pusher.requests)
	}
}

// TestPushTaskLogExportCrossWorkspaceTaskIsNotFound — a task in another
// workspace is indistinguishable from one that does not exist, exactly like
// the download endpoint.
func TestPushTaskLogExportCrossWorkspaceTaskIsNotFound(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git"}}}`)
	pusher := &fakeLogExportPusher{}
	useLogExportPusher(t, pusher)

	otherWS := dbfx.Workspace(t, "Push Other Workspace", fmt.Sprintf("push-other-%d", time.Now().UnixNano()))
	otherAgent := dbfx.Agent(t, "push-other-agent", handlerTestRuntimeID(t), testutil.Cols{"workspace_id": otherWS})
	otherIssue := dbfx.Issue(t, "push other issue", testutil.Cols{"workspace_id": otherWS})
	otherTask := dbfx.Task(t, otherAgent, testutil.Cols{
		"issue_id":   otherIssue,
		"runtime_id": handlerTestRuntimeID(t),
	})

	pushLogsRequest(t, otherTask, map[string]any{"scope": "run"}).Want(http.StatusNotFound)
	if len(pusher.requests) != 0 {
		t.Fatal("pusher ran for a task outside the workspace")
	}
}

// TestPushTaskLogExportRedactionCompleteReflectsTheBundle — the response's
// flag is the bundle's own verdict, not a second guess. An unreadable agent
// environment makes the bundle say "incomplete", and the response must agree.
func TestPushTaskLogExportRedactionCompleteReflectsTheBundle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setWorkspaceLogExportSettings(t, `{"logExport":{"gitRepo":{"enabled":true,"url":"https://github.com/org/repo.git"}}}`)
	useLogExportPusher(t, &fakeLogExportPusher{})

	agentID := dbfx.Agent(t, "log-export-push-bad-env", handlerTestRuntimeID(t), testutil.Cols{
		"custom_env": testutil.Raw(`'{"DATABASE_PASSWORD": 123}'::jsonb`),
	})
	issueID := dbfx.Issue(t, "log export push bad env fixture")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":   issueID,
		"runtime_id": handlerTestRuntimeID(t),
	})
	insertExportMessage(t, taskID, 1, "text", "DATABASE_PASSWORD=hunter2-correct-horse", "")

	response := pushLogsRequest(t, taskID, map[string]any{"scope": "run"}).Want(http.StatusOK)
	var payload pushTaskLogExportResponse
	response.JSON(&payload)
	if payload.RedactionComplete {
		t.Fatalf("response claims a complete redaction over an unreadable env: %+v", payload)
	}
	if payload.RedactionNote == "" {
		t.Fatal("response does not say why redaction was incomplete")
	}
}
