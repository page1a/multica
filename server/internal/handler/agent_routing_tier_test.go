package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The tier tag is the field routing reads off a seat, so the boundary rule is
// that an unknown rung is REFUSED rather than stored: a stored rung nobody
// declared would quietly take the seat off the ladder with nothing on the
// agent page to show it.
func TestAgentRoutingTier(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	t.Cleanup(func() {
		testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'tier-test-%'`,
			testWorkspaceID,
		)
	})

	create := func(t *testing.T, name string, body map[string]any) (string, int, string) {
		t.Helper()
		body["name"] = name
		body["runtime_id"] = runtimeID
		body["visibility"] = "private"
		body["max_concurrent_tasks"] = 1
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
		if w.Code != http.StatusCreated {
			return "", w.Code, w.Body.String()
		}
		var resp AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode create response: %v (%s)", err, w.Body.String())
		}
		return resp.ID, w.Code, resp.RoutingTier
	}

	update := func(t *testing.T, id string, value any) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/agents/"+id, map[string]any{"routing_tier": value})
		testHandler.UpdateAgent(w, withURLParam(req, "id", id))
		if w.Code != http.StatusOK {
			return w.Code, w.Body.String()
		}
		var resp AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode update response: %v (%s)", err, w.Body.String())
		}
		return w.Code, resp.RoutingTier
	}

	t.Run("a seat is born off the ladder", func(t *testing.T) {
		_, code, tier := create(t, "tier-test-empty", map[string]any{})
		if code != http.StatusCreated || tier != "" {
			t.Fatalf("code=%d routing_tier=%q, want 201 and an empty rung", code, tier)
		}
	})

	t.Run("a label is stored as its key", func(t *testing.T) {
		_, code, tier := create(t, "tier-test-label", map[string]any{"routing_tier": "强"})
		if code != http.StatusCreated || tier != "strong" {
			t.Fatalf("code=%d routing_tier=%q, want 201 and strong", code, tier)
		}
	})

	t.Run("an unknown rung is refused at create", func(t *testing.T) {
		_, code, body := create(t, "tier-test-bogus", map[string]any{"routing_tier": "legendary"})
		if code != http.StatusBadRequest {
			t.Fatalf("code=%d (%s), want 400", code, body)
		}
	})

	t.Run("update sets, clears and refuses", func(t *testing.T) {
		id, code, _ := create(t, "tier-test-update", map[string]any{})
		if code != http.StatusCreated {
			t.Fatalf("create failed: %d", code)
		}
		if code, got := update(t, id, "medium"); code != http.StatusOK || got != "medium" {
			t.Fatalf("set: code=%d value=%q, want 200 and medium", code, got)
		}
		if code, got := update(t, id, "最强"); code != http.StatusOK || got != "strongest" {
			t.Fatalf("set by label: code=%d value=%q, want 200 and strongest", code, got)
		}
		if code, got := update(t, id, ""); code != http.StatusOK || got != "" {
			t.Fatalf("clear: code=%d value=%q, want 200 and an empty rung", code, got)
		}
		if code, body := update(t, id, "legendary"); code != http.StatusBadRequest {
			t.Fatalf("unknown rung: code=%d (%s), want 400", code, body)
		}
	})

	t.Run("a specialisation inherits its base role's rung", func(t *testing.T) {
		baseID, code, _ := create(t, "tier-test-base", map[string]any{"routing_tier": "weak"})
		if code != http.StatusCreated {
			t.Fatalf("create base failed: %d", code)
		}
		_, code, tier := create(t, "tier-test-child", map[string]any{"parent_agent_id": baseID})
		if code != http.StatusCreated || tier != "weak" {
			t.Fatalf("code=%d routing_tier=%q, want 201 and the base role's weak rung", code, tier)
		}
	})
}
