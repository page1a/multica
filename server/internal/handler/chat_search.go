package handler

import (
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	chatSearchMaxResults   = 50
	chatSearchSnippetRunes = 120
)

// ChatMessageSearchHit is one chat whose messages contain the query: the
// newest matching message, clipped to a snippet around the first hit.
type ChatMessageSearchHit struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	Role      string `json:"role"`
	Snippet   string `json:"snippet"`
	CreatedAt string `json:"created_at"`
}

// SearchChatMessages backs the chat page's search box: titles match on the
// client, this finds chats whose message content does. Candidates are the
// same set ListChatSessions?status=all returns to this viewer (archived
// included), so a hit never reveals a chat the list would hide.
func (h *Handler) SearchChatMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	words := splitSearchTerms(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))))
	if len(words) == 0 {
		writeJSON(w, http.StatusOK, []ChatMessageSearchHit{})
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}
	projectIDs, err := h.chatProjectIDs(r.Context(), workspaceID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project access")
		return
	}
	sessions, err := h.Queries.ListAllChatSessionsByCreator(r.Context(), db.ListAllChatSessionsByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		ViewerID:    parseUUID(userID),
		ProjectIds:  projectIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
		return
	}
	sessionIDs := make([]pgtype.UUID, 0, len(sessions))
	for _, s := range sessions {
		// Same agent-access rule as ListChatSessions.
		if uuidToString(s.CreatorID) == userID {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
		}
		sessionIDs = append(sessionIDs, s.ID)
	}
	if len(sessionIDs) == 0 {
		writeJSON(w, http.StatusOK, []ChatMessageSearchHit{})
		return
	}

	patterns := make([]string, 0, len(words))
	for _, word := range words {
		patterns = append(patterns, "%"+escapeLike(word)+"%")
	}
	rows, err := h.Queries.SearchChatMessagesInSessions(r.Context(), db.SearchChatMessagesInSessionsParams{
		SessionIds: sessionIDs,
		Patterns:   patterns,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search chat messages")
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].CreatedAt.Time.After(rows[j].CreatedAt.Time)
	})
	if len(rows) > chatSearchMaxResults {
		rows = rows[:chatSearchMaxResults]
	}
	hits := make([]ChatMessageSearchHit, 0, len(rows))
	for _, row := range rows {
		hits = append(hits, ChatMessageSearchHit{
			SessionID: uuidToString(row.ChatSessionID),
			MessageID: uuidToString(row.ID),
			Role:      row.Role,
			Snippet:   chatSearchSnippet(row.Content, words[0]),
			CreatedAt: timestampToString(row.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, hits)
}

// chatSearchSnippet returns a single-line window of content around the first
// occurrence of word (already lowered), with ellipses where it was clipped.
func chatSearchSnippet(content, word string) string {
	flat := strings.Join(strings.Fields(content), " ")
	runes := []rune(flat)
	if len(runes) <= chatSearchSnippetRunes {
		return flat
	}
	start := 0
	if byteIdx := strings.Index(strings.ToLower(flat), word); byteIdx >= 0 {
		// strings.ToLower can change byte lengths only for rare runes; clamp
		// so a drifted offset still yields a valid window.
		if byteIdx > len(flat) {
			byteIdx = len(flat)
		}
		hit := utf8.RuneCountInString(flat[:byteIdx])
		start = hit - chatSearchSnippetRunes/4
		if start < 0 {
			start = 0
		}
	}
	end := start + chatSearchSnippetRunes
	if end > len(runes) {
		end = len(runes)
		start = end - chatSearchSnippetRunes
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
