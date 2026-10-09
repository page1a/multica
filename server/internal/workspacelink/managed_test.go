package workspacelink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// managedRun is an agent of the viewer and a live run of it started by
// originator.
type managedRun struct {
	agent, task pgtype.UUID
}

func (w *world) run(t *testing.T, originator pgtype.UUID, status string) managedRun {
	t.Helper()
	fx := testutil.New(w.fx.Pool, util.UUIDToString(w.viewer), util.UUIDToString(w.vOwner))
	name := "viewer-agent"
	if n := fx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1`, w.viewer); n > 0 {
		name += "-" + itoa(n)
	}
	runtime := fx.Runtime(t, name)
	agent := fx.Agent(t, name, runtime)
	task := fx.Task(t, agent, testutil.Cols{"status": status, "runtime_id": runtime, "originator_user_id": util.UUIDToString(originator), "accountable_user_id": util.UUIDToString(originator)})
	return managedRun{agent: uid(t, agent), task: uid(t, task)}
}

func (w *world) setManaged(t *testing.T, link pgtype.UUID, on bool) {
	t.Helper()
	if _, err := w.svc.Update(context.Background(), w.source, w.srcOwner, owner(), link, Patch{Managed: &on}); err != nil {
		t.Fatalf("set managed %v: %v", on, err)
	}
}

func (w *world) call(r managedRun, source string) ManagedCall {
	return ManagedCall{ViewerWS: w.viewer, AgentID: r.agent, TaskID: r.task, Source: source}
}

// TestSetManagedRules: only the source owner switches it; someone who owns
// the source and manages the viewer may switch it from the viewer side, and
// both sides get an audit row.
func TestSetManagedRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	link := w.link(t, w.shared)
	on := true

	_, err := w.svc.Update(ctx, w.source, w.srcAdmin, admin(), link, Patch{Managed: &on})
	wantStatus(t, err, 403)
	_, err = w.svc.Update(ctx, w.source, w.srcOwner, Actor{Role: permission.RoleOwner, IsAgent: true}, link, Patch{Managed: &on})
	wantStatus(t, err, 403)
	_, err = w.svc.Update(ctx, w.viewer, w.vOwner, owner(), link, Patch{Managed: &on})
	wantStatus(t, err, 403)
	_, err = w.svc.Update(ctx, w.other, w.otherOwner, owner(), link, Patch{Managed: &on})
	wantStatus(t, err, 404)
	_, err = w.svc.Update(ctx, w.source, w.srcOwner, owner(), link, Patch{Managed: &on, Accept: true})
	wantStatus(t, err, 400)

	l, err := w.svc.Update(ctx, w.source, w.srcOwner, owner(), link, Patch{Managed: &on})
	if err != nil || !l.Managed {
		t.Fatalf("owner set managed = %+v, %v", l, err)
	}
	if links, _ := w.svc.List(ctx, w.viewer, member()); len(links) != 1 || !links[0].Managed || links[0].Source.ID != util.UUIDToString(w.source) {
		t.Fatalf("viewer list = %+v, want managed with the source id", links)
	}

	// Owner of the source who is admin here switches it off from the viewer.
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vAdmin), "owner")
	off := false
	if l, err := w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), link, Patch{Managed: &off}); err != nil || l.Managed {
		t.Fatalf("viewer-side switch = %+v, %v", l, err)
	}
	if n := w.fx.Count(t, `SELECT count(*) FROM workspace_link_audit WHERE link_id = $1 AND action = 'set_managed'`, link); n != 3 {
		t.Fatalf("set_managed audit rows = %d, want 3 (one from the source, two from the viewer-side switch)", n)
	}
}

// TestAuthorizeManaged pins every refusal: managed off, revoked, an
// originator who is not a manager there, a finished run, someone else's
// run, an unknown workspace. All are the same ErrManagedDenied.
func TestAuthorizeManaged(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	link := w.link(t, w.shared)
	// The originator: an admin of the source who also works in the viewer.
	w.fx.Member(t, util.UUIDToString(w.viewer), util.UUIDToString(w.srcAdmin), "member")
	live := w.run(t, w.srcAdmin, "running")

	deny := func(name string, call ManagedCall) {
		t.Helper()
		if _, err := w.svc.AuthorizeManaged(ctx, call); err != ErrManagedDenied {
			t.Errorf("%s: err = %v, want ErrManagedDenied", name, err)
		}
	}

	deny("managed off", w.call(live, w.sourceSlug))
	w.setManaged(t, link, true)

	g, err := w.svc.AuthorizeManaged(ctx, w.call(live, w.sourceSlug))
	if err != nil {
		t.Fatalf("managed on: %v", err)
	}
	if g.Originator != w.srcAdmin || g.Source.ID != w.source || g.Agent.ID != live.agent {
		t.Fatalf("grant = %+v", g)
	}
	if _, err := w.svc.AuthorizeManaged(ctx, w.call(live, util.UUIDToString(w.source))); err != nil {
		t.Fatalf("source by id: %v", err)
	}

	deny("unknown workspace", w.call(live, "no-such-workspace"))
	deny("own workspace", w.call(live, w.viewerSlug))
	deny("unlinked workspace", w.call(live, util.UUIDToString(w.source2)))
	deny("originator not in source", w.call(w.run(t, w.vMember, "running"), w.sourceSlug))
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vGuest), "guest")
	deny("originator a guest in source", w.call(w.run(t, w.vGuest, "running"), w.sourceSlug))
	deny("finished run", w.call(w.run(t, w.srcAdmin, "completed"), w.sourceSlug))
	other := w.run(t, w.srcAdmin, "running")
	deny("someone else's task", ManagedCall{ViewerWS: w.viewer, AgentID: live.agent, TaskID: other.task, Source: w.sourceSlug})
	deny("other viewer", ManagedCall{ViewerWS: w.other, AgentID: live.agent, TaskID: live.task, Source: w.sourceSlug})

	w.setManaged(t, link, false)
	deny("switched off again", w.call(live, w.sourceSlug))
	w.setManaged(t, link, true)
	if err := w.svc.Revoke(ctx, w.source, w.srcOwner, owner(), link); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	deny("revoked", w.call(live, w.sourceSlug))
}

// seen is what reached the handler behind the gate.
type seen struct {
	called                                  bool
	user, ws, source, agent, task, wsQuery  string
	slugHeader, linkedHeader, workspaceSlug string
}

func gateRequest(t *testing.T, svc *Service, method, target string, headers map[string]string, status int, body string) (*httptest.ResponseRecorder, *seen) {
	t.Helper()
	s := &seen{}
	h := svc.Managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.called = true
		s.user, s.ws, s.source = r.Header.Get("X-User-ID"), r.Header.Get("X-Workspace-ID"), r.Header.Get("X-Actor-Source")
		s.agent, s.task = r.Header.Get("X-Agent-ID"), r.Header.Get("X-Task-ID")
		s.slugHeader, s.linkedHeader = r.Header.Get("X-Workspace-Slug"), r.Header.Get(LinkedWorkspaceHeader)
		s.wsQuery, s.workspaceSlug = r.URL.Query().Get("workspace_id"), r.URL.Query().Get("workspace_slug")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, s
}

// TestManagedGate drives the HTTP gate: it carries a whitelisted request as
// the originator into the source, refuses everything else the same way, and
// leaves the trail of every write.
func TestManagedGate(t *testing.T) {
	w := newWorld(t)
	link := w.link(t, w.shared)
	w.setManaged(t, link, true)
	live := w.run(t, w.srcAdmin, "running")
	sourceID := util.UUIDToString(w.source)
	issueID := w.fx.Issue(t, "managed-target", testutil.Cols{
		"workspace_id": sourceID, "creator_id": util.UUIDToString(w.srcOwner), "visibility": "workspace",
	})
	number := w.fx.Count(t, `SELECT number FROM issue WHERE id = $1`, issueID)

	token := map[string]string{
		"X-Actor-Source":      "task_token",
		"X-User-ID":           util.UUIDToString(w.vOwner), // the runtime owner
		"X-Workspace-ID":      util.UUIDToString(w.viewer),
		"X-Agent-ID":          util.UUIDToString(live.agent),
		"X-Task-ID":           util.UUIDToString(live.task),
		"X-Workspace-Slug":    w.viewerSlug,
		LinkedWorkspaceHeader: w.sourceSlug,
	}

	// A whitelisted write continues as the originator's own request.
	rec, s := gateRequest(t, w.svc, "PUT", "/api/issues/WL-"+itoa(number)+"?workspace_slug=x&workspace_id=y", token, 200, `{}`)
	if rec.Code != 200 || !s.called {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if s.user != util.UUIDToString(w.srcAdmin) || s.ws != sourceID || s.source != ManagedActorSource ||
		s.agent != "" || s.task != "" || s.slugHeader != "" || s.linkedHeader != "" || s.workspaceSlug != "" || s.wsQuery != sourceID {
		t.Fatalf("handler saw %+v", s)
	}
	var detail struct {
		Route, ViaWorkspace, AgentName string
	}
	row := w.fx.QueryRow(t, `SELECT details->>'route', details->>'via_workspace', details->>'agent_name' FROM activity_log
		WHERE issue_id = $1 AND action = 'linked_write' AND actor_type = 'member' AND actor_id = $2`, issueID, w.srcAdmin)
	row.Scan(&detail.Route, &detail.ViaWorkspace, &detail.AgentName)
	if detail.Route != "issue.update" || detail.ViaWorkspace != "viewer" || detail.AgentName != "viewer-agent" {
		t.Fatalf("activity detail = %+v", detail)
	}

	// An issue create is traced through the id in its response.
	created := w.fx.Issue(t, "created-through-link", testutil.Cols{"workspace_id": sourceID, "creator_id": util.UUIDToString(w.srcAdmin)})
	body, _ := json.Marshal(map[string]string{"id": created})
	if rec, _ := gateRequest(t, w.svc, "POST", "/api/issues", token, 201, string(body)); rec.Code != 201 {
		t.Fatalf("create: %d", rec.Code)
	}
	if n := w.fx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = 'linked_write'`, created); n != 1 {
		t.Fatalf("create activity rows = %d", n)
	}

	// Reads leave no trail; autopilot writes leave an audit row only.
	if rec, _ := gateRequest(t, w.svc, "GET", "/api/autopilots", token, 200, `{}`); rec.Code != 200 {
		t.Fatalf("autopilot list: %d", rec.Code)
	}
	if rec, _ := gateRequest(t, w.svc, "PATCH", "/api/autopilots/ap-1/triggers/tr-1", token, 200, `{}`); rec.Code != 200 {
		t.Fatalf("trigger update: %d", rec.Code)
	}
	// A refused write leaves nothing.
	if rec, _ := gateRequest(t, w.svc, "POST", "/api/autopilots/ap-1/trigger", token, 403, `{}`); rec.Code != 403 {
		t.Fatalf("refused run: %d", rec.Code)
	}
	if n := w.fx.Count(t, `SELECT count(*) FROM workspace_link_audit WHERE link_id = $1 AND action = 'managed_write'`, link); n != 3 {
		t.Fatalf("managed_write audit rows = %d, want 3 (update, create, trigger update)", n)
	}
	if n := w.fx.Count(t, `SELECT count(*) FROM workspace_link_audit WHERE link_id = $1 AND action = 'managed_write'
		AND workspace_id = $2 AND actor_id = $3 AND detail->>'autopilot_id' = 'ap-1'`, link, w.viewer, w.srcAdmin); n != 1 {
		t.Fatalf("autopilot audit row missing its provenance")
	}

	// Outside the whitelist: refused before anything runs.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/workspace-links"},
		{"PATCH", "/api/workspace-links/" + util.UUIDToString(link)},
		{"POST", "/api/autopilots/ap-1/triggers/tr-1/rotate-webhook-token"},
		{"POST", "/api/autopilots/ap-1/collaborators"},
		{"DELETE", "/api/issues/" + issueID},
		{"GET", "/api/agents/x"},
		{"PATCH", "/api/workspaces/" + sourceID},
		{"GET", "/api/runtimes"},
	} {
		rec, s := gateRequest(t, w.svc, c.method, c.path, token, 200, `{}`)
		if rec.Code != 403 || s.called || !strings.Contains(rec.Body.String(), "not available on a linked workspace") {
			t.Errorf("%s %s: %d %s (called=%v)", c.method, c.path, rec.Code, rec.Body, s.called)
		}
	}

	// A workspace in the path must be the source.
	if rec, s := gateRequest(t, w.svc, "GET", "/api/workspaces/"+util.UUIDToString(w.other)+"/members", token, 200, `{}`); rec.Code != 403 || s.called {
		t.Fatalf("other workspace members: %d", rec.Code)
	}
	if rec, _ := gateRequest(t, w.svc, "GET", "/api/workspaces/"+sourceID+"/members", token, 200, `{}`); rec.Code != 200 {
		t.Fatalf("source members lookup: %d", rec.Code)
	}

	// Switched off: the next call is refused.
	w.setManaged(t, link, false)
	if rec, s := gateRequest(t, w.svc, "GET", "/api/issues", token, 200, `{}`); rec.Code != 403 || s.called {
		t.Fatalf("after switch-off: %d", rec.Code)
	}

	// Without the header, or for a person, the gate is invisible.
	plain := map[string]string{}
	for k, v := range token {
		plain[k] = v
	}
	delete(plain, LinkedWorkspaceHeader)
	if _, s := gateRequest(t, w.svc, "DELETE", "/api/issues/x", plain, 200, `{}`); !s.called || s.source != "task_token" || s.ws != util.UUIDToString(w.viewer) {
		t.Fatalf("plain task token touched: %+v", s)
	}
	person := map[string]string{"X-User-ID": util.UUIDToString(w.srcOwner), "X-Workspace-ID": sourceID, LinkedWorkspaceHeader: w.sourceSlug}
	if _, s := gateRequest(t, w.svc, "DELETE", "/api/issues/x", person, 200, `{}`); !s.called || s.user != util.UUIDToString(w.srcOwner) || s.linkedHeader != "" {
		t.Fatalf("person request touched: %+v", s)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
