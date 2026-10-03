package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type DismissInboxRequest struct {
	Reason string `json:"reason"`
}

type DismissInboxResponse struct {
	IssueID     string `json:"issue_id"`
	SummonID    string `json:"summon_id"`
	InboxItemID string `json:"inbox_item_id,omitempty"`
	CommentID   string `json:"comment_id"`
	RecipientID string `json:"recipient_id"`
	Reason      string `json:"reason"`
}

// DismissInbox clears the current person's stale "waiting on you" reminder
// for one issue. The person is derived from the direct-human run originator;
// callers cannot choose another recipient. Closing the summon, archiving its
// one inbox row, and writing the visible audit comment are one transaction.
func (h *Handler) DismissInbox(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "issueId"))
	if !ok {
		return
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "only an agent in a direct-human run may dismiss a waiting reminder")
		return
	}
	recipient, ok := h.inboxBoardPerson(w, r, issue.WorkspaceID)
	if !ok {
		return
	}

	var req DismissInboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reason := strings.TrimSpace(sanitizeNullBytes(req.Reason))
	if reason == "" {
		writeError(w, http.StatusBadRequest, "缺 --reason：为什么这条等待提醒已经过期")
		return
	}

	agentID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, http.StatusForbidden, "invalid agent identity")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusForbidden, "agent identity is no longer available")
		return
	}
	content := fmt.Sprintf("已由智能体 %s 清理这条过期的「等你」：%s", strings.TrimSpace(agent.Name), reason)
	if strings.TrimSpace(agent.Name) == "" {
		content = fmt.Sprintf("已由智能体 %s 清理这条过期的「等你」：%s", actorID, reason)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin dismiss")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   actorType,
		AuthorID:     agentID,
		Content:      content,
		Type:         "comment",
		SourceTaskID: dismissSourceTask(r, h),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to record dismiss reason")
		}
		return
	}
	dismissed, err := qtx.DismissOpenIssueSummonForRecipient(r.Context(), db.DismissOpenIssueSummonForRecipientParams{
		IssueID:     issue.ID,
		RecipientID: recipient,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no open waiting reminder for this person on this issue")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to dismiss waiting reminder")
		}
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit dismiss")
		return
	}

	comment := created.Comment()
	commentResp := commentToResponse(comment, nil, nil)
	commentResp.IssueRevision = created.IssueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"comment":             commentResp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	if dismissed.InboxItemID.Valid {
		h.publish(protocol.EventInboxArchived, uuidToString(issue.WorkspaceID), "member", uuidToString(recipient), map[string]any{
			"item_id":      uuidToString(dismissed.InboxItemID),
			"issue_id":     uuidToString(issue.ID),
			"recipient_id": uuidToString(recipient),
		})
	}

	writeJSON(w, http.StatusOK, DismissInboxResponse{
		IssueID:     uuidToString(issue.ID),
		SummonID:    uuidToString(dismissed.ID),
		InboxItemID: uuidToString(dismissed.InboxItemID),
		CommentID:   uuidToString(comment.ID),
		RecipientID: uuidToString(recipient),
		Reason:      reason,
	})
}

func dismissSourceTask(r *http.Request, h *Handler) pgtype.UUID {
	if task, ok := h.taskFromRequestHeader(r); ok {
		return task.ID
	}
	return pgtype.UUID{}
}
