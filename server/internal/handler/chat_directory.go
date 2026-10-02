package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatDirectoryItem is the compact, read-only projection used by agents and
// the project chat tab. Summary is the latest visible message, never a full
// transcript. The endpoint intentionally does not create/read cursors.
type ChatDirectoryItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	ProjectID    string `json:"project_id,omitempty"`
	ProjectTitle string `json:"project_title,omitempty"`
	AgentID      string `json:"agent_id"`
	AgentName    string `json:"agent_name,omitempty"`
	OriginatorID string `json:"originator_id"`
	Originator   string `json:"originator,omitempty"`
	Status       string `json:"status"`
	Visibility   string `json:"visibility"`
	LastActiveAt string `json:"last_active_at"`
	MessageCount int32  `json:"message_count"`
	Summary      string `json:"summary,omitempty"`
}

// ListChatDirectory serves GET /api/chat/directory. Agent requests use the
// task's originator as viewer_id; a runtime owner must never gain access to
// another member's private chat just because it is executing their task.
func (h *Handler) ListChatDirectory(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	viewerID, taskScoped, currentProject, currentSessionID, ok := h.chatDirectoryViewer(w, r, userID, workspaceID)
	if !ok {
		return
	}
	if viewerID == "" {
		// A task with no human originator has no visibility principal.
		writeJSON(w, http.StatusOK, []ChatDirectoryItem{})
		return
	}

	allProjects := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("all_projects")), "true") || r.URL.Query().Get("all_projects") == "1"
	projectRef := strings.TrimSpace(r.URL.Query().Get("project"))
	if projectRef == "" {
		projectRef = currentProject
	}
	var projectID = pgtypeUUIDNil()
	if projectRef != "" {
		parsed, ok := parseUUIDOrBadRequest(w, projectRef, "project id")
		if !ok {
			return
		}
		projectID = parsed
	} else if !allProjects && !taskScoped {
		// Human callers have no current task project; preserve the useful
		// workspace directory behavior for the project page and web clients.
		allProjects = true
	}

	var since pgtype.Timestamptz
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since timestamp")
			return
		}
		since = pgtype.Timestamptz{Time: t, Valid: true}
	}
	var keyword pgtype.Text
	if raw := strings.TrimSpace(r.URL.Query().Get("q")); raw != "" {
		keyword = pgtype.Text{String: raw, Valid: true}
	}

	projectIDs, err := h.chatProjectIDs(r.Context(), workspaceID, viewerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project access")
		return
	}
	rows, err := h.Queries.ListChatDirectory(r.Context(), db.ListChatDirectoryParams{
		WorkspaceID: parseUUID(workspaceID),
		ViewerID:    parseUUID(viewerID),
		ProjectIds:  projectIDs,
		AllProjects: allProjects,
		ProjectID:   projectID,
		Since:       since,
		Keyword:     keyword,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat directory")
		return
	}
	items := make([]ChatDirectoryItem, 0, len(rows))
	for _, row := range rows {
		if currentSessionID != "" && uuidToString(row.ID) == currentSessionID {
			continue
		}
		items = append(items, ChatDirectoryItem{
			ID:           uuidToString(row.ID),
			Title:        row.Title,
			ProjectID:    uuidToString(row.ProjectID),
			ProjectTitle: row.ProjectTitle,
			AgentID:      uuidToString(row.AgentID),
			AgentName:    row.AgentName,
			OriginatorID: uuidToString(row.CreatorID),
			Originator:   row.CreatorName,
			Status:       row.Status,
			Visibility:   row.Visibility,
			LastActiveAt: timestampToString(row.LastActiveAt),
			MessageCount: row.MessageCount,
			Summary:      directorySummary(row.Summary),
		})
	}
	writeJSON(w, http.StatusOK, items)
}

func directorySummary(raw string) string {
	raw = strings.Join(strings.Fields(raw), " ")
	runes := []rune(raw)
	if len(runes) <= 240 {
		return raw
	}
	return string(runes[:237]) + "…"
}

func pgtypeUUIDNil() (out pgtype.UUID) { return out }

// chatDirectoryViewer resolves the human visibility principal and the task's
// current project. Missing originator attribution fails closed for task-scoped
// agents by returning an empty viewer id.
func (h *Handler) chatDirectoryViewer(w http.ResponseWriter, r *http.Request, fallback, workspaceID string) (viewerID string, taskScoped bool, projectID, currentSessionID string, ok bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return fallback, false, "", "", true
	}
	taskScoped = true
	taskID, valid := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !valid {
		return "", true, "", "", false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil {
		return "", true, "", "", true
	}
	if task.OriginatorUserID.Valid {
		viewerID = uuidToString(task.OriginatorUserID)
	}
	if task.ChatSessionID.Valid {
		currentSessionID = uuidToString(task.ChatSessionID)
		if session, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID); err == nil && session.ProjectID.Valid {
			projectID = uuidToString(session.ProjectID)
		}
	}
	if projectID == "" && task.IssueID.Valid {
		if issue, err := h.Queries.GetIssue(r.Context(), task.IssueID); err == nil && issue.ProjectID.Valid {
			projectID = uuidToString(issue.ProjectID)
		}
	}
	return viewerID, taskScoped, projectID, currentSessionID, true
}
