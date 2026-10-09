package workspacelink

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// shareContext gives the shared project a description, a repository, a local
// directory and a resource type the link must not pass on.
func (w *world) shareContext(t *testing.T) {
	t.Helper()
	w.fx.Exec(t, `UPDATE project SET description = 'Shared project brief' WHERE id = $1`, w.shared)
	add := func(kind, ref string, pos int) {
		w.fx.Insert(t, "project_resource", testutil.Cols{
			"project_id": util.UUIDToString(w.shared), "workspace_id": util.UUIDToString(w.source),
			"resource_type": kind, "resource_ref": testutil.Raw("'" + ref + "'::jsonb"), "position": pos,
		})
	}
	add("github_repo", `{"url":"https://github.com/acme/shared"}`, 0)
	add("local_directory", `{"local_path":"/src/shared","daemon_id":"d-secret","execution_mode":"worktree"}`, 1)
	add("mystery", `{"token":"nope"}`, 2)
}

func TestReferencesCarryProjectContext(t *testing.T) {
	w := newWorld(t)
	w.shareContext(t)
	id := w.link(t, w.shared)
	svc := w.svc.WithMemoryLine(func(_ context.Context, p db.Project) string { return "memory of " + p.Title })

	refs, err := svc.References(context.Background(), w.viewer, permission.RoleMember, []Ref{
		{LinkID: id, ProjectID: w.shared},
		{LinkID: id, ProjectID: w.unticked}, // never shared
		{LinkID: id, ProjectID: w.private},  // private
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 {
		t.Fatalf("references = %+v, want only the shared project", refs)
	}
	got := refs[0]
	if got.Title != "Shared" || got.Description != "Shared project brief" || got.MemoryLine != "memory of Shared" {
		t.Fatalf("reference = %+v", got)
	}
	want := []LinkedResource{
		{Type: "github_repo", URL: "https://github.com/acme/shared"},
		{Type: "local_directory", Path: "/src/shared"},
	}
	if !reflect.DeepEqual(got.Resources, want) {
		t.Fatalf("resources = %+v, want %+v (no daemon id, no unknown types)", got.Resources, want)
	}
}

func TestReferencesFollowTheLink(t *testing.T) {
	w := newWorld(t)
	id := w.link(t, w.shared)
	ctx := context.Background()
	ref := []Ref{{LinkID: id, ProjectID: w.shared}}

	count := func(ws pgtype.UUID, role permission.Role) int {
		t.Helper()
		refs, err := w.svc.References(ctx, ws, role, ref)
		if err != nil {
			t.Fatal(err)
		}
		return len(refs)
	}
	if count(w.viewer, permission.RoleMember) != 1 {
		t.Fatal("viewer member should read the shared project")
	}
	if count(w.viewer, permission.RoleGuest) != 0 {
		t.Fatal("a guest must not read linked projects")
	}
	if count(w.source, permission.RoleOwner) != 0 || count(w.other, permission.RoleOwner) != 0 {
		t.Fatal("only the viewer side resolves references")
	}

	opts, err := w.svc.ReferenceOptions(ctx, w.viewer, permission.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 1 || opts[0].ID != util.UUIDToString(w.shared) || opts[0].Source.Name != "source" {
		t.Fatalf("options = %+v", opts)
	}

	// Unticking, then revoking, takes effect on the next read.
	ids := []pgtype.UUID{w.unticked}
	if _, err := w.svc.Update(ctx, w.source, w.srcOwner, owner(), id, Patch{ProjectIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	if count(w.viewer, permission.RoleMember) != 0 {
		t.Fatal("an unticked project must stop resolving")
	}
	if err := w.svc.Revoke(ctx, w.viewer, w.vAdmin, admin(), id); err != nil {
		t.Fatal(err)
	}
	opts, err = w.svc.ReferenceOptions(ctx, w.viewer, permission.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 0 {
		t.Fatalf("options after revoke = %+v", opts)
	}
}
