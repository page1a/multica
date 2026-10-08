package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
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
// Anything else an agent names is its own guess: on a ticket routing judges it
// is dropped and routing decides. A quote that fails verification is the same
// guess, not a reason to hold the ticket (DENE-1613): the check decides whose
// word the executor is, never whether the ticket gets one.
type assignmentRuling struct {
	// Apply is false when the executor named must not be written.
	Apply bool
	// Source is one of routing.Source*; SourceUser the person behind a
	// "human" or "quote" source; Quote the words the server verified.
	Source     string
	SourceUser pgtype.UUID
	Quote      string
	// Seat, when set, is the agent the pick lands on instead of the one named:
	// a quote naming only a base role puts the issue on that role's
	// specialisation for the issue's domain (DENE-1451).
	Seat pgtype.UUID
	// Reason is returned to an agent when a requested pick is not applied, so
	// it knows what happened to the slot instead of guessing again.
	Reason string
}

// heldExecutor is the executor a ticket holds before the request lands. A
// create has none and passes nil.
type heldExecutor struct {
	Type pgtype.Text
	ID   pgtype.UUID
}

func (e *heldExecutor) is(assigneeType pgtype.Text, assigneeID pgtype.UUID) bool {
	return e != nil && e.Type.Valid && e.ID.Valid &&
		e.Type.String == assigneeType.String && e.ID == assigneeID
}

// rulePick decides one request. actorType/actorID come from resolveActor (the
// server-trusted X-Agent-ID), resultingStatus is the status the ticket will
// have once the request lands, held is the executor already on it (nil on
// create).
func (h *Handler) rulePick(
	r *http.Request, workspaceID, actorType, actorID string,
	assigneeType pgtype.Text, assigneeID pgtype.UUID, quote, resultingStatus string,
	held *heldExecutor, scene sceneRef,
) assignmentRuling {
	if actorType != "agent" {
		// A person's own hand. Whatever quote they sent is not needed.
		ruling := assignmentRuling{Apply: true, Source: routing.SourceHuman}
		if id, err := util.ParseUUID(actorID); err == nil && actorType == "member" {
			ruling.SourceUser = id
		}
		return ruling
	}

	quoteFailed := false
	if quote != "" {
		if task, ok := h.liveTaskOf(r, actorID); ok {
			if user, holds := h.quoteFromInitiator(r.Context(), task, assigneeType, assigneeID, quote); holds {
				ruling := assignmentRuling{Apply: true, Source: routing.SourceQuote, SourceUser: user, Quote: strings.TrimSpace(quote)}
				if seat, ok := h.seatForQuote(r.Context(), workspaceID, assigneeType, assigneeID, quote, scene); ok {
					ruling.Seat = seat
				}
				return ruling
			}
		}
		// Not verified: from here on it is an unquoted pick (DENE-1613).
		quoteFailed = true
	}

	ruling := assignmentRuling{Apply: true, Source: routing.SourceAgent}
	if !assigneeType.Valid || (assigneeType.String != "agent" && assigneeType.String != "squad") {
		// Handing a ticket to a person is not something routing decides.
		return ruling
	}
	if h.Routing == nil || !h.Routing.Active(r.Context(), workspaceID) {
		return ruling
	}
	if resultingStatus == "todo" || resultingStatus == "backlog" {
		// Dropped: routing fills an empty slot from scratch.
		ruling.Apply = false
		if quoteFailed {
			ruling.Reason = routing.ReasonQuoteNotVerified
		}
		return ruling
	}
	// Past todo (DENE-1201). The executor of a ticket in flight is not an
	// agent's to change: the server-side pipelines that move it (quota relay,
	// reviewer relay, escalate, handoff) write the row themselves and never
	// come through here. Re-sending the executor already there is not a
	// change, and a create is not a reassignment.
	if held == nil || held.is(assigneeType, assigneeID) {
		return ruling
	}
	ruling.Apply = false
	ruling.Reason = routing.ReasonAgentReassignInFlight
	if quoteFailed {
		ruling.Reason = "the --per-quote words were not found in an earlier message by the person who started this run, or did not name this assignee; " + ruling.Reason
	}
	return ruling
}

