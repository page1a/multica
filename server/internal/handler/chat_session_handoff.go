package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/go-chi/chi/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HandoffChatSessionRequest is POST /api/chat/sessions/{id}/handoff: hand this
// chat to another agent (DENE-1350).
type HandoffChatSessionRequest struct {
	// To is the agent's id or name.
	To string `json:"to"`
}

// HandoffChatSessionResponse names the new chat and the run its opening
// message started.
type HandoffChatSessionResponse struct {
	FromSessionID string              `json:"from_session_id"`
	Session       ChatSessionResponse `json:"session"`
	MessageID     string              `json:"message_id"`
	TaskID        string              `json:"task_id"`
}

// chatHandoffRecent is how many of the latest messages the opening repeats.
const chatHandoffRecent = 8

// HandoffChatSession opens a new chat with another agent whose first message
// is a summary of this one. It goes through CreateChatSession and
// SendChatMessage so the new chat passes the same agent access gate and
// starts its run the ordinary way. The old chat is left as it is.
func (h *Handler) HandoffChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	// The new chat is created as the caller; only the chat's owner may hand
	// it over, so a shared reader cannot copy it into a chat of their own.
	if uuidToString(session.CreatorID) != userID {
		writeError(w, http.StatusForbidden, "only the chat's owner can hand it to another agent")
		return
	}
	var req HandoffChatSessionRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target := strings.TrimSpace(req.To)
	if target == "" {
		writeError(w, http.StatusBadRequest, "--to is required")
		return
	}
	agents, err := h.Queries.ListAgents(r.Context(), session.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load agents failed")
		return
	}
	var to, from db.Agent
	for _, candidate := range agents {
		if !to.ID.Valid && !candidate.ArchivedAt.Valid && (uuidToString(candidate.ID) == target || strings.EqualFold(candidate.Name, target)) {
			to = candidate
		}
		if candidate.ID == session.AgentID {
			from = candidate
		}
	}
	if !to.ID.Valid {
		writeError(w, http.StatusBadRequest, "--to must be an agent name or id in this workspace")
		return
	}
	if to.ID == session.AgentID {
		writeError(w, http.StatusBadRequest, "this chat is already with "+to.Name+"; pick another agent")
		return
	}

	opening, err := h.chatHandoffOpening(r.Context(), session, from.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read chat session")
		return
	}
	sessionResp := []ChatSessionResponse{chatSessionToResponse(session)}
	if err := h.hydrateChatSessionProjectIDs(r.Context(), sessionResp); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat session projects")
		return
	}
	projectIDs := sessionResp[0].ProjectIDs
	if projectIDs == nil {
		projectIDs = []string{}
	}

	var created ChatSessionResponse
	if !h.chatHandoffCall(w, r, h.CreateChatSession, "", map[string]any{
		"agent_id":    uuidToString(to.ID),
		"title":       strings.TrimSpace(session.Title),
		"project_ids": projectIDs,
	}, &created) {
		return
	}
	var sent SendChatMessageResponse
	if !h.chatHandoffCall(w, r, h.SendChatMessage, created.ID, map[string]any{"content": opening}, &sent) {
		return
	}
	writeJSON(w, http.StatusCreated, HandoffChatSessionResponse{
		FromSessionID: uuidToString(session.ID),
		Session:       created,
		MessageID:     sent.MessageID,
		TaskID:        sent.TaskID,
	})
}

// chatHandoffOpening is the new chat's first message: where it came from,
// how to read the whole thing, its opening line and the latest messages.
func (h *Handler) chatHandoffOpening(ctx context.Context, session db.ChatSession, fromName string) (string, error) {
	rows, err := h.Queries.ListChatMessagesPage(ctx, db.ListChatMessagesPageParams{
		ChatSessionID: session.ID,
		Limit:         int32(chatHandoffRecent + 2),
	})
	if err != nil {
		return "", err
	}
	rows = visibleChatMessages(rows)
	if len(rows) > chatHandoffRecent {
		rows = rows[:chatHandoffRecent]
	}
	total, err := h.Queries.CountHandoffChatMessages(ctx, session.ID)
	if err != nil {
		return "", err
	}
	first, err := h.Queries.GetEarliestHandoffUserMessage(ctx, session.ID)
	if err != nil {
		first = ""
	}
	return buildChatHandoffOpening(strings.TrimSpace(session.Title), fromName, uuidToString(session.ID), int(total), handoffExcerpt(first), rows), nil
}

// buildChatHandoffOpening renders the opening. rows are newest first.
func buildChatHandoffOpening(title, fromName, sessionID string, total int, first string, rows []db.ChatMessage) string {
	if title == "" {
		title = "未命名"
	}
	if fromName == "" {
		fromName = "上一个智能体"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "接手聊天「%s」，原来是 %s 在聊，共 %d 条。完整记录：`multica chat history --session %s`\n", title, fromName, total, sessionID)
	// The opening line is repeated only when the recent messages do not reach it.
	if first != "" && total > len(rows) {
		fmt.Fprintf(&b, "\n开头：%s\n", first)
	}
	if len(rows) > 0 {
		fmt.Fprintf(&b, "\n最近 %d 条：\n", len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			who := "我"
			if rows[i].Role == "assistant" {
				who = fromName
			}
			fmt.Fprintf(&b, "- %s：%s\n", who, handoffExcerpt(rows[i].Content))
		}
	}
	b.WriteString("\n先读上面的摘要，接着往下做。")
	return b.String()
}

// chatHandoffCall runs a chat handler in-process with the caller's identity.
// A refusal is relayed to the client unchanged.
func (h *Handler) chatHandoffCall(w http.ResponseWriter, r *http.Request, handle http.HandlerFunc, sessionID string, body any, out any) bool {
	raw, _ := json.Marshal(body)
	rctx := chi.NewRouteContext()
	if sessionID != "" {
		rctx.URLParams.Add("sessionId", sessionID)
	}
	req := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	req.Method = http.MethodPost
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.ContentLength = int64(len(raw))
	rec := httptest.NewRecorder()
	handle(rec, req)
	if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.Copy(w, rec.Result().Body)
		return false
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read handoff result")
		return false
	}
	return true
}
