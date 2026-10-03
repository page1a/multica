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
// person the run belongs to said it in an earlier message in the direct chat
// or issue thread, and the server can find those words in that message.
// Anything else an agent names is rejected; an unquoted guess may still be
// dropped for routing, but a supplied quote that fails verification leaves the
// ticket unassigned so the agent can ask the person.
type assignmentRuling struct {
	// Apply is false when the executor named must not be written.
	Apply bool
	// Source is one of routing.Source*; SourceUser the person behind a
	// "human" or "quote" source; Quote the words the server verified.
	Source     string
	SourceUser pgtype.UUID
	Quote      string
	// Reason is returned to an agent when a requested pick is rejected. It is
	// intentionally explicit so the agent can ask the person for a real quote
	// instead of guessing again.
	Reason string
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

	if quote != "" {
		if task, ok := h.liveTaskOf(r, actorID); ok {
			if user, holds := h.quoteFromInitiator(r.Context(), task, assigneeType, assigneeID, quote); holds {
				return assignmentRuling{Apply: true, Source: routing.SourceQuote, SourceUser: user, Quote: strings.TrimSpace(quote)}
			}
		}
		if assigneeType.Valid && (assigneeType.String == "agent" || assigneeType.String == "squad") {
			return assignmentRuling{
				Apply:  false,
				Source: routing.SourceQuoteRejected,
				Reason: "assignee quote was not found in an earlier message by the person who started this run, or did not name the requested assignee",
			}
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

// quoteFromInitiator is the whole quote check. The message must have been
// written by the run's initiator (a member, never another agent, never a third
// party), the quote must be a passage of it word for word, and the passage must
// name the agent being assigned. Direct-chat history and the issue thread are
// both valid evidence; the original trigger remains valid for compatibility.
func (h *Handler) quoteFromInitiator(ctx context.Context, task db.AgentTaskQueue, assigneeType pgtype.Text, assigneeID pgtype.UUID, quote string) (pgtype.UUID, bool) {
	initiator := task.OriginatorUserID
	if !initiator.Valid {
		return pgtype.UUID{}, false
	}
	name := h.assigneeName(ctx, assigneeType, assigneeID)
	if name == "" {
		return pgtype.UUID{}, false
	}
	directChat := false
	if task.ChatSessionID.Valid {
		if channelType, err := h.sessionChannelType(ctx, task.ChatSessionID); err == nil {
			directChat = channelType == ""
		}
	}
	for _, msg := range h.initiatorMessages(ctx, task) {
		if msg.author != uuidToString(initiator) {
			continue
		}
		if quoteNamesAssignee(msg.content, quote, name, directChat && uuidToString(task.AgentID) == uuidToString(assigneeID)) {
			return initiator, true
		}
	}
	return pgtype.UUID{}, false
}

type triggerMessage struct {
	author  string // member id; "" when the author is not a member
	content string
}

// initiatorMessages lists the member-authored evidence available to this run:
// the trigger comments, the full history of a direct chat before this run, and
// comments in the issue thread. Only rows authored by the run initiator can
// match, so widening the evidence window does not weaken the anti-forgery gate.
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
	if task.ChatSessionID.Valid {
		if msgs, err := h.Queries.ListChatMessages(ctx, task.ChatSessionID); err == nil {
			creator := ""
			if sess, err := h.Queries.GetChatSession(ctx, task.ChatSessionID); err == nil {
				creator = uuidToString(sess.CreatorID)
			}
			for _, m := range msgs {
				if m.Role != "user" {
					continue
				}
				author := uuidToString(m.SenderUserID)
				if author == "" {
					author = creator
				}
				out = append(out, triggerMessage{author: author, content: m.Content})
			}
		}
	}
	if task.IssueID.Valid {
		if issue, err := h.Queries.GetIssue(ctx, task.IssueID); err == nil {
			if comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
				IssueID: task.IssueID, WorkspaceID: issue.WorkspaceID, Limit: 1000,
			}); err == nil {
				for _, c := range comments {
					if c.DeletedAt.Valid || c.AuthorType != "member" || !c.AuthorID.Valid {
						continue
					}
					out = append(out, triggerMessage{author: uuidToString(c.AuthorID), content: c.Content})
				}
			}
		}
	}
	return out
}

func quoteNamesAssignee(message, quote, name string, directSelf bool) bool {
	if routing.QuoteHolds(message, quote, name) {
		return true
	}
	if !directSelf {
		return false
	}
	// A second-person quote identifies the current agent only in a direct
	// chat, but it still has to be a passage of the user's message. Without
	// this check an agent could invent "你来做" after any unrelated message
	// and bypass the quote provenance check.
	message = strings.Join(strings.Fields(message), " ")
	q := strings.Join(strings.Fields(quote), " ")
	if q == "" || !strings.Contains(strings.ToLower(message), strings.ToLower(q)) {
		return false
	}
	return strings.Contains(q, "你来做") ||
		strings.Contains(q, "你自己做") ||
		strings.Contains(q, "指派给你") ||
		strings.Contains(q, "让你做") ||
		strings.Contains(q, "你来负责")
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
