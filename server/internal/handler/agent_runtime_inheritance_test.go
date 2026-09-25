package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Runtime inheritance for specialisations (DENE-505).
//
// The contract these tests pin, in one place because the three halves only make
// sense together: a specialisation created through the API follows its base
// role's runtime profile by default, a base-role runtime edit re-copies that
// profile onto every follower, and the copy is what dispatch actually reads.
//
// "Runtime profile" is exactly six columns — runtime_id, runtime_mode,
// runtime_config, model, thinking_level, service_tier — because those are the
// ones a daemon claim reads to decide where and how the agent runs. Prompts and
// skills are the DENE-302 inheritance and are covered there; custom_args,
// custom_env and mcp_config follow too, within one owner (DENE-854), and are
// covered in agent_execution_config_inheritance_test.go.
//
// Fixture rows are built through dbfx and every handler call goes through
// testutil.Call/Decode, so a failure prints the request line, both statuses and
// the body.

// runtimeProfile is the six-column copy, read straight off the row: an
// assertion on what the database holds cannot be satisfied by a response that
// merely says the right thing.
type runtimeProfile struct {
	RuntimeID     string
	RuntimeMode   string
	RuntimeConfig string
	Model         string
	ThinkingLevel string
	ServiceTier   string
	Inherited     bool
}

func persistedRuntimeProfile(t *testing.T, agentID string) runtimeProfile {
	t.Helper()

	var p runtimeProfile
	dbfx.QueryRow(t, `
		SELECT COALESCE(runtime_id::text, ''),
		       runtime_mode,
		       runtime_config::text,
		       COALESCE(model, ''),
		       COALESCE(thinking_level, ''),
		       COALESCE(service_tier, ''),
		       runtime_inherited
		FROM agent
		WHERE id = $1
	`, agentID).Scan(&p.RuntimeID, &p.RuntimeMode, &p.RuntimeConfig, &p.Model, &p.ThinkingLevel, &p.ServiceTier, &p.Inherited)
	return p
}

// profileWithoutFlag drops runtime_inherited so a test can compare the copied
// profile across a step whose whole purpose is to flip the flag.
func profileWithoutFlag(p runtimeProfile) runtimeProfile {
	p.Inherited = false
	return p
}

// wantProfileCopied asserts that child carries the base role's whole runtime
// profile, field by field. A whole-profile comparison is the point: a copy that
// lands on five of six columns is the drift this feature exists to remove.
func wantProfileCopied(t *testing.T, parentID, childID string) {
	t.Helper()

	parent := persistedRuntimeProfile(t, parentID)
	child := persistedRuntimeProfile(t, childID)
	if child.RuntimeID != parent.RuntimeID ||
		child.RuntimeMode != parent.RuntimeMode ||
		child.RuntimeConfig != parent.RuntimeConfig ||
		child.Model != parent.Model ||
		child.ThinkingLevel != parent.ThinkingLevel ||
		child.ServiceTier != parent.ServiceTier {
		t.Errorf("child profile %+v does not mirror base role %+v", child, parent)
	}
}

// runtimeInheritance is a base role with a codex runtime profile, the runtime
// it runs on, and a specialisation created through POST /api/agents — so the
// starting state of every test came out of the code path a user hits, not out
// of a fixture that already encoded the answer.
type runtimeInheritance struct {
	RuntimeID string
	ParentID  string
	Child     AgentResponse
}

// codex is the provider these fixtures use because it is the one whose
// thinking-level and service-tier values survive the server's literal gates, so
// the profile copy can cover all six columns instead of an empty tail.
func newRuntimeInheritance(t *testing.T, name string) runtimeInheritance {
	t.Helper()

	runtimeID := dbfx.Runtime(t, name+"-runtime", testutil.Cols{"provider": "codex"})
	parentID := dbfx.Agent(t, name+"-base", runtimeID, testutil.Cols{
		"instructions":         "base role rules",
		"model":                "gpt-5-codex",
		"thinking_level":       "high",
		"service_tier":         "priority",
		"runtime_config":       testutil.Raw(`'{"gateway":{"mode":"embedded"}}'::jsonb`),
		"max_concurrent_tasks": 4,
	})

	req := specRequest(http.MethodPost, "/api/agents", map[string]any{
		"name":            name + "-spec",
		"instructions":    "specialisation delta",
		"parent_agent_id": parentID,
	})
	child := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)
	cleanupAPIAgent(t, child.ID)

	return runtimeInheritance{RuntimeID: runtimeID, ParentID: parentID, Child: child}
}

