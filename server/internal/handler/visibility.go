package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/permission"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Resource-level sharing scope, read side (DENE-698).
//
// One caller's sharing facts — their tier and the projects they can reach —
// are loaded once into a visibilityViewer, and every read surface asks that
// viewer the same question. The decision itself is permission.CanSee's; this
// file only supplies the facts and the two shapes a read needs them in:
//
//   - a Go predicate, for rows already in hand;
//   - a SQL predicate, for lists that must filter before they paginate.
//
// The two must agree. A row the SQL predicate lets through and the Go
// predicate rejects (or the reverse) is a bug, and visibility_test.go holds
// them to the same answers.
//
// A caller who cannot see a resource gets "not found", never "forbidden":
// telling them it exists is most of what they wanted to know.

// visibilityBypassReason records why a viewer skips the scope check, so the
// zero value can never accidentally mean "bypass".
type visibilityBypassReason string

const (
	// bypassAgent: the request is an agent's. Agent access is governed by
	// agent.permission_mode and agent_invocation_target (migration 130), not
	// by this model — see docs/kun/permission-model.md. Running an agent's
	// reads through its owning human's scope would deny an agent the issue it
	// was just assigned.
	bypassAgent visibilityBypassReason = "agent"
	// bypassInternal: a server-internal read with no human caller at all
	// (daemon plumbing, webhooks). These never render a member's list.
	bypassInternal visibilityBypassReason = "internal"
)

// visibilityViewer is everything the sharing layer knows about one caller.
// Build it with visibilityViewerFor; the zero value sees nothing, which is
// the right answer for a caller we failed to identify.
type visibilityViewer struct {
	userID pgtype.UUID
	role   permission.Role
	// projectIDs is listAccessibleProjectIDs' answer: explicit project_member
	// rows plus led projects, or every project in the workspace for the
	// owner. Not a new query — the same one saved views already use.
	projectIDs []pgtype.UUID
	projectSet map[[16]byte]struct{}
	agentSet   map[[16]byte]struct{}
	// sharedIssues / sharedRepos: resource_share rows naming this viewer,
	// keyed by resource_id (issue id text, repo URL).
	sharedIssues map[string]struct{}
	sharedRepos  map[string]struct{}
	bypass       visibilityBypassReason
}

// visibilityViewerFor loads the caller's sharing facts for one workspace.
//
// An error means the facts could not be established; callers must treat that
// as "sees nothing" rather than falling back to an unfiltered read.
func (h *Handler) visibilityViewerFor(r *http.Request, wsUUID pgtype.UUID) (visibilityViewer, error) {
	userID := requestUserID(r)
	workspaceID := uuidToString(wsUUID)
	if actorType, _ := h.resolveActor(r, userID, workspaceID); actorType == "agent" {
		return visibilityViewer{bypass: bypassAgent}, nil
	}
	if userID == "" {
		return visibilityViewer{}, fmt.Errorf("visibility: no caller identity")
	}
	userUUID, err := parseUUIDSafe(userID)
	if err != nil {
		return visibilityViewer{}, fmt.Errorf("visibility: bad caller id: %w", err)
	}
	return h.visibilityViewerForUser(r.Context(), wsUUID, userUUID)
}

// visibilityViewerForUser is the context-only variant, for the paths that
// resolved their user before reaching here.
func (h *Handler) visibilityViewerForUser(ctx context.Context, wsUUID, userUUID pgtype.UUID) (visibilityViewer, error) {
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return visibilityViewer{}, fmt.Errorf("visibility: caller is not a member: %w", err)
	}
	projectIDs, err := h.listAccessibleProjectIDs(ctx, wsUUID, userUUID)
	if err != nil {
		return visibilityViewer{}, fmt.Errorf("visibility: accessible projects: %w", err)
	}
	agentIDs, err := h.Queries.ListAgentIDsOwnedByMember(ctx, db.ListAgentIDsOwnedByMemberParams{
		WorkspaceID: wsUUID,
		OwnerID:     userUUID,
	})
	if err != nil {
		return visibilityViewer{}, fmt.Errorf("visibility: owned agents: %w", err)
	}
	shares, err := h.Queries.ListResourceSharesForMember(ctx, db.ListResourceSharesForMemberParams{
		WorkspaceID: wsUUID,
		MemberID:    userUUID,
	})
	if err != nil {
		return visibilityViewer{}, fmt.Errorf("visibility: direct shares: %w", err)
	}
	v := visibilityViewer{
		userID:       userUUID,
		role:         permission.Role(member.Role),
		projectIDs:   projectIDs,
		projectSet:   make(map[[16]byte]struct{}, len(projectIDs)),
		agentSet:     make(map[[16]byte]struct{}, len(agentIDs)),
		sharedIssues: map[string]struct{}{},
		sharedRepos:  map[string]struct{}{},
	}
	for _, share := range shares {
		switch share.ResourceType {
		case "issue":
			v.sharedIssues[share.ResourceID] = struct{}{}
		case "repo":
			v.sharedRepos[share.ResourceID] = struct{}{}
		}
	}
	for _, id := range projectIDs {
		if id.Valid {
			v.projectSet[id.Bytes] = struct{}{}
		}
	}
	for _, id := range agentIDs {
		if id.Valid {
			v.agentSet[id.Bytes] = struct{}{}
		}
	}
	return v, nil
}

