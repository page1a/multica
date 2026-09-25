package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const chatShareResourceType = "chat_session"

// chatAccess is what one person may do with one chat. level is the value the
// client reads: owner (the creator), speak, view, or "" when the chat is not
// visible to them.
type chatAccess struct {
	see   bool
	speak bool
	level string
}

func (h *Handler) chatProjectIDs(ctx context.Context, workspaceID, userID string) ([]pgtype.UUID, error) {
	ids, err := h.listAccessibleProjectIDs(ctx, parseUUID(workspaceID), parseUUID(userID))
	if err != nil {
		return nil, err
	}
	if ids == nil {
		ids = []pgtype.UUID{}
	}
	return ids, nil
}

// chatAccessFor answers visibility and speaking for one session. Project
// members can speak; a named extra person speaks only when their share says
// so. Private chats are the creator's alone, even if a share row was left
// behind.
func (h *Handler) chatAccessFor(ctx context.Context, session db.ChatSession, userID string) (chatAccess, error) {
	if uuidToString(session.CreatorID) == userID {
		return chatAccess{see: true, speak: true, level: "owner"}, nil
	}
	if session.Visibility != "project" {
		return chatAccess{}, nil
	}
	projectIDs, err := h.chatProjectIDs(ctx, uuidToString(session.WorkspaceID), userID)
	if err != nil {
		return chatAccess{}, err
	}
	inProject, err := h.Queries.ViewerInChatProject(ctx, db.ViewerInChatProjectParams{
		ChatSessionID: session.ID,
		ProjectIds:    projectIDs,
	})
	if err != nil {
		return chatAccess{}, err
	}
	if inProject {
		return chatAccess{see: true, speak: true, level: "speak"}, nil
	}
	share, err := h.Queries.GetChatShareAccess(ctx, db.GetChatShareAccessParams{
		WorkspaceID: session.WorkspaceID,
		ResourceID:  uuidToString(session.ID),
		MemberID:    parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return chatAccess{}, nil
		}
		return chatAccess{}, err
	}
	if share == "speak" {
		return chatAccess{see: true, speak: true, level: "speak"}, nil
	}
	if share == "view" {
		return chatAccess{see: true, speak: false, level: "view"}, nil
	}
	return chatAccess{}, nil
}

func denyUnlessChatCreator(w http.ResponseWriter, session db.ChatSession, userID string) bool {
	if uuidToString(session.CreatorID) == userID {
		return true
	}
	writeError(w, http.StatusForbidden, "only the chat creator can change this")
	return false
}

func (h *Handler) decorateChatSession(ctx context.Context, userID string, session db.ChatSession, resp *ChatSessionResponse) error {
	resp.Visibility = session.Visibility
	access, err := h.chatAccessFor(ctx, session, userID)
	if err != nil {
		return err
	}
	resp.Access = access.level
	pinned, err := h.chatPinnedFor(ctx, session.WorkspaceID, session.ID, userID)
	if err != nil {
		return err
	}
	resp.Pinned = pinned
	agent, err := h.Queries.GetAgent(ctx, session.AgentID)
	if err == nil {
		resp.AgentName = agent.Name
		resp.AgentRuntimeBound = agent.RuntimeID.Valid
		resp.AgentArchived = agent.ArchivedAt.Valid
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	n, err := h.Queries.CountResourceShares(ctx, db.CountResourceSharesParams{
		WorkspaceID:  session.WorkspaceID,
		ResourceType: chatShareResourceType,
		ResourceID:   uuidToString(session.ID),
	})
	if err != nil {
		return err
	}
	resp.ExtraCount = int(n)
	return nil
}

func (h *Handler) publishChatInvalidated(workspaceID, actorID, sessionID string) {
	h.publishChat(protocol.EventChatSessionInvalidated, workspaceID, "member", actorID, sessionID, protocol.ChatSessionInvalidatedPayload{
		ChatSessionID: sessionID,
	})
}

type chatShareInput struct {
	UserID string `json:"user_id"`
	Access string `json:"access"`
}

type chatShareResponse struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Access string `json:"access"`
}

type chatAccessResponse struct {
	Mode       string              `json:"mode"`
	Visibility string              `json:"visibility"`
	CanEdit    bool                `json:"can_edit"`
	HasProject bool                `json:"has_project"`
	Shares     []chatShareResponse `json:"shares"`
}

func chatAccessMode(visibility string, shareCount int) string {
	if visibility != "project" {
		return "private"
	}
	if shareCount > 0 {
		return "extra"
	}
	return "project"
}

