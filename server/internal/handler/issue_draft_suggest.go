package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxIssueDraftSuggestRows bounds one suggestion request. Every row is a call
// to the routing model, and no alignment produces a group anywhere near this
// size.
const maxIssueDraftSuggestRows = 30

type SuggestIssueDraftAssigneesRequest struct {
	ProjectID *string `json:"project_id"`
	// Rows are the draft's issues in display order, root first. They come from
	// the request rather than the stored draft so the panel can ask about
	// edits it has not saved yet.
	Rows []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		HasChildren bool   `json:"has_children"`
	} `json:"rows"`
}

type IssueDraftAssigneeSuggestion struct {
	AssigneeType string `json:"assignee_type"`
	AssigneeID   string `json:"assignee_id"`
	Name         string `json:"name"`
	Tier         string `json:"tier"`
}

type SuggestIssueDraftAssigneesResponse struct {
	// Suggestions is index-aligned with the request rows. A null entry is a
	// row routing has no confident seat for; the panel leaves it unassigned.
	Suggestions []*IssueDraftAssigneeSuggestion `json:"suggestions"`
}

// SuggestIssueDraftAssignees asks the routing module which seat it would put
// on each issue of a draft, without creating or writing anything. The answer
// uses the tier tags on the roster and the project's direction, exactly as the
// post-create routing pass does, so the confirm panel can show — and the user
// can change — the assignee before the group exists (DENE-691).
func (h *Handler) SuggestIssueDraftAssignees(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req SuggestIssueDraftAssigneesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Rows) > maxIssueDraftSuggestRows {
		writeError(w, http.StatusBadRequest, "too many rows")
		return
	}
	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}

	resp := SuggestIssueDraftAssigneesResponse{Suggestions: make([]*IssueDraftAssigneeSuggestion, len(req.Rows))}
	if h.Routing == nil || len(req.Rows) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	projectName := ""
	if req.ProjectID != nil && *req.ProjectID != "" {
		projectID, ok := parseUUIDOrBadRequest(w, *req.ProjectID, "project_id")
		if !ok {
			return
		}
		if p, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: projectID, WorkspaceID: session.WorkspaceID,
		}); err == nil {
			projectName = p.Title
		}
	}

	rows := make([]routing.SuggestRow, len(req.Rows))
	for i, row := range req.Rows {
		rows[i] = routing.SuggestRow{
			Title:              row.Title,
			DescriptionSummary: clipRunes(row.Description, descriptionSummaryLimit),
			HasChildren:        row.HasChildren,
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	suggestions, err := h.Routing.Suggest(ctx, util.UUIDToString(session.WorkspaceID), projectName, rows)
	if err != nil {
		// A suggestion is advisory: the panel works without one, so a routing
		// failure answers with empty rows instead of failing the draft page.
		slog.Warn("issue draft assignee suggestion failed", append(logger.RequestAttrs(r), "error", err)...)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for i, s := range suggestions {
		if s.Seat == nil {
			continue
		}
		resp.Suggestions[i] = &IssueDraftAssigneeSuggestion{
			AssigneeType: "agent",
			AssigneeID:   s.Seat.ID,
			Name:         s.Seat.Name,
			Tier:         s.Seat.TierKey,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
