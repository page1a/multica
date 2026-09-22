package permission

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// allRelations enumerates every Relation, so the invariants below hold over the
// whole input space rather than over hand-picked cases.
func allRelations() []Relation {
	var out []Relation
	for _, creator := range []bool{false, true} {
		for _, inProject := range []bool{false, true} {
			for _, leads := range []bool{false, true} {
				out = append(out, Relation{IsCreator: creator, InProject: inProject, LeadsProject: leads})
			}
		}
	}
	return out
}

// The visibility layer, written out cell by cell: role x scope x relation.
// Columns: not related / in the project / creator.
func TestCanSeeMatrix(t *testing.T) {
	type row struct {
		role                        Role
		vis                         Visibility
		stranger, inProject, author bool
	}
	rows := []row{
		{RoleOwner, VisibilityPrivate, false, false, true},
		{RoleOwner, VisibilityProject, false, true, true},
		{RoleOwner, VisibilityWorkspace, true, true, true},
		{RoleAdmin, VisibilityPrivate, false, false, true},
		{RoleAdmin, VisibilityProject, false, true, true},
		{RoleAdmin, VisibilityWorkspace, true, true, true},
		{RoleMember, VisibilityPrivate, false, false, true},
		{RoleMember, VisibilityProject, false, true, true},
		{RoleMember, VisibilityWorkspace, true, true, true},
		{RoleGuest, VisibilityPrivate, false, false, true},
		{RoleGuest, VisibilityProject, false, true, true},
		{RoleGuest, VisibilityWorkspace, false, false, true},
	}
	if len(rows) != len(Roles)*len(Visibilities) {
		t.Fatalf("matrix has %d rows, want one per role x scope = %d", len(rows), len(Roles)*len(Visibilities))
	}
	for _, r := range rows {
		cases := []struct {
			name string
			rel  Relation
			want bool
		}{
			{"stranger", Relation{}, r.stranger},
			{"in project", Relation{InProject: true}, r.inProject},
			{"creator", Relation{IsCreator: true}, r.author},
		}
		for _, c := range cases {
			if got := CanSee(r.role, r.vis, c.rel); got != c.want {
				t.Errorf("CanSee(%s, %s, %s) = %v, want %v", r.role, r.vis, c.name, got, c.want)
			}
		}
	}
}

// The tier layer for a resource the caller can already see.
func TestTierMatrix(t *testing.T) {
	visible := Relation{InProject: true}
	type want struct{ owner, admin, member, guest bool }
	actions := map[Action]want{
		ActionView:    {true, true, true, true},
		ActionComment: {true, true, true, false},
		ActionEdit:    {true, true, true, false},
		// Without being the creator / the lead, a member manages nothing.
		ActionChangeVisibility:     {true, true, false, false},
		ActionManageProjectMembers: {true, true, false, false},
	}
	if len(actions) != len(ResourceActions) {
		t.Fatalf("tier matrix covers %d actions, want %d", len(actions), len(ResourceActions))
	}
	for action, w := range actions {
		for role, expected := range map[Role]bool{RoleOwner: w.owner, RoleAdmin: w.admin, RoleMember: w.member, RoleGuest: w.guest} {
			if got := Allowed(role, action, VisibilityProject, visible); got != expected {
				t.Errorf("Allowed(%s, %s) = %v, want %v", role, action, got, expected)
			}
		}
	}
}

func TestMemberManagesOnlyTheirOwn(t *testing.T) {
	if !Allowed(RoleMember, ActionChangeVisibility, VisibilityPrivate, Relation{IsCreator: true}) {
		t.Error("a member must be able to re-scope a resource they created")
	}
	if !Allowed(RoleMember, ActionManageProjectMembers, VisibilityProject, Relation{InProject: true, LeadsProject: true}) {
		t.Error("a member must be able to manage members of a project they lead")
	}
	// Being in the project is not the same as owning the resource.
	if Allowed(RoleMember, ActionChangeVisibility, VisibilityProject, Relation{InProject: true, LeadsProject: true}) {
		t.Error("leading the project must not let a member re-scope someone else's resource")
	}
	// A guest who was demoted while still creator or lead keeps sight, never control.
	rel := Relation{IsCreator: true, InProject: true, LeadsProject: true}
	for _, action := range []Action{ActionChangeVisibility, ActionManageProjectMembers, ActionEdit, ActionComment} {
		if Allowed(RoleGuest, action, VisibilityProject, rel) {
			t.Errorf("guest must not %s even as creator and lead", action)
		}
	}
}

