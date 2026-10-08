package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// dispatch_mode (DENE-1600, ADR-0008) is set independently of the tier: a
// seat can keep its rung and leave the auto-dispatch pool. An unknown mode is
// refused, not stored.
func TestAgentDispatchMode(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'dispatch-test-%'`, testWorkspaceID)
	})

	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", map[string]any{
		"name": "dispatch-test-raditz", "runtime_id": runtimeID, "visibility": "private",
		"max_concurrent_tasks": 1, "routing_tier": "weak",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DispatchMode != "auto" {
		t.Fatalf("new seat dispatch_mode = %q, want auto", created.DispatchMode)
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

	code, resp := update(map[string]any{"dispatch_mode": "mention_only"})
	if code != http.StatusOK || resp.DispatchMode != "mention_only" || resp.RoutingTier != "weak" || !resp.WorkEnabled {
		t.Fatalf("set mention_only: code=%d mode=%q tier=%q work=%v, want mention_only on weak, still working",
			code, resp.DispatchMode, resp.RoutingTier, resp.WorkEnabled)
	}
	code, resp = update(map[string]any{"name": "dispatch-test-raditz-renamed"})
	if code != http.StatusOK || resp.DispatchMode != "mention_only" {
		t.Fatalf("unrelated edit: code=%d mode=%q, want the mode preserved", code, resp.DispatchMode)
	}
	if code, _ = update(map[string]any{"dispatch_mode": "manual"}); code != http.StatusBadRequest {
		t.Fatalf("unknown mode: code=%d, want 400", code)
	}
	if code, _ = update(map[string]any{"dispatch_mode": ""}); code != http.StatusBadRequest {
		t.Fatalf("empty mode: code=%d, want 400", code)
	}
	code, resp = update(map[string]any{"dispatch_mode": "auto"})
	if code != http.StatusOK || resp.DispatchMode != "auto" {
		t.Fatalf("back to auto: code=%d mode=%q", code, resp.DispatchMode)
	}
}
