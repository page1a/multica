package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Session lineage (DENE-1345): every issue or chat run records whether its
// CLI session continues an earlier run's session or starts a new one, and
// why a new one. The claim writes the server's intent; the daemon's terminal
// report flips a resumed row to session_lost when the resume fell back.
const (
	sessionModeNew     = "new"
	sessionModeResumed = "resumed"

	sessionBreakFirstRun       = "first_run"
	sessionBreakAgentChanged   = "agent_changed"
	sessionBreakRuntimeChanged = "runtime_changed"
	sessionBreakSessionLost    = "session_lost"
	sessionBreakFreshRequested = "fresh_requested"
)

// sessionBreakContext is what the scope looked like before a new-session run.
type sessionBreakContext struct {
	otherAgentRan    bool
	ownLastRuntimeID string
}

// classifySessionBreak names why a run that resumes nothing starts a new
// session. Sessions are keyed by agent + issue (or chat), so a different
// issue is always a first run there and never shows up as its own reason.
func classifySessionBreak(task db.AgentTaskQueue, bc sessionBreakContext) string {
	if bc.ownLastRuntimeID == "" {
		if bc.otherAgentRan {
			return sessionBreakAgentChanged
		}
		return sessionBreakFirstRun
	}
	if task.ForceFreshSession && !task.RerunOfTaskID.Valid && !task.RetryOfTaskID.Valid {
		return sessionBreakFreshRequested
	}
	if bc.ownLastRuntimeID != uuidToString(task.RuntimeID) {
		return sessionBreakRuntimeChanged
	}
	return sessionBreakSessionLost
}

// recordSessionLineage writes the claim-time lineage. Best-effort: a failure
// costs the run its display line, never the claim.
func (h *Handler) recordSessionLineage(ctx context.Context, task db.AgentTaskQueue, priorSessionID string) {
	if !task.IssueID.Valid && !task.ChatSessionID.Valid {
		return
	}
	params := db.SetAgentTaskSessionLineageParams{ID: task.ID}
	if priorSessionID != "" {
		params.SessionMode = sessionModeResumed
		src, err := h.sessionSourceTask(ctx, task, priorSessionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("session lineage: source lookup failed", "task_id", uuidToString(task.ID), "error", err)
		}
		params.ResumedFromTaskID = src
	} else {
		bc, err := h.sessionBreakContext(ctx, task)
		if err != nil {
			slog.Warn("session lineage: break context failed", "task_id", uuidToString(task.ID), "error", err)
			return
		}
		params.SessionMode = sessionModeNew
		params.SessionBreakReason = pgtype.Text{String: classifySessionBreak(task, bc), Valid: true}
	}
	if err := h.Queries.SetAgentTaskSessionLineage(ctx, params); err != nil {
		slog.Warn("session lineage: write failed", "task_id", uuidToString(task.ID), "error", err)
	}
}

func (h *Handler) sessionSourceTask(ctx context.Context, task db.AgentTaskQueue, sessionID string) (pgtype.UUID, error) {
	if task.IssueID.Valid {
		return h.Queries.GetIssueSessionSourceTask(ctx, db.GetIssueSessionSourceTaskParams{
			IssueID: task.IssueID, AgentID: task.AgentID, SessionID: sessionID, TaskID: task.ID,
		})
	}
	return h.Queries.GetChatSessionSourceTask(ctx, db.GetChatSessionSourceTaskParams{
		ChatSessionID: task.ChatSessionID, AgentID: task.AgentID, SessionID: sessionID, TaskID: task.ID,
	})
}

func (h *Handler) sessionBreakContext(ctx context.Context, task db.AgentTaskQueue) (sessionBreakContext, error) {
	if task.IssueID.Valid {
		row, err := h.Queries.GetIssueRunBreakContext(ctx, db.GetIssueRunBreakContextParams{
			IssueID: task.IssueID, AgentID: task.AgentID, TaskID: task.ID,
		})
		return sessionBreakContext{otherAgentRan: row.OtherAgentRan, ownLastRuntimeID: row.OwnLastRuntimeID}, err
	}
	row, err := h.Queries.GetChatRunBreakContext(ctx, db.GetChatRunBreakContextParams{
		ChatSessionID: task.ChatSessionID, AgentID: task.AgentID, TaskID: task.ID,
	})
	return sessionBreakContext{otherAgentRan: row.OtherAgentRan, ownLastRuntimeID: row.OwnLastRuntimeID}, err
}

// markSessionResumeDropped records a daemon-reported resume fallback.
func (h *Handler) markSessionResumeDropped(ctx context.Context, taskID pgtype.UUID) {
	if err := h.Queries.MarkAgentTaskSessionResumeDropped(ctx, taskID); err != nil {
		slog.Warn("session lineage: mark resume dropped failed", "task_id", uuidToString(taskID), "error", err)
	}
}

// taskSessionLineage is one run's lineage, keyed by task id.
type taskSessionLineage struct {
	mode, resumedFrom, breakReason string
}

// loadTaskSessionLineage batches the lineage of the given runs. Best-effort:
// a failed read returns an empty map and the messages render without it.
func (h *Handler) loadTaskSessionLineage(ctx context.Context, taskIDs []pgtype.UUID) map[string]taskSessionLineage {
	out := map[string]taskSessionLineage{}
	if len(taskIDs) == 0 {
		return out
	}
	rows, err := h.Queries.ListTaskSessionLineageByIDs(ctx, taskIDs)
	if err != nil {
		slog.Warn("session lineage: batch read failed", "error", err)
		return out
	}
	for _, row := range rows {
		if !row.SessionMode.Valid {
			continue
		}
		out[uuidToString(row.ID)] = taskSessionLineage{
			mode:        row.SessionMode.String,
			resumedFrom: uuidToString(row.ResumedFromTaskID),
			breakReason: row.SessionBreakReason.String,
		}
	}
	return out
}

// chatMessageTaskIDs returns the distinct run ids behind a page of messages.
func chatMessageTaskIDs(messages []db.ChatMessage) []pgtype.UUID {
	seen := map[pgtype.UUID]bool{}
	var ids []pgtype.UUID
	for _, m := range messages {
		if m.TaskID.Valid && !seen[m.TaskID] {
			seen[m.TaskID] = true
			ids = append(ids, m.TaskID)
		}
	}
	return ids
}

func (h *Handler) attachChatSessionLineage(ctx context.Context, resp []ChatMessageResponse, messages []db.ChatMessage) {
	lineage := h.loadTaskSessionLineage(ctx, chatMessageTaskIDs(messages))
	for i, m := range messages {
		if l, ok := lineage[uuidToString(m.TaskID)]; ok && m.TaskID.Valid {
			resp[i].SessionMode = l.mode
			resp[i].ResumedFromRun = l.resumedFrom
			resp[i].SessionBreakReason = l.breakReason
		}
	}
}
