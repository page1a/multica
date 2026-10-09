package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/receipt"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// dispatchedReceiptLimit bounds the per-turn list of a chat's tickets.
const dispatchedReceiptLimit = 10

// issueReceipt reads a task's result from what the issue records (DENE-1672).
// The close fields are kept only while the close still describes the current
// status; a stale close would report an old conclusion as the current one.
func (h *Handler) issueReceipt(ctx context.Context, issue db.Issue, prefix string) receipt.Receipt {
	r := receipt.Receipt{
		IssueID:    uuidToString(issue.ID),
		Identifier: issueToResponse(issue, prefix).Identifier,
		Title:      issue.Title,
		Status:     issue.Status,
		PRs:        []receipt.PR{},
		UpdatedAt:  timestampToString(issue.UpdatedAt),
	}
	meta := issueMetaStrings(issue.Metadata)
	if closeprotocol.Complete(meta) && closeprotocol.StatusMatchesIssue(meta[closeprotocol.KeyStatus], issue.Status) && !closeprotocol.Superseded(meta) {
		r.Conclusion = meta[closeprotocol.KeyConclusion]
		r.ClosedAt = strings.TrimSpace(meta[closeprotocol.KeyAt])
		r.Summary = receipt.Clip(h.closeNote(ctx, issue, meta).Summary, receipt.MaxSummary)
		r.Knowledge = knowledgeLine(meta[closeprotocol.KeyKnowledgeAudit])
	}
	if gh, vcsRows, err := h.loadDeliveryRows(ctx, issue.ID); err == nil {
		for _, p := range gh {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
		for _, p := range vcsRows {
			r.PRs = append(r.PRs, receipt.PR{Number: p.PrNumber, URL: p.HtmlUrl, State: prReceiptState(p.State, p.MergedAt.Valid)})
		}
	} else {
		slog.Warn("receipt: list pull requests failed", "issue_id", r.IssueID, "error", err)
	}
	return r
}

// childReceipts is the receipts of a parent's sub-tasks that keep accepts,
// in creation order (DENE-1679). A parent reports for its children, so its
// card, its state card and its child-done comment all list them.
func (h *Handler) childReceipts(ctx context.Context, parent db.Issue, keep func(db.Issue) bool) []receipt.Receipt {
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil {
		slog.Warn("receipt: list children failed", "issue_id", uuidToString(parent.ID), "error", err)
		return nil
	}
	effective := h.childStatusResolver(ctx)
	return h.receiptsOf(ctx, parent.WorkspaceID, children, keep, func(c db.Issue) string {
		status, _ := effective(c)
		return status
	})
}

// receiptsOf reads the receipts of the issues keep accepts. status names each
// issue's canonical status; an empty answer keeps the stored one.
func (h *Handler) receiptsOf(ctx context.Context, workspaceID pgtype.UUID, issues []db.Issue, keep func(db.Issue) bool, status func(db.Issue) string) []receipt.Receipt {
	prefix := h.getIssuePrefix(ctx, workspaceID)
	var out []receipt.Receipt
	for _, c := range issues {
		if !keep(c) {
			continue
		}
		r := h.issueReceipt(ctx, c, prefix)
		if s := status(c); s != "" {
			r.Status = s
		}
		out = append(out, r)
	}
	return out
}

// visibleWithParent keeps a sub-task that everyone who reads the parent can
// see: the system comment it lands in stays on the parent for every present
// and future reader. Two checks must both hold. The child is scoped no wider
// apart from its parent (future readers arrive through the parent's scope),
// and every member who can see the parent now can see the child, by the same
// rules the issue APIs use (an assignee or a share names the parent alone).
// Failing to establish the readers keeps nothing.
func (h *Handler) visibleWithParent(ctx context.Context, parent db.Issue) func(db.Issue) bool {
	none := func(db.Issue) bool { return false }
	members, err := h.Queries.ListMembers(ctx, parent.WorkspaceID)
	if err != nil {
		slog.Warn("receipt: list members failed", "issue_id", uuidToString(parent.ID), "error", err)
		return none
	}
	var readers []visibilityViewer
	for _, m := range members {
		v, err := h.visibilityViewerForUser(ctx, parent.WorkspaceID, m.UserID)
		if err != nil {
			slog.Warn("receipt: load member visibility failed", "issue_id", uuidToString(parent.ID), "error", err)
			return none
		}
		if v.canSeeIssue(parent) {
			readers = append(readers, v)
		}
	}
	scoped := scopedLikeParent(parent)
	return func(c db.Issue) bool {
		if !scoped(c) {
			return false
		}
		for _, v := range readers {
			if !v.canSeeIssue(c) {
				return false
			}
		}
		return true
	}
}

// scopedLikeParent keeps a sub-task whose scope reaches everyone the parent's
// scope does: one open to the workspace under a parent no guest can reach
// (a guest reaches a project, never the workspace), or one scoped exactly like
// its parent.
func scopedLikeParent(parent db.Issue) func(db.Issue) bool {
	return func(c db.Issue) bool {
		switch c.Visibility {
		case "workspace":
			return parent.Visibility != "project"
		case "project":
			return parent.Visibility == "project" && c.ProjectID == parent.ProjectID
		case "private":
			return parent.Visibility == "private" && c.CreatorType == parent.CreatorType && c.CreatorID == parent.CreatorID
		}
		return false
	}
}

// visibleInChat keeps a sub-task everyone who can see the chat may see, the
// rule chat titles follow: the chat's owner sees it, and a chat open beyond
// its owner only names workspace-wide issues.
func (h *Handler) visibleInChat(ctx context.Context, session db.ChatSession) func(db.Issue) bool {
	viewer, err := h.visibilityViewerForUser(ctx, session.WorkspaceID, session.CreatorID)
	if err != nil {
		return func(db.Issue) bool { return false }
	}
	return func(c db.Issue) bool {
		return viewer.canSeeIssue(c) && (session.Visibility == "private" || c.Visibility == "workspace")
	}
}

func prReceiptState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	return state
}

