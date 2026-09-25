package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const maxAgentCLIUpdateBytes = 4096

// SetAgentCLIFollow records that this runtime's CLI should (or should not)
// follow the latest release. The daemon applies it on the next heartbeat.
func (h *Handler) SetAgentCLIFollow(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireManageableAgentCLI(w, r)
	if !ok {
		return
	}
	var body struct {
		Follow *bool `json:"follow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Follow == nil {
		writeError(w, http.StatusBadRequest, "follow is required")
		return
	}
	if h.AgentCLICommands == nil {
		writeError(w, http.StatusInternalServerError, "agent CLI commands are not configured")
		return
	}
	runtimeID := uuidToString(rt.ID)
	if err := h.AgentCLICommands.SetFollow(r.Context(), runtimeID, *body.Follow); err != nil {
		slog.Error("set agent CLI follow", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to save auto-follow")
		return
	}
	h.requestDaemonPendingWork(runtimeID, protocol.PendingWorkKindAgentCLI)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// RequestAgentCLIUpdate asks the daemon to upgrade this CLI once the machine
// has no task running. The page reads the result from metadata.cli_update.
func (h *Handler) RequestAgentCLIUpdate(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.requireManageableAgentCLI(w, r)
	if !ok {
		return
	}
	if h.AgentCLICommands == nil {
		writeError(w, http.StatusInternalServerError, "agent CLI commands are not configured")
		return
	}
	runtimeID := uuidToString(rt.ID)
	requestID := randomID()
	if err := h.AgentCLICommands.SetUpdate(r.Context(), runtimeID, requestID); err != nil {
		slog.Error("queue agent CLI update", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to queue update")
		return
	}
	h.requestDaemonPendingWork(runtimeID, protocol.PendingWorkKindAgentCLI)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "request_id": requestID})
}

func (h *Handler) requireManageableAgentCLI(w http.ResponseWriter, r *http.Request) (db.AgentRuntime, bool) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, member, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return db.AgentRuntime{}, false
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "only the runtime owner or a workspace admin can update this CLI")
		return db.AgentRuntime{}, false
	}
	if rt.RuntimeMode != "local" || rt.ProfileID.Valid {
		writeError(w, http.StatusBadRequest, "only a built-in local CLI can be updated here")
		return db.AgentRuntime{}, false
	}
	return rt, true
}

// ReportAgentCLIStatus merges the daemon's snapshot into runtime metadata and
// clears a manual update or a follow click once the daemon says that one
// finished. A newer click keeps its own id, so a late ack cannot drop it.
func (h *Handler) ReportAgentCLIStatus(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	var body struct {
		CLIUpdate        json.RawMessage `json:"cli_update"`
		AppliedRequestID string          `json:"applied_request_id"`
		AppliedFollowID  string          `json:"applied_follow_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentCLIUpdateBytes)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.CLIUpdate) == 0 || !json.Valid(body.CLIUpdate) || body.CLIUpdate[0] != '{' {
		writeError(w, http.StatusBadRequest, "cli_update must be an object")
		return
	}
	changed, err := h.Queries.MergeAgentRuntimeCLIUpdate(r.Context(), db.MergeAgentRuntimeCLIUpdateParams{
		ID:        rt.ID,
		CliUpdate: body.CLIUpdate,
	})
	if err != nil {
		slog.Error("merge agent CLI status", "error", err, "runtime_id", runtimeID)
		writeError(w, http.StatusInternalServerError, "failed to store CLI status")
		return
	}
	if h.AgentCLICommands != nil {
		if body.AppliedRequestID != "" {
			if err := h.AgentCLICommands.ClearUpdate(r.Context(), runtimeID, body.AppliedRequestID); err != nil {
				slog.Warn("clear agent CLI update request", "error", err, "runtime_id", runtimeID)
			}
		}
		if body.AppliedFollowID != "" {
			if err := h.AgentCLICommands.ClearFollow(r.Context(), runtimeID, body.AppliedFollowID); err != nil {
				slog.Warn("clear agent CLI follow", "error", err, "runtime_id", runtimeID)
			}
		}
	}
	if changed > 0 && rt.WorkspaceID.Valid {
		h.PublishRuntimeRefresh(uuidToString(rt.WorkspaceID), "system", "", "agent_cli_status")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RefreshRuntimeModelCatalog drops the cached model list and enqueues a live
// discovery, so the picker sees the catalog of the CLI that just upgraded.
func (h *Handler) RefreshRuntimeModelCatalog(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID)
	if !ok {
		return
	}
	resolved := uuidToString(rt.ID)
	if h.ModelCatalogCache != nil {
		if err := h.ModelCatalogCache.Invalidate(r.Context(), resolved); err != nil {
			slog.Warn("invalidate model catalog after CLI upgrade", "error", err, "runtime_id", resolved)
		}
	}
	h.revalidateModelCatalog(r.Context(), resolved)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
