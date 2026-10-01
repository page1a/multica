package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// Following specialisations take their base role's routing tier and usage
// (DENE-1016). The pair is copied at create, when follow is turned on, and
// whenever the base role's pair changes — and a follower cannot set its own.
func TestAgentRoutingFollowsParent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	runtimeID := createClaudeProviderRuntime(t)
	t.Cleanup(func() {
		testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'follow-route-%'`,
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
	put := func(t *testing.T, id string, body map[string]any) (int, string, AgentResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/agents/"+id, body)
		testHandler.UpdateAgent(w, withURLParam(req, "id", id))
		var resp AgentResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode update response: %v", err)
			}
		}
		return w.Code, w.Body.String(), resp
	}
	bulk := func(t *testing.T, body map[string]any) (int, string, BulkUpdateAgentRoutingResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.BulkUpdateAgentRouting(w, newRequest(http.MethodPut, "/api/agents/routing", body))
		var resp BulkUpdateAgentRoutingResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode bulk response: %v", err)
			}
		}
		return w.Code, w.Body.String(), resp
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

	t.Run("a following specialisation is born with the base role's tier", func(t *testing.T) {
		base := create(t, "follow-route-born-base", map[string]any{"routing_tier": "weak"})
		// An explicit rung on the create is not an override while following.
		child := create(t, "follow-route-born-child", map[string]any{
			"parent_agent_id": base.ID,
			"routing_tier":    "strongest",
		})
		if child.RoutingTier != "weak" || child.RoutingUsage != "normal" {
			t.Fatalf("child tier=%q usage=%q, want the base role's weak/normal", child.RoutingTier, child.RoutingUsage)
		}
	})

	t.Run("changing the base role copies onto followers", func(t *testing.T) {
		base := create(t, "follow-route-sync-base", map[string]any{"routing_tier": "weak"})
		if code, body, _ := put(t, base.ID, map[string]any{"routing_usage": "tight"}); code != http.StatusOK {
			t.Fatalf("tag base usage: %d %s", code, body)
		}
		child := create(t, "follow-route-sync-child", map[string]any{"parent_agent_id": base.ID})
		own := create(t, "follow-route-sync-own", map[string]any{
			"parent_agent_id":   base.ID,
			"runtime_inherited": false,
			"routing_tier":      "medium",
		})
		if code, body, _ := put(t, own.ID, map[string]any{"routing_usage": "ample"}); code != http.StatusOK {
			t.Fatalf("tag independent child: %d %s", code, body)
		}

		if code, body, _ := put(t, base.ID, map[string]any{"routing_tier": "strong", "routing_usage": "ample"}); code != http.StatusOK {
			t.Fatalf("update base: %d %s", code, body)
		}
		if tier, usage := readBack(t, child.ID); tier == nil || *tier != "strong" || usage != "ample" {
			t.Fatalf("follower after parent patch: tier=%v usage=%q, want strong/ample", tier, usage)
		}
		if tier, usage := readBack(t, own.ID); tier == nil || *tier != "medium" || usage != "ample" {
			t.Fatalf("independent child after parent patch: tier=%v usage=%q, want medium/ample", tier, usage)
		}

		code, body, resp := bulk(t, map[string]any{
			"agent_ids":     []string{base.ID},
			"routing_tier":  "",
			"routing_usage": "tight",
		})
		if code != http.StatusOK {
			t.Fatalf("bulk base: %d %s", code, body)
		}
		if tier, usage := readBack(t, child.ID); tier != nil || usage != "tight" {
			t.Fatalf("follower after parent bulk clear: tier=%v usage=%q, want NULL/tight", tier, usage)
		}
		var childInBulk bool
		for _, agent := range resp.Agents {
			if agent.ID == child.ID {
				childInBulk = true
			}
		}
		if !childInBulk {
			t.Fatal("bulk response did not include the follower whose routing was copied")
		}
		if tier, usage := readBack(t, own.ID); tier == nil || *tier != "medium" || usage != "ample" {
			t.Fatalf("independent child after parent bulk: tier=%v usage=%q, want medium/ample", tier, usage)
		}
	})

	t.Run("a follower cannot set its own tier or usage", func(t *testing.T) {
		base := create(t, "follow-route-孙悟空", map[string]any{"routing_tier": "strong"})
		// Usage is copied at create; set it explicitly on the base first.
		if code, body, _ := put(t, base.ID, map[string]any{"routing_usage": "tight"}); code != http.StatusOK {
			t.Fatalf("tag base: %d %s", code, body)
		}
		child := create(t, "follow-route-refuse-child", map[string]any{"parent_agent_id": base.ID})
		for _, body := range []map[string]any{
			{"routing_tier": "weak"},
			{"routing_usage": "ample"},
			{"routing_tier": "", "routing_usage": "normal"},
		} {
			code, raw, _ := put(t, child.ID, body)
			if code != http.StatusBadRequest || !strings.Contains(raw, "跟随 follow-route-孙悟空，先关掉跟随") {
				t.Fatalf("follower write %v: code=%d body=%s", body, code, raw)
			}
		}
		code, raw, _ := bulk(t, map[string]any{"agent_ids": []string{child.ID}, "routing_usage": "ample"})
		if code != http.StatusBadRequest || !strings.Contains(raw, "跟随 follow-route-孙悟空，先关掉跟随") {
			t.Fatalf("follower bulk: code=%d body=%s", code, raw)
		}
		// A batch that mixes the base role with a follower is refused whole.
		code, _, _ = bulk(t, map[string]any{
			"agent_ids":    []string{base.ID, child.ID},
			"routing_tier": "weak",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("mixed bulk: code=%d, want 400", code)
		}
		if tier, usage := readBack(t, base.ID); tier == nil || *tier != "strong" || usage != "tight" {
			t.Fatalf("base role changed by a refused batch: tier=%v usage=%q", tier, usage)
		}
		if tier, usage := readBack(t, child.ID); tier == nil || *tier != "strong" || usage != "tight" {
			t.Fatalf("follower changed by a refused write: tier=%v usage=%q", tier, usage)
		}
	})

	t.Run("turning follow off allows an own pair, turning it on copies", func(t *testing.T) {
		base := create(t, "follow-route-toggle-base", map[string]any{"routing_tier": "strongest"})
		if code, body, _ := put(t, base.ID, map[string]any{"routing_usage": "tight"}); code != http.StatusOK {
			t.Fatalf("tag base: %d %s", code, body)
		}
		child := create(t, "follow-route-toggle-child", map[string]any{
			"parent_agent_id":   base.ID,
			"runtime_inherited": false,
			"routing_tier":      "weak",
		})
		if code, body, got := put(t, child.ID, map[string]any{"routing_usage": "ample"}); code != http.StatusOK || got.RoutingUsage != "ample" {
			t.Fatalf("own usage while not following: code=%d usage=%q body=%s", code, got.RoutingUsage, body)
		}
		code, body, got := put(t, child.ID, map[string]any{"runtime_inherited": true})
		if code != http.StatusOK || !got.RuntimeInherited || got.RoutingTier != "strongest" || got.RoutingUsage != "tight" {
			t.Fatalf("turn follow on: code=%d inherited=%v tier=%q usage=%q body=%s",
				code, got.RuntimeInherited, got.RoutingTier, got.RoutingUsage, body)
		}
		code, body, _ = put(t, child.ID, map[string]any{"runtime_inherited": false})
		if code != http.StatusOK {
			t.Fatalf("turn follow off: %d %s", code, body)
		}
		code, body, got = put(t, child.ID, map[string]any{"routing_tier": "medium", "routing_usage": "normal"})
		if code != http.StatusOK || got.RoutingTier != "medium" || got.RoutingUsage != "normal" {
			t.Fatalf("own pair after opting out: code=%d tier=%q usage=%q body=%s", code, got.RoutingTier, got.RoutingUsage, body)
		}
		if code, body, _ := put(t, base.ID, map[string]any{"routing_tier": "weak", "routing_usage": "ample"}); code != http.StatusOK {
			t.Fatalf("parent edit after opt-out: %d %s", code, body)
		}
		if tier, usage := readBack(t, child.ID); tier == nil || *tier != "medium" || usage != "normal" {
			t.Fatalf("opted-out child followed a parent edit: tier=%v usage=%q", tier, usage)
		}
	})

	t.Run("backfill aligns followers and leaves the rest", func(t *testing.T) {
		base := create(t, "follow-route-backfill-base", map[string]any{"routing_tier": "strong"})
		if code, body, _ := put(t, base.ID, map[string]any{"routing_usage": "tight"}); code != http.StatusOK {
			t.Fatalf("tag base: %d %s", code, body)
		}
		child := create(t, "follow-route-backfill-child", map[string]any{"parent_agent_id": base.ID})
		own := create(t, "follow-route-backfill-own", map[string]any{
			"parent_agent_id":   base.ID,
			"runtime_inherited": false,
			"routing_tier":      "weak",
		})
		if _, err := testPool.Exec(ctx,
			`UPDATE agent SET routing_tier = 'medium', routing_usage = 'ample' WHERE id = $1`, child.ID,
		); err != nil {
			t.Fatalf("drift follower: %v", err)
		}
		sql, err := os.ReadFile("../../migrations/561_agent_routing_follow_parent.up.sql")
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		if _, err := testPool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("run backfill: %v", err)
		}
		if tier, usage := readBack(t, child.ID); tier == nil || *tier != "strong" || usage != "tight" {
			t.Fatalf("backfill follower: tier=%v usage=%q, want strong/tight", tier, usage)
		}
		// Usage is copied at birth even for a specialisation that does not
		// follow; the backfill must not move it afterwards.
		if tier, usage := readBack(t, own.ID); tier == nil || *tier != "weak" || usage != "tight" {
			t.Fatalf("backfill touched an independent child: tier=%v usage=%q, want weak/tight", tier, usage)
		}
	})
}
