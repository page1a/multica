package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// dispatch_projects (DENE-1648) limits automatic dispatch to the listed
// projects. Only projects of the seat's own workspace are accepted, duplicates
// collapse, an unrelated edit keeps the list, and an empty list clears it.
func TestAgentDispatchProjects(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, 'dispatch-projects-test') RETURNING id
	`, testWorkspaceID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name = 'dispatch-projects-test'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID)
	})

	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name": "dispatch-projects-test", "runtime_id": runtimeID, "visibility": "private",
		"max_concurrent_tasks": 1,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DispatchProjects == nil || len(created.DispatchProjects) != 0 {
		t.Fatalf("new seat dispatch_projects = %#v, want an empty list", created.DispatchProjects)
	}

	update := func(body map[string]any) (int, AgentResponse) {
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/agents/"+created.ID, body)
		testHandler.UpdateAgent(w, withURLParam(req, "id", created.ID))
		var resp AgentResponse
		if w.Code == http.StatusOK {
			_ = json.Unmarshal(w.Body.Bytes(), &resp)
		}
		return w.Code, resp
	}

	code, resp := update(map[string]any{"dispatch_projects": []string{projectID, projectID}})
	if code != http.StatusOK || len(resp.DispatchProjects) != 1 || resp.DispatchProjects[0] != projectID {
		t.Fatalf("set: code=%d projects=%v, want [%s]", code, resp.DispatchProjects, projectID)
	}
	code, resp = update(map[string]any{"description": "unrelated"})
	if code != http.StatusOK || len(resp.DispatchProjects) != 1 {
		t.Fatalf("unrelated edit: code=%d projects=%v, want the limit kept", code, resp.DispatchProjects)
	}
	if code, _ = update(map[string]any{"dispatch_projects": []string{"not-a-uuid"}}); code != http.StatusBadRequest {
		t.Fatalf("bad id: code=%d, want 400", code)
	}
	if code, _ = update(map[string]any{"dispatch_projects": []string{"00000000-0000-0000-0000-000000000001"}}); code != http.StatusBadRequest {
		t.Fatalf("foreign project: code=%d, want 400", code)
	}
	code, resp = update(map[string]any{"dispatch_projects": []string{}})
	if code != http.StatusOK || len(resp.DispatchProjects) != 0 {
		t.Fatalf("clear: code=%d projects=%v, want empty", code, resp.DispatchProjects)
	}
}
