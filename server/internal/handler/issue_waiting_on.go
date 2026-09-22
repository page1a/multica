package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// notifyWaitersOfIssueDone wakes issues that recorded close.waiting_on for
// this issue when it transitions into a terminal status (done or cancelled).
//
// This is the Stage 3 (DENE-232) immediate path of protocol scan D: a
// cross-family wait that was only a metadata string becomes an observable
// wake the moment the waited-on issue finishes. Same-family waits belong on
// parent_issue_id + stage (see notifyParentOfChildDone) and are skipped here
// so a parent does not get a child-done comment and a waiting_on comment for
// the same completion.
//
// Enqueue reuses EnqueueTaskForMention / EnqueueTaskForSquadLeader — the same
// surfaces as child-done and @mention — and skips when the waiter (issue,
// agent) already has a queued or running task (HasActiveTaskForIssueAndAgent).
// Errors are warn-and-swallow: the waited-on status write has already
// committed.
func (h *Handler) notifyWaitersOfIssueDone(ctx context.Context, prev, issue db.Issue) {
	effective := h.childStatusResolver(ctx)
	prevStatus, err := effective(prev)
	if err != nil {
		slog.Warn("waiting_on: failed to resolve previous status", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	nowStatus, err := effective(issue)
	if err != nil {
		slog.Warn("waiting_on: failed to resolve status", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	if isTerminalChildStatus(prevStatus) || !isTerminalChildStatus(nowStatus) {
		return
	}
	h.wakeWaitersOf(ctx, issue, effective)
}

// notifyWaitersOfIssuesDone is the batch counterpart. `completed` must already
// be the set of issues that entered a terminal status in this batch; the
// transition guard is not re-applied so a mid-batch waiter that also finished
// is still filtered by wakeWaitersOf's live-row reload.
func (h *Handler) notifyWaitersOfIssuesDone(ctx context.Context, completed []db.Issue) {
	if len(completed) == 0 {
		return
	}
	effective := h.childStatusResolver(ctx)
	for _, issue := range completed {
		h.wakeWaitersOf(ctx, issue, effective)
	}
}

func (h *Handler) wakeWaitersOf(ctx context.Context, issue db.Issue, effective func(db.Issue) (string, error)) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	identifier := prefix + "-" + strconv.Itoa(int(issue.Number))
	issueID := uuidToString(issue.ID)
	waiters, err := h.Queries.ListIssuesWaitingOn(ctx, db.ListIssuesWaitingOnParams{
		WorkspaceID:         issue.WorkspaceID,
		WaitingOnIdentifier: waitingOnFilter(identifier),
		WaitingOnID:         waitingOnFilter(issueID),
	})
	if err != nil {
		slog.Warn("waiting_on: failed to list waiters",
			"error", err,
			"issue_id", issueID,
			"identifier", identifier)
		return
	}
	for _, waiter := range waiters {
		h.wakeWaitingIssue(ctx, waiter, issue, identifier, effective)
	}
}

func waitingOnFilter(value string) []byte {
	b, err := json.Marshal(map[string]string{closeprotocol.KeyWaitingOn: value})
	if err != nil {
		return []byte(`{"close.waiting_on":""}`)
	}
	return b
}

func (h *Handler) wakeWaitingIssue(ctx context.Context, waiter, completed db.Issue, completedIdentifier string, effective func(db.Issue) (string, error)) {
	if waiter.ID == completed.ID {
		return
	}
	// Same-family waits are the stage barrier's job. A parent that also set
	// close.waiting_on to this child would otherwise get two system comments
	// when the barrier closes (DENE-232: convert those waits to stages).
	if completed.ParentIssueID.Valid && completed.ParentIssueID == waiter.ID {
		return
	}

	fresh, err := h.Queries.GetIssue(ctx, waiter.ID)
	if err != nil {
		slog.Warn("waiting_on: failed to reload waiter",
			"error", err,
			"waiter_id", uuidToString(waiter.ID),
			"completed_id", uuidToString(completed.ID))
		return
	}
	waiterStatus, err := effective(fresh)
	if err != nil {
		slog.Warn("waiting_on: failed to resolve waiter status", "error", err, "waiter_id", uuidToString(fresh.ID))
		return
	}
	if isTerminalChildStatus(waiterStatus) || waiterStatus == "backlog" {
		return
	}
	if fresh.AssigneeType.Valid && fresh.AssigneeType.String == "member" {
		return
	}
	if !fresh.AssigneeType.Valid || !fresh.AssigneeID.Valid {
		return
	}

	mentionPrefix := h.buildParentAssigneeMention(ctx, fresh)
	title := sanitizeChildTitleForSystemComment(completed.Title)
	content := fmt.Sprintf(
		"%sThe issue you were waiting on, [%s](mention://issue/%s) — \"%s\" — just finished. close.waiting_on is resolved; continue this issue.",
		mentionPrefix, completedIdentifier, uuidToString(completed.ID), title,
	)

	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     fresh.ID,
		WorkspaceID: fresh.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("waiting_on: create system comment failed",
			"error", err,
			"waiter_id", uuidToString(fresh.ID),
			"completed_id", uuidToString(completed.ID))
		return
	}
	comment := created.Comment()

	h.publish(protocol.EventCommentCreated, uuidToString(fresh.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         fresh.Title,
		"issue_assignee_type": textToPtr(fresh.AssigneeType),
		"issue_assignee_id":   uuidToPtr(fresh.AssigneeID),
		"issue_status":        fresh.Status,
		"issue_revision":      created.IssueRevision,
	})

	h.dispatchWaitingOnAssigneeTrigger(ctx, fresh, comment.ID)
}

// dispatchWaitingOnAssigneeTrigger mirrors dispatchParentAssigneeTrigger but
// keys idempotency on HasActiveTaskForIssueAndAgent (queued / dispatched /
// running / waiting_local_directory). Protocol §5.5 and DENE-232: skip
// enqueue when that pair is already in flight; the system comment above is
// still the observable wake.
func (h *Handler) dispatchWaitingOnAssigneeTrigger(ctx context.Context, waiter db.Issue, triggerCommentID pgtype.UUID) {
	if !waiter.AssigneeType.Valid || !waiter.AssigneeID.Valid {
		return
	}
	switch waiter.AssigneeType.String {
	case "agent":
		h.triggerWaitingOnAgent(ctx, waiter, waiter.AssigneeID, triggerCommentID)
	case "squad":
		h.triggerWaitingOnSquad(ctx, waiter, triggerCommentID)
	}
}

func (h *Handler) triggerWaitingOnAgent(ctx context.Context, waiter db.Issue, agentID pgtype.UUID, triggerCommentID pgtype.UUID) {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: waiter.WorkspaceID,
	})
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid || !agent.WorkEnabled {
		return
	}
	hasActive, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: waiter.ID,
		AgentID: agentID,
	})
	if err != nil || hasActive {
		return
	}
	if _, err := h.TaskService.EnqueueTaskForMention(ctx, waiter, agentID, triggerCommentID, service.OriginDerived); err != nil {
		slog.Warn("waiting_on: enqueue waiter agent task failed",
			"error", err,
			"waiter_id", uuidToString(waiter.ID),
			"agent_id", uuidToString(agentID))
	}
}

func (h *Handler) triggerWaitingOnSquad(ctx context.Context, waiter db.Issue, triggerCommentID pgtype.UUID) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID:          waiter.AssigneeID,
		WorkspaceID: waiter.WorkspaceID,
	})
	if err != nil {
		return
	}
	agent, err := h.Queries.GetAgent(ctx, squad.LeaderID)
	if err != nil || !agent.RuntimeID.Valid || agent.ArchivedAt.Valid || !agent.WorkEnabled {
		return
	}
	hasActive, err := h.Queries.HasActiveTaskForIssueAndAgent(ctx, db.HasActiveTaskForIssueAndAgentParams{
		IssueID: waiter.ID,
		AgentID: squad.LeaderID,
	})
	if err != nil || hasActive {
		return
	}
	if _, err := h.TaskService.EnqueueTaskForSquadLeader(ctx, waiter, squad.LeaderID, squad.ID, triggerCommentID, service.OriginDerived); err != nil {
		slog.Warn("waiting_on: enqueue waiter squad leader task failed",
			"error", err,
			"waiter_id", uuidToString(waiter.ID),
			"squad_id", uuidToString(squad.ID),
			"leader_id", uuidToString(squad.LeaderID))
	}
}
