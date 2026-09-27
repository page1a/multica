package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type issueCreateInheritanceFixture struct {
	agentID   string
	runtimeID string
	taskID    string
}

func newIssueCreateInheritanceFixture(t *testing.T, sourceIssueID, chatSessionID string) issueCreateInheritanceFixture {
	t.Helper()
	ctx := context.Background()
	var fx issueCreateInheritanceFixture
	if err := testPool.QueryRow(ctx, `
		SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, "Handler Test Agent").Scan(&fx.agentID, &fx.runtimeID); err != nil {
		t.Fatalf("find test agent: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, chat_session_id,
			originator_user_id, accountable_user_id)
		VALUES ($1, $2, 'running', 0, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $5)
		RETURNING id
	`, fx.agentID, fx.runtimeID, sourceIssueID, chatSessionID, testUserID).Scan(&fx.taskID); err != nil {
		t.Fatalf("create acting task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, fx.taskID) })
	return fx
}

func createAgentIssueForInheritanceTest(t *testing.T, fx issueCreateInheritanceFixture, title string, fields map[string]any) IssueResponse {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["title"] = title
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, fields)
	req.Header.Set("X-Agent-ID", fx.agentID)
	req.Header.Set("X-Task-ID", fx.taskID)
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue %q: expected 201, got %d: %s", title, w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue %q: %v", title, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func TestCreateIssue_AgentOriginInheritsSourceProject(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID := createChatProjectTestProject(t, testWorkspaceID, "agent origin source project", "")
	parentW := testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "agent origin source issue", "project_id": projectID,
	})).Want(http.StatusCreated)
	var parentResponse IssueResponse
	if err := json.NewDecoder(parentW.Body).Decode(&parentResponse); err != nil {
		t.Fatalf("decode source issue: %v", err)
	}
	parent := parentResponse.ID
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, parent) })
	fx := newIssueCreateInheritanceFixture(t, parent, "")
	child := createAgentIssueForInheritanceTest(t, fx, "inherits source issue project", nil)
	if child.ProjectID == nil || *child.ProjectID != projectID {
		t.Fatalf("source issue project_id = %v, want %s", child.ProjectID, projectID)
	}
}

func TestCreateIssue_AgentOriginInheritsChatProjectOnlyWhenUnambiguous(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "issue-inheritance-chat-agent", nil)
	primary := createChatProjectTestProject(t, testWorkspaceID, "single chat source project", "")
	secondary := createChatProjectTestProject(t, testWorkspaceID, "second chat source project", "")

	newChatTask := func(t *testing.T, projects ...string) issueCreateInheritanceFixture {
		t.Helper()
		var sessionID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
			VALUES ($1, $2, $3, 'issue inheritance source chat', 'active') RETURNING id
		`, testWorkspaceID, agentID, testUserID).Scan(&sessionID); err != nil {
			t.Fatalf("create source chat: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, sessionID) })
		attachChatSessionProjectsForTest(t, sessionID, testWorkspaceID, projects...)
		return newIssueCreateInheritanceFixture(t, "", sessionID)
	}

	t.Run("single project", func(t *testing.T) {
		fx := newChatTask(t, primary)
		child := createAgentIssueForInheritanceTest(t, fx, "inherits single chat project", nil)
		if child.ProjectID == nil || *child.ProjectID != primary {
			t.Fatalf("single chat project_id = %v, want %s", child.ProjectID, primary)
		}
	})
	t.Run("multiple projects stay unset", func(t *testing.T) {
		fx := newChatTask(t, primary, secondary)
		child := createAgentIssueForInheritanceTest(t, fx, "leaves ambiguous chat project unset", nil)
		if child.ProjectID != nil {
			t.Fatalf("multi-project chat project_id = %v, want nil", child.ProjectID)
		}
	})
	t.Run("explicit project wins", func(t *testing.T) {
		fx := newChatTask(t, primary)
		child := createAgentIssueForInheritanceTest(t, fx, "keeps explicit project", map[string]any{"project_id": secondary})
		if child.ProjectID == nil || *child.ProjectID != secondary {
			t.Fatalf("explicit project_id = %v, want %s", child.ProjectID, secondary)
		}
	})
}

func TestCreateIssue_SubIssueInheritsPriorityOnlyWhenUnset(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	parentW := testutil.Call(t, testHandler.CreateIssue, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "priority inheritance parent", "priority": "high",
	})).Want(http.StatusCreated)
	var parentResponse IssueResponse
	if err := json.NewDecoder(parentW.Body).Decode(&parentResponse); err != nil {
		t.Fatalf("decode priority parent: %v", err)
	}
	parent := parentResponse.ID
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, parent) })
	fx := newIssueCreateInheritanceFixture(t, "", "")
	child := createAgentIssueForInheritanceTest(t, fx, "inherits parent priority", map[string]any{"parent_issue_id": parent})
	if child.Priority != "high" {
		t.Fatalf("unset child priority = %q, want high", child.Priority)
	}
	explicit := createAgentIssueForInheritanceTest(t, fx, "keeps explicit child priority", map[string]any{
		"parent_issue_id": parent,
		"priority":        "low",
	})
	if explicit.Priority != "low" {
		t.Fatalf("explicit child priority = %q, want low", explicit.Priority)
	}
}
