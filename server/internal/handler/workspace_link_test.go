package handler

import (
	"context"
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
			r.Get("/lookup", testHandler.LookupWorkspaceLinkTarget)
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
	// A pasted workspace link names the workspace the same way the slug does,
	// on the lookup the form confirms with and on create.
	pasted := "https://ai.example.test/link-view-" + tag + "/issues"
	lookup := srcOwnerCall
	lookup.query = "target=" + pasted
	if rec := lookup.do(t, http.MethodGet, "/api/workspace-links/lookup", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"link-view"`) {
		t.Fatalf("lookup = %d %s", rec.Code, rec.Body)
	}
	lookup.query = "target=no-such-" + tag
	if rec := lookup.do(t, http.MethodGet, "/api/workspace-links/lookup", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("lookup missing = %d %s", rec.Code, rec.Body)
	}
	memberLookup := linkCaller{ws: viewer, user: vMember, query: "target=link-src-" + tag}
	if rec := memberLookup.do(t, http.MethodGet, "/api/workspace-links/lookup", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("member lookup = %d, want 403", rec.Code)
	}
	rec := srcOwnerCall.do(t, http.MethodPost, "/api/workspace-links",
		fmt.Sprintf(`{"target_slug":%q,"project_ids":["%s"]}`, pasted, project))
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

// A person who owns both workspaces picks the other one's projects from the
// viewer side; the lookup lists them and the pull lands active (DENE-1582).
func TestWorkspaceLinkPullFromTheViewerSide(t *testing.T) {
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "pull-src", "pull-src-"+tag, testutil.Cols{"issue_prefix": "PS"})
	viewer := dbfx.Workspace(t, "pull-view", "pull-view-"+tag, testutil.Cols{"issue_prefix": "PV"})
	both := dbfx.User(t, "pull both", "pull-both-"+tag+"@wl.test")
	dbfx.Member(t, source, both, "owner")
	dbfx.Member(t, viewer, both, "owner")
	project := dbfx.Project(t, "Pulled", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	dbfx.Project(t, "Hidden", testutil.Cols{"workspace_id": source, "visibility": "private"})

	call := linkCaller{ws: viewer, user: both, query: "target=pull-src-" + tag}
	rec := call.do(t, http.MethodGet, "/api/workspace-links/lookup", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed":true`) ||
		!strings.Contains(rec.Body.String(), `"title":"Pulled"`) || strings.Contains(rec.Body.String(), "Hidden") {
		t.Fatalf("lookup = %d %s", rec.Code, rec.Body)
	}
	call.query = ""
	if rec := call.do(t, http.MethodPost, "/api/workspace-links",
		fmt.Sprintf(`{"target_slug":"pull-src-%s","project_ids":["%s"],"direction":"sideways"}`, tag, project)); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown direction = %d, want 400", rec.Code)
	}
	rec = call.do(t, http.MethodPost, "/api/workspace-links",
		fmt.Sprintf(`{"target_slug":"pull-src-%s","project_ids":["%s"],"direction":"pull"}`, tag, project))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"status":"active"`) || !strings.Contains(rec.Body.String(), `"side":"viewer"`) {
		t.Fatalf("pull = %d %s", rec.Code, rec.Body)
	}
	// Both decisions were the caller's own: nobody is asked.
	if n := linkNotices(t, viewer, both, "workspace_link_request", false); n != 0 {
		t.Fatalf("pull left %d request notices", n)
	}
}

// linkNotices counts the link notices of one type a person holds in a
// workspace's inbox, archived or not.
func linkNotices(t *testing.T, ws, user, itemType string, archived bool) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE workspace_id = $1 AND recipient_id = $2 AND type = $3 AND archived = $4 AND issue_id IS NULL`,
		ws, user, itemType, archived).Scan(&n); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	return n
}

