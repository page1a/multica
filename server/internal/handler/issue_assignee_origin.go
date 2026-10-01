package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// assignmentRuling is the server's answer to "may this request put THIS
// executor on the ticket, and whose decision is it" (DENE-1033).
//
// The rule, in one place: an executor picked by a person's own hand (web,
// desktop, CLI as a member) stands. One picked by an agent stands only when the
// person the run belongs to said it in the message that started the run, and
// the server can find those words in that message. Anything else an agent
// names is the agent's own idea, and on a ticket routing will judge it is
// dropped — routing then decides from scratch, and says one sentence that a
// pick was ignored.
type assignmentRuling struct {
	// Apply is false when the executor named must not be written.
	Apply bool
	// Source is one of routing.Source*; SourceUser the person behind a
	// "human" or "quote" source; Quote the words the server verified.
	Source     string
	SourceUser pgtype.UUID
	Quote      string
}

// rulePick decides one request. actorType/actorID come from resolveActor (the
// server-trusted X-Agent-ID), resultingStatus is the status the ticket will
// have once the request lands.
func (h *Handler) rulePick(
	r *http.Request, workspaceID, actorType, actorID string,
	assigneeType pgtype.Text, assigneeID pgtype.UUID, quote, resultingStatus string,
) assignmentRuling {
	if actorType != "agent" {
		// A person's own hand. Whatever quote they sent is not needed.
		ruling := assignmentRuling{Apply: true, Source: routing.SourceHuman}
		if id, err := util.ParseUUID(actorID); err == nil && actorType == "member" {
			ruling.SourceUser = id
		}
		return ruling
	}

	if task, ok := h.liveTaskOf(r, actorID); ok && quote != "" {
		if user, holds := h.quoteFromInitiator(r.Context(), task, assigneeType, assigneeID, quote); holds {
			return assignmentRuling{Apply: true, Source: routing.SourceQuote, SourceUser: user, Quote: strings.TrimSpace(quote)}
		}
	}

	ruling := assignmentRuling{Apply: true, Source: routing.SourceAgent}
	if !assigneeType.Valid || (assigneeType.String != "agent" && assigneeType.String != "squad") {
		// Handing a ticket to a person is not something routing decides.
		return ruling
	}
	if resultingStatus != "todo" && resultingStatus != "backlog" {
		// Past todo the agent handoff pipelines (review, blocked, in-flight
		// reassignment) do their own work and routing does not re-pick.
		return ruling
	}
	if h.Routing == nil || !h.Routing.Active(r.Context(), workspaceID) {
		return ruling
	}
	ruling.Apply = false
	return ruling
}

// liveTaskOf returns the run the request belongs to: named by the
// server-trusted X-Task-ID, still live, and run by the acting agent. A finished
// run lends nothing, same as invokeOriginatorFromRequest.
func (h *Handler) liveTaskOf(r *http.Request, agentID string) (db.AgentTaskQueue, bool) {
	task, ok := h.taskFromRequestHeader(r)
	if !ok || isTerminalTaskStatus(task.Status) || uuidToString(task.AgentID) != agentID {
		return db.AgentTaskQueue{}, false
	}
	return task, true
}

// quoteFromInitiator is the whole quote check. The message that started the
// run must have been written by the run's initiator (a member, never another
// agent, never a third party), the quote must be a passage of it word for
// word, and the passage must name the agent being assigned.
func (h *Handler) quoteFromInitiator(ctx context.Context, task db.AgentTaskQueue, assigneeType pgtype.Text, assigneeID pgtype.UUID, quote string) (pgtype.UUID, bool) {
	initiator := task.OriginatorUserID
	if !initiator.Valid {
		return pgtype.UUID{}, false
	}
	name := h.assigneeName(ctx, assigneeType, assigneeID)
	if name == "" {
		return pgtype.UUID{}, false
	}
	for _, msg := range h.initiatorMessages(ctx, task) {
		if msg.author != uuidToString(initiator) {
			continue
		}
		if routing.QuoteHolds(msg.content, quote, name) {
			return initiator, true
		}
	}
	return pgtype.UUID{}, false
}

type triggerMessage struct {
	author  string // member id; "" when the author is not a member
	content string
}

// initiatorMessages lists the messages that started the run: the trigger
// comment (and any comments folded into it) for a comment-started run, the
// sealed input batch for a chat turn. Only member-authored rows carry an
// author; an agent-authored or unattributed row never matches an initiator.
func (h *Handler) initiatorMessages(ctx context.Context, task db.AgentTaskQueue) []triggerMessage {
	var out []triggerMessage
	ids := append([]pgtype.UUID{}, task.CoalescedCommentIds...)
	if task.TriggerCommentID.Valid {
		ids = append(ids, task.TriggerCommentID)
	}
	for _, id := range ids {
		c, err := h.Queries.GetComment(ctx, id)
		if err != nil || c.DeletedAt.Valid || c.AuthorType != "member" || !c.AuthorID.Valid {
			continue
		}
		out = append(out, triggerMessage{author: uuidToString(c.AuthorID), content: c.Content})
	}
	if task.ChatInputTaskID.Valid {
		msgs, err := h.Queries.ListChatInputMessages(ctx, task.ChatInputTaskID)
		if err == nil {
			creator := ""
			if task.ChatSessionID.Valid {
				if sess, err := h.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
					creator = uuidToString(sess.CreatorID)
				}
			}
			for _, m := range msgs {
				author := uuidToString(m.SenderUserID)
				if author == "" {
					author = creator
				}
				out = append(out, triggerMessage{author: author, content: m.Content})
			}
		}
	}
	return out
}

func (h *Handler) assigneeName(ctx context.Context, assigneeType pgtype.Text, assigneeID pgtype.UUID) string {
	if !assigneeType.Valid || !assigneeID.Valid {
		return ""
	}
	switch assigneeType.String {
	case "agent":
		if a, err := h.Queries.GetAgent(ctx, assigneeID); err == nil {
			return a.Name
		}
	case "squad":
		if s, err := h.Queries.GetSquad(ctx, assigneeID); err == nil {
			return s.Name
		}
	}
	return ""
}

// stampAssignee records whose decision the executor on the ticket is. Best
// effort: the executor is already written, and an unstamped ticket reads as
// legacy (trusted), so a failure here loses a label, not a ruling.
func (h *Handler) stampAssignee(ctx context.Context, issue db.Issue, ruling assignmentRuling) db.Issue {
	stamped, err := h.Queries.SetIssueAssigneeSource(ctx, db.SetIssueAssigneeSourceParams{
		ID:                   issue.ID,
		WorkspaceID:          issue.WorkspaceID,
		AssigneeSource:       pgtype.Text{String: ruling.Source, Valid: ruling.Source != ""},
		AssigneeSourceUserID: ruling.SourceUser,
		AssigneeQuote:        pgtype.Text{String: ruling.Quote, Valid: ruling.Quote != ""},
	})
	if err != nil {
		return issue
	}
	return stamped
}
