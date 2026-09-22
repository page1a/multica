package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestBackfillLocalDirectoryIdentityFillsMissingRealPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill")
	const daemonID = "daemon-backfill-identity"
	created := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/legacy-dir",
		"daemon_id":  daemonID,
	})

	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+created.ID+"/identity", map[string]any{
		"real_path":   "/private/tmp/legacy-dir",
		"repo_key":    "https://github.com/Acme/App.git",
		"is_git_repo": true,
	}, testWorkspaceID, daemonID)
	req = withURLParam(req, "resourceId", created.ID)
	w := testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusOK)

	var resp struct {
		Applied bool `json:"applied"`
	}
	w.JSON(&resp)
	if !resp.Applied {
		t.Fatal("expected the missing identity to be written")
	}

	got := getProjectResource(t, project.ID, created.ID)
	var ref localDirectoryRef
	if err := json.Unmarshal(got.ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RealPath != "/private/tmp/legacy-dir" {
		t.Errorf("real_path = %q", ref.RealPath)
	}
	if ref.RepoKey != "github.com/acme/app" {
		t.Errorf("repo_key = %q, want normalized github.com/acme/app", ref.RepoKey)
	}
	if ref.IsGitRepo == nil || !*ref.IsGitRepo {
		t.Error("is_git_repo was not stored")
	}
}

func TestBackfillLocalDirectoryIdentityConflictDoesNotFailTheDaemon(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill conflict")
	const daemonID = "daemon-backfill-conflict"
	first := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/private/tmp/same-dir",
		"daemon_id":  daemonID,
	})
	second := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/same-dir",
		"daemon_id":  daemonID,
	})

	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+second.ID+"/identity", map[string]any{
		"real_path": "/private/tmp/same-dir",
	}, testWorkspaceID, daemonID)
	req = withURLParam(req, "resourceId", second.ID)
	w := testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusOK)

	var resp struct {
		Applied  bool `json:"applied"`
		Conflict bool `json:"conflict"`
	}
	w.JSON(&resp)
	if resp.Applied || !resp.Conflict {
		t.Fatalf("expected a skipped conflict, got applied=%v conflict=%v", resp.Applied, resp.Conflict)
	}

	got := getProjectResource(t, project.ID, second.ID)
	var ref localDirectoryRef
	if err := json.Unmarshal(got.ResourceRef, &ref); err != nil {
		t.Fatal(err)
	}
	if ref.RealPath != "" {
		t.Errorf("conflicting backfill must leave the second row untouched, got real_path %q", ref.RealPath)
	}
	_ = first
}

func TestBackfillLocalDirectoryIdentityRefusesAnotherDaemonsRow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 identity backfill ownership")
	created := createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/tmp/owned-elsewhere",
		"daemon_id":  "daemon-other",
	})
	req := newDaemonTokenRequest("POST", "/api/daemon/project-resources/"+created.ID+"/identity", map[string]any{
		"real_path": "/private/tmp/owned-elsewhere",
	}, testWorkspaceID, "daemon-this")
	req = withURLParam(req, "resourceId", created.ID)
	testutil.Call(t, testHandler.BackfillLocalDirectoryIdentity, req).Want(http.StatusForbidden)
}

func getProjectResource(t *testing.T, projectID, resourceID string) ProjectResourceResponse {
	t.Helper()
	w := testutil.Call(t, testHandler.ListProjectResources, withURLParam(
		newRequest("GET", "/api/projects/"+projectID+"/resources", nil), "id", projectID,
	)).Want(http.StatusOK)
	var list struct {
		Resources []ProjectResourceResponse `json:"resources"`
	}
	w.JSON(&list)
	for _, r := range list.Resources {
		if r.ID == resourceID {
			return r
		}
	}
	t.Fatalf("resource %s not in project %s", resourceID, projectID)
	return ProjectResourceResponse{}
}

func TestWorktreeRootConflictsWithAnExistingBinding(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "DENE-618 worktree_root conflict")
	const daemonID = "daemon-worktree-root-conflict"
	createLocalDirectoryResourceFor(t, project.ID, map[string]any{
		"local_path": "/Users/me/code/app",
		"daemon_id":  daemonID,
		"real_path":  "/Users/me/code/app",
	})
	w := httptestRecorderCreate(t, project.ID, map[string]any{
		"local_path":    "/Users/me/code/docs",
		"daemon_id":     daemonID,
		"real_path":     "/Users/me/code/docs",
		"worktree_root": "/Users/me/code/app",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("worktree_root pointing at an already-bound directory: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "/Users/me/code/app") {
		t.Errorf("409 body %q does not name the conflicting directory", w.Body.String())
	}
}

func httptestRecorderCreate(t *testing.T, projectID string, ref map[string]any) *testutil.Response {
	t.Helper()
	req := newRequest("POST", "/api/projects/"+projectID+"/resources", map[string]any{
		"resource_type": "local_directory",
		"resource_ref":  ref,
	})
	req = withURLParam(req, "id", projectID)
	return testutil.Call(t, testHandler.CreateProjectResource, req)
}