// cleanupAPIAgent removes a row this test created through the handler. dbfx
// only owns the rows it inserted itself, and the suite shares one workspace for
// the whole run: a leaked agent would change what a later test counts.
func cleanupAPIAgent(t *testing.T, agentID string) {
	t.Helper()

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})
}

// createAgentThroughAPI creates an agent and returns its response, failing the
// test on anything but 200. Used for the specialisations whose flag is set
// explicitly rather than defaulted.
func createAgentThroughAPI(t *testing.T, body map[string]any) AgentResponse {
	t.Helper()

	req := specRequest(http.MethodPost, "/api/agents", body)
	resp := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, req, http.StatusCreated)
	cleanupAPIAgent(t, resp.ID)
	return resp
}

func updateAgentThroughAPI(t *testing.T, agentID string, body map[string]any) AgentResponse {
	t.Helper()

	req := testutil.WithURLParams(specRequest(http.MethodPut, "/api/agents/"+agentID, body), "id", agentID)
	return testutil.Decode[AgentResponse](t, testHandler.UpdateAgent, req, http.StatusOK)
}

// TestCreateAgent_SpecialisationInheritsBaseRoleRuntime is acceptance criterion
// 1: a specialisation created with a base role follows that base role's runtime
// without being told to, and does so even when the request still carries the
// pre-DENE-505 runtime fields (the current UI sends a picked runtime on every
// create, and "follow by default" is what the feature means).
func TestCreateAgent_SpecialisationInheritsBaseRoleRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newRuntimeInheritance(t, "inherit-create")

	if !fx.Child.RuntimeInherited {
		t.Errorf("runtime_inherited = false on a specialisation created with a base role, want true")
	}
	if fx.Child.RuntimeID != fx.RuntimeID {
		t.Errorf("child runtime_id = %q, want the base role's %q", fx.Child.RuntimeID, fx.RuntimeID)
	}
	if fx.Child.Model != "gpt-5-codex" {
		t.Errorf("child model = %q, want the base role's %q", fx.Child.Model, "gpt-5-codex")
	}
	if fx.Child.ThinkingLevel != "high" || fx.Child.ServiceTier != "priority" {
		t.Errorf("child thinking/tier = (%q, %q), want (%q, %q)",
			fx.Child.ThinkingLevel, fx.Child.ServiceTier, "high", "priority")
	}
	wantProfileCopied(t, fx.ParentID, fx.Child.ID)

	t.Run("a runtime sent alongside the base role is not an override", func(t *testing.T) {
		// The legacy payload: the create form has always required a runtime_id,
		// so this is what "create a specialisation" looked like before the
		// feature. The default still wins — runtime_inherited=false is the only
		// way to ask for the pick to be used.
		otherRuntimeID := dbfx.Runtime(t, "inherit-create-other-runtime", testutil.Cols{"provider": "codex"})
		child := createAgentThroughAPI(t, map[string]any{
			"name":            "inherit-create-legacy-spec",
			"parent_agent_id": fx.ParentID,
			"runtime_id":      otherRuntimeID,
			"model":           "some-other-model",
			"runtime_config":  map[string]any{"picked": true},
		})

		if !child.RuntimeInherited {
			t.Error("runtime_inherited = false for a create that only omitted the flag")
		}
		if child.RuntimeID != fx.RuntimeID {
			t.Errorf("child runtime_id = %q, want the base role's %q", child.RuntimeID, fx.RuntimeID)
		}
		if child.Model != "gpt-5-codex" {
			t.Errorf("child model = %q, want the base role's %q, not the request's", child.Model, "gpt-5-codex")
		}
		wantProfileCopied(t, fx.ParentID, child.ID)
	})
}