// An offer asks every manager of the viewer; the answer goes back to the
// source's managers and closes the request everywhere (DENE-1641).
func TestWorkspaceLinkRequestNotices(t *testing.T) {
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "notice-src", "notice-src-"+tag, testutil.Cols{"issue_prefix": "NS"})
	viewer := dbfx.Workspace(t, "notice-view", "notice-view-"+tag, testutil.Cols{"issue_prefix": "NV"})
	person := func(name, ws, role string) string {
		id := dbfx.User(t, name, name+"-"+tag+"@wl.test")
		dbfx.Member(t, ws, id, role)
		return id
	}
	srcOwner := person("notice-src-owner", source, "owner")
	srcAdmin := person("notice-src-admin", source, "admin")
	vOwner := person("notice-v-owner", viewer, "owner")
	vAdmin := person("notice-v-admin", viewer, "admin")
	vMember := person("notice-v-member", viewer, "member")
	project := dbfx.Project(t, "Shared roadmap", testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	runtime := dbfx.Runtime(t, "notice-rt-"+tag, testutil.Cols{"workspace_id": viewer, "owner_id": vOwner})
	agent := dbfx.Agent(t, "notice-agent-"+tag, runtime, testutil.Cols{"workspace_id": viewer, "owner_id": vOwner})

	offer := func() string {
		t.Helper()
		rec := (linkCaller{ws: source, user: srcOwner}).do(t, http.MethodPost, "/api/workspace-links",
			fmt.Sprintf(`{"target_slug":"notice-view-%s","project_ids":["%s"]}`, tag, project))
		if rec.Code != http.StatusCreated {
			t.Fatalf("offer = %d %s", rec.Code, rec.Body)
		}
		return decodeField(t, rec, "id")
	}

	linkID := offer()
	for _, who := range []string{vOwner, vAdmin} {
		if n := linkNotices(t, viewer, who, "workspace_link_request", false); n != 1 {
			t.Fatalf("manager %s holds %d request notices, want 1", who, n)
		}
	}
	if n := linkNotices(t, viewer, vMember, "workspace_link_request", false); n != 0 {
		t.Fatalf("member holds %d request notices", n)
	}
	var details string
	if err := testPool.QueryRow(context.Background(), `
		SELECT details::text FROM inbox_item WHERE recipient_id = $1 AND type = 'workspace_link_request'`, vOwner).Scan(&details); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{linkID, "notice-src-" + tag, "Shared roadmap"} {
		if !strings.Contains(details, want) {
			t.Fatalf("request details %s miss %q", details, want)
		}
	}

	// An owner's agent lists what waits for its person; it still cannot accept.
	agentCall := linkCaller{ws: viewer, user: vOwner, agentID: agent}
	if rec := agentCall.do(t, http.MethodGet, "/api/workspace-links", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), linkID) || !strings.Contains(rec.Body.String(), `"status":"pending"`) {
		t.Fatalf("agent list = %d %s", rec.Code, rec.Body)
	}
	if rec := (linkCaller{ws: viewer, user: vMember}).do(t, http.MethodGet, "/api/workspace-links", ""); strings.Contains(rec.Body.String(), linkID) {
		t.Fatalf("member sees the pending offer: %s", rec.Body)
	}
	if rec := agentCall.do(t, http.MethodPatch, "/api/workspace-links/"+linkID, `{"accept":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent accept = %d, want 403", rec.Code)
	}

	// Accept: the request closes for both managers, the source hears back.
	if rec := (linkCaller{ws: viewer, user: vAdmin}).do(t, http.MethodPatch, "/api/workspace-links/"+linkID, `{"accept":true}`); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body)
	}
	for _, who := range []string{vOwner, vAdmin} {
		if n := linkNotices(t, viewer, who, "workspace_link_request", false); n != 0 {
			t.Fatalf("request still open for %s after accept", who)
		}
	}
	for _, who := range []string{srcOwner, srcAdmin} {
		if n := linkNotices(t, source, who, "workspace_link_accepted", false); n != 1 {
			t.Fatalf("source manager %s holds %d accepted receipts, want 1", who, n)
		}
	}

	// Withdraw: the source takes back a pending offer, nobody gets a receipt.
	if rec := (linkCaller{ws: source, user: srcOwner}).do(t, http.MethodDelete, "/api/workspace-links/"+linkID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke active = %d", rec.Code)
	}
	linkID = offer()
	if rec := (linkCaller{ws: source, user: srcOwner}).do(t, http.MethodDelete, "/api/workspace-links/"+linkID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("withdraw = %d", rec.Code)
	}
	if n := linkNotices(t, viewer, vOwner, "workspace_link_request", false); n != 0 {
		t.Fatalf("withdrawn request still open")
	}
	if n := linkNotices(t, source, srcAdmin, "workspace_link_declined", false); n != 0 {
		t.Fatalf("withdraw sent a decline receipt")
	}

	// Decline: the viewer turns a pending offer down.
	linkID = offer()
	if rec := (linkCaller{ws: viewer, user: vOwner}).do(t, http.MethodDelete, "/api/workspace-links/"+linkID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("decline = %d", rec.Code)
	}
	if n := linkNotices(t, viewer, vAdmin, "workspace_link_request", false); n != 0 {
		t.Fatalf("declined request still open")
	}
	for _, who := range []string{srcOwner, srcAdmin} {
		if n := linkNotices(t, source, who, "workspace_link_declined", false); n != 1 {
			t.Fatalf("source manager %s holds %d declined receipts, want 1", who, n)
		}
	}
}
