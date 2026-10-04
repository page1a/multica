package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/permission"
)

// Sharing access reads (kun fork, DENE-1214): what the share button needs to
// explain itself before anyone clicks — the scope, how many people it reaches,
// and whether the caller may change it. The answer comes from the same rule
// the PUT /visibility handlers enforce, so the button and the save agree.

// Reasons a caller cannot change the scope. The client turns these into copy;
// it never re-derives them.
const (
	accessReasonGuest      = "guest"
	accessReasonNotCreator = "not_creator"
)

type SharingAccessResponse struct {
	Visibility   string  `json:"visibility"`
	AudienceSize int32   `json:"audience_size"`
	CanChange    bool    `json:"can_change"`
	Reason       *string `json:"reason"`
}

func sharingAccessReason(viewer visibilityViewer, canChange bool) *string {
	if canChange {
		return nil
	}
	reason := accessReasonNotCreator
	if viewer.role == permission.RoleGuest {
		reason = accessReasonGuest
	}
	return &reason
}

// GetIssueAccess: GET /api/issues/{id}/access.
func (h *Handler) GetIssueAccess(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, issue.WorkspaceID)
	if err != nil || !viewer.canSeeIssue(issue) {
		hiddenIssueNotFound(w)
		return
	}
	canChange := viewer.canChangeIssueVisibility(issue)
	writeJSON(w, http.StatusOK, SharingAccessResponse{
		Visibility: issue.Visibility,
		AudienceSize: h.resourceAudienceSize(r.Context(), issue.WorkspaceID,
			permission.Visibility(issue.Visibility), issue.ProjectID, "issue", uuidToString(issue.ID)),
		CanChange: canChange,
		Reason:    sharingAccessReason(viewer, canChange),
	})
}

// GetProjectAccess: GET /api/projects/{id}/access.
func (h *Handler) GetProjectAccess(w http.ResponseWriter, r *http.Request) {
	project, viewer, ok := h.loadProjectForVisibility(w, r)
	if !ok {
		return
	}
	canChange := viewer.canChangeProjectVisibility(project)
	writeJSON(w, http.StatusOK, SharingAccessResponse{
		Visibility:   project.Visibility,
		AudienceSize: h.audienceSize(r.Context(), project.WorkspaceID, permission.Visibility(project.Visibility), project.ID),
		CanChange:    canChange,
		Reason:       sharingAccessReason(viewer, canChange),
	})
}