// TestCreateAgent_RuntimeInheritanceGuards covers the edges of the create
// contract: which requests are refused, and what "follow" means when the base
// role has no runtime or one this caller may not use.
func TestCreateAgent_RuntimeInheritanceGuards(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("inheriting without a base role is refused", func(t *testing.T) {
		req := specRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":              "inherit-no-parent",
			"runtime_id":        testRuntimeID,
			"runtime_inherited": true,
		})
		testutil.Call(t, testHandler.CreateAgent, req).Want(http.StatusBadRequest)
	})

	t.Run("opting out still requires a runtime", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "inherit-optout-noruntime")
		req := specRequest(http.MethodPost, "/api/agents", map[string]any{
			"name":              "inherit-optout-noruntime-spec",
			"parent_agent_id":   fx.ParentID,
			"runtime_inherited": false,
		})
		testutil.Call(t, testHandler.CreateAgent, req).Want(http.StatusBadRequest)
	})

	t.Run("a base role without a runtime yields a follower without one", func(t *testing.T) {
		// The base role kept its configuration when its runtime was deleted
		// (MUL-5559). Following it means following it there too: the child is
		// created unbound and binds whatever the base role binds next.
		parentID := dbfx.Agent(t, "inherit-unbound-base", "", nil)
		child := createAgentThroughAPI(t, map[string]any{
			"name":            "inherit-unbound-spec",
			"parent_agent_id": parentID,
		})

		if !child.RuntimeInherited {
			t.Error("runtime_inherited = false for a specialisation of an unbound base role")
		}
		if child.RuntimeBound || child.RuntimeID != "" {
			t.Errorf("child runtime = (%q, bound=%v), want unbound like its base role", child.RuntimeID, child.RuntimeBound)
		}
	})
}

// TestCreateAgent_InheritanceStopsAtAnotherMembersPrivateRuntime pins the one
// place inheritance is refused rather than copied: dispatch already rejects a
// private runtime whose owner is not the agent's owner, so following a base
// role onto one would create an agent that can never run. The caller's own
// runtime is the fallback, and without one the request fails instead of
// producing that agent.
func TestCreateAgent_InheritanceStopsAtAnotherMembersPrivateRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	wsID := dbfx.Workspace(t, "Inherit Cross Member", "inherit-cross-member")
	ownerID := dbfx.User(t, "Inherit Owner", "inherit-owner@multica.test")
	callerID := dbfx.User(t, "Inherit Caller", "inherit-caller@multica.test")
	dbfx.Member(t, wsID, ownerID, "member")
	dbfx.Member(t, wsID, callerID, "member")

	ownerRuntimeID := dbfx.Runtime(t, "inherit-private-runtime", testutil.Cols{
		"workspace_id": wsID,
		"owner_id":     ownerID,
		"visibility":   "private",
	})
	baseID := dbfx.Agent(t, "inherit-cross-base", ownerRuntimeID, testutil.Cols{
		"workspace_id":    wsID,
		"owner_id":        ownerID,
		"visibility":      "workspace",
		"permission_mode": "public_to",
	})
	dbfx.InsertNoID(t, "agent_invocation_target",
		testutil.Cols{"agent_id": baseID, "target_type": "workspace", "target_id": wsID},
		"agent_id = $1", baseID)

	callerRuntimeID := dbfx.Runtime(t, "inherit-caller-runtime", testutil.Cols{
		"workspace_id": wsID,
		"owner_id":     callerID,
		"visibility":   "private",
	})
	callerRequest := func(body map[string]any) *http.Request {
		return testutil.WithHeaders(testutil.JSONRequest(http.MethodPost, "/api/agents", body),
			"X-User-ID", callerID, "X-Workspace-ID", wsID)
	}

	t.Run("following with no runtime of its own is refused", func(t *testing.T) {
		testutil.Call(t, testHandler.CreateAgent, callerRequest(map[string]any{
			"name":            "inherit-cross-nowhere",
			"parent_agent_id": baseID,
		})).Want(http.StatusForbidden)
	})

	t.Run("an explicit runtime keeps the agent runnable", func(t *testing.T) {
		resp := testutil.Decode[AgentResponse](t, testHandler.CreateAgent, callerRequest(map[string]any{
			"name":            "inherit-cross-own-runtime",
			"parent_agent_id": baseID,
			"runtime_id":      callerRuntimeID,
		}), http.StatusCreated)
		cleanupAPIAgent(t, resp.ID)

		if resp.RuntimeInherited {
			t.Error("runtime_inherited = true for a child that had to fall back to its own runtime")
		}
		if resp.RuntimeID != callerRuntimeID {
			t.Errorf("runtime_id = %q, want the caller's own %q", resp.RuntimeID, callerRuntimeID)
		}
	})
}

