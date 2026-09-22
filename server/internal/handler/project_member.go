package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ProjectMemberResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	MemberID    string  `json:"member_id"`
	AddedBy     *string `json:"added_by"`
	CreatedAt   string  `json:"created_at"`
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	AvatarURL   *string `json:"avatar_url"`
}

func canManageProjectMembers(member db.Member, project db.Project) bool {
	if roleAllowed(member.Role, "owner", "admin") {
		return true
	}
	return project.LeadType.Valid && project.LeadType.String == "member" &&
		project.LeadID.Valid && uuidToString(project.LeadID) == uuidToString(member.UserID)
}

func (h *Handler) loadProjectMemberContext(w http.ResponseWriter, r *http.Request, write bool) (db.Project, db.Member, bool) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(project.WorkspaceID), "workspace not found")
	if !ok {
		return db.Project{}, db.Member{}, false
	}
	if write && !canManageProjectMembers(member, project) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Project{}, db.Member{}, false
	}
	return project, member, true
}

func (h *Handler) projectMemberRowToResponse(row db.ListProjectMembersRow) ProjectMemberResponse {
	return ProjectMemberResponse{
		ID:          uuidToString(row.ID),
		WorkspaceID: uuidToString(row.WorkspaceID),
		ProjectID:   uuidToString(row.ProjectID),
		MemberID:    uuidToString(row.MemberID),
		AddedBy:     uuidToPtr(row.AddedBy),
		CreatedAt:   timestampToString(row.CreatedAt),
		Name:        row.UserName,
		Email:       row.UserEmail,
		AvatarURL:   h.resolveAvatarURLPtr(textToPtr(row.UserAvatarUrl)),
	}
}

func (h *Handler) projectMemberToResponse(m db.ProjectMember, user db.User) ProjectMemberResponse {
	return ProjectMemberResponse{
		ID:          uuidToString(m.ID),
		WorkspaceID: uuidToString(m.WorkspaceID),
		ProjectID:   uuidToString(m.ProjectID),
		MemberID:    uuidToString(m.MemberID),
		AddedBy:     uuidToPtr(m.AddedBy),
		CreatedAt:   timestampToString(m.CreatedAt),
		Name:        user.Name,
		Email:       user.Email,
		AvatarURL:   h.resolveAvatarURLPtr(textToPtr(user.AvatarUrl)),
	}
}

func (h *Handler) ListProjectMembers(w http.ResponseWriter, r *http.Request) {
	project, _, ok := h.loadProjectMemberContext(w, r, false)
	if !ok {
		return
	}
	rows, err := h.Queries.ListProjectMembers(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project members")
		return
	}
	resp := make([]ProjectMemberResponse, len(rows))
	for i, row := range rows {
		resp[i] = h.projectMemberRowToResponse(row)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) AddProjectMember(w http.ResponseWriter, r *http.Request) {
	project, actor, ok := h.loadProjectMemberContext(w, r, true)
	if !ok {
		return
	}

	var req struct {
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MemberID == "" {
		writeError(w, http.StatusBadRequest, "member_id is required")
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, req.MemberID, "member_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      memberUUID,
		WorkspaceID: project.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "member not found in this workspace")
		return
	}

	sm, err := h.Queries.AddProjectMember(r.Context(), db.AddProjectMemberParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
		MemberID:    memberUUID,
		AddedBy:     actor.UserID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			sm, err = h.Queries.GetProjectMember(r.Context(), db.GetProjectMemberParams{
				ProjectID: project.ID,
				MemberID:  memberUUID,
			})
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add project member")
			return
		}
	}

	user, err := h.Queries.GetUser(r.Context(), memberUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}

	// Joining a project is a sharing change for the person who joined: every
	// 'project'-scoped resource this project holds becomes visible to them
	// now, not when a cache expires (DENE-698).
	h.invalidateSharingCaches(r.Context(), project.WorkspaceID, memberUUID)

	h.publish(protocol.EventProjectUpdated, uuidToString(project.WorkspaceID), "member", uuidToString(actor.UserID), map[string]any{
		"project_id": uuidToString(project.ID),
	})
	writeJSON(w, http.StatusCreated, h.projectMemberToResponse(sm, user))
}

func (h *Handler) RemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	project, actor, ok := h.loadProjectMemberContext(w, r, true)
	if !ok {
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memberId"), "member_id")
	if !ok {
		return
	}
	rows, err := h.Queries.RemoveProjectMember(r.Context(), db.RemoveProjectMemberParams{
		WorkspaceID: project.WorkspaceID,
		ProjectID:   project.ID,
		MemberID:    memberUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove project member")
		return
	}
	if rows == 0 {
		writeError(w, http.StatusNotFound, "project member not found")
		return
	}
	// Leaving a project narrows what this person can see; the same caches have
	// to drop for the narrowing to take effect immediately.
	h.invalidateSharingCaches(r.Context(), project.WorkspaceID, memberUUID)
	h.publish(protocol.EventProjectUpdated, uuidToString(project.WorkspaceID), "member", uuidToString(actor.UserID), map[string]any{
		"project_id": uuidToString(project.ID),
	})
	w.WriteHeader(http.StatusNoContent)
}
