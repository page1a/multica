package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ChatSessionHandoffResponse is what a new session reads when it takes over an
// older one. Summary plus the latest page — never the whole transcript.
type ChatSessionHandoffResponse struct {
	SessionID    string                   `json:"session_id"`
	Title        string                   `json:"title"`
	Summary      string                   `json:"summary"`
	MessageCount int                      `json:"message_count"`
	Messages     []channel.HistoryMessage `json:"messages"`
	NextCursor   string                   `json:"next_cursor,omitempty"`
}

const (
	defaultHandoffLimit = 20
	maxHandoffLimit     = 50
	handoffExcerptRunes = 240
)

// GetChatSessionHandoff serves `multica chat history --session` / `chat thread
// --session`. The caller must already be allowed to open that chat: same
// workspace, and the session's creator (a task token acts as the person who
// started the run). Another workspace gets nothing back.
func (h *Handler) GetChatSessionHandoff(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	h.writeChatSessionHandoff(w, r, session)
}

// GetChatSessionLinkRead serves a session URL whose workspace slug may differ
// from the caller's bound workspace. Cross-workspace reads are authorized as
// the direct human at the top of the caller's task chain and are audited in a
// dedicated table because chat sessions have no issue timeline.
func (h *Handler) GetChatSessionLinkRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	target, err := h.Queries.GetWorkspaceBySlug(r.Context(), chi.URLParam(r, "workspaceSlug"))
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID: parseUUID(chi.URLParam(r, "sessionId")), WorkspaceID: target.ID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	if _, err := h.Queries.GetPublicChatSessionInWorkspace(r.Context(), db.GetPublicChatSessionInWorkspaceParams{ID: session.ID, WorkspaceID: target.ID}); err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	callerWorkspace := ctxWorkspaceID(r.Context())
	if callerWorkspace == "" || callerWorkspace == uuidToString(target.ID) {
		access, err := h.chatAccessFor(r.Context(), session, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to check chat access")
			return
		}
		if !access.see {
			writeError(w, http.StatusForbidden, "chat session is not visible")
			return
		}
		h.writeChatSessionHandoff(w, r, session)
		return
	}
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	if taskID == "" {
		writeError(w, http.StatusForbidden, "cross-workspace chat reads require a direct human task")
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), parseUUID(taskID))
	if err != nil || !task.OriginatorSource.Valid || task.OriginatorSource.String != "direct_human" || !task.OriginatorUserID.Valid {
		writeError(w, http.StatusForbidden, "cross-workspace chat reads require a direct human task")
		return
	}
	access, err := h.chatAccessFor(r.Context(), session, uuidToString(task.OriginatorUserID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check chat access")
		return
	}
	if !access.see {
		writeError(w, http.StatusForbidden, "chat session is not visible")
		return
	}
	if _, err := h.DB.Exec(r.Context(), `INSERT INTO chat_session_link_read_audit (workspace_id, chat_session_id, reader_workspace_id, reader_user_id, reader_agent_id, reader_task_id) VALUES ($1,$2,$3,$4,$5,$6)`, target.ID, session.ID, parseUUID(callerWorkspace), task.OriginatorUserID, task.AgentID, task.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to audit chat read")
		return
	}
	h.writeChatSessionHandoff(w, r, session)
}

func (h *Handler) writeChatSessionHandoff(w http.ResponseWriter, r *http.Request, session db.ChatSession) {
	limit := clampHandoffLimit(parseHistoryLimit(r.URL.Query().Get("limit")))
	beforeCreatedAt, beforeID := parseTranscriptCursor(r.URL.Query().Get("before"))
	// One extra row covers a hidden kickoff that visibleChatMessages drops, so
	// a full visible page still advertises a cursor.
	rows, err := h.Queries.ListChatMessagesPage(r.Context(), db.ListChatMessagesPageParams{
		ChatSessionID:   session.ID,
		Limit:           int32(limit + 2),
		BeforeCreatedAt: beforeCreatedAt,
		BeforeID:        beforeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read chat session")
		return
	}
	rows = visibleChatMessages(rows)
	hasOlder := len(rows) > limit
	if hasOlder {
		rows = rows[:limit]
	}

	total, err := h.Queries.CountHandoffChatMessages(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read chat session")
		return
	}
	opening, err := h.Queries.GetEarliestHandoffUserMessage(r.Context(), session.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to read chat session")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		opening = ""
	}

	messages := handoffMessages(rows)
	var nextCursor string
	if hasOlder && len(rows) > 0 {
		oldest := rows[len(rows)-1]
		nextCursor = transcriptCursor(oldest.CreatedAt.Time, oldest.ID)
	}
	title := strings.TrimSpace(session.Title)
	writeJSON(w, http.StatusOK, ChatSessionHandoffResponse{
		SessionID:    uuidToString(session.ID),
		Title:        title,
		Summary:      buildHandoffSummary(title, int(total), len(messages), handoffExcerpt(opening), hasOlder),
		MessageCount: int(total),
		Messages:     messages,
		NextCursor:   nextCursor,
	})
}

func clampHandoffLimit(n int) int {
	if n <= 0 {
		return defaultHandoffLimit
	}
	if n > maxHandoffLimit {
		return maxHandoffLimit
	}
	return n
}

func handoffMessages(rows []db.ChatMessage) []channel.HistoryMessage {
	out := make([]channel.HistoryMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		m := rows[i]
		role := channel.HistoryRoleUser
		if m.Role == "assistant" {
			role = channel.HistoryRoleAssistant
		}
		out = append(out, channel.HistoryMessage{
			ID:     uuidToString(m.ID),
			Role:   role,
			Text:   m.Content,
			Author: transcriptAuthor(role),
			TS:     m.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

func buildHandoffSummary(title string, total, shown int, opening string, hasOlder bool) string {
	if title == "" {
		title = "Untitled"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", title)
	fmt.Fprintf(&b, "Messages: %d. This page shows %d, oldest first.\n", total, shown)
	if opening != "" {
		fmt.Fprintf(&b, "Opening: %s\n", opening)
	}
	if hasOlder {
		b.WriteString("Older messages exist. Pass next_cursor to --before to read the previous page.")
	} else {
		b.WriteString("This page reaches the start of the session.")
	}
	return b.String()
}

func handoffExcerpt(content string) string {
	content = strings.Join(strings.Fields(content), " ")
	runes := []rune(content)
	if len(runes) <= handoffExcerptRunes {
		return content
	}
	return string(runes[:handoffExcerptRunes]) + "…"
}