// TestUpdateAgent_BaseRoleRuntimeChangeCascades is acceptance criterion 2: the
// base role's own update endpoint is what moves its followers, and it moves
// only the followers — a specialisation that owns its runtime is untouched.
func TestUpdateAgent_BaseRoleRuntimeChangeCascades(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newRuntimeInheritance(t, "cascade")
	ownChild := createAgentThroughAPI(t, map[string]any{
		"name":              "cascade-own-spec",
		"parent_agent_id":   fx.ParentID,
		"runtime_inherited": false,
		"runtime_id":        fx.RuntimeID,
		"model":             "own-model",
	})

	newRuntimeID := dbfx.Runtime(t, "cascade-runtime-2", testutil.Cols{"provider": "codex"})
	updated := updateAgentThroughAPI(t, fx.ParentID, map[string]any{
		"runtime_id":     newRuntimeID,
		"model":          "gpt-5.1-codex",
		"thinking_level": "medium",
		"service_tier":   "flex",
	})

	if updated.RuntimeID != newRuntimeID {
		t.Fatalf("base role runtime_id = %q, want %q", updated.RuntimeID, newRuntimeID)
	}
	wantProfileCopied(t, fx.ParentID, fx.Child.ID)

	follower := persistedRuntimeProfile(t, fx.Child.ID)
	if follower.RuntimeID != newRuntimeID || follower.Model != "gpt-5.1-codex" ||
		follower.ThinkingLevel != "medium" || follower.ServiceTier != "flex" {
		t.Errorf("follower profile = %+v, want the base role's new runtime profile", follower)
	}
	if !follower.Inherited {
		t.Error("the cascade cleared runtime_inherited on the follower")
	}

	mine := persistedRuntimeProfile(t, ownChild.ID)
	if mine.RuntimeID != fx.RuntimeID || mine.Model != "own-model" {
		t.Errorf("independent specialisation was rewritten to %+v; a base-role edit must not reach it", mine)
	}
	if mine.Inherited {
		t.Error("independent specialisation reports runtime_inherited after a cascade")
	}

	t.Run("a metadata-only edit does not walk the followers", func(t *testing.T) {
		// The row-level guard is the profile comparison in the sync statement;
		// this proves the handler does not even call it for an edit that cannot
		// have moved a runtime, by checking the follower's updated_at is stable.
		before := persistedAgentUpdatedAt(t, fx.Child.ID)
		updateAgentThroughAPI(t, fx.ParentID, map[string]any{"description": "renamed only"})
		if after := persistedAgentUpdatedAt(t, fx.Child.ID); after != before {
			t.Errorf("follower updated_at moved on a description-only edit: %q -> %q", before, after)
		}
	})
}

func persistedAgentUpdatedAt(t *testing.T, agentID string) string {
	t.Helper()

	var updatedAt string
	dbfx.QueryRow(t, `SELECT updated_at::text FROM agent WHERE id = $1`, agentID).Scan(&updatedAt)
	return updatedAt
}

// TestUpdateAgent_FollowingSpecialisationRefusesOwnRuntimeEdit pins the "no
// silent coercion" half of the contract: while an agent follows its base role,
// a request that tries to set a runtime field is a 400 naming the way out,
// never a quiet copy that leaves the caller believing their value was stored.
func TestUpdateAgent_FollowingSpecialisationRefusesOwnRuntimeEdit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fx := newRuntimeInheritance(t, "refuse-own-edit")

	tests := []struct {
		name string
		body map[string]any
	}{
		{"model", map[string]any{"model": "gpt-5.1-codex"}},
		{"thinking_level", map[string]any{"thinking_level": "low"}},
		{"runtime_config", map[string]any{"runtime_config": map[string]any{"mode": "gateway"}}},
		{"follow and configure at once", map[string]any{"runtime_inherited": true, "model": "gpt-5.1-codex"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := testutil.WithURLParams(
				specRequest(http.MethodPut, "/api/agents/"+fx.Child.ID, tc.body), "id", fx.Child.ID)
			testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)
		})
	}

	// Nothing above may have landed.
	wantProfileCopied(t, fx.ParentID, fx.Child.ID)
}

