package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/gitconn"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/repoident"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type attachProjectRepoRequest struct {
	RepoURL           string  `json:"repo_url"`
	URL               string  `json:"url"`
	DefaultBranchHint string  `json:"default_branch_hint,omitempty"`
	Ref               string  `json:"ref,omitempty"`
	Label             *string `json:"label,omitempty"`
}

func (req attachProjectRepoRequest) repoURL() string {
	if url := strings.TrimSpace(req.RepoURL); url != "" {
		return url
	}
	return strings.TrimSpace(req.URL)
}

type projectRepoItem struct {
	Resource ProjectResourceResponse `json:"resource"`
	Repo     RepoReach               `json:"repo"`
}

type projectRepoAttachment struct {
	Resource   ProjectResourceResponse `json:"resource"`
	Repo       RepoReach               `json:"repo"`
	Created    bool                    `json:"created"`
	Registered bool                    `json:"registered"`
}

// ListProjectRepos returns the repositories attached to one project, each with its reach.
func (h *Handler) ListProjectRepos(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project repositories")
		return
	}
	viewer, err := h.visibilityViewerFor(r, project.WorkspaceID)
	var viewerPtr *visibilityViewer
	if err == nil {
		viewerPtr = &viewer
	}
	ws, _ := h.Queries.GetWorkspace(r.Context(), project.WorkspaceID)
	conns, _ := h.Queries.ListVCSConnectionsByWorkspace(r.Context(), project.WorkspaceID)
	out := make([]projectRepoItem, 0)
	for _, row := range rows {
		if row.ResourceType != "github_repo" {
			continue
		}
		out = append(out, projectRepoItem{
			Resource: projectResourceToResponse(row),
			Repo:     h.reachForResource(r, project.WorkspaceID, ws, row, conns, viewerPtr),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out, "total": len(out)})
}

