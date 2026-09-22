// Package permission is the decision matrix for workspace tiers and resource
// visibility. It is pure: callers load the facts (the caller's tier, the
// resource's visibility, how the caller relates to the resource) and this
// package turns them into one answer.
//
// Two layers, never mixed:
//
//   - Visibility decides whether a resource exists for the caller at all.
//   - Tier decides what the caller may do with a resource that exists for them.
//
// Sharing never grants write access and a tier never grants sight of an
// unshared resource. The human-readable matrix is docs/kun/permission-model.md;
// this file is its executable form and the tests keep the two honest.
//
// Agents and squads are deliberately absent. Their access is owned by
// agent.permission_mode and agent_invocation_target, and giving them a second
// source here would leave conflicts with no winner.
package permission

// Role is a workspace tier, stored in member.role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleGuest  Role = "guest"
)

// Roles lists every tier, strongest first. It must match the member.role
// CHECK constraint.
var Roles = []Role{RoleOwner, RoleAdmin, RoleMember, RoleGuest}

// Visibility is a resource's sharing scope. The vocabulary is issue_view's,
// so the whole product speaks one set of scope names.
type Visibility string

const (
	VisibilityPrivate   Visibility = "private"
	VisibilityProject   Visibility = "project"
	VisibilityWorkspace Visibility = "workspace"
)

// Visibilities lists every scope, narrowest first.
var Visibilities = []Visibility{VisibilityPrivate, VisibilityProject, VisibilityWorkspace}

// DefaultVisibility is what a newly created resource gets: zero trust.
const DefaultVisibility = VisibilityPrivate

// Relation is how the caller relates to one resource. Callers fill it from
// data they already hold; nothing here touches the database.
type Relation struct {
	// IsCreator: the caller created the resource.
	IsCreator bool
	// IsAssignee: the resource is assigned to the caller. Assigning work to
	// somebody is itself an act of sharing — an issue whose assignee cannot
	// open it is a state the product must not be able to reach — so the
	// assignee sees it at any scope, exactly like the creator. Only issues
	// have assignees; always false for a project or a repository.
	IsAssignee bool
	// InProject: the resource belongs to a project and that project is in the
	// caller's accessible set, as returned by the handler's
	// listAccessibleProjectIDs (project_member rows, led projects, and every
	// project for owner/admin). Always false for a resource with no project.
	InProject bool
	// LeadsProject: the caller is the lead of the resource's project. Only
	// consulted for ActionManageProjectMembers.
	LeadsProject bool
}

// Action is something a caller does to a visible resource.
type Action string

const (
	// ActionView reads the resource and everything hanging off it.
	ActionView Action = "view"
	// ActionComment posts or edits a comment or reply.
	ActionComment Action = "comment"
	// ActionEdit creates or edits the resource, changes status, uploads or
	// removes attachments, assigns it.
	ActionEdit Action = "edit"
	// ActionChangeVisibility changes the resource's sharing scope.
	ActionChangeVisibility Action = "change_visibility"
	// ActionManageProjectMembers adds or removes people on a project.
	ActionManageProjectMembers Action = "manage_project_members"
)

// ResourceActions lists every Action.
var ResourceActions = []Action{
	ActionView, ActionComment, ActionEdit, ActionChangeVisibility, ActionManageProjectMembers,
}

// WorkspaceAction is something done to the workspace itself. These never
// consult visibility: there is no resource to share.
type WorkspaceAction string

const (
	// WorkspaceCreateResource creates an issue, project or repo.
	WorkspaceCreateResource WorkspaceAction = "create_resource"
	// WorkspaceBeAssigned: the caller can be an assignee or be @-mentioned
	// into triggering a run.
	WorkspaceBeAssigned WorkspaceAction = "be_assigned"
	// WorkspaceManageMembers invites people and changes other people's tier.
	WorkspaceManageMembers WorkspaceAction = "manage_members"
	// WorkspaceManageSettings covers workspace settings, integrations, keys.
	WorkspaceManageSettings WorkspaceAction = "manage_settings"
	// WorkspaceOwn covers billing and transferring or deleting the workspace.
	WorkspaceOwn WorkspaceAction = "own"
)

// WorkspaceActions lists every WorkspaceAction.
var WorkspaceActions = []WorkspaceAction{
	WorkspaceCreateResource, WorkspaceBeAssigned, WorkspaceManageMembers,
	WorkspaceManageSettings, WorkspaceOwn,
}

// Valid reports whether r is a known tier.
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleGuest:
		return true
	}
	return false
}

