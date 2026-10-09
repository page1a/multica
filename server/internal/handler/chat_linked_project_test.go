package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workspacelink"
)

// A chat attaches another workspace's shared project read-only (DENE-1643):
// the link is re-checked on every write, list and claim, the project never
// becomes the chat's own, and a revoke stops it at the next run while the
// chat still names it as stale.
func TestChatLinkedProjects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "Acme", "chat-link-src-"+tag, testutil.Cols{"issue_prefix": "CL"})
	srcOwner := dbfx.User(t, "chat link src owner", "chat-link-src-"+tag+"@wl.test")
	dbfx.Member(t, source, srcOwner, "owner")
	shared := dbfx.Project(t, "Shared brief", testutil.Cols{
		"workspace_id": source, "visibility": "workspace", "description": "Read the shared design notes.",
	})
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": shared, "workspace_id": source, "resource_type": "github_repo",
		"resource_ref": testutil.Raw(`'{"url":"https://github.com/acme/shared"}'::jsonb`), "position": 0,
	})
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": shared, "workspace_id": source, "resource_type": "local_directory",
		"resource_ref": testutil.Raw(`'{"local_path":"/nowhere/shared","daemon_id":"d-secret"}'::jsonb`), "position": 1,
	})

	var viewerSlug string
	dbfx.QueryRow(t, `SELECT slug FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&viewerSlug)
	links := testHandler.workspaceLinks()
	owner := workspacelink.Actor{Role: permission.RoleOwner}
	link, err := links.Create(ctx, util.MustParseUUID(source), util.MustParseUUID(srcOwner), owner, viewerSlug, []pgtype.UUID{util.MustParseUUID(shared)})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	linkID := util.MustParseUUID(link.ID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace_link_project WHERE link_id = $1`, link.ID)
		testPool.Exec(context.Background(), `DELETE FROM workspace_link WHERE id = $1`, link.ID)
	})
	if _, err := links.Update(ctx, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(testUserID), owner, linkID, workspacelink.Patch{Accept: true}); err != nil {
		t.Fatalf("accept link: %v", err)
	}

	// The menu lists it with its source workspace.
	w := httptest.NewRecorder()
	testHandler.ListChatLinkedProjectOptions(w, withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/workspace-links/projects", nil)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), shared) || !strings.Contains(w.Body.String(), `"name":"Acme"`) {
		t.Fatalf("options = %d %s", w.Code, w.Body)
	}

	agentID := createHandlerTestAgent(t, "ChatLinkedProjectAgent", []byte("[]"))
	create := func(refs []map[string]string) (*httptest.ResponseRecorder, ChatSessionResponse) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.CreateChatSession(w, withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
			"agent_id": agentID, "title": "linked", "linked_projects": refs,
		})))
		var resp ChatSessionResponse
		if w.Code == http.StatusCreated || w.Code == http.StatusOK {
			json.NewDecoder(w.Body).Decode(&resp)
			t.Cleanup(func() {
				testPool.Exec(context.Background(), `DELETE FROM chat_session_linked_project WHERE chat_session_id = $1`, resp.ID)
				testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, resp.ID)
				testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, resp.ID)
			})
		}
		return w, resp
	}

	// A project the link does not share is refused outright.
	if w, _ := create([]map[string]string{{"link_id": link.ID, "project_id": uuid.NewString()}}); w.Code != http.StatusNotFound {
		t.Fatalf("unshared project = %d %s, want 404", w.Code, w.Body)
	}

	w, session := create([]map[string]string{{"link_id": link.ID, "project_id": shared}})
	if session.ID == "" {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	if len(session.ProjectIDs) != 0 || session.ProjectID != nil {
		t.Fatalf("a linked project must not become the chat's project: %v %v", session.ProjectIDs, session.ProjectID)
	}
	if len(session.LinkedProjects) != 1 || !session.LinkedProjects[0].Available || session.LinkedProjects[0].SourceName != "Acme" {
		t.Fatalf("linked_projects = %+v", session.LinkedProjects)
	}

	runtimeID := handlerTestRuntimeID(t)
	claim := func() (string, []TaskLinkedProjectData) {
		t.Helper()
		dbfx.Exec(t, `INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1, 'user', 'read it')`, session.ID)
		var taskID string
		dbfx.QueryRow(t, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id)
			VALUES ($1, $2, 'queued', 1000, $3) RETURNING id`, agentID, runtimeID, session.ID).Scan(&taskID)
		w := httptest.NewRecorder()
		req := withURLParam(newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/claim", nil, testWorkspaceID, "chat-linked-test"), "runtimeId", runtimeID)
		testHandler.ClaimTaskByRuntime(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("claim = %d %s", w.Code, w.Body)
		}
		var out struct {
			Task *struct {
				ID             string                  `json:"id"`
				ProjectID      string                  `json:"project_id"`
				LinkedProjects []TaskLinkedProjectData `json:"linked_projects"`
			} `json:"task"`
		}
		json.NewDecoder(w.Body).Decode(&out)
		if out.Task == nil || out.Task.ID != taskID {
			t.Fatalf("claimed %+v, want %s", out.Task, taskID)
		}
		dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, taskID)
		return out.Task.ProjectID, out.Task.LinkedProjects
	}

	projectID, linked := claim()
	if projectID != "" {
		t.Fatalf("claim project_id = %q, the linked project must stay a reference", projectID)
	}
	if len(linked) != 1 || linked[0].Description != "Read the shared design notes." || linked[0].SourceName != "Acme" {
		t.Fatalf("claim linked_projects = %+v", linked)
	}
	resources, _ := json.Marshal(linked[0].Resources)
	if !strings.Contains(string(resources), "https://github.com/acme/shared") || !strings.Contains(string(resources), "/nowhere/shared") || strings.Contains(string(resources), "d-secret") {
		t.Fatalf("claim resources = %s", resources)
	}

	// Revoke: the next run gets nothing, the chat still names it as stale.
	if err := links.Revoke(ctx, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(testUserID), owner, linkID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, linked := claim(); len(linked) != 0 {
		t.Fatalf("claim after revoke = %+v, want none", linked)
	}
	hydrated := []ChatSessionResponse{{ID: session.ID}}
	if err := testHandler.hydrateChatSessionProjectIDs(ctx, hydrated); err != nil {
		t.Fatal(err)
	}
	if got := hydrated[0].LinkedProjects; len(got) != 1 || got[0].Available || got[0].Title != "Shared brief" {
		t.Fatalf("after revoke linked_projects = %+v, want one stale entry", got)
	}
}

// Stale entries stay editable (DENE-1643 review F1): a chat drops them one at
// a time and adds or removes live ones around them, while a ref the chat does
// not already carry is still checked strictly.
func TestChatLinkedProjectsEditAroundStale(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tag := uuid.NewString()[:8]
	source := dbfx.Workspace(t, "Acme", "chat-link-stale-"+tag, testutil.Cols{"issue_prefix": "CS"})
	srcOwner := dbfx.User(t, "chat link stale owner", "chat-link-stale-"+tag+"@wl.test")
	dbfx.Member(t, source, srcOwner, "owner")
	project := func(title string) string {
		return dbfx.Project(t, title, testutil.Cols{"workspace_id": source, "visibility": "workspace"})
	}
	a, b, c := project("Alpha"), project("Beta"), project("Gamma")
	uuids := func(ids ...string) *[]pgtype.UUID {
		out := make([]pgtype.UUID, len(ids))
		for i, id := range ids {
			out[i] = util.MustParseUUID(id)
		}
		return &out
	}

	var viewerSlug string
	dbfx.QueryRow(t, `SELECT slug FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&viewerSlug)
	links := testHandler.workspaceLinks()
	owner := workspacelink.Actor{Role: permission.RoleOwner}
	link, err := links.Create(ctx, util.MustParseUUID(source), util.MustParseUUID(srcOwner), owner, viewerSlug, *uuids(a, b, c))
	if err != nil {
		t.Fatalf("create link: %v", err)
	}
	linkID := util.MustParseUUID(link.ID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace_link_project WHERE link_id = $1`, link.ID)
		testPool.Exec(context.Background(), `DELETE FROM workspace_link WHERE id = $1`, link.ID)
	})
	if _, err := links.Update(ctx, util.MustParseUUID(testWorkspaceID), util.MustParseUUID(testUserID), owner, linkID, workspacelink.Patch{Accept: true}); err != nil {
		t.Fatalf("accept link: %v", err)
	}

	agentID := createHandlerTestAgent(t, "ChatLinkedStaleAgent", []byte("[]"))
	ref := func(id string) map[string]string { return map[string]string{"link_id": link.ID, "project_id": id} }
	w := httptest.NewRecorder()
	testHandler.CreateChatSession(w, withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions", map[string]any{
		"agent_id": agentID, "title": "stale", "linked_projects": []map[string]string{ref(a), ref(b)},
	})))
	var session ChatSessionResponse
	json.NewDecoder(w.Body).Decode(&session)
	if session.ID == "" {
		t.Fatalf("create = %d", w.Code)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_session_linked_project WHERE chat_session_id = $1`, session.ID)
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, session.ID)
	})

	// The source unticks Alpha and Beta: both go stale, Gamma stays shared.
	if _, err := links.Update(ctx, util.MustParseUUID(source), util.MustParseUUID(srcOwner), owner, linkID, workspacelink.Patch{ProjectIDs: uuids(c)}); err != nil {
		t.Fatalf("untick: %v", err)
	}

	set := func(ids ...string) (int, []ChatLinkedProjectResponse) {
		t.Helper()
		refs := make([]map[string]string, len(ids))
		for i, id := range ids {
			refs[i] = ref(id)
		}
		w := httptest.NewRecorder()
		req := withURLParam(withChatTestWorkspaceCtx(t, newRequest(http.MethodPatch, "/api/chat/sessions/"+session.ID, map[string]any{
			"linked_projects": refs,
		})), "sessionId", session.ID)
		testHandler.UpdateChatSession(w, req)
		var resp ChatSessionResponse
		json.NewDecoder(w.Body).Decode(&resp)
		return w.Code, resp.LinkedProjects
	}
	describe := func(items []ChatLinkedProjectResponse) string {
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = item.Title
			if !item.Available {
				parts[i] += "(stale)"
			}
		}
		return strings.Join(parts, ",")
	}

	steps := []struct {
		ids  []string
		code int
		want string
	}{
		{[]string{b}, http.StatusOK, "Beta(stale)"},              // drop one stale entry, keep the other
		{[]string{b, c}, http.StatusOK, "Beta(stale),Gamma"},     // add a live one beside a stale one
		{[]string{c}, http.StatusOK, "Gamma"},                    // drop the last stale one
		{[]string{c, a}, http.StatusNotFound, ""},                // a stale ref the chat no longer carries is refused
		{[]string{c, uuid.NewString()}, http.StatusNotFound, ""}, // so is one it never carried
	}
	for i, step := range steps {
		code, got := set(step.ids...)
		if code != step.code {
			t.Fatalf("step %d: code = %d, want %d", i, code, step.code)
		}
		if step.code == http.StatusOK && describe(got) != step.want {
			t.Fatalf("step %d: linked = %s, want %s", i, describe(got), step.want)
		}
	}
}