// internalVisibilityViewer is for reads with no human caller to scope: the
// daemon's own plumbing, webhook fan-out, background workers. Naming it here
// keeps those call sites greppable.
func internalVisibilityViewer() visibilityViewer {
	return visibilityViewer{bypass: bypassInternal}
}

func (v visibilityViewer) bypasses() bool { return v.bypass != "" }

func (v visibilityViewer) inProject(projectID pgtype.UUID) bool {
	if !projectID.Valid {
		return false
	}
	_, ok := v.projectSet[projectID.Bytes]
	return ok
}

// relation describes how the viewer relates to one resource: who created it
// and which project it sits in.
func (v visibilityViewer) relation(creatorType string, creatorID, projectID pgtype.UUID) permission.Relation {
	return permission.Relation{
		IsCreator: v.isMe(creatorType, creatorID),
		InProject: v.inProject(projectID),
	}
}

// isMe answers a polymorphic actor column: assignee_type / assignee_id and
// creator_type / creator_id both name a member or an agent, and only a member
// can be this viewer.
func (v visibilityViewer) isMe(actorType string, actorID pgtype.UUID) bool {
	return actorType == "member" && actorID.Valid && v.userID.Valid && actorID.Bytes == v.userID.Bytes
}

func (v visibilityViewer) isOwnedAgent(actorType string, actorID pgtype.UUID) bool {
	if actorType != "agent" || !actorID.Valid {
		return false
	}
	_, ok := v.agentSet[actorID.Bytes]
	return ok
}

func (v visibilityViewer) canSee(vis string, rel permission.Relation) bool {
	if v.bypasses() {
		return true
	}
	return permission.CanSee(v.role, permission.Visibility(vis), rel)
}

// canSeeIssueFields is the Go twin of issueVisibilitySQL, taking the columns
// the decision needs rather than a row type. sqlc gives each list query its
// own row struct, and every one of them has to answer this question the same
// way. The issue id is what a direct share (resource_share) names.
func (v visibilityViewer) canSeeIssueFields(
	issueID pgtype.UUID, visibility, creatorType string, creatorID, projectID pgtype.UUID,
	assigneeType string, assigneeID pgtype.UUID,
) bool {
	rel := v.relation(creatorType, creatorID, projectID)
	rel.IsCreator = rel.IsCreator || v.isOwnedAgent(creatorType, creatorID)
	rel.IsAssignee = v.isMe(assigneeType, assigneeID) || v.isOwnedAgent(assigneeType, assigneeID)
	if issueID.Valid {
		_, rel.SharedWith = v.sharedIssues[uuidToString(issueID)]
	}
	return v.canSee(visibility, rel)
}

// canSeeIssue is canSeeIssueFields over a full issue row.
func (v visibilityViewer) canSeeIssue(issue db.Issue) bool {
	return v.canSeeIssueFields(issue.ID,
		issue.Visibility, issue.CreatorType, issue.CreatorID, issue.ProjectID,
		issue.AssigneeType.String, issue.AssigneeID)
}

// canSeeProject: a project is its own project, so "in project" means the
// viewer can reach this project.
//
// Two project-only widenings on top of the generic matrix (DENE-698 follow-up):
//
//   - The workspace owner sees every project in their own workspace, at any
//     scope. "仅我可见" means "only me" where me is the workspace owner; a
//     private project the owner cannot see is a lockout, not privacy — the very
//     bug this branch fixes. This never widens to admins or to human friends
//     (member/guest); they stay bound by the scope tiers below.
//   - A project created or led by an agent this viewer owns counts as the
//     viewer's own, exactly as an owned agent's issues do (canSeeIssueFields).
//     An agent is its human's extension, so putting one in the lead seat must
//     not hide a private project from the person that agent belongs to.
func (v visibilityViewer) canSeeProject(p db.Project) bool {
	if !v.bypasses() && v.role == permission.RoleOwner {
		return true
	}
	isCreator := (p.CreatedBy.Valid && v.userID.Valid && p.CreatedBy.Bytes == v.userID.Bytes) ||
		v.isOwnedAgent(p.LeadType.String, p.LeadID)
	return v.canSee(p.Visibility, permission.Relation{
		IsCreator:    isCreator,
		InProject:    v.inProject(p.ID),
		LeadsProject: p.LeadType.Valid && p.LeadType.String == "member" && p.LeadID.Valid && v.userID.Valid && p.LeadID.Bytes == v.userID.Bytes,
	})
}