// knowledgeLine is what the close wrote into project memory, in one line;
// empty when it wrote nothing.
func knowledgeLine(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	audit, err := closeprotocol.ParseStoredKnowledgeAudit(raw)
	if err != nil {
		return ""
	}
	if audit.None {
		return "" // nothing worth a line on the card
	}
	parts := make([]string, 0, len(audit.Changes))
	for _, c := range audit.Changes {
		parts = append(parts, c.Location+"："+c.Summary)
	}
	return receipt.Clip(strings.Join(parts, "；"), 200)
}

// postSourceChatReceipt puts a receipt card into the chat an issue was opened
// from when the issue enters a status the chat must hear about. It sits
// beside notifyParentOfChildDone on every status-transition path. A
// sub-issue of a ticket from the same chat stays quiet: its parent reports.
// Best-effort: a failure never undoes the status change.
func (h *Handler) postSourceChatReceipt(ctx context.Context, prev, issue db.Issue) {
	if !issue.OriginChatSessionID.Valid {
		return
	}
	effective := h.childStatusResolver(ctx)
	prevStatus, err := effective(prev)
	if err != nil {
		return
	}
	nowStatus, err := effective(issue)
	if err != nil || prevStatus == nowStatus || !receipt.Reportable(nowStatus) {
		return
	}
	if issue.ParentIssueID.Valid {
		if parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID); err == nil && parent.OriginChatSessionID == issue.OriginChatSessionID {
			return
		}
	}
	session, err := h.Queries.GetChatSession(ctx, issue.OriginChatSessionID)
	if err != nil || session.WorkspaceID != issue.WorkspaceID {
		return
	}
	// The card names the issue to everyone in the chat; the chat's owner must
	// be able to see it.
	if viewer, err := h.visibilityViewerForUser(ctx, issue.WorkspaceID, session.CreatorID); err != nil || !viewer.canSeeIssue(issue) {
		return
	}
	r := h.issueReceipt(ctx, issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
	r.Status = nowStatus
	r.Children = h.childReceipts(ctx, issue, h.visibleInChat(ctx, session))
	msg, err := h.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: session.ID,
		Role:          "assistant",
		Content:       r.Markdown(),
		MessageKind:   pgtype.Text{String: protocol.ChatMessageKindIssueReceipt, Valid: true},
	})
	if err != nil {
		slog.Warn("receipt: create chat message failed", "issue_id", r.IssueID, "chat_session_id", uuidToString(session.ID), "error", err)
		return
	}
	if err := h.Queries.TouchChatSession(ctx, session.ID); err != nil {
		slog.Warn("receipt: touch chat session failed", "chat_session_id", uuidToString(session.ID), "error", err)
	}
	sessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatMessage, uuidToString(session.WorkspaceID), "system", "", sessionID, protocol.ChatMessagePayload{
		ChatSessionID: sessionID,
		MessageID:     uuidToString(msg.ID),
		Role:          "assistant",
		Content:       msg.Content,
		CreatedAt:     timestampToString(msg.CreatedAt),
	})
}

// chatDispatchedLines is the per-turn "tickets you opened" list a chat run
// opens with: the newest few, limited to what the chat's owner can see.
func (h *Handler) chatDispatchedLines(ctx context.Context, session db.ChatSession) []string {
	viewer, err := h.visibilityViewerForUser(ctx, session.WorkspaceID, session.CreatorID)
	if err != nil {
		return nil
	}
	issues, err := h.Queries.ListIssuesByOriginChatSession(ctx, db.ListIssuesByOriginChatSessionParams{
		WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID,
	})
	if err != nil {
		slog.Warn("receipt: list chat tickets failed", "chat_session_id", uuidToString(session.ID), "error", err)
		return nil
	}
	prefix := h.getIssuePrefix(ctx, session.WorkspaceID)
	lines := []string{}
	for i := len(issues) - 1; i >= 0 && len(lines) < dispatchedReceiptLimit; i-- {
		if viewer.canSeeIssue(issues[i]) {
			lines = append(lines, h.issueReceipt(ctx, issues[i], prefix).Line())
		}
	}
	return lines
}
