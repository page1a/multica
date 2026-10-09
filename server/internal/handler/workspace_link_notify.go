package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Link request notices (DENE-1641). An offer waits for the viewer's owner or
// admin, so they get a request in the viewer workspace's inbox; the answer
// goes back to the source's managers as a receipt. A pull is active at once
// and notifies nobody. The notices are side effects after the write commits:
// a failed notice never fails the link change.
const (
	inboxTypeWorkspaceLinkRequest  = "workspace_link_request"
	inboxTypeWorkspaceLinkAccepted = "workspace_link_accepted"
	inboxTypeWorkspaceLinkDeclined = "workspace_link_declined"
)

// linkNoticeFacts is what every notice about one link carries.
type linkNoticeFacts struct {
	link           db.WorkspaceLink
	source, target db.Workspace
}

func (h *Handler) loadLinkNoticeFacts(ctx context.Context, link db.WorkspaceLink) (linkNoticeFacts, bool) {
	source, err := h.Queries.GetWorkspace(ctx, link.SourceWorkspaceID)
	if err != nil {
		slog.WarnContext(ctx, "workspace link notice: source workspace", "error", err)
		return linkNoticeFacts{}, false
	}
	target, err := h.Queries.GetWorkspace(ctx, link.TargetWorkspaceID)
	if err != nil {
		slog.WarnContext(ctx, "workspace link notice: target workspace", "error", err)
		return linkNoticeFacts{}, false
	}
	return linkNoticeFacts{link: link, source: source, target: target}, true
}

func (f linkNoticeFacts) details(extra map[string]string) []byte {
	d := map[string]string{
		"link_id":     uuidToString(f.link.ID),
		"source_name": f.source.Name,
		"source_slug": f.source.Slug,
		"target_name": f.target.Name,
		"target_slug": f.target.Slug,
	}
	for k, v := range extra {
		d[k] = v
	}
	raw, _ := json.Marshal(d)
	return raw
}

// notifyWorkspaceLinkOffered tells the viewer's managers a link waits for them.
func (h *Handler) notifyWorkspaceLinkOffered(ctx context.Context, linkID, actorUser pgtype.UUID) {
	link, err := h.Queries.GetWorkspaceLink(ctx, linkID)
	if err != nil || link.Status != "pending" {
		return
	}
	f, ok := h.loadLinkNoticeFacts(ctx, link)
	if !ok {
		return
	}
	titles := []string{}
	if projects, err := h.Queries.ListWorkspaceLinkProjects(ctx, db.ListWorkspaceLinkProjectsParams{
		LinkID: link.ID, SourceWorkspaceID: link.SourceWorkspaceID,
	}); err == nil {
		for _, p := range projects {
			titles = append(titles, p.Title)
		}
	}
	body := strings.Join(titles, ", ")
	details := f.details(map[string]string{"project_titles": body})
	h.notifyWorkspaceManagers(ctx, link.TargetWorkspaceID, actorUser, inboxTypeWorkspaceLinkRequest, "action_required",
		f.source.Name+" wants to share projects with "+f.target.Name, body, details)
}

// answerWorkspaceLinkRequest closes the request notice everywhere it landed
// and, for an accept or a decline, sends the source's managers a receipt.
// receiptType is empty when the source withdrew its own offer.
func (h *Handler) answerWorkspaceLinkRequest(ctx context.Context, link db.WorkspaceLink, actorUser pgtype.UUID, receiptType string) {
	h.archiveWorkspaceLinkRequest(ctx, link)
	if receiptType == "" {
		return
	}
	f, ok := h.loadLinkNoticeFacts(ctx, link)
	if !ok {
		return
	}
	title := f.target.Name + " declined the link"
	if receiptType == inboxTypeWorkspaceLinkAccepted {
		title = f.target.Name + " accepted the link"
	}
	h.notifyWorkspaceManagers(ctx, link.SourceWorkspaceID, actorUser, receiptType, "info", title, "", f.details(nil))
}

func (h *Handler) archiveWorkspaceLinkRequest(ctx context.Context, link db.WorkspaceLink) {
	rows, err := h.Queries.ArchiveWorkspaceLinkRequestInbox(ctx, db.ArchiveWorkspaceLinkRequestInboxParams{
		WorkspaceID: link.TargetWorkspaceID, LinkID: uuidToString(link.ID),
	})
	if err != nil {
		slog.WarnContext(ctx, "workspace link notice: archive request", "error", err)
		return
	}
	ws := uuidToString(link.TargetWorkspaceID)
	for _, row := range rows {
		recipient := uuidToString(row.RecipientID)
		h.publish(protocol.EventInboxArchived, ws, "member", recipient, map[string]any{
			"item_id":      uuidToString(row.ID),
			"issue_id":     nil,
			"recipient_id": recipient,
		})
	}
}

// notifyWorkspaceManagers writes one issue-less notice to every owner and
// admin of ws except the person who acted.
func (h *Handler) notifyWorkspaceManagers(ctx context.Context, ws, actorUser pgtype.UUID, itemType, severity, title, body string, details []byte) {
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, ws)
	if err != nil {
		slog.WarnContext(ctx, "workspace link notice: managers", "error", err)
		return
	}
	for _, recipient := range managers {
		if recipient == actorUser {
			continue
		}
		item, err := h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   ws,
			RecipientType: "member",
			RecipientID:   recipient,
			Type:          itemType,
			Severity:      severity,
			Title:         title,
			Body:          pgtype.Text{String: body, Valid: body != ""},
			// The actor sits in the other workspace; naming them here would
			// point at someone this inbox cannot resolve.
			ActorType: pgtype.Text{String: "system", Valid: true},
			Details:   details,
		})
		if err != nil {
			slog.WarnContext(ctx, "workspace link notice: inbox write", "type", itemType, "error", err)
			continue
		}
		h.publish(protocol.EventInboxNew, uuidToString(ws), "system", "", map[string]any{"item": inboxToResponse(item)})
	}
}
