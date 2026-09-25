package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Execution-config inheritance for specialisations (DENE-854).
//
// A specialisation that follows its base role (runtime_inherited, DENE-505)
// also takes the base role's custom_env, custom_args and mcp_config — but only
// when both rows have the same owner, because env and MCP config carry the
// base role's credentials. The tests read the columns straight off the row.

type executionConfig struct {
	CustomEnv  string
	CustomArgs string
	McpConfig  string
}

func persistedExecutionConfig(t *testing.T, agentID string) executionConfig {
	t.Helper()

	var c executionConfig
	dbfx.QueryRow(t, `
		SELECT custom_env::text, custom_args::text, COALESCE(mcp_config::text, '')
		FROM agent WHERE id = $1
	`, agentID).Scan(&c.CustomEnv, &c.CustomArgs, &c.McpConfig)
	return c
}

func wantExecutionConfigCopied(t *testing.T, parentID, childID string) {
	t.Helper()

	parent := persistedExecutionConfig(t, parentID)
	if child := persistedExecutionConfig(t, childID); child != parent {
		t.Errorf("child execution config %+v does not mirror base role %+v", child, parent)
	}
}

// newExecutionConfigInheritance is a base role carrying all three execution
// config columns, plus a specialisation created through the API by the same
// owner.
func newExecutionConfigInheritance(t *testing.T, name string) runtimeInheritance {
	t.Helper()

	runtimeID := dbfx.Runtime(t, name+"-runtime", testutil.Cols{"provider": "codex"})
	parentID := dbfx.Agent(t, name+"-base", runtimeID, testutil.Cols{
		"model":       "gpt-5-codex",
		"custom_env":  testutil.Raw(`'{"API_KEY":"base-secret"}'::jsonb`),
		"custom_args": testutil.Raw(`'["-c","model_reasoning_effort=high"]'::jsonb`),
		"mcp_config":  testutil.Raw(`'{"mcpServers":{"docs":{"command":"docs-mcp"}}}'::jsonb`),
	})
	child := createAgentThroughAPI(t, map[string]any{
		"name":            name + "-spec",
		"parent_agent_id": parentID,
		"custom_args":     []string{"--ignored"},
	})
	return runtimeInheritance{RuntimeID: runtimeID, ParentID: parentID, Child: child}
}

func putAgentEnv(t *testing.T, agentID string, env map[string]string, want int) {
	t.Helper()

	req := testutil.WithURLParams(
		specRequest(http.MethodPut, "/api/agents/"+agentID+"/env", map[string]any{"custom_env": env}), "id", agentID)
	testutil.Call(t, testHandler.UpdateAgentEnv, req).Want(want)
}

func TestCreateAgent_SpecialisationInheritsExecutionConfig(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newExecutionConfigInheritance(t, "exec-create")
	wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)

	t.Run("an independent specialisation keeps its own", func(t *testing.T) {
		own := createAgentThroughAPI(t, map[string]any{
			"name":              "exec-create-own-spec",
			"parent_agent_id":   fx.ParentID,
			"runtime_inherited": false,
			"runtime_id":        fx.RuntimeID,
			"custom_args":       []string{"--mine"},
		})
		if got := persistedExecutionConfig(t, own.ID); got.CustomArgs != `["--mine"]` || got.CustomEnv != "{}" || got.McpConfig != "" {
			t.Errorf("independent specialisation execution config = %+v, want only its own custom_args", got)
		}
	})
}

func TestUpdateAgent_BaseRoleExecutionConfigCascades(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newExecutionConfigInheritance(t, "exec-cascade")
	own := createAgentThroughAPI(t, map[string]any{
		"name":              "exec-cascade-own-spec",
		"parent_agent_id":   fx.ParentID,
		"runtime_inherited": false,
		"runtime_id":        fx.RuntimeID,
		"custom_args":       []string{"--mine"},
	})

	updateAgentThroughAPI(t, fx.ParentID, map[string]any{
		"custom_args": []string{"--gemini_dir=/accounts/2"},
		"mcp_config":  json.RawMessage(`{"mcpServers":{}}`),
	})
	wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)
	if got := persistedExecutionConfig(t, fx.Child.ID); got.CustomArgs != `["--gemini_dir=/accounts/2"]` {
		t.Errorf("follower custom_args = %s, want the base role's new value", got.CustomArgs)
	}

	putAgentEnv(t, fx.ParentID, map[string]string{"API_KEY": "rotated"}, http.StatusOK)
	wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)

	updateAgentThroughAPI(t, fx.ParentID, map[string]any{"mcp_config": nil})
	wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)

	if got := persistedExecutionConfig(t, own.ID); got.CustomArgs != `["--mine"]` || got.CustomEnv != "{}" {
		t.Errorf("independent specialisation was rewritten to %+v", got)
	}
}