func (h *Handler) GetChatSessionAccess(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	shares, err := h.Queries.ListChatShares(r.Context(), db.ListChatSharesParams{
		WorkspaceID: session.WorkspaceID,
		ResourceID:  uuidToString(session.ID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat shares")
		return
	}
	hasProject, err := h.Queries.ChatSessionHasProject(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat projects")
		return
	}
	resp := chatAccessResponse{
		Mode:       chatAccessMode(session.Visibility, len(shares)),
		Visibility: session.Visibility,
		CanEdit:    uuidToString(session.CreatorID) == userID,
		HasProject: hasProject.Valid && hasProject.Bool,
		Shares:     make([]chatShareResponse, 0, len(shares)),
	}
	for _, share := range shares {
		resp.Shares = append(resp.Shares, chatShareResponse{
			UserID: uuidToString(share.MemberID),
			Name:   share.UserName,
			Email:  share.UserEmail,
			Access: share.Access,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type putChatAccessRequest struct {
	Mode   string           `json:"mode"`
	Shares []chatShareInput `json:"shares"`
}

func (h *Handler) PutChatSessionAccess(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	if !denyUnlessChatCreator(w, session, userID) {
		return
	}
	var req putChatAccessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Mode != "project" && req.Mode != "extra" && req.Mode != "private" {
		writeError(w, http.StatusBadRequest, "mode must be project, extra, or private")
		return
	}
	hasProject, err := h.Queries.ChatSessionHasProject(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat projects")
		return
	}
	bound := hasProject.Valid && hasProject.Bool
	if req.Mode != "private" && !bound {
		writeError(w, http.StatusBadRequest, "bind a project before sharing this chat")
		return
	}

	type grant struct {
		userID pgtype.UUID
		access string
	}
	var grants []grant
	if req.Mode == "extra" {
		seen := map[string]struct{}{}
		for _, share := range req.Shares {
			if share.Access != "view" && share.Access != "speak" {
				writeError(w, http.StatusBadRequest, "share access must be view or speak")
				return
			}
			memberID, ok := parseUUIDOrBadRequest(w, share.UserID, "user_id")
			if !ok {
				return
			}
			if uuidToString(memberID) == userID {
				writeError(w, http.StatusBadRequest, "the creator already has this chat")
				return
			}
			key := uuidToString(memberID)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID:      memberID,
				WorkspaceID: session.WorkspaceID,
			}); err != nil {
				writeError(w, http.StatusBadRequest, "that person is not in this workspace")
				return
			}
			grants = append(grants, grant{userID: memberID, access: share.Access})
		}
	}

	visibility := "private"
	if req.Mode != "private" {
		visibility = "project"
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.SetChatSessionVisibility(r.Context(), db.SetChatSessionVisibilityParams{
		ID:         session.ID,
		Visibility: visibility,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat visibility")
		return
	}
	if err := qtx.DeleteResourceSharesByResource(r.Context(), db.DeleteResourceSharesByResourceParams{
		WorkspaceID:  session.WorkspaceID,
		ResourceType: chatShareResourceType,
		ResourceID:   uuidToString(session.ID),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat shares")
		return
	}
	for _, g := range grants {
		if err := qtx.UpsertChatShare(r.Context(), db.UpsertChatShareParams{
			WorkspaceID: session.WorkspaceID,
			ResourceID:  uuidToString(session.ID),
			MemberID:    g.userID,
			AddedBy:     parseUUID(userID),
			Access:      g.access,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update chat shares")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save chat access")
		return
	}
	h.publishChatInvalidated(workspaceID, userID, uuidToString(session.ID))
	session.Visibility = visibility
	h.GetChatSessionAccess(w, r)
}

type makeChatsPrivateRequest struct {
	SessionIDs []string `json:"session_ids"`
}

func (h *Handler) MakeChatSessionsPrivate(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	var req makeChatsPrivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.SessionIDs) == 0 {
		writeError(w, http.StatusBadRequest, "session_ids is required")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated := 0
	var touched []string
	for _, raw := range req.SessionIDs {
		sessionID, ok := parseUUIDOrBadRequest(w, raw, "session_ids")
		if !ok {
			return
		}
		session, err := qtx.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          sessionID,
			WorkspaceID: parseUUID(workspaceID),
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		if uuidToString(session.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "only the chat creator can change this")
			return
		}
		if session.Visibility == "private" {
			continue
		}
		if _, err := qtx.SetChatSessionVisibility(r.Context(), db.SetChatSessionVisibilityParams{
			ID:         session.ID,
			Visibility: "private",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update chat visibility")
			return
		}
		if err := qtx.DeleteResourceSharesByResource(r.Context(), db.DeleteResourceSharesByResourceParams{
			WorkspaceID:  session.WorkspaceID,
			ResourceType: chatShareResourceType,
			ResourceID:   uuidToString(session.ID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update chat shares")
			return
		}
		updated++
		touched = append(touched, uuidToString(session.ID))
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to make chats private")
		return
	}
	for _, id := range touched {
		h.publishChatInvalidated(workspaceID, userID, id)
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

type chatNoticeSession struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type chatVisibilityNoticeResponse struct {
	Pending  bool                `json:"pending"`
	Count    int                 `json:"count"`
	Sessions []chatNoticeSession `json:"sessions"`
}

func (h *Handler) GetChatVisibilityNotice(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	dismissed, err := h.Queries.ChatVisibilityNoticeDismissed(r.Context(), db.ChatVisibilityNoticeDismissedParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat notice")
		return
	}
	empty := chatVisibilityNoticeResponse{Sessions: []chatNoticeSession{}}
	if dismissed {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	rows, err := h.Queries.ListOpenProjectChatsByCreator(r.Context(), db.ListOpenProjectChatsByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat notice")
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	resp := chatVisibilityNoticeResponse{
		Pending:  true,
		Count:    len(rows),
		Sessions: make([]chatNoticeSession, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Sessions = append(resp.Sessions, chatNoticeSession{
			ID:    uuidToString(row.ID),
			Title: row.Title,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DismissChatVisibilityNotice(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	if err := h.Queries.DismissChatVisibilityNotice(r.Context(), db.DismissChatVisibilityNoticeParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to dismiss chat notice")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
