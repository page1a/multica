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
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Module-level sharing (DENE-699).
//
// A third AND gate on top of resource visibility: the caller must both see
// the resource and be allowed into that product area. The closed list is
// permission.Modules (Issues, Projects, Repos, Runtimes). Agents, squads,
// settings, members and billing are not modules.
//
// Missing row = workspace. Modules are not created the way an issue is, so
// defaulting them to private would lock the product on upgrade.

type moduleVisibilityRow struct {
	module     permission.Module
	visibility permission.Visibility
	projectID  pgtype.UUID
}

type moduleVisibilityResponse struct {
	Key        string  `json:"key"`
	Visibility string  `json:"visibility"`
	ProjectID  *string `json:"project_id"`
	Allowed    bool    `json:"allowed"`
}

type moduleVisibilityRequest struct {
	Visibility string  `json:"visibility"`
	ProjectID  *string `json:"project_id"`
}

func hiddenModuleNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not found")
}

func (v visibilityViewer) canSeeModule(row moduleVisibilityRow) bool {
	if v.bypasses() {
		return true
	}
	return permission.CanSeeModule(v.role, row.visibility, v.inProject(row.projectID))
}

func (h *Handler) loadModuleVisibility(ctx context.Context, wsUUID pgtype.UUID, module permission.Module) (moduleVisibilityRow, error) {
	row := moduleVisibilityRow{
		module:     module,
		visibility: permission.DefaultModuleVisibility,
	}
	stored, err := h.Queries.GetWorkspaceModuleVisibility(ctx, db.GetWorkspaceModuleVisibilityParams{
		WorkspaceID: wsUUID,
		Module:      string(module),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row, nil
		}
		return row, err
	}
	row.visibility = permission.Visibility(stored.Visibility)
	row.projectID = stored.ProjectID
	return row, nil
}

func (h *Handler) callerCanSeeModule(r *http.Request, wsUUID pgtype.UUID, module permission.Module) (bool, error) {
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		return false, err
	}
	row, err := h.loadModuleVisibility(r.Context(), wsUUID, module)
	if err != nil {
		return false, err
	}
	return viewer.canSeeModule(row), nil
}

// requireModuleAccess is the single deny point for a module the caller may
// not enter. False means the deny response has already been written.
func (h *Handler) requireModuleAccess(w http.ResponseWriter, r *http.Request, module permission.Module) bool {
	wsID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return false
	}
	allowed, err := h.callerCanSeeModule(r, wsUUID, module)
	if err != nil || !allowed {
		hiddenModuleNotFound(w)
		return false
	}
	return true
}

// RequireModule rejects requests whose caller cannot enter this product area.
// Attach it to the route group for that area. Agent and internal callers
// bypass, matching resource-level visibility.
func (h *Handler) RequireModule(module permission.Module) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !h.requireModuleAccess(w, r, module) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func moduleToResponse(row moduleVisibilityRow, allowed bool) moduleVisibilityResponse {
	var projectID *string
	if row.projectID.Valid {
		id := uuidToString(row.projectID)
		projectID = &id
	}
	return moduleVisibilityResponse{
		Key:        string(row.module),
		Visibility: string(row.visibility),
		ProjectID:  projectID,
		Allowed:    allowed,
	}
}

// ListModuleVisibility returns every restrictable module and whether the
// caller may enter it. Missing rows surface as workspace.
func (h *Handler) ListModuleVisibility(w http.ResponseWriter, r *http.Request) {
	wsID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		hiddenModuleNotFound(w)
		return
	}
	out := make([]moduleVisibilityResponse, 0, len(permission.Modules))
	for _, module := range permission.Modules {
		row, err := h.loadModuleVisibility(r.Context(), wsUUID, module)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load module visibility")
			return
		}
		out = append(out, moduleToResponse(row, viewer.canSeeModule(row)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": out})
}

// SetModuleVisibility changes one module's sharing scope. Owner/admin only;
// agents cannot write this.
func (h *Handler) SetModuleVisibility(w http.ResponseWriter, r *http.Request) {
	wsID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	module := permission.Module(chi.URLParam(r, "module"))
	if !module.Valid() {
		writeError(w, http.StatusBadRequest,
			"module must be one of: issues, projects, repos, runtimes")
		return
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		hiddenModuleNotFound(w)
		return
	}
	if viewer.bypasses() || !permission.AllowedInWorkspace(viewer.role, permission.WorkspaceManageSettings) {
		writeError(w, http.StatusForbidden, "only workspace owners and admins can change module visibility")
		return
	}

	var req moduleVisibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	vis, ok := parseVisibilityValue(w, req.Visibility)
	if !ok {
		return
	}

	var projectID pgtype.UUID
	if vis == permission.VisibilityProject {
		if req.ProjectID == nil || *req.ProjectID == "" {
			writeError(w, http.StatusBadRequest,
				"project visibility needs a project: pass project_id")
			return
		}
		parsed, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project id")
		if !ok {
			return
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: parsed, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "project not found in this workspace")
			return
		}
		projectID = parsed
	}

	previous, err := h.loadModuleVisibility(r.Context(), wsUUID, module)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load module visibility")
		return
	}

	updated, err := h.Queries.UpsertWorkspaceModuleVisibility(r.Context(), db.UpsertWorkspaceModuleVisibilityParams{
		WorkspaceID: wsUUID,
		Module:      string(module),
		Visibility:  string(vis),
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to change module visibility")
		return
	}

	h.recordVisibilityChange(r, wsUUID, visibilityChange{
		resourceType: "module",
		resourceID:   string(module),
		previous:     string(previous.visibility),
		next:         vis,
		source:       auditSourceDirect,
		projectID:    projectID,
	})

	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	h.publish(protocol.EventModuleVisibilityUpdated, wsID, actorType, actorID, map[string]any{
		"workspace_id": wsID,
		"module":       string(module),
		"visibility":   string(vis),
	})

	row := moduleVisibilityRow{
		module:     module,
		visibility: permission.Visibility(updated.Visibility),
		projectID:  updated.ProjectID,
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"key":           row.module,
		"visibility":    row.visibility,
		"project_id":    moduleToResponse(row, true).ProjectID,
		"allowed":       true,
		"audience_size": h.moduleAudienceSize(r.Context(), wsUUID, vis, projectID),
	})
}
