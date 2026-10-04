package handler

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	defaultAgentChatsLimit = 5
	maxAgentChatsLimit     = 100
)

// AgentChatResponse is one chat in the agent overview's Chats section
// (DENE-1310). A chat the caller may not open still appears, because it holds
// the agent's concurrency like any other, but without its title or creator:
// Title and CreatorID are null and Visible is false.
type AgentChatResponse struct {
	ID             string  `json:"id"`
	Visible        bool    `json:"visible"`
	Title          *string `json:"title"`
	CreatorID      *string `json:"creator_id"`
	Status         string  `json:"status"` // running | queued | idle
	LastActivityAt string  `json:"last_activity_at"`
}

type ListAgentChatsResponse struct {
	Chats   []AgentChatResponse `json:"chats"`
	HasMore bool                `json:"has_more"`
}

// ListAgentChats lists an agent's open chats, busy first, filtered through
// the chat visibility rule: what the caller cannot open comes back redacted.
func (h *Handler) ListAgentChats(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	limit := defaultAgentChatsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxAgentChatsLimit)
	}

	projectIDs, err := h.chatProjectIDs(r.Context(), workspaceID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project access")
		return
	}
	rows, err := h.Queries.ListAgentChatSessions(r.Context(), db.ListAgentChatSessionsParams{
		ViewerID:    parseUUID(userID),
		ProjectIds:  projectIDs,
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
		PageLimit:   int32(limit + 1),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent chats")
		return
	}

	resp := ListAgentChatsResponse{Chats: make([]AgentChatResponse, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		resp.HasMore = true
	}
	for _, row := range rows {
		resp.Chats = append(resp.Chats, agentChatResponse(row))
	}
	writeJSON(w, http.StatusOK, resp)
}

func agentChatResponse(row db.ListAgentChatSessionsRow) AgentChatResponse {
	status := "idle"
	if row.Running {
		status = "running"
	} else if row.Active {
		status = "queued"
	}
	lastActivity := row.UpdatedAt
	if row.LastMessageAt.Valid {
		lastActivity = row.LastMessageAt
	}
	chat := AgentChatResponse{
		ID:             uuidToString(row.ID),
		Visible:        row.ViewerCanSee,
		Status:         status,
		LastActivityAt: timestampToString(lastActivity),
	}
	if row.ViewerCanSee {
		title := row.Title
		chat.Title = &title
		chat.CreatorID = uuidToPtr(row.CreatorID)
	}
	return chat
}