func TestWorkspaceActionMatrix(t *testing.T) {
	type want struct{ owner, admin, member, guest bool }
	actions := map[WorkspaceAction]want{
		WorkspaceCreateResource: {true, true, true, false},
		WorkspaceBeAssigned:     {true, true, true, false},
		WorkspaceManageMembers:  {true, true, false, false},
		WorkspaceManageSettings: {true, true, false, false},
		WorkspaceOwn:            {true, false, false, false},
	}
	if len(actions) != len(WorkspaceActions) {
		t.Fatalf("matrix covers %d workspace actions, want %d", len(actions), len(WorkspaceActions))
	}
	for action, w := range actions {
		for role, expected := range map[Role]bool{RoleOwner: w.owner, RoleAdmin: w.admin, RoleMember: w.member, RoleGuest: w.guest} {
			if got := AllowedInWorkspace(role, action); got != expected {
				t.Errorf("AllowedInWorkspace(%s, %s) = %v, want %v", role, action, got, expected)
			}
		}
	}
}

// Zero trust: with nothing shared, a member or guest sees no resource they did
// not create, and therefore can do nothing to it.
func TestZeroTrustDefault(t *testing.T) {
	if DefaultVisibility != VisibilityPrivate {
		t.Fatalf("DefaultVisibility = %s, want private", DefaultVisibility)
	}
	for _, role := range []Role{RoleMember, RoleGuest} {
		for _, action := range ResourceActions {
			if Allowed(role, action, DefaultVisibility, Relation{}) {
				t.Errorf("%s may %s an unshared resource", role, action)
			}
		}
	}
}

// Over the whole input space: sharing never grants anything by itself, a guest
// never writes, and widening a scope never takes sight away.
func TestInvariants(t *testing.T) {
	writes := []Action{ActionComment, ActionEdit, ActionChangeVisibility, ActionManageProjectMembers}
	for _, role := range Roles {
		for _, rel := range allRelations() {
			for _, vis := range Visibilities {
				seen := CanSee(role, vis, rel)
				for _, action := range ResourceActions {
					allowed := Allowed(role, action, vis, rel)
					if allowed && !seen {
						t.Errorf("%s may %s a %s resource they cannot see (%+v)", role, action, vis, rel)
					}
					if action == ActionView && allowed != seen {
						t.Errorf("view must equal sight for %s/%s/%+v", role, vis, rel)
					}
				}
				if role == RoleGuest {
					for _, action := range writes {
						if Allowed(role, action, vis, rel) {
							t.Errorf("guest may %s (%s, %+v)", action, vis, rel)
						}
					}
				}
			}
			// Tier answers must not depend on which scope made the resource visible.
			for _, action := range writes {
				var answers []bool
				for _, vis := range Visibilities {
					if CanSee(role, vis, rel) {
						answers = append(answers, Allowed(role, action, vis, rel))
					}
				}
				for _, a := range answers {
					if a != answers[0] {
						t.Errorf("%s/%s/%+v: write answer changes with scope; sharing must not decide writes", role, action, rel)
					}
				}
			}
		}
	}
	for _, action := range WorkspaceActions {
		if action != "" && AllowedInWorkspace(RoleGuest, action) {
			t.Errorf("guest may %s", action)
		}
	}
}

func TestFailsClosed(t *testing.T) {
	everything := Relation{IsCreator: true, InProject: true, LeadsProject: true}
	for _, role := range []Role{"", "superuser", "Owner"} {
		for _, vis := range Visibilities {
			if CanSee(role, vis, everything) {
				t.Errorf("unknown role %q sees a %s resource", role, vis)
			}
		}
		for _, action := range WorkspaceActions {
			if AllowedInWorkspace(role, action) {
				t.Errorf("unknown role %q may %s", role, action)
			}
		}
	}
	for _, vis := range []Visibility{"", "public", "team"} {
		if CanSee(RoleOwner, vis, Relation{InProject: true}) {
			t.Errorf("unknown scope %q is visible to a non-creator", vis)
		}
		if CanSetVisibility(vis, true) {
			t.Errorf("unknown scope %q can be set", vis)
		}
	}
	if Allowed(RoleOwner, "delete_everything", VisibilityWorkspace, everything) {
		t.Error("unknown action is allowed")
	}
	if AllowedInWorkspace(RoleOwner, "delete_everything") {
		t.Error("unknown workspace action is allowed")
	}
}

