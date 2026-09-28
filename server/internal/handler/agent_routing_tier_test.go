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

// Usage is a three-valued tag every seat has (DENE-922). The bulk endpoint is
// the seats table's write: one change on many seats, all or nothing.
func TestAgentRoutingUsageAndBulk(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	t.Cleanup(func() {
		testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'usage-test-%'`,
			testWorkspaceID,
		)
	})

	create := func(t *testing.T, name string, body map[string]any) AgentResponse {
		t.Helper()
		body["name"] = name
		body["runtime_id"] = runtimeID
		body["visibility"] = "private"
		body["max_concurrent_tasks"] = 1
		w := httptest.NewRecorder()
		testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		var resp AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return resp
	}
	update := func(t *testing.T, id string, value any) (int, AgentResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/agents/"+id, map[string]any{"routing_usage": value})
		testHandler.UpdateAgent(w, withURLParam(req, "id", id))
		var resp AgentResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode update response: %v", err)
			}
		}
		return w.Code, resp
	}
	bulk := func(t *testing.T, body map[string]any) (int, BulkUpdateAgentRoutingResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.BulkUpdateAgentRouting(w, newRequest(http.MethodPut, "/api/agents/routing", body))
		var resp BulkUpdateAgentRoutingResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode bulk response: %v", err)
			}
		}
		return w.Code, resp
	}
	readBack := func(t *testing.T, id string) (tier *string, usage string) {
		t.Helper()
		if err := testPool.QueryRow(ctx,
			`SELECT routing_tier, routing_usage FROM agent WHERE id = $1`, id,
		).Scan(&tier, &usage); err != nil {
			t.Fatalf("read back %s: %v", id, err)
		}
		return tier, usage
	}

	t.Run("a seat is born normal and update sets or refuses", func(t *testing.T) {
		a := create(t, "usage-test-single", map[string]any{})
		if a.RoutingUsage != "normal" {
			t.Fatalf("routing_usage=%q, want normal", a.RoutingUsage)
		}
		if code, got := update(t, a.ID, "充足"); code != http.StatusOK || got.RoutingUsage != "ample" {
			t.Fatalf("set by label: code=%d value=%q", code, got.RoutingUsage)
		}
		if code, _ := update(t, a.ID, ""); code != http.StatusBadRequest {
			t.Fatalf("empty usage: code=%d, want 400 — usage has no clear", code)
		}
		if code, _ := update(t, a.ID, "plenty"); code != http.StatusBadRequest {
			t.Fatalf("unknown usage: code=%d, want 400", code)
		}
	})

	t.Run("a specialisation starts with its base role's usage", func(t *testing.T) {
		base := create(t, "usage-test-base", map[string]any{})
		if code, _ := update(t, base.ID, "tight"); code != http.StatusOK {
			t.Fatalf("tag base: %d", code)
		}
		child := create(t, "usage-test-child", map[string]any{"parent_agent_id": base.ID})
		if child.RoutingUsage != "tight" {
			t.Fatalf("child routing_usage=%q, want the base role's tight", child.RoutingUsage)
		}
	})

	t.Run("bulk writes both fields on every seat", func(t *testing.T) {
		a := create(t, "usage-test-bulk-a", map[string]any{"routing_tier": "weak"})
		b := create(t, "usage-test-bulk-b", map[string]any{})
		code, resp := bulk(t, map[string]any{
			"agent_ids":     []string{a.ID, b.ID, a.ID},
			"routing_tier":  "强",
			"routing_usage": "ample",
		})
		if code != http.StatusOK || len(resp.Agents) != 2 {
			t.Fatalf("bulk: code=%d agents=%d, want 200 and 2 (duplicates collapse)", code, len(resp.Agents))
		}
		for _, id := range []string{a.ID, b.ID} {
			if tier, usage := readBack(t, id); tier == nil || *tier != "strong" || usage != "ample" {
				t.Fatalf("%s: tier=%v usage=%q, want strong/ample", id, tier, usage)
			}
		}

		// Usage alone leaves the tier; an empty tier clears it.
		if code, _ := bulk(t, map[string]any{"agent_ids": []string{a.ID}, "routing_usage": "tight"}); code != http.StatusOK {
			t.Fatalf("usage only: %d", code)
		}
		if tier, usage := readBack(t, a.ID); tier == nil || *tier != "strong" || usage != "tight" {
			t.Fatalf("usage only: tier=%v usage=%q", tier, usage)
		}
		if code, _ := bulk(t, map[string]any{"agent_ids": []string{a.ID}, "routing_tier": ""}); code != http.StatusOK {
			t.Fatalf("clear tier: %d", code)
		}
		if tier, usage := readBack(t, a.ID); tier != nil || usage != "tight" {
			t.Fatalf("clear tier: tier=%v usage=%q, want NULL/tight", tier, usage)
		}
	})

	t.Run("bulk is all or nothing", func(t *testing.T) {
		a := create(t, "usage-test-atomic", map[string]any{})
		missing := "00000000-0000-0000-0000-000000000001"
		code, _ := bulk(t, map[string]any{
			"agent_ids":     []string{a.ID, missing},
			"routing_usage": "ample",
		})
		if code != http.StatusNotFound {
			t.Fatalf("code=%d, want 404", code)
		}
		if _, usage := readBack(t, a.ID); usage != "normal" {
			t.Fatalf("usage=%q after a refused batch, want normal", usage)
		}
	})

	t.Run("bulk refuses bad input", func(t *testing.T) {
		a := create(t, "usage-test-bad", map[string]any{})
		for name, body := range map[string]map[string]any{
			"no ids":        {"agent_ids": []string{}, "routing_usage": "ample"},
			"no field":      {"agent_ids": []string{a.ID}},
			"unknown tier":  {"agent_ids": []string{a.ID}, "routing_tier": "legendary"},
			"unknown usage": {"agent_ids": []string{a.ID}, "routing_usage": "plenty"},
			"bad id":        {"agent_ids": []string{"nope"}, "routing_usage": "ample"},
		} {
			if code, _ := bulk(t, body); code != http.StatusBadRequest {
				t.Errorf("%s: code=%d, want 400", name, code)
			}
		}
	})
}