// canChangeProjectVisibility applies canSeeProject's two widenings to the
// write side: a viewer let in by them must also be able to reshare. Without
// this the owner could see a private project nobody created (or one an owned
// agent leads) yet every PUT /visibility answered 403.
func (v visibilityViewer) canChangeProjectVisibility(p db.Project) bool {
	if v.bypasses() || v.role == permission.RoleOwner {
		return true
	}
	isCreator := (p.CreatedBy.Valid && v.userID.Valid && p.CreatedBy.Bytes == v.userID.Bytes) ||
		v.isOwnedAgent(p.LeadType.String, p.LeadID)
	return permission.Allowed(v.role, permission.ActionChangeVisibility, permission.Visibility(p.Visibility), permission.Relation{
		IsCreator: isCreator,
		InProject: v.inProject(p.ID),
	})
}

// canSeeRepo takes the repo's projects as a set because a workspace repo can
// sit in several (migration 511): being in any one of them is enough.
func (v visibilityViewer) canSeeRepo(entry workspaceRepoRef, repoProjectIDs []pgtype.UUID) bool {
	rel := permission.Relation{
		IsCreator: entry.CreatedBy != "" && entry.CreatedBy == uuidToString(v.userID),
	}
	for _, id := range repoProjectIDs {
		if v.inProject(id) {
			rel.InProject = true
			break
		}
	}
	_, rel.SharedWith = v.sharedRepos[entry.URL]
	return v.canSee(entry.Visibility, rel)
}

func (v visibilityViewer) filterIssues(issues []db.Issue) []db.Issue {
	if v.bypasses() {
		return issues
	}
	out := make([]db.Issue, 0, len(issues))
	for _, issue := range issues {
		if v.canSeeIssue(issue) {
			out = append(out, issue)
		}
	}
	return out
}

func (v visibilityViewer) filterProjects(projects []db.Project) []db.Project {
	if v.bypasses() {
		return projects
	}
	out := make([]db.Project, 0, len(projects))
	for _, p := range projects {
		if v.canSeeProject(p) {
			out = append(out, p)
		}
	}
	return out
}

// issueVisibilitySQL renders the scope filter as a SQL predicate over an issue
// alias, so a list filters before it paginates instead of paging through rows
// the caller cannot see. addArg binds a value and returns its placeholder.
//
// The disjunction mirrors permission.CanSee exactly:
//
//	creator OR assignee OR (workspace scope, unless guest)
//	OR (project scope AND reachable)
//
// An unknown tier yields FALSE — fail closed, same as the matrix.
func (v visibilityViewer) issueVisibilitySQL(alias string, addArg func(any) string) string {
	if v.bypasses() {
		return "TRUE"
	}
	if !v.role.Valid() || !v.userID.Valid {
		return "FALSE"
	}
	parts := []string{
		fmt.Sprintf("(%s.creator_type = 'member' AND %s.creator_id = %s::uuid)",
			alias, alias, addArg(v.userID)),
		fmt.Sprintf("(%s.assignee_type = 'member' AND %s.assignee_id = %s::uuid)",
			alias, alias, addArg(v.userID)),
		fmt.Sprintf("(%s.creator_type = 'agent' AND EXISTS (SELECT 1 FROM agent owner_agent WHERE owner_agent.id = %s.creator_id AND owner_agent.workspace_id = %s.workspace_id AND owner_agent.owner_id = %s::uuid))",
			alias, alias, alias, addArg(v.userID)),
		fmt.Sprintf("(%s.assignee_type = 'agent' AND EXISTS (SELECT 1 FROM agent assignee_agent WHERE assignee_agent.id = %s.assignee_id AND assignee_agent.workspace_id = %s.workspace_id AND assignee_agent.owner_id = %s::uuid))",
			alias, alias, alias, addArg(v.userID)),
	}
	if v.role != permission.RoleGuest {
		parts = append(parts, fmt.Sprintf("%s.visibility = 'workspace'", alias))
	}
	if len(v.projectIDs) > 0 {
		parts = append(parts, fmt.Sprintf(
			"(%s.visibility = 'project' AND %s.project_id = ANY(%s::uuid[]))",
			alias, alias, addArg(v.projectIDs)))
	}
	parts = append(parts, fmt.Sprintf(
		"(%s.visibility = 'project' AND EXISTS (SELECT 1 FROM resource_share share WHERE share.workspace_id = %s.workspace_id AND share.resource_type = 'issue' AND share.resource_id = %s.id::text AND share.member_id = %s::uuid))",
		alias, alias, alias, addArg(v.userID)))
	return "(" + strings.Join(parts, " OR ") + ")"
}

