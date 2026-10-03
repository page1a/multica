package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentCustomArgsRejectCodexConfigSyntaxForClaude(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := createClaudeProviderRuntime(t)
	const rejectedName = "custom-args-claude-rejected"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, rejectedName)
	})

	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":                 rejectedName,
		"runtime_id":           runtimeID,
		"visibility":           "private",
		"max_concurrent_tasks": 1,
		"custom_args":          []string{"-c", "model_reasoning_effort=high"},
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateAgent with Codex -c syntax: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "uses -c for session continuation") {
		t.Fatalf("rejection should explain the provider conflict, got %s", w.Body.String())
	}
}

func TestUpdateAgentCustomArgsRejectCodexConfigSyntaxForClaude(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := createClaudeProviderRuntime(t)
	const name = "custom-args-claude-update"
	create := httptest.NewRecorder()
	testHandler.CreateAgent(create, newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":                 name,
		"runtime_id":           runtimeID,
		"visibility":           "private",
		"max_concurrent_tasks": 1,
	}))
	if create.Code != http.StatusCreated {
		t.Fatalf("seed CreateAgent: expected 201, got %d: %s", create.Code, create.Body.String())
	}
	var created AgentResponse
	if err := json.NewDecoder(create.Body).Decode(&created); err != nil {
		t.Fatalf("decode seed agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, created.ID)
	})

	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequest(http.MethodPut, "/api/agents/"+created.ID, map[string]any{
		"custom_args": []string{"-c", "model_reasoning_effort=high"},
	}), "id", created.ID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateAgent with Codex -c syntax: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "uses -c for session continuation") {
		t.Fatalf("rejection should explain the provider conflict, got %s", w.Body.String())
	}
}
