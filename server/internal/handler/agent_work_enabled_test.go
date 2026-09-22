package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentWorkEnabledGetAndUpdate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := createClaudeProviderRuntime(t)
	agentID := createAgentOnRuntime(t, "work-enabled-switch-test", runtimeID, "")

	t.Run("get defaults to true", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID, nil), "id", agentID)
		testHandler.GetAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode GET: %v", err)
		}
		if resp["work_enabled"] != true {
			t.Errorf("work_enabled = %v, want true", resp["work_enabled"])
		}
	})

	t.Run("put false then true", func(t *testing.T) {
		for _, enabled := range []bool{false, true} {
			w := httptest.NewRecorder()
			req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
				"work_enabled": enabled,
			}), "id", agentID)
			testHandler.UpdateAgent(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("PUT work_enabled=%v: expected 200, got %d: %s", enabled, w.Code, w.Body.String())
			}
			var resp map[string]any
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode PUT: %v", err)
			}
			if resp["work_enabled"] != enabled {
				t.Errorf("work_enabled = %v, want %v", resp["work_enabled"], enabled)
			}
		}
	})

	t.Run("omitted field leaves the switch alone", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPut, "/api/agents/"+agentID, map[string]any{
			"description": "name-only-adjacent",
		}), "id", agentID)
		testHandler.UpdateAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("omitted switch: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode omitted PUT: %v", err)
		}
		if resp["work_enabled"] != true {
			t.Errorf("omitted work_enabled changed the switch: got %v", resp["work_enabled"])
		}
	})
}

func TestRoutingRosterOmitsDisabledSeats(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	liveID := createAgentOnRuntime(t, "work-enabled-live-seat", runtimeID, "")
	offID := createAgentOnRuntime(t, "work-enabled-off-seat", runtimeID, "")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = $1`, offID); err != nil {
		t.Fatalf("disable seat: %v", err)
	}

	roster, err := testHandler.RoutingStore().Roster(ctx, testWorkspaceID)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if _, ok := roster["work-enabled-live-seat"]; !ok {
		t.Error("enabled seat missing from routing roster")
	}
	if _, ok := roster["work-enabled-off-seat"]; ok {
		t.Error("disabled seat must not be a routing candidate")
	}
	_ = liveID
}
