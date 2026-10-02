package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const progressHistoryLimit = 100

type progressRequest struct {
	Text string `json:"text"`
	// Tone is optional; empty derives it (issue status for issues, working
	// for chats).
	Tone string `json:"tone"`
}

// progressEntry is one write of the goal/progress subtitle.
type progressEntry struct {
	Text       string
	Source     string
	Tone       string
	AuthorType string
	AuthorID   string
}

// progressAuthorID is NULL for system writers (recap, parking) that have no
// author id.
func progressAuthorID(id string) pgtype.UUID {
	u, _ := util.ParseUUID(id)
	return u
}

func progressPayload(p *ProgressResponse) *protocol.ProgressPayload {
	if p == nil {
		return nil
	}
	return &protocol.ProgressPayload{Text: p.Text, Source: p.Source, Tone: p.Tone, AuthorType: p.AuthorType, AuthorID: p.AuthorID, UpdatedAt: p.UpdatedAt}
}

func decodeProgress(w http.ResponseWriter, r *http.Request) (progressRequest, bool) {
	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return req, false
	}
	if len([]rune(req.Text)) > progress.MaxLen*2 {
		writeError(w, http.StatusBadRequest, "text is too long")
		return req, false
	}
	req.Text = progress.Clip(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return req, false
	}
	tone, ok := progress.NormalizeTone(req.Tone)
	if !ok {
		writeError(w, http.StatusBadRequest, "tone must be one of working, waiting, stuck, done")
		return req, false
	}
	req.Tone = tone
	return req, true
}

// recordIssueProgress writes the issue's progress line and its history row.
// It never touches revision or updated_at (see progress.sql). The caller
// publishes; publishIssueProgress is the shared broadcast.
func (h *Handler) recordIssueProgress(ctx context.Context, issue db.Issue, e progressEntry) (db.Issue, error) {
	if e.Tone == "" {
		e.Tone = progress.ForStatus(issuestatus.Effective(ctx, h.Queries, issue.WorkspaceID, issue.Status))
	}
	updated, err := h.Queries.UpdateIssueProgress(ctx, db.UpdateIssueProgressParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Text: e.Text, Source: e.Source, Tone: e.Tone,
		AuthorType: e.AuthorType, AuthorID: progressAuthorID(e.AuthorID),
	})
	if err != nil {
		return db.Issue{}, err
	}
	if err := h.Queries.CreateIssueProgress(ctx, db.CreateIssueProgressParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Text: e.Text, Source: e.Source, Tone: e.Tone,
		AuthorType: e.AuthorType, AuthorID: progressAuthorID(e.AuthorID),
	}); err != nil {
		return db.Issue{}, err
	}
	return updated, nil
}

// publishIssueProgress pushes issue:updated so every board, list and detail
// view patches the progress line in place. progress_changed lets the detail
// view refresh its history without refetching the issue.
func (h *Handler) publishIssueProgress(ctx context.Context, issue db.Issue, actorType, actorID string) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue":            service.IssueToMapResolved(ctx, h.Queries, issue, prefix),
		"status_changed":   false,
		"prev_status":      issue.Status,
		"progress_changed": true,
	})
}

// WriteIssueProgress is POST /api/issues/{id}/progress — `multica issue
// progress`. An agent's own line always lands (latest explicit line wins).
func (h *Handler) WriteIssueProgress(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	req, ok := decodeProgress(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	updated, err := h.recordIssueProgress(r.Context(), issue, progressEntry{
		Text: req.Text, Source: progress.SourceAgent, Tone: req.Tone, AuthorType: actorType, AuthorID: actorID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update issue progress")
		return
	}
	h.publishIssueProgress(r.Context(), updated, actorType, actorID)
	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	resp := issueToResponse(updated, prefix)
	h.fillStatusCategory(r.Context(), issue.WorkspaceID, &resp)
	writeJSON(w, http.StatusOK, resp)
}

// ListIssueProgress is GET /api/issues/{id}/progress — newest first.
func (h *Handler) ListIssueProgress(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueProgress(r.Context(), db.ListIssueProgressParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, RowLimit: progressHistoryLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue progress")
		return
	}
	items := make([]ProgressResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProgressResponse{Text: row.Text, Source: row.Source, Tone: row.Tone, AuthorType: row.AuthorType, AuthorID: uuidToString(row.AuthorID), UpdatedAt: timestampToString(row.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": items})
}

// recordChatProgress writes the chat's progress line plus history. With
// fallbackOnly (the model recap), an agent line written at or after since
// wins and nothing is written: (zero, false, nil).
func (h *Handler) recordChatProgress(ctx context.Context, session db.ChatSession, e progressEntry, fallbackOnly bool, since pgtype.Timestamptz) (db.ChatSession, bool, error) {
	if e.Tone == "" {
		e.Tone = progress.ToneWorking
	}
	updated, err := h.Queries.UpdateChatSessionProgress(ctx, db.UpdateChatSessionProgressParams{
		ID: session.ID, WorkspaceID: session.WorkspaceID, Text: e.Text, Source: e.Source, Tone: e.Tone,
		AuthorType: e.AuthorType, AuthorID: progressAuthorID(e.AuthorID), FallbackOnly: fallbackOnly, Since: since,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.ChatSession{}, false, nil
		}
		return db.ChatSession{}, false, err
	}
	if err := h.Queries.CreateChatSessionProgress(ctx, db.CreateChatSessionProgressParams{
		WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID, Text: e.Text, Source: e.Source, Tone: e.Tone,
		AuthorType: e.AuthorType, AuthorID: progressAuthorID(e.AuthorID),
	}); err != nil {
		return db.ChatSession{}, false, err
	}
	return updated, true, nil
}

func (h *Handler) publishChatSessionState(workspaceID, actorType, actorID string, s db.ChatSession) {
	resp := chatSessionToResponse(s)
	h.publishChat(protocol.EventChatSessionUpdated, workspaceID, actorType, actorID, uuidToString(s.ID), protocol.ChatSessionUpdatedPayload{
		ChatSessionID: uuidToString(s.ID),
		Title:         s.Title,
		TitleLocked:   boolPointer(s.TitleLocked),
		Progress:      progressPayload(resp.Progress),
		UpdatedAt:     timestampToString(s.UpdatedAt),
	})
}

// WriteChatProgress is POST /api/chat/sessions/{sessionId}/progress — `multica
// chat progress`.
func (h *Handler) WriteChatProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	req, ok := decodeProgress(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	updated, _, err := h.recordChatProgress(r.Context(), session, progressEntry{
		Text: req.Text, Source: progress.SourceAgent, Tone: req.Tone, AuthorType: actorType, AuthorID: actorID,
	}, false, pgtype.Timestamptz{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat progress")
		return
	}
	h.publishChatSessionState(workspaceID, actorType, actorID, updated)
	writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
}

// ListChatProgress is GET /api/chat/sessions/{sessionId}/progress.
func (h *Handler) ListChatProgress(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListChatSessionProgress(r.Context(), db.ListChatSessionProgressParams{ChatSessionID: session.ID, WorkspaceID: session.WorkspaceID, RowLimit: progressHistoryLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat progress")
		return
	}
	items := make([]ProgressResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, ProgressResponse{Text: row.Text, Source: row.Source, Tone: row.Tone, AuthorType: row.AuthorType, AuthorID: uuidToString(row.AuthorID), UpdatedAt: timestampToString(row.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": items})
}