// TestUpdateAgent_RuntimeInheritanceTransitions covers the two explicit
// switches and the detach. Both directions have to be lossless in a different
// sense: turning "follow" on copies immediately, turning it off keeps exactly
// what the agent was running with, and detaching from the base role drops the
// flag in the same statement that drops the parent.
func TestUpdateAgent_RuntimeInheritanceTransitions(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("turning follow on copies the base role immediately", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-on")
		ownChild := createAgentThroughAPI(t, map[string]any{
			"name":              "transition-on-own-spec",
			"parent_agent_id":   fx.ParentID,
			"runtime_inherited": false,
			"runtime_id":        fx.RuntimeID,
			"model":             "own-model",
		})

		resp := updateAgentThroughAPI(t, ownChild.ID, map[string]any{"runtime_inherited": true})

		if !resp.RuntimeInherited {
			t.Error("runtime_inherited = false after switching follow on")
		}
		// The response must carry the copy, not the row as it stood before it.
		if resp.Model != "gpt-5-codex" || resp.RuntimeID != fx.RuntimeID {
			t.Errorf("response = (runtime %q, model %q), want the base role's profile", resp.RuntimeID, resp.Model)
		}
		wantProfileCopied(t, fx.ParentID, ownChild.ID)
	})

	t.Run("turning follow off keeps what the agent was running with", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-off")
		before := persistedRuntimeProfile(t, fx.Child.ID)

		resp := updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"runtime_inherited": false})

		if resp.RuntimeInherited {
			t.Error("runtime_inherited = true after opting out")
		}
		after := persistedRuntimeProfile(t, fx.Child.ID)
		if profileWithoutFlag(after) != profileWithoutFlag(before) {
			t.Errorf("opting out changed the profile: %+v -> %+v", before, after)
		}

		// And it is now genuinely independent: a later base-role edit must not
		// reach it.
		newRuntimeID := dbfx.Runtime(t, "transition-off-runtime-2", testutil.Cols{"provider": "codex"})
		updateAgentThroughAPI(t, fx.ParentID, map[string]any{"runtime_id": newRuntimeID, "model": "gpt-5.1-codex"})
		if now := persistedRuntimeProfile(t, fx.Child.ID); now.RuntimeID != before.RuntimeID {
			t.Errorf("an opted-out specialisation followed the base role to %q", now.RuntimeID)
		}
	})

	t.Run("detaching clears the flag and keeps the profile", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-detach")
		before := persistedRuntimeProfile(t, fx.Child.ID)

		resp := updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"parent_agent_id": ""})

		if resp.RuntimeInherited {
			t.Error("runtime_inherited stayed true on an agent detached from its base role")
		}
		after := persistedRuntimeProfile(t, fx.Child.ID)
		if after.Inherited {
			t.Error("persisted runtime_inherited stayed true after detaching")
		}
		if after.RuntimeID != before.RuntimeID || after.Model != before.Model {
			t.Errorf("detaching changed the runtime profile: %+v -> %+v", before, after)
		}
	})

	t.Run("a base role cannot inherit", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-base")
		req := testutil.WithURLParams(
			specRequest(http.MethodPut, "/api/agents/"+fx.ParentID, map[string]any{"runtime_inherited": true}),
			"id", fx.ParentID)
		testutil.Call(t, testHandler.UpdateAgent, req).Want(http.StatusBadRequest)
	})

	t.Run("re-parenting onto another base role re-copies the runtime", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-reparent")
		otherRuntimeID := dbfx.Runtime(t, "transition-reparent-runtime-2", testutil.Cols{"provider": "codex"})
		otherBaseID := dbfx.Agent(t, "transition-reparent-base-2", otherRuntimeID, testutil.Cols{
			"model":          "gpt-5.2-codex",
			"thinking_level": "low",
		})

		updateAgentThroughAPI(t, fx.Child.ID, map[string]any{"parent_agent_id": otherBaseID})

		wantProfileCopied(t, otherBaseID, fx.Child.ID)
		if got := persistedRuntimeProfile(t, fx.Child.ID); got.Model != "gpt-5.2-codex" {
			t.Errorf("re-parented child model = %q, want the new base role's", got.Model)
		}
	})

	t.Run("a solidified specialisation stops following", func(t *testing.T) {
		fx := newRuntimeInheritance(t, "transition-solidify")
		req := testutil.WithURLParams(
			specRequest(http.MethodPost, "/api/agents/"+fx.Child.ID+"/solidify", nil), "id", fx.Child.ID)
		resp := testutil.Decode[AgentResponse](t, testHandler.SolidifyAgent, req, http.StatusOK)

		if resp.RuntimeInherited {
			t.Error("a solidified agent still reports runtime_inherited")
		}
		if got := persistedRuntimeProfile(t, fx.Child.ID); got.Inherited {
			t.Error("persisted runtime_inherited stayed true after solidify")
		}
	})
}

