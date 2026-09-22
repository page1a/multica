package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func moduleVisibilityCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM workspace_module_visibility WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(context.Background(),
			`DELETE FROM visibility_audit WHERE workspace_id = $1 AND resource_type = 'module'`, testWorkspaceID)
	})
}

func TestListModuleVisibilityDefaultsToWorkspace(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	member := visibilityTestMember(t, "Mod Default", "mod-default@multica.ai")
	var out struct {
		Modules []struct {
			Key        string  `json:"key"`
			Visibility string  `json:"visibility"`
			Allowed    bool    `json:"allowed"`
			ProjectID  *string `json:"project_id"`
		} `json:"modules"`
	}
	testutil.Call(t, testHandler.ListModuleVisibility,
		newRequestAs(member, "GET", "/api/modules", nil)).
		Want(200).JSON(&out)

	if len(out.Modules) != len(permission.Modules) {
		t.Fatalf("got %d modules, want the closed list of %d", len(out.Modules), len(permission.Modules))
	}
	seen := map[string]bool{}
	for _, row := range out.Modules {
		seen[row.Key] = true
		if row.Visibility != "workspace" {
			t.Errorf("%s visibility = %q, want workspace (missing row is the upgrade default)", row.Key, row.Visibility)
		}
		if !row.Allowed {
			t.Errorf("%s allowed = false for a member; workspace-scoped modules include every member", row.Key)
		}
		if row.ProjectID != nil {
			t.Errorf("%s project_id = %v, want null", row.Key, row.ProjectID)
		}
	}
	for _, key := range []string{"issues", "projects", "repos", "runtimes"} {
		if !seen[key] {
			t.Errorf("missing module %q", key)
		}
	}
	if seen["agents"] || seen["squads"] {
		t.Fatal("agents and squads must not appear: their access is Parent B")
	}
}

func TestSetModuleVisibilityPrivateHidesModuleFromMember(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	member := visibilityTestMember(t, "Mod Hidden", "mod-hidden@multica.ai")

	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/issues/visibility", map[string]any{"visibility": "private"}),
			"module", "issues")).
		Want(200)

	var out struct {
		Modules []struct {
			Key     string `json:"key"`
			Allowed bool   `json:"allowed"`
		} `json:"modules"`
	}
	testutil.Call(t, testHandler.ListModuleVisibility,
		newRequestAs(member, "GET", "/api/modules", nil)).
		Want(200).JSON(&out)
	for _, row := range out.Modules {
		if row.Key == "issues" && row.Allowed {
			t.Fatal("a member must not enter a private Issues module")
		}
		if row.Key == "projects" && !row.Allowed {
			t.Fatal("restricting Issues must not take Projects away")
		}
	}

	var ownerOut struct {
		Modules []struct {
			Key     string `json:"key"`
			Allowed bool   `json:"allowed"`
		} `json:"modules"`
	}
	testutil.Call(t, testHandler.ListModuleVisibility,
		newRequest("GET", "/api/modules", nil)).
		Want(200).JSON(&ownerOut)
	for _, row := range ownerOut.Modules {
		if row.Key == "issues" && !row.Allowed {
			t.Fatal("owner must still enter a private module so they can un-restrict it")
		}
	}
}

func TestSetModuleVisibilityWritesAudit(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/runtimes/visibility", map[string]any{"visibility": "private"}),
			"module", "runtimes")).
		Want(200)

	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM visibility_audit
		WHERE workspace_id = $1 AND resource_type = 'module' AND resource_id = 'runtimes'
		  AND new_visibility = 'private' AND source = 'direct'`, testWorkspaceID).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows for the runtimes change = %d, want 1", n)
	}
}

func TestSetModuleVisibilityRejectsMemberAndUnknownModule(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	member := visibilityTestMember(t, "Mod Writer", "mod-writer@multica.ai")
	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequestAs(member, "PUT", "/api/modules/issues/visibility", map[string]any{"visibility": "private"}),
			"module", "issues")).
		Want(403)

	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/agents/visibility", map[string]any{"visibility": "private"}),
			"module", "agents")).
		Want(400)
}

func TestSetModuleVisibilityProjectRequiresProject(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/projects/visibility", map[string]any{"visibility": "project"}),
			"module", "projects")).
		Want(400)

	author := visibilityTestMember(t, "Mod Lead", "mod-lead@multica.ai")
	projectID := dbfx.Project(t, "module audience", testutil.Cols{
		"visibility": "workspace",
		"created_by": author,
	})
	dbfx.Insert(t, "project_member", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
		"member_id":    author,
		"added_by":     testUserID,
	})

	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/issues/visibility", map[string]any{
			"visibility": "project",
			"project_id": projectID,
		}), "module", "issues")).
		Want(200)

	insider := author
	outsider := visibilityTestMember(t, "Mod Out", "mod-out@multica.ai")

	allowed := func(userID string) bool {
		var out struct {
			Modules []struct {
				Key     string `json:"key"`
				Allowed bool   `json:"allowed"`
			} `json:"modules"`
		}
		testutil.Call(t, testHandler.ListModuleVisibility,
			newRequestAs(userID, "GET", "/api/modules", nil)).
			Want(200).JSON(&out)
		for _, row := range out.Modules {
			if row.Key == "issues" {
				return row.Allowed
			}
		}
		t.Fatal("issues row missing")
		return false
	}
	if !allowed(insider) {
		t.Fatal("a member of the designated project must enter a project-scoped Issues module")
	}
	if allowed(outsider) {
		t.Fatal("a member outside the designated project must not enter a project-scoped Issues module")
	}
}

func TestRequireModuleRejectsIssueAPIAndLeavesAgentsOpen(t *testing.T) {
	requireDB(t)
	moduleVisibilityCleanup(t)

	member := visibilityTestMember(t, "Mod Gate", "mod-gate@multica.ai")
	testutil.Call(t, testHandler.SetModuleVisibility,
		withURLParam(newRequest("PUT", "/api/modules/issues/visibility", map[string]any{"visibility": "private"}),
			"module", "issues")).
		Want(200)

	issues := chi.NewRouter()
	issues.Use(testHandler.RequireModule(permission.ModuleIssues))
	issues.Get("/api/issues", testHandler.ListIssues)

	req := newRequestAs(member, "GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	rec := httptest.NewRecorder()
	issues.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Issues API as a member with no module access: got %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "not found") || strings.Contains(body, "permission") {
		t.Fatalf("denied module answered %q; it must read as not found, not a permission leak", body)
	}

	ownerReq := newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	ownerRec := httptest.NewRecorder()
	issues.ServeHTTP(ownerRec, ownerReq)
	if ownerRec.Code != http.StatusOK {
		t.Fatalf("Issues API as owner: got %d (%s), want 200", ownerRec.Code, ownerRec.Body.String())
	}

	testutil.Call(t, testHandler.ListAgents, newRequestAs(member, "GET", "/api/agents", nil)).Want(200)
	testutil.Call(t, testHandler.ListSquads,
		withURLParam(newRequestAs(member, "GET", "/api/squads", nil), "workspaceId", testWorkspaceID)).
		Want(200)
}