// Valid reports whether v is a known scope.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityProject, VisibilityWorkspace:
		return true
	}
	return false
}

func (r Role) isManager() bool { return r == RoleOwner || r == RoleAdmin }

// CanWrite reports whether the tier may write anything at all. Guest is the
// only tier that may not; this is the single fact the global read-only
// interceptor needs.
func (r Role) CanWrite() bool {
	return r == RoleOwner || r == RoleAdmin || r == RoleMember
}

// CanSee is layer one: does the resource exist for this caller. A false
// answer must surface as "not found", never as "forbidden", and must hold in
// lists, search, aggregates and notifications alike.
//
// Creator and assignee are above the scope tiers: both are named on the
// resource itself, so no amount of narrowing hides it from them.
//
// Unknown tiers and unknown scopes see nothing.
func CanSee(role Role, vis Visibility, rel Relation) bool {
	if !role.Valid() {
		return false
	}
	if rel.IsCreator || rel.IsAssignee {
		return true
	}
	switch vis {
	case VisibilityWorkspace:
		// Guests are not part of "the whole workspace". A project is the only
		// way to show a guest anything.
		return role != RoleGuest
	case VisibilityProject:
		return rel.InProject
	default:
		// private, and anything unrecognised.
		return false
	}
}

// Allowed is the full two-layer answer for one caller, one resource, one
// action. Visibility is checked first; tier only matters for a resource the
// caller can see.
func Allowed(role Role, action Action, vis Visibility, rel Relation) bool {
	if !CanSee(role, vis, rel) {
		return false
	}
	switch action {
	case ActionView:
		return true
	case ActionComment, ActionEdit:
		return role.CanWrite()
	case ActionChangeVisibility:
		return role.isManager() || (role == RoleMember && rel.IsCreator)
	case ActionManageProjectMembers:
		return role.isManager() || (role == RoleMember && rel.LeadsProject)
	default:
		return false
	}
}

// AllowedInWorkspace answers workspace-level actions from tier alone.
func AllowedInWorkspace(role Role, action WorkspaceAction) bool {
	switch action {
	case WorkspaceCreateResource, WorkspaceBeAssigned:
		return role.CanWrite()
	case WorkspaceManageMembers, WorkspaceManageSettings:
		return role.isManager()
	case WorkspaceOwn:
		return role == RoleOwner
	default:
		return false
	}
}

// CanSetVisibility reports whether a resource may be given scope vis.
// hasProject is whether the resource belongs to a project: "project" scope
// names that project's people, so without one it names nobody.
func CanSetVisibility(vis Visibility, hasProject bool) bool {
	if !vis.Valid() {
		return false
	}
	return vis != VisibilityProject || hasProject
}

// Module is a product area that can be shared independently of any one
// resource. Agents and squads are not modules: their access stays with
// agent.permission_mode. Workspace settings, members and billing are not
// modules either — those follow tier alone.
type Module string

const (
	ModuleIssues   Module = "issues"
	ModuleProjects Module = "projects"
	ModuleRepos    Module = "repos"
	ModuleRuntimes Module = "runtimes"
)

// Modules is the closed list a workspace can restrict. Keep in sync with the
// workspace_module_visibility.module CHECK constraint.
var Modules = []Module{ModuleIssues, ModuleProjects, ModuleRepos, ModuleRuntimes}

// DefaultModuleVisibility is what a module with no row is treated as: every
// member, including guests, may enter. Modules are not created the way an
// issue is, so defaulting them to private would lock the product on upgrade.
const DefaultModuleVisibility = VisibilityWorkspace

// Valid reports whether m is a known module.
func (m Module) Valid() bool {
	switch m {
	case ModuleIssues, ModuleProjects, ModuleRepos, ModuleRuntimes:
		return true
	}
	return false
}

// CanSeeModule is the third AND gate on top of CanSee: may this caller enter
// this product area at all. It does not change the resource matrix. A caller
// who can see an issue but not the Issues module must not find Issues in the
// nav and must be refused at the Issues APIs.
//
// Owner and admin always enter every module so they can administer a
// restriction they just set. For everyone else:
//
//   - workspace: every member, including guests (resource visibility still
//     filters what they see inside)
//   - project: only people in the module's designated project
//   - private: nobody but owner/admin
//
// Unknown roles and unknown scopes fail closed.
func CanSeeModule(role Role, vis Visibility, inProject bool) bool {
	if !role.Valid() {
		return false
	}
	if role.isManager() {
		return true
	}
	switch vis {
	case VisibilityWorkspace:
		return true
	case VisibilityProject:
		return inProject
	default:
		return false
	}
}