// TestClaim_SpecialisationRunsInheritedRuntimeProfile is acceptance criterion 3
// at the moment it matters: the claim response is the entire contract the
// daemon boots from, so a value that is not in this payload is not in the run.
func TestClaim_SpecialisationRunsInheritedRuntimeProfile(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	fx := newRuntimeInheritance(t, "claim-inherit")
	issueID := dbfx.Issue(t, "claim-inherit issue")
	dbfx.Task(t, fx.Child.ID, testutil.Cols{
		"runtime_id": fx.RuntimeID,
		"issue_id":   issueID,
	})

	claim := claimRuntimeProfile(t, fx.RuntimeID)
	if claim.TaskID == "" {
		t.Fatalf("claim returned no task: %s", claim.Raw)
	}
	if claim.Model != "gpt-5-codex" || claim.ThinkingLevel != "high" || claim.ServiceTier != "priority" {
		t.Errorf("claimed (model, thinking, tier) = (%q, %q, %q), want the base role's profile",
			claim.Model, claim.ThinkingLevel, claim.ServiceTier)
	}
	if claim.RuntimeConfig["gateway"] == nil {
		t.Errorf("claimed runtime_config = %v, want the base role's config", claim.RuntimeConfig)
	}

	// Move the base role and queue the specialisation's next run: dispatch reads
	// the inherited copy, so the new runtime has to be in the next claim.
	newRuntimeID := dbfx.Runtime(t, "claim-inherit-runtime-2", testutil.Cols{"provider": "codex"})
	updateAgentThroughAPI(t, fx.ParentID, map[string]any{
		"runtime_id": newRuntimeID,
		"model":      "gpt-5.1-codex",
	})
	nextIssueID := dbfx.Issue(t, "claim-inherit issue 2")
	dbfx.Task(t, fx.Child.ID, testutil.Cols{
		"runtime_id": newRuntimeID,
		"issue_id":   nextIssueID,
	})

	next := claimRuntimeProfile(t, newRuntimeID)
	if next.TaskID == "" {
		t.Fatalf("second claim returned no task: %s", next.Raw)
	}
	if next.Model != "gpt-5.1-codex" {
		t.Errorf("second claim model = %q, want the moved base role's %q", next.Model, "gpt-5.1-codex")
	}
}

// runtimeProfileClaim is the slice of a claim response these assertions read.
type runtimeProfileClaim struct {
	TaskID        string
	Model         string
	ThinkingLevel string
	ServiceTier   string
	RuntimeConfig map[string]any
	Raw           string
}

func claimRuntimeProfile(t *testing.T, runtimeID string) runtimeProfileClaim {
	t.Helper()

	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+runtimeID+"/tasks/claim", nil, testWorkspaceID, "dene-505-daemon")
	req = withURLParam(req, "runtimeId", runtimeID)

	var resp struct {
		Task *struct {
			ID    string `json:"id"`
			Agent *struct {
				Model         string          `json:"model"`
				ThinkingLevel string          `json:"thinking_level"`
				ServiceTier   string          `json:"service_tier"`
				RuntimeConfig json.RawMessage `json:"runtime_config"`
			} `json:"agent"`
		} `json:"task"`
	}
	w := testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(http.StatusOK)
	w.JSON(&resp)
	if resp.Task == nil || resp.Task.Agent == nil {
		return runtimeProfileClaim{Raw: w.Body.String()}
	}

	claim := runtimeProfileClaim{
		TaskID:        resp.Task.ID,
		Model:         resp.Task.Agent.Model,
		ThinkingLevel: resp.Task.Agent.ThinkingLevel,
		ServiceTier:   resp.Task.Agent.ServiceTier,
		Raw:           w.Body.String(),
	}
	if len(resp.Task.Agent.RuntimeConfig) > 0 {
		if err := json.Unmarshal(resp.Task.Agent.RuntimeConfig, &claim.RuntimeConfig); err != nil {
			t.Fatalf("claim runtime_config is not an object: %v (%s)", err, resp.Task.Agent.RuntimeConfig)
		}
	}
	return claim
}
