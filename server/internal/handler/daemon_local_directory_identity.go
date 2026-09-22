package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/repoident"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// BackfillLocalDirectoryIdentity fills missing real_path / repo_key /
// is_git_repo on a local_directory the calling daemon owns.
//
// The server cannot see the user's disk, so identity for rows written before
// those fields existed has to be reported by the machine holding the folder
// (DENE-618). A unique-index collision — two legacy spellings of one directory
// both resolving to the same real_path — must not fail the daemon: the first
// row keeps the identity, this one is left as it was, and we log.
func (h *Handler) BackfillLocalDirectoryIdentity(w http.ResponseWriter, r *http.Request) {
	daemonID := middleware.DaemonIDFromContext(r.Context())
	if strings.TrimSpace(daemonID) == "" {
		writeError(w, http.StatusUnauthorized, "daemon token required")
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "resourceId"), "resource id")
	if !ok {
		return
	}
	var req struct {
		RealPath  string `json:"real_path"`
		RepoKey   string `json:"repo_key"`
		IsGitRepo *bool  `json:"is_git_repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.RealPath = strings.TrimSpace(req.RealPath)
	if req.RealPath != "" && !isAbsoluteLocalPath(req.RealPath) {
		writeError(w, http.StatusBadRequest, "real_path must be an absolute path")
		return
	}
	req.RepoKey = string(repoident.NormalizeURL(req.RepoKey))

	existing, err := h.Queries.GetProjectResource(r.Context(), resourceUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project resource not found")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(existing.WorkspaceID)) {
		return
	}
	if existing.ResourceType != "local_directory" {
		writeError(w, http.StatusBadRequest, "resource is not a local_directory")
		return
	}
	var ref localDirectoryRef
	if err := json.Unmarshal(existing.ResourceRef, &ref); err != nil {
		writeError(w, http.StatusBadRequest, "invalid local_directory payload")
		return
	}
	if ref.DaemonID != daemonID {
		writeError(w, http.StatusForbidden, "resource is not owned by this daemon")
		return
	}

	patched, changed, err := withLocalDirectoryIdentity(existing.ResourceRef, req.RealPath, req.RepoKey, req.IsGitRepo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to patch resource identity")
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, map[string]any{"applied": false})
		return
	}

	updated, err := h.Queries.UpdateProjectResource(r.Context(), db.UpdateProjectResourceParams{
		ID:          existing.ID,
		ResourceRef: patched,
		Label:       existing.Label,
		Position:    existing.Position,
	})
	if err != nil {
		if isUniqueViolation(err) {
			slog.Warn("local_directory identity backfill skipped: unique conflict; keeping the first row",
				"resource_id", uuidToString(existing.ID),
				"project_id", uuidToString(existing.ProjectID),
				"daemon_id", daemonID,
				"real_path", req.RealPath,
				"local_path", ref.LocalPath,
			)
			writeJSON(w, http.StatusOK, map[string]any{"applied": false, "conflict": true})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update project resource")
		return
	}

	resp := projectResourceToResponse(updated)
	h.publish(
		protocol.EventProjectResourceUpdated,
		uuidToString(existing.WorkspaceID),
		"daemon",
		daemonID,
		map[string]any{"resource": resp, "project_id": uuidToString(existing.ProjectID)},
	)
	writeJSON(w, http.StatusOK, map[string]any{"applied": true, "resource": resp})
}

// withLocalDirectoryIdentity writes identity fields that are currently empty.
// Existing values are left alone: a later report must not move a directory's
// identity out from under uniqueness rules that already accepted it.
func withLocalDirectoryIdentity(ref json.RawMessage, realPath, repoKey string, isGitRepo *bool) (json.RawMessage, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(ref, &fields); err != nil {
		return nil, false, err
	}
	changed := false
	putString := func(key, val string) error {
		if strings.TrimSpace(val) == "" || jsonFieldSet(fields[key]) {
			return nil
		}
		encoded, err := json.Marshal(val)
		if err != nil {
			return err
		}
		fields[key] = encoded
		changed = true
		return nil
	}
	if err := putString("real_path", realPath); err != nil {
		return nil, false, err
	}
	if err := putString("repo_key", repoKey); err != nil {
		return nil, false, err
	}
	if isGitRepo != nil && !jsonFieldSet(fields["is_git_repo"]) {
		encoded, err := json.Marshal(*isGitRepo)
		if err != nil {
			return nil, false, err
		}
		fields["is_git_repo"] = encoded
		changed = true
	}
	if !changed {
		return ref, false, nil
	}
	out, err := json.Marshal(fields)
	return out, true, err
}

func jsonFieldSet(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}
