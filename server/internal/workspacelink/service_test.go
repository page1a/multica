package workspacelink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// world is one source workspace, one viewer workspace, an unrelated
// workspace, and a second source that also links to the viewer.
type world struct {
	svc *Service
	fx  *testutil.Fixture

	source, viewer, other, source2 pgtype.UUID
	sourceSlug, viewerSlug         string

	srcOwner, srcAdmin                  pgtype.UUID
	vOwner, vAdmin, vMember, vGuest     pgtype.UUID
	otherOwner, src2Owner               pgtype.UUID
	shared, unticked, private, project2 pgtype.UUID
}

func uid(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := util.ParseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	pool := testutil.OpenTestDatabase(ctx, t)
	fx := testutil.New(pool, "", "")
	tag := uuid.NewString()[:8]
	w := &world{svc: New(db.New(pool), pool), fx: fx}

	ws := func(name string) (pgtype.UUID, string) {
		slug := fmt.Sprintf("wl-%s-%s", name, tag)
		id := fx.Workspace(t, name, slug, testutil.Cols{"issue_prefix": "WL"})
		return uid(t, id), slug
	}
	w.source, w.sourceSlug = ws("source")
	w.viewer, w.viewerSlug = ws("viewer")
	w.other, _ = ws("other")
	w.source2, _ = ws("source2")
	if err := db.New(pool).SeedIssueStatusEntries(ctx, w.source); err != nil {
		t.Fatalf("seed statuses: %v", err)
	}

	person := func(name string, wsID pgtype.UUID, role string) pgtype.UUID {
		id := fx.User(t, name, fmt.Sprintf("%s-%s@wl.test", name, tag))
		fx.Member(t, util.UUIDToString(wsID), id, role)
		return uid(t, id)
	}
	w.srcOwner = person("src-owner", w.source, "owner")
	w.srcAdmin = person("src-admin", w.source, "admin")
	w.vOwner = person("v-owner", w.viewer, "owner")
	w.vAdmin = person("v-admin", w.viewer, "admin")
	w.vMember = person("v-member", w.viewer, "member")
	w.vGuest = person("v-guest", w.viewer, "guest")
	w.otherOwner = person("other-owner", w.other, "owner")
	w.src2Owner = person("src2-owner", w.source2, "owner")

	project := func(wsID pgtype.UUID, title, visibility string) pgtype.UUID {
		return uid(t, fx.Project(t, title, testutil.Cols{
			"workspace_id": util.UUIDToString(wsID), "visibility": visibility,
		}))
	}
	w.shared = project(w.source, "Shared", "workspace")
	w.unticked = project(w.source, "Unticked", "project")
	w.private = project(w.source, "Private", "private")
	w.project2 = project(w.source2, "Other source", "workspace")

	issue := func(wsID, projectID pgtype.UUID, creator pgtype.UUID, title, visibility string, over ...testutil.Cols) {
		cols := testutil.Cols{
			"workspace_id": util.UUIDToString(wsID), "project_id": util.UUIDToString(projectID),
			"creator_id": util.UUIDToString(creator), "visibility": visibility,
		}
		for _, o := range over {
			for k, v := range o {
				cols[k] = v
			}
		}
		fx.Issue(t, title, cols)
	}
	issue(w.source, w.shared, w.srcOwner, "visible-a", "workspace", testutil.Cols{
		"assignee_type": "member", "assignee_id": util.UUIDToString(w.srcAdmin),
		"description": "SECRET DESCRIPTION", "due_date": "2026-11-01",
		"updated_at": testutil.Raw("now() - interval '1 hour'"),
	})
	issue(w.source, w.shared, w.srcOwner, "visible-b", "project", testutil.Cols{"status": "done"})
	issue(w.source, w.shared, w.srcOwner, "private-issue", "private")
	issue(w.source, w.unticked, w.srcOwner, "unticked-issue", "workspace")
	issue(w.source2, w.project2, w.src2Owner, "other-source-issue", "workspace")
	return w
}

func owner() Actor  { return Actor{Role: permission.RoleOwner} }
func admin() Actor  { return Actor{Role: permission.RoleAdmin} }
func member() Actor { return Actor{Role: permission.RoleMember} }

func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Status != status {
		t.Fatalf("err = %v, want status %d", err, status)
	}
}

// link offers and accepts source→viewer sharing projects.
func (w *world) link(t *testing.T, projects ...pgtype.UUID) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	l, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, projects)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := uid(t, l.ID)
	if _, err := w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), id, Patch{Accept: true}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	return id
}

func titles(v LinkedView) []string {
	out := []string{}
	for _, i := range v.Issues {
		out = append(out, i.Title)
	}
	sort.Strings(out)
	return out
}

func TestCreateRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	_, err := w.svc.Create(ctx, w.source, w.srcAdmin, admin(), w.viewerSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 403)
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, Actor{Role: permission.RoleOwner, IsAgent: true}, w.viewerSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 403)
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.private})
	wantStatus(t, err, 400)
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.project2})
	wantStatus(t, err, 400)
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.sourceSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 400)
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, nil)
	wantStatus(t, err, 400)

	l, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared})
	if err != nil {
		t.Fatalf("owner create: %v", err)
	}
	if l.Status != "pending" || l.Side != SideSource || len(l.Projects) != 1 {
		t.Fatalf("created link = %+v", l)
	}
	_, err = w.svc.Create(ctx, w.source, w.srcOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 409)

	// Pending: viewers read nothing, members do not even see it listed.
	id := uid(t, l.ID)
	_, err = w.svc.View(ctx, w.viewer, permission.RoleOwner, id, Page{})
	if err != ErrNotFound {
		t.Fatalf("pending view err = %v, want ErrNotFound", err)
	}
	if links, _ := w.svc.List(ctx, w.viewer, member()); len(links) != 0 {
		t.Fatalf("member sees pending link: %+v", links)
	}
	if links, _ := w.svc.List(ctx, w.viewer, admin()); len(links) != 1 || links[0].Side != SideViewer || len(links[0].Projects) != 0 {
		t.Fatalf("viewer admin list = %+v (viewer must not get the project picks)", links)
	}

	// Accept: only the viewer's owner/admin.
	_, err = w.svc.Update(ctx, w.viewer, w.vMember, member(), id, Patch{Accept: true})
	wantStatus(t, err, 403)
	_, err = w.svc.Update(ctx, w.source, w.srcOwner, owner(), id, Patch{Accept: true})
	wantStatus(t, err, 403)
	_, err = w.svc.Update(ctx, w.other, w.otherOwner, owner(), id, Patch{Accept: true})
	wantStatus(t, err, 404)
	if _, err := w.svc.Update(ctx, w.viewer, w.vAdmin, admin(), id, Patch{Accept: true}); err != nil {
		t.Fatalf("admin accept: %v", err)
	}
}

func TestViewLeakProofs(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.link(t, w.shared)

	// Source 2 also links to the viewer; its issues must not leak into link 1.
	l2, err := w.svc.Create(ctx, w.source2, w.src2Owner, owner(), w.viewerSlug, []pgtype.UUID{w.project2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Update(ctx, w.viewer, w.vOwner, owner(), uid(t, l2.ID), Patch{Accept: true}); err != nil {
		t.Fatal(err)
	}

	v, err := w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{})
	if err != nil {
		t.Fatalf("member view: %v", err)
	}
	if got := titles(v); !reflect.DeepEqual(got, []string{"visible-a", "visible-b"}) {
		t.Fatalf("visible issues = %v; private, unticked and other-source issues must be absent", got)
	}
	if len(v.Projects) != 1 || v.Projects[0].Total != 2 || v.Projects[0].Done != 1 {
		t.Fatalf("projects = %+v; progress must count only the visible issues", v.Projects)
	}
	raw, _ := json.Marshal(v)
	for _, secret := range []string{"SECRET DESCRIPTION", "@wl.test", util.UUIDToString(w.srcAdmin), util.UUIDToString(w.source)} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("view leaks %q: %s", secret, raw)
		}
	}

	// Every refusal is the same 404.
	for name, call := range map[string]func() error{
		"guest":            func() error { _, e := w.svc.View(ctx, w.viewer, permission.RoleGuest, id, Page{}); return e },
		"source side":      func() error { _, e := w.svc.View(ctx, w.source, permission.RoleOwner, id, Page{}); return e },
		"unrelated ws":     func() error { _, e := w.svc.View(ctx, w.other, permission.RoleOwner, id, Page{}); return e },
		"unticked project": func() error { _, e := w.svc.View(ctx, w.viewer, permission.RoleOwner, id, Page{ProjectID: w.unticked}); return e },
		"no such link": func() error {
			_, e := w.svc.View(ctx, w.viewer, permission.RoleOwner, uid(t, uuid.NewString()), Page{})
			return e
		},
	} {
		if err := call(); err != ErrNotFound {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}

	// Pagination walks every visible issue exactly once.
	p1, err := w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{Limit: 1})
	if err != nil || len(p1.Issues) != 1 || p1.Next == nil {
		t.Fatalf("page 1 = %+v, %v", p1, err)
	}
	p2, err := w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{Limit: 1, Cursor: *p1.Next})
	if err != nil || len(p2.Issues) != 1 || p2.Next != nil || p2.Issues[0].Identifier == p1.Issues[0].Identifier {
		t.Fatalf("page 2 = %+v, %v", p2, err)
	}

	// Unticking the project hides its issues on the very next read.
	if _, err := w.svc.Update(ctx, w.source, w.srcOwner, owner(), id, Patch{ProjectIDs: &[]pgtype.UUID{w.unticked}}); err != nil {
		t.Fatalf("update projects: %v", err)
	}
	v, _ = w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{})
	if got := titles(v); !reflect.DeepEqual(got, []string{"unticked-issue"}) {
		t.Fatalf("after re-tick = %v", got)
	}

	// A ticked project turned private disappears without anyone touching the link.
	w.fx.Exec(t, `UPDATE project SET visibility = 'private' WHERE id = $1`, w.unticked)
	v, _ = w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{})
	if len(v.Issues) != 0 || len(v.Projects) != 0 {
		t.Fatalf("private project still visible: %+v", v)
	}

	// Revoke from the viewer side: the next read is 404.
	if err := w.svc.Revoke(ctx, w.viewer, w.vMember, member(), id); err == nil {
		t.Fatal("member revoked a link")
	}
	if err := w.svc.Revoke(ctx, w.viewer, w.vAdmin, admin(), id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := w.svc.View(ctx, w.viewer, permission.RoleOwner, id, Page{}); err != ErrNotFound {
		t.Fatalf("view after revoke err = %v", err)
	}

	// The audit trail outlives the link, for both owners only.
	for _, side := range []pgtype.UUID{w.source, w.viewer} {
		entries, err := w.svc.Audit(ctx, side, owner())
		if err != nil {
			t.Fatal(err)
		}
		actions := map[string]bool{}
		for _, e := range entries {
			if e.LinkID == util.UUIDToString(id) {
				actions[e.Action] = true
			}
		}
		for _, a := range []string{"create", "accept", "update_projects", "revoke"} {
			if !actions[a] {
				t.Errorf("audit from %s missing %s: %+v", util.UUIDToString(side), a, entries)
			}
		}
	}
	_, err = w.svc.Audit(ctx, w.source, admin())
	wantStatus(t, err, 403)
	if entries, _ := w.svc.Audit(ctx, w.other, owner()); len(entries) != 0 {
		t.Fatalf("unrelated workspace reads audit: %+v", entries)
	}
}

func TestRevokeRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.link(t, w.shared)

	err := w.svc.Revoke(ctx, w.source, w.srcAdmin, admin(), id)
	wantStatus(t, err, 403)
	err = w.svc.Revoke(ctx, w.other, w.otherOwner, owner(), id)
	wantStatus(t, err, 404)
	err = w.svc.Revoke(ctx, w.viewer, w.vOwner, Actor{Role: permission.RoleOwner, IsAgent: true}, id)
	wantStatus(t, err, 403)
	if err := w.svc.Revoke(ctx, w.source, w.srcOwner, owner(), id); err != nil {
		t.Fatalf("source owner revoke: %v", err)
	}
	if _, err := w.svc.View(ctx, w.viewer, permission.RoleMember, id, Page{}); err != ErrNotFound {
		t.Fatalf("view after revoke err = %v", err)
	}
}

// TestViewDTOFieldSet is the whitelist's tripwire: every key the view can
// emit is listed here. Adding a field to the DTO fails this test until the
// field is added below on purpose.
func TestViewDTOFieldSet(t *testing.T) {
	w := newWorld(t)
	id := w.link(t, w.shared)
	v, err := w.svc.View(context.Background(), w.viewer, permission.RoleMember, id, Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)

	want := map[string][]string{
		"":         {"issues", "link_id", "next_cursor", "projects", "source", "statuses"},
		"source":   {"avatar_url", "name"},
		"projects": {"done", "icon", "id", "status", "title", "total"},
		"issues": {"assignee_avatar_url", "assignee_name", "due_date", "identifier", "priority",
			"project_id", "status", "title", "updated_at"},
		"statuses": {"category", "key", "name"},
	}
	check := func(name string, obj map[string]json.RawMessage) {
		got := make([]string, 0, len(obj))
		for k := range obj {
			got = append(got, k)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want[name]) {
			t.Errorf("view %q fields = %v, want %v", name, got, want[name])
		}
	}
	check("", top)
	var src map[string]json.RawMessage
	_ = json.Unmarshal(top["source"], &src)
	check("source", src)
	for _, key := range []string{"projects", "issues", "statuses"} {
		var list []map[string]json.RawMessage
		_ = json.Unmarshal(top[key], &list)
		if len(list) == 0 {
			t.Fatalf("fixture produced no %s", key)
		}
		check(key, list[0])
	}
}
