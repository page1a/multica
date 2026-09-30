package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadWorkspaceConnection loads one connection in this workspace. An admin
// can use any of them. Anyone else can use the one they saved.
func (h *Handler) loadWorkspaceConnection(w http.ResponseWriter, r *http.Request) (pgtype.UUID, db.VcsConnection, bool) {
	workspaceID := chi.URLParam(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectionId"), "connection id")
	if !ok {
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	conn, err := h.Queries.GetVCSConnectionByID(r.Context(), idUUID)
	if err != nil || uuidToString(conn.WorkspaceID) != uuidToString(wsUUID) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load connection")
			return pgtype.UUID{}, db.VcsConnection{}, false
		}
		writeError(w, http.StatusNotFound, "connection not found")
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	if !roleAllowed(member.Role, "owner", "admin") && uuidToString(conn.ConnectedByID) != uuidToString(member.UserID) {
		writeError(w, http.StatusForbidden, "只有管理员或保存这条连接的人可以操作")
		return pgtype.UUID{}, db.VcsConnection{}, false
	}
	return wsUUID, conn, true
}

// TestStoredConnection re-checks a saved token. The token is not returned.
func (h *Handler) TestStoredConnection(w http.ResponseWriter, r *http.Request) {
	_, conn, ok := h.loadWorkspaceConnection(w, r)
	if !ok {
		return
	}
	if !h.isVCSConfigured() {
		writeFeatureDisabled(w, "vcs_not_configured", "vcs integration not configured (MULTICA_VCS_SECRET_KEY unset)")
		return
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil || token == "" || token == "local" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "saved token is unreadable"})
		return
	}
	host, owner, name, _ := splitRepoKey(conn.RepoUrl)
	_ = host
	account, err := delivery.ValidateToken(r.Context(), h.deliveryClient(), conn.Provider, delivery.APIBase(conn.Provider, conn.InstanceUrl, ""), token, owner, name)
	if err != nil {
		if delivery.Unauthorized(err) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "the provider rejected the access token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "could not reach the provider"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account_login": account, "repo_url": conn.RepoUrl})
}
