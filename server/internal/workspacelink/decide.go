// Package workspacelink is the cross-workspace read-only link (DENE-1225).
//
// A link lets the members of one workspace (the viewer) read the state of
// chosen projects in another workspace (the source) without joining it. The
// whole feature is one read rule:
//
//	A viewer-workspace member (guests excluded) may read the status fields of
//	the non-private issues in the source's ticked, non-private projects.
//
// Every decision about who may do what lives in this package: Decide is the
// pure table, Service loads the facts and applies it. Handlers, the frontend
// and the CLI are thin shells over Service. The only way source data leaves is
// Service.View, and it returns a whitelisted DTO (view.go), never an existing
// issue or project payload.
//
// The caller's request is always scoped to its own workspace (the
// X-Workspace-ID header, already checked by RequireWorkspaceMember). The
// source's member table, realtime channels, mentions, inbox and search never
// see the viewer, so none of them needed to change. See
// docs/adr/0004-workspace-link.md.
package workspacelink

import "github.com/multica-ai/multica/server/internal/permission"

// Side is how the caller's current workspace relates to a link.
type Side string

const (
	// SideSource: the caller's workspace hands its data out.
	SideSource Side = "source"
	// SideViewer: the caller's workspace reads the source's data.
	SideViewer Side = "viewer"
	// SideNone: the caller's workspace is not on the link. Every operation
	// answers "link not found".
	SideNone Side = "none"
)

// Op is one thing a caller can do with links.
type Op string

const (
	// OpCreate offers a new link, OpUpdateProjects changes what it exposes.
	OpCreate         Op = "create"
	OpUpdateProjects Op = "update_projects"
	// OpAccept turns a pending link active.
	OpAccept Op = "accept"
	// OpRevoke deletes a link from either side.
	OpRevoke Op = "revoke"
	// OpView reads the linked data.
	OpView Op = "view"
	// OpManage reads the management list: both directions, pending links,
	// project picks.
	OpManage Op = "manage"
	// OpAudit reads the audit trail.
	OpAudit Op = "audit"
	// OpSeePending lists the offers waiting for this workspace's answer
	// (DENE-1641): names only, so an agent can tell its person what to
	// accept. Answering stays OpAccept.
	OpSeePending Op = "see_pending"
	// OpSetManaged turns managed access on or off (DENE-1663). Like handing
	// projects out, it is the source owner's call.
	OpSetManaged Op = "set_managed"
	// OpManageRemote is an agent of the viewer acting on the source's issues
	// and autopilots for its run's originator (DENE-1663). Asked from the
	// viewer side with the ORIGINATOR's tier in the source: the agent never
	// holds more there than the person who started it.
	OpManageRemote Op = "manage_remote"
)

// Ops lists every operation, for the decision-table test.
var Ops = []Op{OpCreate, OpUpdateProjects, OpAccept, OpRevoke, OpView, OpManage, OpAudit, OpSeePending, OpSetManaged, OpManageRemote}

// Actor is the caller as this package needs it.
type Actor struct {
	// Role is the caller's tier in its current workspace. For an agent it is
	// the tier of the human the task token runs as.
	Role permission.Role
	// IsAgent: the call came from an agent. Agents may only read (OpView and
	// the active-link directory) and, on a managed link, act in the source
	// for their originator (OpManageRemote); setting a link up, switching
	// managed access or tearing it down is a person's decision.
	IsAgent bool
}

// Decide is the whole permission table. It is pure so the test can pin every
// cell; Service never answers a permission question any other way.
func Decide(op Op, side Side, actor Actor) bool {
	if side == SideNone {
		return false
	}
	role := actor.Role
	ownerOrAdmin := role == permission.RoleOwner || role == permission.RoleAdmin
	if actor.IsAgent {
		// Agents read exactly what a person of the same tier reads, nothing
		// more: no bypass, no management.
		switch op {
		case OpView:
			return side == SideViewer && role.CanWrite()
		case OpSeePending:
			return side == SideViewer && ownerOrAdmin
		case OpManageRemote:
			// Guests of the source only read there; a manager writes.
			return side == SideViewer && role.CanWrite()
		}
		return false
	}
	switch op {
	case OpCreate, OpUpdateProjects:
		// Handing data out of the workspace weighs more than any setting.
		return side == SideSource && role == permission.RoleOwner
	case OpAccept:
		return side == SideViewer && ownerOrAdmin
	case OpRevoke:
		if side == SideSource {
			return role == permission.RoleOwner
		}
		return ownerOrAdmin
	case OpView:
		// Guests only see what was shared with them by name.
		return side == SideViewer && role.CanWrite()
	case OpManage:
		return ownerOrAdmin
	case OpAudit:
		return role == permission.RoleOwner
	case OpSeePending:
		return side == SideViewer && ownerOrAdmin
	case OpSetManaged:
		return side == SideSource && role == permission.RoleOwner
	case OpManageRemote:
		// A person acts in the source through their own membership there.
		return false
	}
	return false
}
