package workspacelink

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestPullRules pins the viewer-side form (DENE-1582): only a person who owns
// the other workspace and may accept here can pull its projects in, and the
// link is then active at once.
func TestPullRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// vOwner also owns the source; vAdmin is only admin there.
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vOwner), "owner")
	w.fx.Member(t, util.UUIDToString(w.source), util.UUIDToString(w.vAdmin), "admin")

	// Lookup lists the source's shareable projects only for its owner.
	pt, err := w.svc.LookupPull(ctx, w.viewer, w.vOwner, owner(), w.sourceSlug)
	if err != nil {
		t.Fatalf("lookup pull: %v", err)
	}
	got := map[string]bool{}
	for _, p := range pt.Projects {
		got[p.Title] = true
	}
	if !pt.Allowed || !got["Shared"] || !got["Unticked"] || got["Private"] || got["Other source"] {
		t.Fatalf("owner lookup = %+v", pt)
	}
	for _, c := range []struct {
		user  pgtype.UUID
		actor Actor
	}{
		{w.vAdmin, admin()},   // admin, not owner, of the source
		{w.vMember, member()}, // may not accept here
		{w.vOwner, Actor{Role: permission.RoleOwner, IsAgent: true}},
	} {
		pt, err := w.svc.LookupPull(ctx, w.viewer, c.user, c.actor, w.sourceSlug)
		if err != nil || pt.Allowed || len(pt.Projects) != 0 {
			t.Fatalf("lookup for %+v = %+v, %v; want not allowed, no projects", c.actor, pt, err)
		}
	}

	_, err = w.svc.Pull(ctx, w.viewer, w.vAdmin, admin(), w.sourceSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 403)
	_, err = w.svc.Pull(ctx, w.viewer, w.vMember, member(), w.sourceSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 403)
	_, err = w.svc.Pull(ctx, w.viewer, w.vOwner, Actor{Role: permission.RoleOwner, IsAgent: true}, w.sourceSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 403)
	_, err = w.svc.Pull(ctx, w.viewer, w.vOwner, owner(), w.sourceSlug, []pgtype.UUID{w.private})
	wantStatus(t, err, 400)
	_, err = w.svc.Pull(ctx, w.viewer, w.vOwner, owner(), w.sourceSlug, []pgtype.UUID{w.project2})
	wantStatus(t, err, 400)
	_, err = w.svc.Pull(ctx, w.viewer, w.vOwner, owner(), w.viewerSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 400)

	l, err := w.svc.Pull(ctx, w.viewer, w.vOwner, owner(), w.sourceSlug, []pgtype.UUID{w.shared})
	if err != nil {
		t.Fatalf("owner pull: %v", err)
	}
	if l.Status != "active" || l.Side != SideViewer || l.Source.Slug != w.sourceSlug {
		t.Fatalf("pulled link = %+v", l)
	}
	_, err = w.svc.Pull(ctx, w.viewer, w.vOwner, owner(), w.sourceSlug, []pgtype.UUID{w.shared})
	wantStatus(t, err, 409)

	// The source side sees the same link with the pulled projects, and the
	// viewer's members can read it right away.
	links, err := w.svc.List(ctx, w.source, owner())
	if err != nil || len(links) != 1 || links[0].Side != SideSource || len(links[0].Projects) != 1 {
		t.Fatalf("source list = %+v, %v", links, err)
	}
	v, err := w.svc.View(ctx, w.viewer, permission.RoleMember, uid(t, l.ID), Page{})
	if err != nil || len(v.Issues) == 0 {
		t.Fatalf("member view after pull = %+v, %v", v, err)
	}

	// Both decisions are audited, each on the side whose authority it used.
	entries, err := w.svc.Audit(ctx, w.viewer, owner())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Side{}
	for _, e := range entries {
		by[e.Action] = e.BySide
	}
	if by["create"] != SideSource || by["accept"] != SideViewer {
		t.Fatalf("audit = %+v", entries)
	}
}
