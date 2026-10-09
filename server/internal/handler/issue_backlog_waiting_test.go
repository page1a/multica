package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBacklogWaitingForRejection(t *testing.T) {
	reason := "等公司注册办好"
	blank := "  "
	long := strings.Repeat("等", maxBacklogWaitingForRunes+1)
	cases := []struct {
		name       string
		waitingFor *string
		actor      string
		staged     bool
		wantReject bool
	}{
		{"agent without reason", nil, "agent", false, true},
		{"agent with blank reason", &blank, "agent", false, true},
		{"agent with reason", &reason, "agent", false, false},
		{"agent with too long reason", &long, "agent", false, true},
		{"agent staged child", nil, "agent", true, false},
		{"member without reason", nil, "member", false, false},
	}
	for _, tc := range cases {
		got := backlogWaitingForRejection(tc.waitingFor, tc.actor, tc.staged)
		if (got != "") != tc.wantReject {
			t.Errorf("%s: rejection = %q, want reject=%v", tc.name, got, tc.wantReject)
		}
	}
	if msg := backlogWaitingForRejection(nil, "agent", false); !strings.Contains(msg, "todo") || !strings.Contains(msg, "--waiting-for") {
		t.Errorf("rejection should point at todo and --waiting-for, got %q", msg)
	}
}

// asTaskAgent marks a request the way the auth middleware does for a task
// token, so resolveActor reads it as the agent.
func asTaskAgent(req *http.Request, agentID string) *http.Request {
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	return req
}

func createIssueRaw(t *testing.T, body map[string]any, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	body["title"] = body["title"].(string) + " " + time.Now().Format(time.RFC3339Nano)
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, body)
	if agentID != "" {
		req = asTaskAgent(req, agentID)
	}
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, req)
	if w.Code == http.StatusCreated {
		var issue IssueResponse
		if err := json.Unmarshal(w.Body.Bytes(), &issue); err == nil {
			t.Cleanup(func() {
				ctx := context.Background()
				testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
				testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issue.ID)
				testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issue.ID)
			})
		}
	}
	return w
}

func readBacklogWaitingFor(t *testing.T, issueID string) *string {
	t.Helper()
	var v *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>'backlog.waiting_for' FROM issue WHERE id = $1`, issueID).Scan(&v); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	return v
}

func TestAgentBacklogCreateNeedsWaitingFor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)

	w := createIssueRaw(t, map[string]any{"title": "agent parks", "status": "backlog"}, agentID)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "todo") {
		t.Fatalf("agent backlog without reason: status = %d, body = %s; want 400 pointing at todo", w.Code, w.Body.String())
	}

	w = createIssueRaw(t, map[string]any{"title": "agent parks", "status": "backlog", "waiting_for": "等公司注册办好"}, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent backlog with reason: status = %d, body = %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := issue.Metadata[metaKeyBacklogWaitingFor]; got != "等公司注册办好" {
		t.Fatalf("response metadata waiting_for = %v", got)
	}
	if got := readBacklogWaitingFor(t, issue.ID); got == nil || *got != "等公司注册办好" {
		t.Fatalf("stored waiting_for = %v", got)
	}

	w = createIssueRaw(t, map[string]any{"title": "member parks", "status": "backlog"}, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("member backlog without reason: status = %d, body = %s", w.Code, w.Body.String())
	}

	parent := createIssueHTTP(t, "staged parent", "in_progress")
	w = createIssueRaw(t, map[string]any{"title": "stage 2", "status": "backlog", "parent_issue_id": parent.ID, "stage": 2}, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent staged backlog child: status = %d, body = %s", w.Code, w.Body.String())
	}

	w = createIssueRaw(t, map[string]any{"title": "agent default"}, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("agent default create: status = %d, body = %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issue); err != nil || issue.Status != "todo" {
		t.Fatalf("agent default create status = %q (err %v), want todo", issue.Status, err)
	}
}

func TestAgentMoveToBacklogNeedsWaitingFor(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	issue := createIssueHTTP(t, "agent moves to backlog", "todo")

	put := func(body map[string]any, agent bool) *httptest.ResponseRecorder {
		req := withURLParam(newRequest("PUT", "/api/issues/"+issue.ID, body), "id", issue.ID)
		if agent {
			req = asTaskAgent(req, agentID)
		}
		w := httptest.NewRecorder()
		testHandler.UpdateIssue(w, req)
		return w
	}

	if w := put(map[string]any{"status": "backlog", "suppress_run": true}, true); w.Code != http.StatusBadRequest {
		t.Fatalf("agent move without reason: status = %d, body = %s; want 400", w.Code, w.Body.String())
	}
	w := put(map[string]any{"status": "backlog", "waiting_for": "上线前终测", "suppress_run": true}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("agent move with reason: status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Metadata[metaKeyBacklogWaitingFor] != "上线前终测" {
		t.Fatalf("response metadata waiting_for = %v", resp.Metadata[metaKeyBacklogWaitingFor])
	}

	if w := put(map[string]any{"status": "todo", "suppress_run": true}, false); w.Code != http.StatusOK {
		t.Fatalf("member move to todo: status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := readBacklogWaitingFor(t, issue.ID); got != nil {
		t.Fatalf("waiting_for survived leaving backlog: %s", *got)
	}

	if w := put(map[string]any{"status": "backlog", "suppress_run": true}, false); w.Code != http.StatusOK {
		t.Fatalf("member move without reason: status = %d, body = %s", w.Code, w.Body.String())
	}
}
