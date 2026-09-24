package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/permission"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Saved issue views (MUL-4796): server-backed filter definitions with
// display defaults. `query` is the shared identity of a view; `display`
// only seeds a member's first open. Both are opaque JSON to the server —
// interpretation lives in the client definition_version contract.

const issueViewNameMaxLen = 80

// issueViewBodyMaxBytes bounds every saved-view write. The query/display
// blobs are filter documents — real ones are a few KB; the cap is the
// abuse backstop, comfortably above any legitimate payload.
const issueViewBodyMaxBytes = 128 * 1024

// issueViewsPerOwnerMax bounds table growth per member per workspace.
const issueViewsPerOwnerMax = 100

var (
	validIssueViewScopeTypes        = []string{"workspace", "my", "project"}
	validIssueViewMyVariants        = []string{"assigned", "created", "involved", "any"}
	validIssueViewWorkspaceVariants = []string{"members", "agents"}
	validIssueViewVisibilities      = []string{"private", "workspace", "project"}
)

// validateIssueViewVariant returns the pgtype value for a scope_variant
// input under the given scope_type, or ok=false when the pairing is
// invalid. Workspace and project variants are optional assignee-type
// narrowing (NULL = the unrestricted All tab); my variants are required.
func validateIssueViewVariant(scopeType string, variant *string) (pgtype.Text, bool) {
	switch scopeType {
	case "my":
		if variant == nil || !contains(validIssueViewMyVariants, *variant) {
			return pgtype.Text{}, false
		}
		return pgtype.Text{String: *variant, Valid: true}, true
	default: // workspace, project
		if variant == nil || *variant == "" || *variant == "all" {
			return pgtype.Text{}, true
		}
		if !contains(validIssueViewWorkspaceVariants, *variant) {
			return pgtype.Text{}, false
		}
		return pgtype.Text{String: *variant, Valid: true}, true
	}
}

