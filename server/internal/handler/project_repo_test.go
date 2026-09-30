package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAttachProjectRepoIdempotentAcrossProjects(t *testing.T) {
	if testPool == nil || testHandler == nil {
		t.Fatal("handler database tests require scripts/test-db.sh")
	}
	setHandlerTestWorkspaceRepos(t, nil)
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(t.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member: %v", err)
	}

	const repoURL = "https://github.com/acme/shared-docs"
	first := createTestProject(t, "repo reach one")
	second := createTestProject(t, "repo reach two")

	added := postProjectRepo(t, member, first.ID, repoURL)
	if !added.Created || !added.Registered {
		t.Fatalf("first attach created=%v registered=%v", added.Created, added.Registered)
	}
	if added.Repo.Key != "github.com/acme/shared-docs" || added.Repo.Mode != "none" || added.Repo.NextAction == nil {
		t.Fatalf("first reach = %#v", added.Repo)
	}
	switch added.Repo.NextAction.Kind {
	case "install_app", "create_app":
	default:
		t.Fatalf("next_action.kind = %s, want install_app or create_app", added.Repo.NextAction.Kind)
	}

	again := postProjectRepo(t, member, first.ID, "https://github.com/ACME/shared-docs")
	if again.Created || again.Registered || again.Resource.ID != added.Resource.ID {
		t.Fatalf("repeat attach created=%v registered=%v id=%s want id=%s", again.Created, again.Registered, again.Resource.ID, added.Resource.ID)
	}

	other := postProjectRepo(t, member, second.ID, repoURL)
	if !other.Created || other.Registered {
		t.Fatalf("second project created=%v registered=%v", other.Created, other.Registered)
	}
	if other.Resource.ID == added.Resource.ID {
		t.Fatal("two projects shared one resource row")
	}

	if got := listProjectRepoIDs(t, member, first.ID); len(got) != 1 || got[0] != added.Resource.ID {
		t.Fatalf("first project repos = %v", got)
	}
	if got := listProjectRepoIDs(t, member, second.ID); len(got) != 1 || got[0] != other.Resource.ID {
		t.Fatalf("second project repos = %v", got)
	}
	if n := workspaceRepoCount(t); n != 1 {
		t.Fatalf("workspace repos = %d, want 1", n)
	}

	req := memberRequest(t, member, http.MethodDelete, "/api/projects/"+first.ID+"/repos/"+added.Resource.ID, nil)
	req = withURLParams(req, "id", first.ID, "repoId", added.Resource.ID)
	w := httptest.NewRecorder()
	testHandler.RemoveProjectRepo(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if got := listProjectRepoIDs(t, member, first.ID); len(got) != 0 {
		t.Fatalf("first project after remove = %v", got)
	}
	if got := listProjectRepoIDs(t, member, second.ID); len(got) != 1 {
		t.Fatalf("second project after remove = %v", got)
	}
	if n := workspaceRepoCount(t); n != 1 {
		t.Fatalf("workspace repos after detach = %d, want 1", n)
	}
}

func createTestProject(t *testing.T, title string) ProjectResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/projects?workspace_id="+testWorkspaceID, map[string]any{"title": title})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	t.Cleanup(func() {
		req := newRequest(http.MethodDelete, "/api/projects/"+project.ID, nil)
		req = withURLParam(req, "id", project.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), req)
	})
	return project
}

func postProjectRepo(t *testing.T, member db.Member, projectID, repoURL string) projectRepoAttachment {
	t.Helper()
	req := memberRequest(t, member, http.MethodPost, "/api/projects/"+projectID+"/repos", map[string]any{"repo_url": repoURL})
	req = withURLParam(req, "id", projectID)
	w := httptest.NewRecorder()
	testHandler.AttachProjectRepo(w, req)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	var out projectRepoAttachment
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode attach: %v", err)
	}
	return out
}

func listProjectRepoIDs(t *testing.T, member db.Member, projectID string) []string {
	t.Helper()
	req := memberRequest(t, member, http.MethodGet, "/api/projects/"+projectID+"/repos", nil)
	req = withURLParam(req, "id", projectID)
	w := httptest.NewRecorder()
	testHandler.ListProjectRepos(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Repos []projectRepoItem `json:"repos"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	ids := make([]string, 0, len(body.Repos))
	for _, item := range body.Repos {
		ids = append(ids, item.Resource.ID)
	}
	return ids
}

func memberRequest(t *testing.T, member db.Member, method, path string, body any) *http.Request {
	t.Helper()
	req := newRequest(method, path, body)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func workspaceRepoCount(t *testing.T) int {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(t.Context(), `SELECT repos FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&raw); err != nil {
		t.Fatalf("read workspace repos: %v", err)
	}
	return len(decodeWorkspaceRepos(raw))
}
