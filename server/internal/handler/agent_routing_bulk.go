package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxBulkRoutingAgents bounds one bulk edit. A workspace roster is a few
// dozen seats; anything far past that is a client bug, not a selection.
const maxBulkRoutingAgents = 200

// BulkUpdateAgentRoutingRequest edits the routing pair on several seats at
// once (DENE-922). Either field may be omitted to leave it alone; an empty
// routing_tier takes the seats off the ladder, like the single-agent update.
type BulkUpdateAgentRoutingRequest struct {
	AgentIDs     []string `json:"agent_ids"`
	RoutingTier  *string  `json:"routing_tier"`
	RoutingUsage *string  `json:"routing_usage"`
}

type BulkUpdateAgentRoutingResponse struct {
	Agents []AgentResponse `json:"agents"`
}

// BulkUpdateAgentRouting is the seats table's batch edit. The whole batch is
// one statement inside one transaction: every id must name a live seat in
// this workspace, or nothing is written. A half-applied batch would leave the
// table showing a selection that routing reads differently.
//
// Owner/admin only, like the routing settings the table lives in.
func (h *Handler) BulkUpdateAgentRouting(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}

	var req BulkUpdateAgentRoutingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.AgentIDs) == 0 {
		writeError(w, http.StatusBadRequest, "agent_ids is required")
		return
	}
	if len(req.AgentIDs) > maxBulkRoutingAgents {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d agents per request", maxBulkRoutingAgents))
		return
	}
	if req.RoutingTier == nil && req.RoutingUsage == nil {
		writeError(w, http.StatusBadRequest, "routing_tier or routing_usage is required")
		return
	}

	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return
	}
	params := db.SetAgentsRoutingParams{WorkspaceID: wsUUID}
	if req.RoutingTier != nil {
		key, ok := routing.DefaultLadder.NormalizeTier(*req.RoutingTier)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"routing_tier %q is not a known tier; expected one of %s",
				*req.RoutingTier, strings.Join(routing.DefaultLadder.TierKeys(), ", ")))
			return
		}
		params.SetTier = true
		params.RoutingTier = pgtype.Text{String: key, Valid: key != ""}
	}
	if req.RoutingUsage != nil {
		key, ok := routing.NormalizeUsage(*req.RoutingUsage)
		if !ok {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"routing_usage %q is not a known value; expected one of %s",
				*req.RoutingUsage, strings.Join(routing.UsageKeys(), ", ")))
			return
		}
		params.RoutingUsage = pgtype.Text{String: key, Valid: true}
	}

	seen := make(map[string]bool, len(req.AgentIDs))
	for _, raw := range req.AgentIDs {
		id, err := util.ParseUUID(strings.TrimSpace(raw))
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("agent id %q is not a uuid", raw))
			return
		}
		key := uuidToString(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		params.Ids = append(params.Ids, id)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.SetAgentsRouting(r.Context(), params)
	if err != nil {
		slog.Warn("bulk update agent routing failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update agents")
		return
	}
	if len(updated) != len(params.Ids) {
		// Some id is archived, gone, or in another workspace. The rollback
		// above undoes the rows that did match.
		writeError(w, http.StatusNotFound, "one or more agents not found")
		return
	}
	// A following specialisation does not own its rung or usage (DENE-1016).
	// Refuse the whole batch: a half-applied selection would be the thing
	// this endpoint exists to avoid. The rollback drops the rows above.
	for _, agent := range updated {
		if agent.RuntimeInherited && agent.ParentAgentID.Valid {
			parent, parentErr := qtx.GetAgent(r.Context(), agent.ParentAgentID)
			parentName := ""
			if parentErr == nil {
				parentName = parent.Name
			}
			writeError(w, http.StatusBadRequest, routingFollowError(parentName))
			return
		}
	}
	// Base roles in the batch copy the new pair onto the specialisations
	// that follow them, in this same transaction.
	var cascaded []db.Agent
	seenParent := make(map[pgtype.UUID]bool, len(updated))
	for _, agent := range updated {
		if agent.ParentAgentID.Valid || seenParent[agent.ID] {
			continue
		}
		seenParent[agent.ID] = true
		children, syncErr := qtx.SyncInheritedAgentRuntimeProfiles(r.Context(), agent.ID)
		if syncErr != nil {
			slog.Warn("bulk sync following agent routing failed", append(logger.RequestAttrs(r), "error", syncErr)...)
			writeError(w, http.StatusInternalServerError, "failed to update following specialisations")
			return
		}
		cascaded = append(cascaded, children...)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update agents")
		return
	}

	resp := BulkUpdateAgentRoutingResponse{Agents: make([]AgentResponse, 0, len(updated)+len(cascaded))}
	for _, agent := range updated {
		h.publishAgentUpdate(r, agent)
		resp.Agents = append(resp.Agents, h.agentToResponse(agent))
	}
	// Followers were not in the request, but their rows changed. Publishing
	// them (and returning them) is what lets the seats table paint the copy
	// without waiting for a reload.
	for _, child := range cascaded {
		h.publishAgentUpdate(r, child)
		resp.Agents = append(resp.Agents, h.agentToResponse(child))
	}
	writeJSON(w, http.StatusOK, resp)
}