type IssueViewResponse struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	OwnerID           string          `json:"owner_id"`
	Name              string          `json:"name"`
	ScopeType         string          `json:"scope_type"`
	ScopeID           *string         `json:"scope_id"`
	ScopeVariant      *string         `json:"scope_variant"`
	Visibility        string          `json:"visibility"`
	DefinitionVersion int32           `json:"definition_version"`
	Query             json.RawMessage `json:"query"`
	Display           json.RawMessage `json:"display"`
	Revision          int32           `json:"revision"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

func issueViewToResponse(v db.IssueView) IssueViewResponse {
	return IssueViewResponse{
		ID:                uuidToString(v.ID),
		WorkspaceID:       uuidToString(v.WorkspaceID),
		OwnerID:           uuidToString(v.OwnerID),
		Name:              v.Name,
		ScopeType:         v.ScopeType,
		ScopeID:           uuidToPtr(v.ScopeID),
		ScopeVariant:      textToPtr(v.ScopeVariant),
		Visibility:        v.Visibility,
		DefinitionVersion: v.DefinitionVersion,
		Query:             json.RawMessage(v.Query),
		Display:           json.RawMessage(v.Display),
		Revision:          v.Revision,
		CreatedAt:         timestampToString(v.CreatedAt),
		UpdatedAt:         timestampToString(v.UpdatedAt),
	}
}

// canReadIssueView: owner always; workspace-shared views for any member
// except a guest; project-shared views when the caller is in projectIDs
// (explicit members plus lead, or every workspace project for owner/admin).
// My-scope views are constrained to private by the DB CHECK, so they only
// ever match the owner branch.
//
// "workspace" does not include guests (DENE-695): a project is the only way
// to show a guest anything, so a workspace-shared view is invisible to them
// even though they are members of the workspace. role is the caller's tier;
// permission.CanSee is the matrix this mirrors.
func canReadIssueView(v db.IssueView, userID pgtype.UUID, role string, projectIDs []pgtype.UUID) bool {
	if v.OwnerID == userID {
		return true
	}
	if v.Visibility == "workspace" {
		return permission.CanSee(permission.Role(role), permission.VisibilityWorkspace, permission.Relation{})
	}
	if v.Visibility != "project" || v.ScopeType != "project" || !v.ScopeID.Valid {
		return false
	}
	for _, id := range projectIDs {
		if id.Valid && id.Bytes == v.ScopeID.Bytes {
			return true
		}
	}
	return false
}

func (h *Handler) userCanReadIssueView(ctx context.Context, view db.IssueView, wsUUID pgtype.UUID, userID string) bool {
	userUUID := parseUUID(userID)
	role := h.workspaceRole(ctx, wsUUID, userUUID)
	if canReadIssueView(view, userUUID, role, nil) {
		return true
	}
	if view.Visibility != "project" {
		return false
	}
	ids, err := h.listAccessibleProjectIDs(ctx, wsUUID, userUUID)
	if err != nil {
		return false
	}
	return canReadIssueView(view, userUUID, role, ids)
}

// workspaceRole reads the caller's tier for visibility decisions. An
// unreadable membership yields "", which permission.CanSee treats as an
// unknown tier and denies — the same answer as not being a member.
func (h *Handler) workspaceRole(ctx context.Context, wsUUID, userUUID pgtype.UUID) string {
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return ""
	}
	return member.Role
}

// listAccessibleProjectIDs is the caller's project set for visibility='project'
// reads: explicit project_member rows plus projects they lead, merged once.
// The workspace owner gets every project in the workspace. Admins do not:
// "specific people" means the people picked, so an admin sees a project-scoped
// resource only when they were added to it (kun fork). Never JOIN this into
// ListIssueViewsForUser.
func (h *Handler) listAccessibleProjectIDs(ctx context.Context, wsUUID, userUUID pgtype.UUID) ([]pgtype.UUID, error) {
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userUUID,
		WorkspaceID: wsUUID,
	})
	if err == nil && roleAllowed(member.Role, "owner") {
		ids, err := h.Queries.ListProjectIDsInWorkspace(ctx, wsUUID)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			return []pgtype.UUID{}, nil
		}
		return ids, nil
	}

	memberships, err := h.Queries.ListProjectMembershipsForUser(ctx, db.ListProjectMembershipsForUserParams{
		WorkspaceID: wsUUID,
		MemberID:    userUUID,
	})
	if err != nil {
		return nil, err
	}
	led, err := h.Queries.ListProjectIDsLedByMember(ctx, db.ListProjectIDsLedByMemberParams{
		WorkspaceID: wsUUID,
		LeadID:      userUUID,
	})
	if err != nil {
		return nil, err
	}
	return mergeProjectIDs(memberships, led), nil
}

func mergeProjectIDs(parts ...[]pgtype.UUID) []pgtype.UUID {
	seen := make(map[[16]byte]struct{})
	out := []pgtype.UUID{}
	for _, part := range parts {
		for _, id := range part {
			if !id.Valid {
				continue
			}
			if _, ok := seen[id.Bytes]; ok {
				continue
			}
			seen[id.Bytes] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func rejectProjectVisibilityOffProject(w http.ResponseWriter, visibility, scopeType string) bool {
	if visibility == "project" && scopeType != "project" {
		writeError(w, http.StatusBadRequest, "project visibility is only valid for project views")
		return true
	}
	return false
}

func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	// JSON null unmarshals into a nil map without error — reject it here
	// instead of letting the DB CHECK turn it into a 500.
	return m != nil
}

type CreateIssueViewRequest struct {
	Name              string          `json:"name"`
	ScopeType         string          `json:"scope_type"`
	ScopeID           *string         `json:"scope_id"`
	ScopeVariant      *string         `json:"scope_variant"`
	Visibility        string          `json:"visibility"`
	DefinitionVersion int32           `json:"definition_version"`
	Query             json.RawMessage `json:"query"`
	Display           json.RawMessage `json:"display"`
}

func (h *Handler) CreateIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}

	var req CreateIssueViewRequest
	r.Body = http.MaxBytesReader(w, r.Body, issueViewBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if l := len([]rune(req.Name)); l < 1 || l > issueViewNameMaxLen {
		writeError(w, http.StatusBadRequest, "name must be between 1 and 80 characters")
		return
	}
	ownedCount, err := h.Queries.CountIssueViewsByOwner(r.Context(), db.CountIssueViewsByOwnerParams{
		WorkspaceID: wsUUID,
		OwnerID:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check view quota")
		return
	}
	if ownedCount >= issueViewsPerOwnerMax {
		writeError(w, http.StatusBadRequest, "view limit reached for this workspace")
		return
	}
	if !contains(validIssueViewScopeTypes, req.ScopeType) {
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if !contains(validIssueViewVisibilities, req.Visibility) {
		writeError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	if rejectProjectVisibilityOffProject(w, req.Visibility, req.ScopeType) {
		return
	}
	if req.DefinitionVersion <= 0 {
		req.DefinitionVersion = 1
	}
	if len(req.Query) == 0 || !isJSONObject(req.Query) {
		writeError(w, http.StatusBadRequest, "query must be a JSON object")
		return
	}
	if len(req.Display) == 0 {
		req.Display = json.RawMessage("{}")
	}
	if !isJSONObject(req.Display) {
		writeError(w, http.StatusBadRequest, "display must be a JSON object")
		return
	}

	scopeVariant, variantOK := validateIssueViewVariant(req.ScopeType, req.ScopeVariant)
	if !variantOK {
		writeError(w, http.StatusBadRequest, "invalid scope_variant for this scope_type")
		return
	}
	var scopeID pgtype.UUID
	switch req.ScopeType {
	case "project":
		if req.ScopeID == nil {
			writeError(w, http.StatusBadRequest, "scope_id is required for project views")
			return
		}
		projUUID, ok := parseUUIDOrBadRequest(w, *req.ScopeID, "scope_id")
		if !ok {
			return
		}
		// The project must exist in this workspace — a view on a foreign or
		// deleted project would be unreachable and could leak across tenants.
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: projUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		scopeID = projUUID
	case "my":
		// My Issues is a per-user perspective; sharing it is meaningless.
		req.Visibility = "private"
	}

	view, err := h.Queries.CreateIssueView(r.Context(), db.CreateIssueViewParams{
		WorkspaceID:       wsUUID,
		OwnerID:           parseUUID(userID),
		Name:              req.Name,
		ScopeType:         req.ScopeType,
		ScopeID:           scopeID,
		ScopeVariant:      scopeVariant,
		Visibility:        req.Visibility,
		DefinitionVersion: req.DefinitionVersion,
		Query:             req.Query,
		Display:           req.Display,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create view")
		return
	}
	writeJSON(w, http.StatusCreated, issueViewToResponse(view))
}

func (h *Handler) ListIssueViews(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}

	scopeType := r.URL.Query().Get("scope_type")
	if !contains(validIssueViewScopeTypes, scopeType) {
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return
	}
	var scopeID pgtype.UUID
	if raw := r.URL.Query().Get("scope_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "scope_id")
		if !ok {
			return
		}
		scopeID = id
	}

	projectIDs, err := h.listAccessibleProjectIDs(r.Context(), wsUUID, parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list views")
		return
	}
	views, err := h.Queries.ListIssueViewsForUser(r.Context(), db.ListIssueViewsForUserParams{
		WorkspaceID: wsUUID,
		ScopeType:   scopeType,
		OwnerID:     parseUUID(userID),
		ScopeID:     scopeID,
		ProjectIds:  projectIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list views")
		return
	}
	// ListIssueViewsForUser pre-filters in SQL but cannot express "workspace
	// does not include guests" without a second parameter on a query that is
	// already the hot path for every board load. canReadIssueView stays the
	// one authority: the SQL narrows, this re-checks, and the list can only
	// lose rows here, never gain them.
	role := h.workspaceRole(r.Context(), wsUUID, parseUUID(userID))
	resp := make([]IssueViewResponse, 0, len(views))
	for _, v := range views {
		if !canReadIssueView(v, parseUUID(userID), role, projectIDs) {
			continue
		}
		resp = append(resp, issueViewToResponse(v))
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadIssueViewForUser resolves the id, enforces workspace scoping, and
// applies read authorization. Unauthorized and missing are both 404 so a
// private view's existence never leaks.
func (h *Handler) loadIssueViewForUser(w http.ResponseWriter, r *http.Request, userID string) (db.IssueView, pgtype.UUID, bool) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.IssueView{}, pgtype.UUID{}, false
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "view id")
	if !ok {
		return db.IssueView{}, pgtype.UUID{}, false
	}
	view, err := h.Queries.GetIssueView(r.Context(), db.GetIssueViewParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil || !h.userCanReadIssueView(r.Context(), view, wsUUID, userID) {
		writeError(w, http.StatusNotFound, "view not found")
		return db.IssueView{}, pgtype.UUID{}, false
	}
	return view, wsUUID, true
}

func (h *Handler) GetIssueViewByID(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, _, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, issueViewToResponse(view))
}

// canManageIssueView: the owner, or a workspace admin/owner for shared
// (workspace or project) views. Project members do not gain manage
// rights. Private views of others are invisible (404 in the loader), so
// admin powers never reach them.
func (h *Handler) canManageIssueView(r *http.Request, view db.IssueView, userID string) bool {
	if view.OwnerID == parseUUID(userID) {
		return true
	}
	if view.Visibility != "workspace" && view.Visibility != "project" {
		return false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: view.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return roleAllowed(member.Role, "owner", "admin")
}

type UpdateIssueViewRequest struct {
	Name             *string         `json:"name"`
	Visibility       *string         `json:"visibility"`
	ScopeVariant     *string         `json:"scope_variant"`
	Query            json.RawMessage `json:"query"`
	Display          json.RawMessage `json:"display"`
	ExpectedRevision int32           `json:"expected_revision"`
}

func (h *Handler) UpdateIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, wsUUID, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	if !h.canManageIssueView(r, view, userID) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var req UpdateIssueViewRequest
	r.Body = http.MaxBytesReader(w, r.Body, issueViewBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedRevision <= 0 {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}

	name := view.Name
	if req.Name != nil {
		if l := len([]rune(*req.Name)); l < 1 || l > issueViewNameMaxLen {
			writeError(w, http.StatusBadRequest, "name must be between 1 and 80 characters")
			return
		}
		name = *req.Name
	}
	visibility := view.Visibility
	if req.Visibility != nil {
		if !contains(validIssueViewVisibilities, *req.Visibility) {
			writeError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		if rejectProjectVisibilityOffProject(w, *req.Visibility, view.ScopeType) {
			return
		}
		if view.ScopeType == "my" && *req.Visibility != "private" {
			writeError(w, http.StatusBadRequest, "my views are always private")
			return
		}
		visibility = *req.Visibility
	}
	query := json.RawMessage(view.Query)
	if len(req.Query) > 0 {
		if !isJSONObject(req.Query) {
			writeError(w, http.StatusBadRequest, "query must be a JSON object")
			return
		}
		query = req.Query
	}
	display := json.RawMessage(view.Display)
	if len(req.Display) > 0 {
		if !isJSONObject(req.Display) {
			writeError(w, http.StatusBadRequest, "display must be a JSON object")
			return
		}
		display = req.Display
	}
	// scope_type itself is immutable; the variant within it may switch.
	scopeVariant := view.ScopeVariant
	if req.ScopeVariant != nil {
		next, variantOK := validateIssueViewVariant(view.ScopeType, req.ScopeVariant)
		if !variantOK {
			writeError(w, http.StatusBadRequest, "invalid scope_variant for this scope_type")
			return
		}
		scopeVariant = next
	}

	updated, err := h.Queries.UpdateIssueView(r.Context(), db.UpdateIssueViewParams{
		ID:           view.ID,
		WorkspaceID:  wsUUID,
		Name:         name,
		Visibility:   visibility,
		ScopeVariant: scopeVariant,
		Query:        query,
		Display:      display,
		Revision:     req.ExpectedRevision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row exists (we just loaded it) — zero rows means the
			// revision moved underneath the caller.
			writeError(w, http.StatusConflict, "view was modified by someone else")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update view")
		return
	}
	writeJSON(w, http.StatusOK, issueViewToResponse(updated))
}

func (h *Handler) DeleteIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, wsUUID, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	if !h.canManageIssueView(r, view, userID) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	if _, err := h.Queries.DeleteIssueView(r.Context(), db.DeleteIssueViewParams{
		ID: view.ID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete view")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
