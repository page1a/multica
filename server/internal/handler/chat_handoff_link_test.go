package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/testutil"
)

type chatLinkFixture struct {
	target, slug, session, originator, task string
}

func newChatLinkFixture(t *testing.T, visibility string) chatLinkFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("test database not available")
	}
	slug := "chat-link-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	if len(slug) > 55 {
		slug = slug[:55]
	}
	target := dbfx.Workspace(t, "Chat link target", slug, testutil.Cols{"issue_prefix": "CL"})
	owner := dbfx.User(t, "Chat link owner", slug+"-owner@example.test")
	dbfx.Member(t, target, owner, "owner")
	originator := dbfx.User(t, "Chat link reader", slug+"-reader@example.test")
	dbfx.Member(t, testWorkspaceID, originator, "member")
	dbfx.Member(t, target, originator, "member")
	runtime := dbfx.Runtime(t, "chat-link-runtime", testutil.Cols{"workspace_id": testWorkspaceID})
	agent := dbfx.Agent(t, "chat-link-agent", runtime, testutil.Cols{"workspace_id": testWorkspaceID})
	task := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "status": "running", "originator_user_id": originator,
		"accountable_user_id": originator, "originator_source": "direct_human",
	})
	targetRuntime := dbfx.Runtime(t, "chat-link-target-runtime", testutil.Cols{"workspace_id": target})
	targetAgent := dbfx.Agent(t, "chat-link-target-agent", targetRuntime, testutil.Cols{"workspace_id": target, "owner_id": owner})
	session := dbfx.Insert(t, "chat_session", testutil.Cols{
		"workspace_id": target, "agent_id": targetAgent, "creator_id": owner, "title": "linked chat", "status": "active",
		"visibility": visibility, "explicitly_created_at": testutil.Raw("now()"),
	})
	return chatLinkFixture{target: target, slug: slug, session: session, originator: originator, task: task}
}

func (f chatLinkFixture) request(t *testing.T) *http.Request {
	t.Helper()
	r := newRequest(http.MethodGet, "/api/chat/links/"+f.slug+"/sessions/"+f.session+"/handoff", nil)
	r.Header.Set("X-Task-ID", f.task)
	r.Header.Set("X-Agent-ID", "")
	r = withChatTestWorkspaceCtx(t, r)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("workspaceSlug", f.slug)
	rctx.URLParams.Add("sessionId", f.session)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	return r
}

func invokeChatLink(t *testing.T, f chatLinkFixture) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.GetChatSessionLinkRead(w, f.request(t))
	return w
}

func TestChatSessionLinkRead_PrivateDenied(t *testing.T) {
	f := newChatLinkFixture(t, "private")
	if w := invokeChatLink(t, f); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSessionLinkRead_ProjectMemberAndNamedShare(t *testing.T) {
	f := newChatLinkFixture(t, "project")
	project := dbfx.Project(t, "Chat link project", testutil.Cols{"workspace_id": f.target})
	dbfx.Exec(t, `UPDATE chat_session SET project_id = $1 WHERE id = $2`, project, f.session)
	dbfx.Exec(t, `INSERT INTO project_member (workspace_id, project_id, member_id) VALUES ($1,$2,$3)`, f.target, project, f.originator)
	if w := invokeChatLink(t, f); w.Code != http.StatusOK {
		t.Fatalf("project member status = %d, body=%s", w.Code, w.Body.String())
	}
	dbfx.Exec(t, `DELETE FROM project_member WHERE project_id = $1 AND member_id = $2`, project, f.originator)
	dbfx.Exec(t, `INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, added_by, access) VALUES ($1,'chat_session',$2,$3,$4,'view')`, f.target, f.session, f.originator, f.originator)
	if w := invokeChatLink(t, f); w.Code != http.StatusOK {
		t.Fatalf("named share status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSessionLinkRead_NonDirectHumanDenied(t *testing.T) {
	f := newChatLinkFixture(t, "workspace")
	dbfx.Exec(t, `UPDATE agent_task_queue SET originator_source = 'delegation' WHERE id = $1`, f.task)
	if w := invokeChatLink(t, f); w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSessionLinkRead_AuditsCrossWorkspaceRead(t *testing.T) {
	f := newChatLinkFixture(t, "workspace")
	if w := invokeChatLink(t, f); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session_link_read_audit WHERE chat_session_id = $1 AND reader_task_id = $2`, f.session, f.task).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("audit rows = %d, want 1", count)
	}
}