// AttachProjectRepo registers a repository on the workspace (if it is new) and
// attaches it to the project. Repeating the same repository is a no-op.
func (h *Handler) AttachProjectRepo(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req attachProjectRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rawURL := req.repoURL()
	if rawURL == "" {
		writeError(w, http.StatusBadRequest, "repo_url is required")
		return
	}
	ref, err := validateGithubRepoRef(mustJSON(map[string]any{
		"url":                 rawURL,
		"default_branch_hint": strings.TrimSpace(req.DefaultBranchHint),
		"ref":                 strings.TrimSpace(req.Ref),
	}))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var stored struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(ref, &stored)
	actor := userID
	registered, err := h.registerWorkspaceRepo(r.Context(), project.WorkspaceID, stored.URL, actor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register workspace repository")
		return
	}
	key := projectRepoKey(ref)
	if existing, found := h.findProjectRepo(r.Context(), project.ID, key); found {
		h.writeProjectRepoAttachment(w, r, project, existing, false, registered)
		return
	}
	count, _ := h.Queries.CountProjectResources(r.Context(), project.ID)
	creator, _ := h.parseUserUUIDOrZero(userID)
	label := pgtype.Text{}
	if req.Label != nil && strings.TrimSpace(*req.Label) != "" {
		label = pgtype.Text{String: strings.TrimSpace(*req.Label), Valid: true}
	}
	row, err := h.Queries.CreateProjectResource(r.Context(), db.CreateProjectResourceParams{
		ProjectID:    project.ID,
		WorkspaceID:  project.WorkspaceID,
		ResourceType: "github_repo",
		ResourceRef:  ref,
		Label:        label,
		Position:     int32(count),
		CreatedBy:    creator,
	})
	if err != nil {
		if isUniqueViolation(err) {
			if existing, found := h.findProjectRepo(r.Context(), project.ID, key); found {
				h.writeProjectRepoAttachment(w, r, project, existing, false, registered)
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to attach project repository")
		return
	}
	h.noteUnconnectedProjectRepo(r.Context(), project, row.ResourceType, row.ResourceRef, creator)
	resp := projectResourceToResponse(row)
	h.publish(protocol.EventProjectResourceCreated, uuidToString(project.WorkspaceID), "member", userID, map[string]any{
		"resource": resp, "project_id": uuidToString(project.ID),
	})
	h.writeProjectRepoAttachment(w, r, project, row, true, registered)
}

// RemoveProjectRepo detaches a repository from the project. The workspace registry row stays.
func (h *Handler) RemoveProjectRepo(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	row, found := h.projectRepoByParam(r, project, chi.URLParam(r, "repoId"))
	if !found {
		writeError(w, http.StatusNotFound, "project repository not found")
		return
	}
	if err := h.Queries.DeleteProjectResource(r.Context(), row.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove project repository")
		return
	}
	h.publish(protocol.EventProjectResourceDeleted, uuidToString(project.WorkspaceID), "member", userID, map[string]any{
		"project_id": uuidToString(project.ID), "resource_id": uuidToString(row.ID),
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": uuidToString(row.ID), "removed": true})
}

func (h *Handler) writeProjectRepoAttachment(w http.ResponseWriter, r *http.Request, project db.Project, row db.ProjectResource, created, registered bool) {
	ws, _ := h.Queries.GetWorkspace(r.Context(), project.WorkspaceID)
	conns, _ := h.Queries.ListVCSConnectionsByWorkspace(r.Context(), project.WorkspaceID)
	viewer, err := h.visibilityViewerFor(r, project.WorkspaceID)
	var viewerPtr *visibilityViewer
	if err == nil {
		viewerPtr = &viewer
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, projectRepoAttachment{
		Resource:   projectResourceToResponse(row),
		Repo:       h.reachForResource(r, project.WorkspaceID, ws, row, conns, viewerPtr),
		Created:    created,
		Registered: registered,
	})
}

func (h *Handler) reachForResource(r *http.Request, wsID pgtype.UUID, ws db.Workspace, row db.ProjectResource, conns []db.VcsConnection, viewer *visibilityViewer) RepoReach {
	repoURL := ""
	var payload struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(row.ResourceRef, &payload) == nil {
		repoURL = payload.URL
	}
	ref := workspaceRepoRef{URL: repoURL}
	if row.CreatedBy.Valid {
		ref.CreatedBy = uuidToString(row.CreatedBy)
	}
	for _, entry := range decodeWorkspaceRepos(ws.Repos) {
		if string(repoident.NormalizeURL(entry.URL)) == string(repoident.NormalizeURL(repoURL)) {
			ref = entry
			ref.URL = repoURL
			break
		}
	}
	return h.buildRepoReach(r, wsID, ref, conns, viewer)
}

func (h *Handler) findProjectRepo(ctx context.Context, projectID pgtype.UUID, key string) (db.ProjectResource, bool) {
	rows, err := h.Queries.ListProjectResources(ctx, projectID)
	if err != nil {
		return db.ProjectResource{}, false
	}
	for _, row := range rows {
		if row.ResourceType == "github_repo" && projectRepoKey(row.ResourceRef) == key {
			return row, true
		}
	}
	return db.ProjectResource{}, false
}

func (h *Handler) projectRepoByParam(r *http.Request, project db.Project, param string) (db.ProjectResource, bool) {
	rows, err := h.Queries.ListProjectResources(r.Context(), project.ID)
	if err != nil {
		return db.ProjectResource{}, false
	}
	wantKey := string(repoident.NormalizeURL(param))
	for _, row := range rows {
		if row.ResourceType != "github_repo" || row.ProjectID != project.ID {
			continue
		}
		if uuidToString(row.ID) == param || projectRepoKey(row.ResourceRef) == wantKey {
			return row, true
		}
	}
	return db.ProjectResource{}, false
}

func projectRepoKey(ref []byte) string {
	repo, ok := gitconn.FromResource("github_repo", ref)
	if !ok {
		return ""
	}
	return repo.Key
}

// registerWorkspaceRepo appends a repository to workspace.repos when that
// identity is not already there. The workspace row is locked so two adds
// cannot overwrite each other's JSON.
func (h *Handler) registerWorkspaceRepo(ctx context.Context, ws pgtype.UUID, rawURL, actor string) (bool, error) {
	if h.TxStarter == nil {
		return false, errors.New("workspace registration is unavailable")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var stored []byte
	if err := tx.QueryRow(ctx, `SELECT repos FROM workspace WHERE id = $1 FOR UPDATE`, ws).Scan(&stored); err != nil {
		return false, err
	}
	want := string(repoident.NormalizeURL(rawURL))
	entries := decodeWorkspaceRepos(stored)
	for _, entry := range entries {
		if string(repoident.NormalizeURL(entry.URL)) == want {
			return false, nil
		}
	}
	entries = append(entries, workspaceRepoRef{
		URL:        strings.TrimSpace(rawURL),
		Visibility: string(permission.DefaultVisibility),
		CreatedBy:  actor,
	})
	encoded, err := json.Marshal(entries)
	if err != nil {
		return false, err
	}
	if _, err := h.Queries.WithTx(tx).UpdateWorkspace(ctx, db.UpdateWorkspaceParams{ID: ws, Repos: encoded}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