func TestCanSeeModuleMatrix(t *testing.T) {
	type row struct {
		role            Role
		vis             Visibility
		inProject, want bool
	}
	rows := []row{
		{RoleOwner, VisibilityPrivate, false, true},
		{RoleOwner, VisibilityPrivate, true, true},
		{RoleOwner, VisibilityProject, false, true},
		{RoleOwner, VisibilityWorkspace, false, true},
		{RoleAdmin, VisibilityPrivate, false, true},
		{RoleAdmin, VisibilityProject, false, true},
		{RoleAdmin, VisibilityWorkspace, false, true},
		{RoleMember, VisibilityPrivate, false, false},
		{RoleMember, VisibilityPrivate, true, false},
		{RoleMember, VisibilityProject, false, false},
		{RoleMember, VisibilityProject, true, true},
		{RoleMember, VisibilityWorkspace, false, true},
		{RoleGuest, VisibilityPrivate, false, false},
		{RoleGuest, VisibilityProject, false, false},
		{RoleGuest, VisibilityProject, true, true},
		{RoleGuest, VisibilityWorkspace, false, true},
	}
	for _, r := range rows {
		if got := CanSeeModule(r.role, r.vis, r.inProject); got != r.want {
			t.Errorf("CanSeeModule(%s, %s, inProject=%v) = %v, want %v",
				r.role, r.vis, r.inProject, got, r.want)
		}
	}
	if DefaultModuleVisibility != VisibilityWorkspace {
		t.Fatalf("DefaultModuleVisibility = %s, want workspace — upgrading must not hide every module", DefaultModuleVisibility)
	}
	if CanSeeModule("superuser", VisibilityWorkspace, true) {
		t.Error("unknown role must not enter a module")
	}
	if CanSeeModule(RoleMember, "public", true) {
		t.Error("unknown scope must not open a module to a member")
	}
	if !CanSeeModule(RoleOwner, "public", false) {
		t.Error("owner must still enter a module with a broken scope so they can fix it")
	}
	if got, want := len(Modules), 4; got != want {
		t.Fatalf("Modules has %d entries, want the closed list of %d", got, want)
	}
	for _, m := range []Module{"agents", "squads", "settings", "members", "billing", ""} {
		if m.Valid() {
			t.Errorf("%q must not be a module: agents/squads belong to Parent B, settings follow tier", m)
		}
	}
}

func TestProjectScopeNeedsAProject(t *testing.T) {
	for _, vis := range Visibilities {
		if !CanSetVisibility(vis, true) {
			t.Errorf("%s must be settable on a resource in a project", vis)
		}
		want := vis != VisibilityProject
		if got := CanSetVisibility(vis, false); got != want {
			t.Errorf("CanSetVisibility(%s, no project) = %v, want %v", vis, got, want)
		}
	}
}

// Agents and squads are owned by agent_invocation_target. If either shows up
// as a tier, scope or action here, a second permission source has crept in.
func TestNoAgentOrSquad(t *testing.T) {
	var names []string
	for _, r := range Roles {
		names = append(names, string(r))
	}
	for _, v := range Visibilities {
		names = append(names, string(v))
	}
	for _, a := range ResourceActions {
		names = append(names, string(a))
	}
	for _, a := range WorkspaceActions {
		names = append(names, string(a))
	}
	for _, m := range Modules {
		names = append(names, string(m))
	}
	for _, n := range names {
		if strings.Contains(n, "agent") || strings.Contains(n, "squad") {
			t.Errorf("%q brings agents or squads into the sharing model", n)
		}
	}
}

// The tiers here and the member.role CHECK constraint are one list kept in two
// places; a tier the database rejects, or accepts but this package fails
// closed on, is a silent lockout.
func TestRolesMatchMigration(t *testing.T) {
	sql, err := os.ReadFile("../../migrations/502_member_role_guest.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`CHECK \(role IN \(([^)]*)\)\)`).FindSubmatch(sql)
	if m == nil {
		t.Fatal("no role CHECK in migration 502")
	}
	var want []string
	for _, r := range Roles {
		want = append(want, fmt.Sprintf("'%s'", r))
	}
	if got := string(m[1]); got != strings.Join(want, ", ") {
		t.Errorf("migration allows %s, package knows %s", got, strings.Join(want, ", "))
	}
}