// voidRouterSeatRuns applies service.ReassignVoidsSeatRuns to a write that just
// landed: prev is the issue before it, issue after it, ruling whose decision
// the new executor is. Best effort — the reassignment stands either way.
func (h *Handler) voidRouterSeatRuns(r *http.Request, prev, issue db.Issue, ruling assignmentRuling) {
	if !ruling.Apply || h.TaskService == nil {
		return
	}
	var except pgtype.UUID
	if task, ok := h.taskFromRequestHeader(r); ok {
		except = task.ID
	}
	cancelled, err := h.TaskService.CancelRunsVoidedByReassign(r.Context(), issue.ID,
		service.ExecutorSeat{Type: prev.AssigneeType, ID: prev.AssigneeID, Source: prev.AssigneeSource.String},
		service.ExecutorSeat{Type: issue.AssigneeType, ID: issue.AssigneeID, Source: ruling.Source},
		except)
	if err != nil {
		slog.Warn("cancel runs of replaced router seat failed", "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if len(cancelled) > 0 {
		slog.Info("cancelled runs of replaced router seat", "issue_id", uuidToString(issue.ID), "count", len(cancelled))
	}
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
// name the agent being assigned, or the base role it is a specialisation of.
// Direct-chat history and the issue thread are
// both valid evidence; the original trigger remains valid for compatibility.
func (h *Handler) quoteFromInitiator(ctx context.Context, task db.AgentTaskQueue, assigneeType pgtype.Text, assigneeID pgtype.UUID, quote string) (pgtype.UUID, bool) {
	initiator := task.OriginatorUserID
	if !initiator.Valid {
		return pgtype.UUID{}, false
	}
	names := h.assigneeNames(ctx, assigneeType, assigneeID, quote)
	if len(names) == 0 {
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
		directSelf := directChat && uuidToString(task.AgentID) == uuidToString(assigneeID)
		for _, name := range names {
			if quoteNamesAssignee(msg.content, quote, name, directSelf) {
				return initiator, true
			}
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

// assigneeNames lists the names a person's words may use to pick this
// assignee: its own, and for a specialisation also its base role's, so "交给孙悟空"
// covers any direction seat under 孙悟空. Only upward: a quote that names some
// direction seat ("孙悟空出海") contains the base name too, so it counts for that
// seat's own name alone and never reaches its siblings. Which direction fits
// is the agent's call by project; the server only checks the name was said.
func (h *Handler) assigneeNames(ctx context.Context, assigneeType pgtype.Text, assigneeID pgtype.UUID, quote string) []string {
	if !assigneeType.Valid || !assigneeID.Valid {
		return nil
	}
	switch assigneeType.String {
	case "agent":
		a, err := h.Queries.GetAgent(ctx, assigneeID)
		if err != nil {
			return nil
		}
		names := []string{a.Name}
		if !a.ParentAgentID.Valid {
			return names
		}
		parent, err := h.Queries.GetAgent(ctx, a.ParentAgentID)
		if err != nil {
			return names
		}
		siblings, err := h.Queries.ListAgentChildren(ctx, a.ParentAgentID)
		if err != nil {
			return names
		}
		q := strings.ToLower(strings.Join(strings.Fields(quote), " "))
		for _, sib := range siblings {
			if n := strings.ToLower(strings.Join(strings.Fields(sib.Name), " ")); n != "" && strings.Contains(q, n) {
				return names
			}
		}
		return append(names, parent.Name)
	case "squad":
		if s, err := h.Queries.GetSquad(ctx, assigneeID); err == nil {
			return []string{s.Name}
		}
	}
	return nil
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

// sceneRef is what a pick's scene is resolved from: the issue's domain (as
// it will be stored) and its project.
type sceneRef struct {
	Domain  pgtype.UUID
	Project pgtype.UUID
}

// seatForQuote is rule 3 of DENE-1477: a quote naming only a base role puts
// the issue on that role's specialisation that fits the issue's scene
// (service.LoadDomainScene, then routing.DomainFit). A quote naming the
// specialisation itself, or a generic scene, leaves the named seat standing.
func (h *Handler) seatForQuote(ctx context.Context, workspaceID string, assigneeType pgtype.Text, assigneeID pgtype.UUID, quote string, at sceneRef) (pgtype.UUID, bool) {
	if !assigneeType.Valid || assigneeType.String != "agent" || !assigneeID.Valid {
		return pgtype.UUID{}, false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	scene := service.LoadDomainScene(ctx, h.Queries, wsUUID, at.Domain, at.Project)
	if scene.Scene.Generic() {
		return pgtype.UUID{}, false
	}
	named, err := h.Queries.GetAgent(ctx, assigneeID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	base := named
	if named.ParentAgentID.Valid {
		if base, err = h.Queries.GetAgent(ctx, named.ParentAgentID); err != nil {
			return pgtype.UUID{}, false
		}
	}
	children, err := h.Queries.ListAgentChildren(ctx, base.ID)
	if err != nil {
		return pgtype.UUID{}, false
	}
	q := strings.ToLower(strings.Join(strings.Fields(quote), " "))
	for _, child := range children {
		if n := strings.ToLower(strings.Join(strings.Fields(child.Name), " ")); n != "" && strings.Contains(q, n) {
			return pgtype.UUID{}, false
		}
	}
	// The fitting specialisation, in the scene's domain order when a project
	// in several domains has one for more than one of them.
	var seat db.Agent
	rank := len(scene.Scene.Domains)
	for _, child := range children {
		if child.ArchivedAt.Valid || service.AgentDomainFit(scene.Scene, child, scene.Names) != routing.FitMatch {
			continue
		}
		domain := service.AgentDomain(child, scene.Names)
		for i, d := range scene.Scene.Domains {
			if d == domain && i < rank {
				seat, rank = child, i
			}
		}
	}
	if !seat.ID.Valid || seat.ID == named.ID {
		return pgtype.UUID{}, false
	}
	return seat.ID, true
}
