package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Send modes for a message that arrives while the agent is still replying
// (DENE-1346). The same three words are used by chat send and comment add.
const (
	// sendModeSteer hands the message to the running reply: it reads it
	// after the current step, in the same process and session.
	sendModeSteer = "steer"
	// sendModeQueue waits for the reply to finish, then starts a new run
	// that resumes the same session. The default.
	sendModeQueue = "queue"
	// sendModeRestart stops the reply now and starts over from this message,
	// in a new run that resumes the same session.
	sendModeRestart = "restart"
)

// errCodeSteerUnsupported marks a steer the running CLI cannot take. The
// response lists the modes that still work, so a CLI caller can retry.
const errCodeSteerUnsupported = "steer_unsupported"

func normalizeSendMode(mode string) (string, bool) {
	switch mode {
	case "", sendModeQueue:
		return sendModeQueue, true
	case sendModeSteer, sendModeRestart:
		return mode, true
	}
	return "", false
}

// steerUnsupportedReason names why a running reply cannot take a message.
func steerUnsupportedReason(provider string) string {
	switch provider {
	case "claude", "codex", "grok":
		return fmt.Sprintf("the running %s CLI is too old to read a message mid-reply; upgrade it, or send with queue or restart", provider)
	case "":
		return "the running agent cannot read a message mid-reply; send with queue or restart"
	}
	return fmt.Sprintf("%s cannot read a message mid-reply; send with queue or restart", provider)
}

func writeSteerUnsupported(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":           reason,
		"code":            errCodeSteerUnsupported,
		"available_modes": []string{sendModeQueue, sendModeRestart},
	})
}

// chatSteerTarget is the reply a steered chat message would be read by. ok is
// false when nothing is replying, so steer degrades to an ordinary send.
func (h *Handler) chatSteerTarget(ctx context.Context, sessionID pgtype.UUID) (db.GetChatSteerTargetRow, bool, error) {
	target, err := h.Queries.GetChatSteerTarget(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, false, nil
	}
	if err != nil {
		return target, false, err
	}
	return target, true, nil
}

func chatSteerSupported(target db.GetChatSteerTargetRow) bool {
	return target.Capability == protocol.DaemonCapabilityTaskSupplementV1
}

// steerChatFollowup asks the running reply to read the message whose queued
// follow-up was just created. A reply that finished in between is not an
// error: the follow-up answers the message as an ordinary queued turn, and
// the returned mode says so.
func (h *Handler) steerChatFollowup(ctx context.Context, session db.ChatSession, target db.GetChatSteerTargetRow, message db.ChatMessage, followup db.AgentTaskQueue, authorID pgtype.UUID) string {
	row, err := h.Queries.CreateChatTaskSupplement(ctx, db.CreateChatTaskSupplementParams{
		TaskID:         target.ID,
		ChatSessionID:  session.ID,
		ChatMessageID:  message.ID,
		FollowupTaskID: followup.ID,
		WorkspaceID:    session.WorkspaceID,
		AuthorID:       authorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sendModeQueue
	}
	if err != nil {
		slog.Warn("steer chat message failed; it stays queued",
			"chat_session_id", uuidToString(session.ID), "task_id", uuidToString(target.ID), "error", err)
		return sendModeQueue
	}
	if h.DaemonTaskSupplement != nil && row.RuntimeID.Valid {
		h.DaemonTaskSupplement.NotifyTaskSupplementAvailable(uuidToString(row.RuntimeID), uuidToString(row.TaskID))
	}
	return sendModeSteer
}

// restartChatReply moves the just-queued follow-up to the front and stops the
// reply in progress, so the agent starts over from the new message. Returns
// false when there was no reply left to stop; the follow-up then simply runs.
func (h *Handler) restartChatReply(r *http.Request, session db.ChatSession, followup db.AgentTaskQueue, actorType, actorID string) (bool, error) {
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.GetAgentForClaimUpdate(ctx, session.AgentID); err != nil {
		return false, fmt.Errorf("lock chat agent: %w", err)
	}
	prioritized, err := qtx.PrioritizeQueuedChatTask(ctx, db.PrioritizeQueuedChatTaskParams{
		ID: followup.ID, ChatSessionID: session.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("prioritize follow-up: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	if !prioritized.ActiveTaskID.Valid {
		return false, nil
	}
	_, err = h.TaskService.CancelTaskWithResult(ctx, prioritized.ActiveTaskID, service.CancelTaskOptions{
		ClientSupportsDraftRestore: requestHasClientCapability(r, protocol.AppCapabilityChatDraftRestoreV1),
		CancelledBy:                h.taskCancellationActor(ctx, actorType, actorID),
		UserInitiated:              true,
	})
	if err != nil {
		return false, fmt.Errorf("stop current reply: %w", err)
	}
	return true, nil
}

// commentSteerTaskIDs picks the running turns a steered comment goes into:
// one per agent the comment would wake. ok=false means a refusal was written.
// No running turn at all is not a refusal — the comment starts runs as usual.
func (h *Handler) commentSteerTaskIDs(w http.ResponseWriter, ctx context.Context, issue db.Issue, triggers []commentAgentTrigger, authorType string, hasAttachments bool) ([]pgtype.UUID, bool) {
	if authorType != "member" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":           "only a person can steer a running reply; send with queue or restart",
			"code":            errCodeSteerUnsupported,
			"available_modes": []string{sendModeQueue, sendModeRestart},
		})
		return nil, false
	}
	running, err := h.Queries.ListIssueSteerTargets(ctx, issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load running replies")
		return nil, false
	}
	woken := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		woken[uuidToString(trigger.Agent.ID)] = true
	}
	var (
		ids        []pgtype.UUID
		seen       = map[string]bool{}
		refusedFor string
		anyRunning bool
	)
	for _, task := range running {
		agentID := uuidToString(task.AgentID)
		if !woken[agentID] || seen[agentID] {
			continue
		}
		seen[agentID] = true
		anyRunning = true
		if task.Capability != protocol.DaemonCapabilityTaskSupplementV1 {
			refusedFor = task.Provider
			continue
		}
		ids = append(ids, task.ID)
	}
	if !anyRunning {
		return nil, true
	}
	if hasAttachments {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":           "a running reply only reads text; send files with queue or restart",
			"code":            errCodeSteerUnsupported,
			"available_modes": []string{sendModeQueue, sendModeRestart},
		})
		return nil, false
	}
	if len(ids) == 0 {
		writeSteerUnsupported(w, steerUnsupportedReason(refusedFor))
		return nil, false
	}
	return ids, true
}

// stopCommentRecipients stops the replies of the agents a restart comment
// wakes, so the comment starts their next run instead of waiting behind it.
func (h *Handler) stopCommentRecipients(ctx context.Context, issue db.Issue, triggers []commentAgentTrigger, actorType, actorID string) {
	tasks, err := h.Queries.ListActiveTasksByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("restart comment: list active tasks failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	woken := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		woken[uuidToString(trigger.Agent.ID)] = true
	}
	for _, task := range tasks {
		if !woken[uuidToString(task.AgentID)] || task.Status == "queued" {
			continue
		}
		if _, err := h.TaskService.CancelTaskWithResult(ctx, task.ID, service.CancelTaskOptions{
			CancelledBy:   h.taskCancellationActor(ctx, actorType, actorID),
			UserInitiated: true,
		}); err != nil {
			slog.Warn("restart comment: stop reply failed", "task_id", uuidToString(task.ID), "error", err)
		}
	}
}
