package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/permission"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Direct shares (kun fork): the "specific people" scope for an issue or repo.
//
// A 'project'-scoped issue or repo reaches its project's people plus whoever
// is named here. That makes the scope usable without a project too: an issue
// that sits in no project can still be shared with just JEFF. Changing who is
// on the list is the same action as changing the scope itself, so it takes the
// same tier rule (permission.ActionChangeVisibility).

type ResourceShareResponse struct {
	MemberID  string  `json:"member_id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	AvatarURL *string `json:"avatar_url"`
	AddedBy   *string `json:"added_by"`
	CreatedAt string  `json:"created_at"`
}

// shareTarget is one resource whose direct-share list is being read or
// written, already checked against the caller.
type shareTarget struct {
	workspaceID  pgtype.UUID
	resourceType string
	resourceID   string
	canManage    bool
	// issueID is set for an issue target so the realtime frames can name it.
	issueID string
}

func (h *Handler) loadIssueShareTarget(w http.ResponseWriter, r *http.Request) (shareTarget, bool) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return shareTarget{}, false
	}
	viewer, err := h.visibilityViewerFor(r, issue.WorkspaceID)
	if err != nil || !viewer.canSeeIssue(issue) {
		hiddenIssueNotFound(w)
		return shareTarget{}, false
	}
	rel := viewer.relation(issue.CreatorType, issue.CreatorID, issue.ProjectID)
	rel.IsCreator = rel.IsCreator || viewer.isOwnedAgent(issue.CreatorType, issue.CreatorID)
	_, rel.SharedWith = viewer.sharedIssues[uuidToString(issue.ID)]
	return shareTarget{
		workspaceID:  issue.WorkspaceID,
		resourceType: "issue",
		resourceID:   uuidToString(issue.ID),
		canManage: viewer.bypasses() ||
			permission.Allowed(viewer.role, permission.ActionChangeVisibility, permission.Visibility(issue.Visibility), rel),
		issueID: uuidToString(issue.ID),
	}, true
}

func (h *Handler) loadRepoShareTarget(w http.ResponseWriter, r *http.Request, url string) (shareTarget, bool) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return shareTarget{}, false
	}
	url = strings.TrimSpace(url)
	if url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return shareTarget{}, false
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return shareTarget{}, false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "repository not found")
		return shareTarget{}, false
	}
	projectIDs := h.repoProjectIDs(r.Context(), wsUUID, url)
	for _, entry := range decodeWorkspaceRepos(ws.Repos) {
		if entry.URL != url {
			continue
		}
		if !viewer.canSeeRepo(entry, projectIDs) {
			break
		}
		return shareTarget{
			workspaceID:  wsUUID,
			resourceType: "repo",
			resourceID:   url,
			canManage:    viewer.canChangeRepoVisibility(entry, projectIDs),
		}, true
	}
	writeError(w, http.StatusNotFound, "repository not found")
	return shareTarget{}, false
}

func (h *Handler) listShares(w http.ResponseWriter, r *http.Request, t shareTarget) {
	rows, err := h.Queries.ListResourceShares(r.Context(), db.ListResourceSharesParams{
		WorkspaceID:  t.workspaceID,
		ResourceType: t.resourceType,
		ResourceID:   t.resourceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	out := make([]ResourceShareResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, ResourceShareResponse{
			MemberID:  uuidToString(row.MemberID),
			Name:      row.UserName,
			Email:     row.UserEmail,
			AvatarURL: h.resolveAvatarURLPtr(textToPtr(row.UserAvatarUrl)),
			AddedBy:   uuidToPtr(row.AddedBy),
			CreatedAt: timestampToString(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) addShare(w http.ResponseWriter, r *http.Request, t shareTarget, memberID string) {
	if !t.canManage {
		writeError(w, http.StatusForbidden, "you cannot change who this is shared with")
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(memberID), "member_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      memberUUID,
		WorkspaceID: t.workspaceID,
	}); err != nil {
		writeError(w, http.StatusBadRequest, "member not found in this workspace")
		return
	}
	actorUUID, _ := parseUUIDSafe(requestUserID(r))
	params := db.AddResourceShareParams{
		WorkspaceID:  t.workspaceID,
		ResourceType: t.resourceType,
		ResourceID:   t.resourceID,
		MemberID:     memberUUID,
		AddedBy:      actorUUID,
	}
	share, err := h.Queries.AddResourceShare(r.Context(), params)
	if errors.Is(err, pgx.ErrNoRows) {
		share, err = h.Queries.GetResourceShare(r.Context(), db.GetResourceShareParams{
			WorkspaceID:  t.workspaceID,
			ResourceType: t.resourceType,
			ResourceID:   t.resourceID,
			MemberID:     memberUUID,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to share")
		return
	}
	user, err := h.Queries.GetUser(r.Context(), memberUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}
	h.afterShareChange(r, t, memberUUID)
	writeJSON(w, http.StatusCreated, ResourceShareResponse{
		MemberID:  uuidToString(share.MemberID),
		Name:      user.Name,
		Email:     user.Email,
		AvatarURL: h.resolveAvatarURLPtr(textToPtr(user.AvatarUrl)),
		AddedBy:   uuidToPtr(share.AddedBy),
		CreatedAt: timestampToString(share.CreatedAt),
	})
}

func (h *Handler) removeShare(w http.ResponseWriter, r *http.Request, t shareTarget, memberID string) {
	if !t.canManage {
		writeError(w, http.StatusForbidden, "you cannot change who this is shared with")
		return
	}
	memberUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(memberID), "member_id")
	if !ok {
		return
	}
	if _, err := h.Queries.RemoveResourceShare(r.Context(), db.RemoveResourceShareParams{
		WorkspaceID:  t.workspaceID,
		ResourceType: t.resourceType,
		ResourceID:   t.resourceID,
		MemberID:     memberUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove share")
		return
	}
	h.afterShareChange(r, t, memberUUID)
	w.WriteHeader(http.StatusNoContent)
}

// afterShareChange tells the one person whose access moved. Their cached
// membership answer is dropped, and their open tab is told to refetch: an
// issue through a targeted invalidation, a repo through the workspace
// snapshot (repos travel inside it).
func (h *Handler) afterShareChange(r *http.Request, t shareTarget, memberUUID pgtype.UUID) {
	ctx := r.Context()
	h.invalidateSharingCaches(ctx, t.workspaceID, memberUUID)
	wsID := uuidToString(t.workspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	switch t.resourceType {
	case "issue":
		h.publishIssueInvalidated(wsID, actorType, actorID, t.issueID, "", uuidToString(memberUUID))
	case "repo":
		h.publishWorkspaceSnapshot(ctx, t.workspaceID, wsID, actorType, actorID)
	}
}

// deleteRepoShares drops the share rows of repositories that left the
// workspace's repo list, so a later repo with the same URL does not inherit
// them.
func (h *Handler) deleteRepoShares(ctx context.Context, wsUUID pgtype.UUID, urls []string) {
	for _, url := range urls {
		_ = h.Queries.DeleteResourceSharesByResource(ctx, db.DeleteResourceSharesByResourceParams{
			WorkspaceID:  wsUUID,
			ResourceType: "repo",
			ResourceID:   url,
		})
	}
}

// ---------------------------------------------------------------------------
// /api/issues/{id}/shares
// ---------------------------------------------------------------------------

func (h *Handler) ListIssueShares(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.loadIssueShareTarget(w, r); ok {
		h.listShares(w, r, t)
	}
}

func (h *Handler) AddIssueShare(w http.ResponseWriter, r *http.Request) {
	t, ok := h.loadIssueShareTarget(w, r)
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
	h.addShare(w, r, t, req.MemberID)
}

func (h *Handler) RemoveIssueShare(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.loadIssueShareTarget(w, r); ok {
		h.removeShare(w, r, t, chi.URLParam(r, "memberId"))
	}
}

// ---------------------------------------------------------------------------
// /api/repos/shares — a repo is keyed by URL, which does not fit a path segment.
// ---------------------------------------------------------------------------

func (h *Handler) ListRepoShares(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.loadRepoShareTarget(w, r, r.URL.Query().Get("url")); ok {
		h.listShares(w, r, t)
	}
}

func (h *Handler) AddRepoShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if t, ok := h.loadRepoShareTarget(w, r, req.URL); ok {
		h.addShare(w, r, t, req.MemberID)
	}
}

func (h *Handler) RemoveRepoShare(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if t, ok := h.loadRepoShareTarget(w, r, q.Get("url")); ok {
		h.removeShare(w, r, t, q.Get("member_id"))
	}
}
