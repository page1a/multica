package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Tests for DENE-896: the task-credential boundaries that the workspace
// middleware does not cover because these routes resolve their target
// themselves.

// TestDownloadAttachment_TaskTokenBoundToOtherWorkspaceDenied: the download
// route resolves the workspace from the attachment row and checks membership
// of the token's *user*. A mat_ token whose owner is a member of two
// workspaces could therefore pull the other workspace's files. The binding the
// auth middleware stamped in X-Workspace-ID must win, with the non-member 404
// shape.
func TestDownloadAttachment_TaskTokenBoundToOtherWorkspaceDenied(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	store := &mockStorage{}
	origStorage, origCfg, origSigner := testHandler.Storage, testHandler.cfg, testHandler.CFSigner
	testHandler.Storage = store
	testHandler.cfg.AttachmentDownloadMode = "proxy"
	testHandler.CFSigner = nil
	t.Cleanup(func() {
		testHandler.Storage, testHandler.cfg, testHandler.CFSigner = origStorage, origCfg, origSigner
	})

	// A second workspace where the token's owner IS a member, so the
	// membership check alone would let the request through.
	otherWorkspaceID := dbfx.Workspace(t, "DENE-896 Other", "dene896-other", testutil.Cols{"issue_prefix": "D896"})
	dbfx.Member(t, otherWorkspaceID, testUserID, "owner")

	key := "downloads/dene896-other.txt"
	body := []byte("other-workspace-body")
	store.put(key, body)
	attachmentID := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  otherWorkspaceID,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      "other.txt",
		"url":           "https://s3.example.com/test-bucket/" + key,
		"content_type":  "text/plain",
		"size_bytes":    len(body),
	})

	get := func(boundWorkspaceID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+attachmentID+"/download", nil)
		req.Header.Set("X-User-ID", testUserID)
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Workspace-ID", boundWorkspaceID)
		w := httptest.NewRecorder()
		newDownloadRouter().ServeHTTP(w, req)
		return w
	}

	if w := get(testWorkspaceID); w.Code != http.StatusNotFound {
		t.Fatalf("token bound to another workspace: status = %d, want 404; body=%s", w.Code, w.Body.String())
	} else if strings.Contains(w.Body.String(), string(body)) {
		t.Fatalf("response leaked file contents: %q", w.Body.String())
	}
	if w := get(otherWorkspaceID); w.Code != http.StatusOK {
		t.Fatalf("token bound to the attachment's workspace: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

// hiddenIssueTaskFixture builds a private issue owned by someone else with a
// finished run attached, so the test user (a member, not creator, assignee or
// sharee) cannot see the issue.
func hiddenIssueTaskFixture(t *testing.T) (issueID, taskID string) {
	t.Helper()
	otherUserID := dbfx.User(t, "DENE-896 Owner", "dene896-owner@multica.ai")
	dbfx.Member(t, testWorkspaceID, otherUserID, "member")
	issueID = dbfx.Issue(t, "dene896 hidden issue", testutil.Cols{
		"visibility":   "private",
		"creator_type": "member",
		"creator_id":   otherUserID,
	})
	agentID := createHandlerTestAgent(t, "dene896-hidden-agent", []byte("[]"))
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"issue_id":     issueID,
		"status":       "completed",
		"runtime_id":   handlerTestRuntimeID(t),
		"started_at":   testutil.Raw("now() - interval '30 seconds'"),
		"completed_at": testutil.Raw("now()"),
	})
	dbfx.Insert(t, "task_message", testutil.Cols{
		"task_id": taskID,
		"seq":     1,
		"type":    "text",
		"content": "dene896 secret transcript",
	})
	return issueID, taskID
}

// TestListTaskMessagesByUser_HiddenIssueDenied: a run's transcript is gated by
// the visibility of the issue it ran on, with the task-level 404 shape.
func TestListTaskMessagesByUser_HiddenIssueDenied(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	_, taskID := hiddenIssueTaskFixture(t)

	req := newRequest(http.MethodGet, "/api/tasks/"+taskID+"/messages", nil)
	req = withURLParam(withChatTestWorkspaceCtx(t, req), "taskId", taskID)
	w := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret transcript") {
		t.Fatalf("response leaked transcript: %s", w.Body.String())
	}
}

// TestExportTaskLogs_HiddenIssueDenied: the log export bundle carries the same
// transcript and is gated the same way, on both the download and push paths
// (they share resolveExportTask).
func TestExportTaskLogs_HiddenIssueDenied(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	_, taskID := hiddenIssueTaskFixture(t)

	resp := exportLogsRequest(t, taskID, "")
	if resp.Code != http.StatusNotFound {
		t.Fatalf("export status = %d, want 404; body=%s", resp.Code, resp.Text())
	}

	req := newRequest(http.MethodPost, "/api/tasks/"+taskID+"/logs/export/push", map[string]any{"scope": "run"})
	req = withURLParam(withChatTestWorkspaceCtx(t, req), "taskId", taskID)
	w := httptest.NewRecorder()
	testHandler.PushTaskLogExport(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("push status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

// TestHandoffByAgentInheritsOriginator: `issue handoff --to <agent>` issued
// from inside a run must carry the run's originator onto the new task as a
// delegation, instead of dropping the chain as unattributed.
func TestHandoffByAgentInheritsOriginator(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	sourceAgentID := createHandlerTestAgent(t, "dene896-handoff-source", []byte("[]"))
	targetAgentID := createHandlerTestAgent(t, "dene896-handoff-target", []byte("[]"))
	issueID := dbfx.Issue(t, "dene896 handoff originator")
	sourceTaskID := dbfx.Task(t, sourceAgentID, testutil.Cols{
		"issue_id":            issueID,
		"status":              "running",
		"runtime_id":          handlerTestRuntimeID(t),
		"originator_user_id":  testUserID,
		"accountable_user_id": testUserID,
		"originator_source":   "direct_human",
	})

	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/handoff", map[string]string{"to": "dene896-handoff-target"})
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", sourceAgentID)
	req.Header.Set("X-Task-ID", sourceTaskID)
	req = withURLParam(req, "id", issueID)
	w := httptest.NewRecorder()
	testHandler.HandoffIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s; want 200", w.Code, w.Body.String())
	}

	var originator, source string
	dbfx.QueryRow(t, `
		SELECT COALESCE(originator_user_id::text, ''), COALESCE(originator_source, '')
		FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC LIMIT 1
	`, issueID, targetAgentID).Scan(&originator, &source)
	if originator != testUserID {
		t.Fatalf("handed-off task originator = %q, want %q (source=%q)", originator, testUserID, source)
	}
	if source != "delegation" {
		t.Fatalf("handed-off task originator_source = %q, want delegation", source)
	}
	var commentSource string
	dbfx.QueryRow(t, `
		SELECT COALESCE(source_task_id::text, '') FROM comment
		WHERE issue_id = $1 AND author_type = 'agent' ORDER BY created_at DESC LIMIT 1
	`, issueID).Scan(&commentSource)
	if commentSource != sourceTaskID {
		t.Fatalf("handoff comment source_task_id = %q, want %q", commentSource, sourceTaskID)
	}
}