func TestUpdateAgent_FollowingSpecialisationRefusesOwnExecutionConfigEdit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newExecutionConfigInheritance(t, "exec-refuse")

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"custom_args", map[string]any{"custom_args": []string{"--mine"}}},
		{"mcp_config", map[string]any{"mcp_config": map[string]any{"mcpServers": map[string]any{}}}},
		{"clear mcp_config", map[string]any{"mcp_config": nil}},
		{"follow and configure at once", map[string]any{"runtime_inherited": true, "custom_args": []string{"--mine"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testutil.WithURLParams(
				specRequest(http.MethodPut, "/api/agents/"+fx.Child.ID, tc.body), "id", fx.Child.ID)
			testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)
		})
	}
	t.Run("env", func(t *testing.T) {
		putAgentEnv(t, fx.Child.ID, map[string]string{"API_KEY": "mine"}, http.StatusBadRequest)
	})
	wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)

	t.Run("going independent keeps the copy and makes it editable", func(t *testing.T) {
		updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"runtime_inherited": false})
		wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)
		updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"custom_args": []string{"--mine"}})
		putAgentEnv(t, fx.Child.ID, map[string]string{"API_KEY": "mine"}, http.StatusOK)
	})

	t.Run("following again re-copies the base role", func(t *testing.T) {
		updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"runtime_inherited": true})
		wantExecutionConfigCopied(t, fx.ParentID, fx.Child.ID)
	})
}

// Another member may follow a public base role's runtime, but must not receive
// its env or MCP config: that would reveal the base role owner's secrets
// through the follower's own env endpoint.
func TestExecutionConfigInheritance_StopsAtAnotherOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	wsID := dbfx.Workspace(t, "Exec Cross Owner", "exec-cross-owner")
	ownerID := dbfx.User(t, "Exec Owner", "exec-owner@multica.test")
	callerID := dbfx.User(t, "Exec Caller", "exec-caller@multica.test")
	dbfx.Member(t, wsID, ownerID, "member")
	dbfx.Member(t, wsID, callerID, "member")

	runtimeID := dbfx.Runtime(t, "exec-cross-runtime", testutil.Cols{
		"workspace_id": wsID,
		"owner_id":     ownerID,
		"visibility":   "public",
		"provider":     "codex",
	})
	baseID := dbfx.Agent(t, "exec-cross-base", runtimeID, testutil.Cols{
		"workspace_id":    wsID,
		"owner_id":        ownerID,
		"visibility":      "workspace",
		"permission_mode": "public_to",
		"custom_env":      testutil.Raw(`'{"API_KEY":"owner-secret"}'::jsonb`),
		"custom_args":     testutil.Raw(`'["--owner"]'::jsonb`),
		"mcp_config":      testutil.Raw(`'{"mcpServers":{"owner":{}}}'::jsonb`),
	})
	dbfx.InsertNoID(t, "agent_invocation_target",
		testutil.Cols{"agent_id": baseID, "target_type": "workspace", "target_id": wsID},
		"agent_id = $1", baseID)

	as := func(userID string, req *http.Request) *http.Request {
		return testutil.WithHeaders(req, "X-User-ID", userID, "X-Workspace-ID", wsID)
	}

	child := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, as(callerID,
		testutil.JSONRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":            "exec-cross-spec",
			"parent_agent_id": baseID,
		})), http.StatusCreated)
	cleanupAPIAgent(t, child.ID)

	if !child.RuntimeInherited || child.RuntimeID != runtimeID {
		t.Fatalf("cross-owner child runtime = (%v, %q), want it to follow the shared runtime %q", child.RuntimeInherited, child.RuntimeID, runtimeID)
	}
	wantOwn := func(t *testing.T) {
		t.Helper()
		if got := persistedExecutionConfig(t, child.ID); got.CustomEnv != "{}" || got.CustomArgs != "[]" || got.McpConfig != "" {
			t.Errorf("cross-owner child execution config = %+v, want none of the base role's", got)
		}
	}
	wantOwn(t)

	testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, testutil.WithURLParams(as(ownerID,
		testutil.JSONRequest(http.MethodPut, "/api/agents/"+baseID, map[string]any{"custom_args": []string{"--owner-2"}})),
		"id", baseID), http.StatusOK)
	wantOwn(t)

	// Its own execution config stays editable.
	testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, testutil.WithURLParams(as(callerID,
		testutil.JSONRequest(http.MethodPut, "/api/agents/"+child.ID, map[string]any{"custom_args": []string{"--caller"}})),
		"id", child.ID), http.StatusOK)
}
