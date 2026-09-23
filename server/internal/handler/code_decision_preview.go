package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/coderesolve"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// PreviewProjectCodeDecision answers "where would a new task on this project
// run, if this machine claimed it?" with the same Decision the claim path
// stamps onto a task (DENE-619).
//
// The project page shows that answer at the top of the source list. It must
// not walk the resource list and pick a directory itself: the moment it does,
// the sentence on screen and the directory a task actually opens can disagree,
// and the user only finds out after something has been written.
//
// This is a preview, not a claim. Nothing is persisted, and there is no task
// id, so a parallel-mode path's per-task segment is not a directory that
// exists yet. The stable fields — which directory, which landing folder, which
// failure code — are the ones the UI renders.
func (h *Handler) PreviewProjectCodeDecision(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	daemonID := strings.TrimSpace(r.URL.Query().Get("daemon_id"))
	if daemonID == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}

	loaded, err := h.resolveClaimProjectContext(r.Context(), project.ID, project.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project code decision")
		return
	}
	runtimes, err := h.Queries.ListAgentRuntimes(r.Context(), project.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project code decision")
		return
	}

	claiming := daemonForPreview(daemonID, newestRuntimeForDaemon(runtimes, daemonID))
	resp := AgentTaskResponse{}
	loaded.applyTo(&resp)
	// Same narrowing the claim applies before it resolves, so the preview
	// names a directory the daemon would actually be sent.
	resp.ProjectResources = filterResourcesForDaemonCapabilities(resp.ProjectResources, claiming)
	for i := range resp.Projects {
		resp.Projects[i].Resources = filterResourcesForDaemonCapabilities(resp.Projects[i].Resources, claiming)
	}
	applyCodeDecision(&resp, claiming)
	if resp.CodeDecision == nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project code decision")
		return
	}
	writeJSON(w, http.StatusOK, resp.CodeDecision)
}

// daemonForPreview reads the capabilities the daemon advertised the last time
// it checked in. Declarations, never a version string — the same rule
// daemonForRequest uses on a live claim. A machine with no runtime row
// advertises nothing, and a mode it cannot run stays Unresolvable rather than
// being quietly rewritten to in-place.
func daemonForPreview(daemonID string, rt *db.AgentRuntime) coderesolve.Daemon {
	d := coderesolve.Daemon{ID: daemonID}
	if rt == nil {
		return d
	}
	userRoot := runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityLocalWorktreeUserRootV1)
	multi := runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityLocalDirectoryMultiV1)
	// Daemons that shipped user-root and several directories together
	// advertise only the former. Reading either as "can take more than one
	// directory" is what the claim path does.
	d.MultiLocalDirectory = multi || userRoot
	d.WorktreeUserRoot = userRoot
	d.LocalWorktree = runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityLocalWorktreeV1)
	d.LocalShared = runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityLocalSharedV1)
	return d
}