// projectVisibilitySQL is the same filter for a project alias. A project's own
// id is what "in project" means for it.
func (v visibilityViewer) projectVisibilitySQL(alias string, addArg func(any) string) string {
	if v.bypasses() {
		return "TRUE"
	}
	if !v.role.Valid() || !v.userID.Valid {
		return "FALSE"
	}
	// Owner sees every project in the workspace — the Go twin's owner branch.
	if v.role == permission.RoleOwner {
		return "TRUE"
	}
	parts := []string{
		fmt.Sprintf("%s.created_by = %s::uuid", alias, addArg(v.userID)),
		// A project led by an agent this viewer owns counts as the viewer's own,
		// mirroring the agent clauses in issueVisibilitySQL and isOwnedAgent.
		fmt.Sprintf("(%s.lead_type = 'agent' AND EXISTS (SELECT 1 FROM agent lead_agent WHERE lead_agent.id = %s.lead_id AND lead_agent.workspace_id = %s.workspace_id AND lead_agent.owner_id = %s::uuid))",
			alias, alias, alias, addArg(v.userID)),
	}
	if v.role != permission.RoleGuest {
		parts = append(parts, fmt.Sprintf("%s.visibility = 'workspace'", alias))
	}
	if len(v.projectIDs) > 0 {
		parts = append(parts, fmt.Sprintf(
			"(%s.visibility = 'project' AND %s.id = ANY(%s::uuid[]))",
			alias, alias, addArg(v.projectIDs)))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// hiddenIssueNotFound writes the deny response for an issue the caller cannot
// see. It is deliberately byte-identical to the response for an issue that
// does not exist.
func hiddenIssueNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "issue not found")
}

// requireIssueVisible gates a single already-loaded issue. It reports false
// after writing the deny response.
//
// A viewer that could not be built is treated as seeing nothing: an error
// loading the caller's tier must never degrade into an unfiltered read.
func (h *Handler) requireIssueVisible(w http.ResponseWriter, r *http.Request, issue db.Issue) bool {
	viewer, err := h.visibilityViewerFor(r, issue.WorkspaceID)
	if err != nil {
		hiddenIssueNotFound(w)
		return false
	}
	if !viewer.canSeeIssue(issue) {
		hiddenIssueNotFound(w)
		return false
	}
	return true
}

// parseUUIDSafe is util.ParseUUID under the name this file's callers expect;
// handler code must never use the panicking parseUUID on request input.
func parseUUIDSafe(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	if !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid uuid %q", s)
	}
	return id, nil
}

// visibleWorkspaceRepos narrows a workspace's repo registry to what the caller
// may see (DENE-698). A repository is a JSONB entry in workspace.repos rather
// than a row, so its scope travels on the entry and "belongs to a project" is
// answered by the project_resource rows pointing at its URL.
//
// This is deliberately applied at the GET paths rather than inside
// workspaceToResponse: that function also builds the workspace:updated
// broadcast, which has one payload for every recipient and therefore cannot
// carry a per-caller answer. Known debt, recorded in
// docs/kun/permission-model.md.
func (h *Handler) visibleWorkspaceRepos(r *http.Request, ws db.Workspace) []workspaceRepoRef {
	entries := decodeWorkspaceRepos(ws.Repos)
	if len(entries) == 0 {
		return []workspaceRepoRef{}
	}
	viewer, err := h.visibilityViewerFor(r, ws.ID)
	if err != nil {
		return []workspaceRepoRef{}
	}
	visible := make([]workspaceRepoRef, 0, len(entries))
	for _, entry := range entries {
		projectIDs := h.repoProjectIDs(r.Context(), ws.ID, entry.URL)
		if len(projectIDs) > 0 {
			entry.ProjectID = uuidToString(projectIDs[0])
		}
		if viewer.bypasses() || viewer.canSeeRepo(entry, projectIDs) {
			visible = append(visible, entry)
		}
	}
	return visible
}
