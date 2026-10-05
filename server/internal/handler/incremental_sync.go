package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// IncrementalChange is deliberately small: clients can merge an upsert by id
// and remove an id from deleted. The data field is the same server projection
// used by the owning list endpoint, so this contract can be adopted without
// changing existing list responses.
type IncrementalChange struct {
	ID        string          `json:"id"`
	UpdatedAt time.Time       `json:"updated_at"`
	Data      json.RawMessage `json:"data"`
}

type incrementalCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func encodeIncrementalCursor(c incrementalCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeIncrementalCursor(s string) (incrementalCursor, error) {
	var c incrementalCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) > 4096 || json.Unmarshal(b, &c) != nil || c.At.IsZero() || c.ID == "" {
		return c, errInvalidCursor
	}
	return c, nil
}

var errInvalidCursor = &incrementalSyncError{"invalid cursor"}

type incrementalSyncError struct{ message string }

func (e *incrementalSyncError) Error() string { return e.message }

// ListIncrementalChanges serves the shared high-frequency sync protocol. A
// cursor is opaque and keyset ordered by (updated_at,id). Cursors older than
// the retention window return 410, allowing clients to rebuild safely.
func (h *Handler) ListIncrementalChanges(w http.ResponseWriter, r *http.Request) {
	resource := r.URL.Query().Get("resource")
	if resource != "issues" && resource != "inbox" && resource != "chats" && resource != "timeline" {
		writeError(w, http.StatusBadRequest, "invalid resource")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscan(raw, &limit); err != nil || limit < 1 || limit > 500 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
	}
	var cur incrementalCursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cur, err = decodeIncrementalCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	} else if raw := r.URL.Query().Get("updated_since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid updated_since")
			return
		}
		cur.At = t
	}
	if !cur.At.IsZero() && cur.At.Before(time.Now().Add(-30*24*time.Hour)) {
		writeError(w, http.StatusGone, "incremental cursor expired; rebuild the list")
		return
	}
	if cur.At.IsZero() {
		cur.At = time.Unix(0, 0).UTC()
	}
	if cur.ID == "" {
		cur.ID = "00000000-0000-0000-0000-000000000000"
	}

	ws := ctxWorkspaceID(r.Context())
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	args := []any{ws, cur.At, cur.ID, limit + 1}
	query := ""
	switch resource {
	case "issues":
		wsUUID, err := parseUUIDSafe(ws)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid workspace id")
			return
		}
		viewer, err := h.visibilityViewerFor(r, wsUUID)
		if err != nil {
			writeError(w, http.StatusForbidden, "unable to resolve issue visibility")
			return
		}
		addArg := func(value any) string {
			args = append(args, value)
			return fmt.Sprintf("$%d", len(args))
		}
		query = `(SELECT id, updated_at, row_to_json(i) FROM issue i WHERE workspace_id=$1 AND ` + viewer.issueVisibilitySQL("i", addArg) + ` AND (updated_at, id) > ($2::timestamptz, NULLIF($3,'')::uuid)) UNION ALL (SELECT id, changed_at, NULL::json FROM incremental_sync_tombstone WHERE resource='issues' AND workspace_id=$1 AND (changed_at, id) > ($2::timestamptz, NULLIF($3,'')::uuid)) ORDER BY updated_at,id LIMIT $4`
	case "inbox":
		// inbox_item predates updated_at. read_at is the only durable mutation
		// timestamp available today; include it so mark-read changes can be
		// replayed while keeping the endpoint compatible with older schemas.
		query = `(SELECT id, GREATEST(created_at, COALESCE(read_at, created_at)), row_to_json(i) FROM inbox_item i WHERE workspace_id=$1 AND recipient_type='member' AND recipient_id=$5 AND (GREATEST(created_at, COALESCE(read_at, created_at)), id) > ($2::timestamptz, NULLIF($3,'')::uuid)) UNION ALL (SELECT id, changed_at, NULL::json FROM incremental_sync_tombstone WHERE resource='inbox' AND workspace_id=$1 AND subject_id=$5 AND (changed_at, id) > ($2::timestamptz, NULLIF($3,'')::uuid)) ORDER BY updated_at,id LIMIT $4`
	case "chats":
		query = `(SELECT id, updated_at, row_to_json(s) FROM chat_session s WHERE workspace_id=$1 AND creator_id=$5 AND (updated_at, id) > ($2::timestamptz, NULLIF($3,'')::uuid)) UNION ALL (SELECT id, changed_at, NULL::json FROM incremental_sync_tombstone WHERE resource='chats' AND workspace_id=$1 AND subject_id=$5 AND (changed_at, id) > ($2::timestamptz, NULLIF($3,'')::uuid)) ORDER BY updated_at,id LIMIT $4`
	case "timeline":
		issueID := r.URL.Query().Get("issue_id")
		if issueID == "" {
			writeError(w, http.StatusBadRequest, "issue_id is required for timeline")
			return
		}
		issueUUID, err := parseUUIDSafe(issueID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid issue_id")
			return
		}
		args = append(args, issueUUID)
		wsUUID, err := parseUUIDSafe(ws)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid workspace id")
			return
		}
		viewer, err := h.visibilityViewerFor(r, wsUUID)
		if err != nil {
			writeError(w, http.StatusForbidden, "unable to resolve issue visibility")
			return
		}
		addArg := func(value any) string {
			args = append(args, value)
			return fmt.Sprintf("$%d", len(args))
		}
		query = `SELECT c.id, c.updated_at, row_to_json(c) FROM comment c JOIN issue i ON i.id = c.issue_id WHERE c.workspace_id=$1 AND c.issue_id=$5 AND ` + viewer.issueVisibilitySQL("i", addArg) + ` AND (c.updated_at, c.id) > ($2::timestamptz, $3::uuid) ORDER BY c.updated_at,c.id LIMIT $4`
	}
	if resource != "timeline" && resource != "issues" {
		args = append(args, userID)
	}
	rows, err := h.DB.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list incremental changes")
		return
	}
	defer rows.Close()
	// Keep the raw page separate from the classified result. A tombstone still
	// consumes a page slot and advances the cursor; classifying first could
	// otherwise return too many rows or skip rows after a delete.
	rawChanges := make([]IncrementalChange, 0, limit+1)
	var last incrementalCursor
	for rows.Next() {
		var c IncrementalChange
		if err := rows.Scan(&c.ID, &c.UpdatedAt, &c.Data); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read incremental changes")
			return
		}
		rawChanges = append(rawChanges, c)
		last = incrementalCursor{At: c.UpdatedAt, ID: c.ID}
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read incremental changes")
		return
	}
	hasMore := len(rawChanges) > limit
	if hasMore {
		rawChanges = rawChanges[:limit]
		last = incrementalCursor{At: rawChanges[len(rawChanges)-1].UpdatedAt, ID: rawChanges[len(rawChanges)-1].ID}
	}
	changes := make([]IncrementalChange, 0, len(rawChanges))
	deleted := make([]string, 0)
	for _, c := range rawChanges {
		if len(c.Data) == 0 || string(c.Data) == "null" {
			deleted = append(deleted, c.ID)
			continue
		}
		if resource == "timeline" {
			var row struct {
				DeletedAt *time.Time `json:"deleted_at"`
			}
			if json.Unmarshal(c.Data, &row) == nil && row.DeletedAt != nil {
				deleted = append(deleted, c.ID)
				continue
			}
		}
		changes = append(changes, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": resource, "upserts": changes, "deleted": deleted, "next_cursor": func() string {
		if last.At.IsZero() {
			return r.URL.Query().Get("cursor")
		}
		return encodeIncrementalCursor(last)
	}(), "has_more": hasMore})
}
