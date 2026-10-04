package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// workspaceLinkRouter mirrors the production wiring: everything sits behind
// RequireWorkspaceMember, which scopes the caller to the header's workspace.
func workspaceLinkRouter() http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
		r.Route("/api/workspace-links", func(r chi.Router) {
			r.Get("/", testHandler.ListWorkspaceLinks)
			r.Post("/", testHandler.CreateWorkspaceLink)
			r.Patch("/{id}", testHandler.UpdateWorkspaceLink)
			r.Delete("/{id}", testHandler.RevokeWorkspaceLink)
			r.Get("/{id}/view", testHandler.GetWorkspaceLinkView)
		})
		r.Get("/api/issues", testHandler.ListIssues)
		r.Get("/api/projects", testHandler.ListProjects)
	})
	return r
}

type linkCaller struct {
	ws, user string
	agentID  string // set: the request carries a task token
	query    string
}

func (c linkCaller) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	if c.query != "" {
		path += "?" + c.query
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Workspace-ID", c.ws)
	req.Header.Set("X-User-ID", c.user)
	if c.agentID != "" {
		// What the auth middleware stamps for a mat_ task token: the
		// token's own workspace in X-Workspace-ID, the runtime owner as
		// the user, and the server-set actor source.
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", c.agentID)
	}
	rec := httptest.NewRecorder()
	workspaceLinkRouter().ServeHTTP(rec, req)
	return rec
}

func TestWorkspaceLinkRoutesStayInsideTheCallersWorkspace(t *testing.T) {
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "link-src", "link-src-"+tag, testutil.Cols{"issue_prefix": "LS"})
	viewer := dbfx.Workspace(t, "link-view", "link-view-"+tag, testutil.Cols{"issue_prefix": "LV"})
	srcOwner := dbfx.User(t, "link src owner", "link-src-owner-"+tag+"@wl.test")
	dbfx.Member(t, source, srcOwner, "owner")
	vOwner := dbfx.User(t, "link view owner", "link-view-owner-"+tag+"@wl.test")
	dbfx.Member(t, viewer, vOwner, "owner")
	vMember := dbfx.User(t, "link view member", "link-view-member-"+tag+"@wl.test")
	dbfx.Member(t, viewer, vMember, "member")
	project := dbfx.Project(t, "Linked", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	dbfx.Issue(t, "linked issue", testutil.Cols{
		"workspace_id": source, "project_id": project, "creator_id": srcOwner, "visibility": "workspace",
	})
	runtime := dbfx.Runtime(t, "link-rt-"+tag, testutil.Cols{"workspace_id": viewer, "owner_id": vMember})
	agent := dbfx.Agent(t, "link-agent-"+tag, runtime, testutil.Cols{"workspace_id": viewer, "owner_id": vMember})

	srcOwnerCall := linkCaller{ws: source, user: srcOwner}
	rec := srcOwnerCall.do(t, http.MethodPost, "/api/workspace-links",
		fmt.Sprintf(`{"target_slug":"link-view-%s","project_ids":["%s"]}`, tag, project))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	linkID := decodeField(t, rec, "id")
	viewPath := "/api/workspace-links/" + linkID + "/view"

	member := linkCaller{ws: viewer, user: vMember}
	if rec := member.do(t, http.MethodGet, viewPath, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("pending view = %d, want 404", rec.Code)
	}
	if rec := (linkCaller{ws: viewer, user: vOwner}).do(t, http.MethodPatch, "/api/workspace-links/"+linkID, `{"accept":true}`); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body)
	}

	rec = member.do(t, http.MethodGet, viewPath, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "linked issue") {
		t.Fatalf("member view = %d %s", rec.Code, rec.Body)
	}

	// The viewer's people are not the source's members: the source's
	// header gets them nowhere, on the link route or any existing one.
	asSource := linkCaller{ws: source, user: vMember}
	for _, path := range []string{viewPath, "/api/issues", "/api/projects", "/api/workspace-links"} {
		if rec := asSource.do(t, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("viewer member with source header GET %s = %d, want 404", path, rec.Code)
		}
	}

	// An agent's task token reads the view in its own workspace…
	agentCall := linkCaller{ws: viewer, user: vMember, agentID: agent}
	if rec := agentCall.do(t, http.MethodGet, viewPath, ""); rec.Code != http.StatusOK {
		t.Fatalf("agent view = %d %s", rec.Code, rec.Body)
	}
	// …cannot widen to the source by naming it…
	widened := agentCall
	widened.query = "workspace_slug=link-src-" + tag
	if rec := widened.do(t, http.MethodGet, "/api/issues", ""); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "linked issue") {
		t.Fatalf("agent widened into the source: %s", rec.Body)
	}
	// …is refused when its bound workspace is the source…
	if rec := (linkCaller{ws: source, user: vMember, agentID: agent}).do(t, http.MethodGet, viewPath, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("agent with source workspace = %d, want 404", rec.Code)
	}
	// …and never manages a link.
	if rec := agentCall.do(t, http.MethodDelete, "/api/workspace-links/"+linkID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("agent revoke = %d, want 403", rec.Code)
	}

	// Revoke: the very next read is the same 404.
	if rec := (linkCaller{ws: viewer, user: vOwner}).do(t, http.MethodDelete, "/api/workspace-links/"+linkID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	rec = member.do(t, http.MethodGet, viewPath, "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "link not found") {
		t.Fatalf("view after revoke = %d %s", rec.Code, rec.Body)
	}
}

func decodeField(t *testing.T, rec *httptest.ResponseRecorder, key string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, _ := body[key].(string)
	if v == "" {
		t.Fatalf("response has no %q: %s", key, rec.Body)
	}
	return v
}
