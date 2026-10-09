package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// metaKeyBacklogWaitingFor is the one line saying what a backlog ticket waits
// for (DENE-1638). Routing leaves backlog alone, so an agent that parks a
// ticket it meant to start strands it; asking for the reason makes "nothing to
// wait for" visible as "use todo". The ticket detail shows it while the ticket
// is in backlog; leaving backlog drops it.
const metaKeyBacklogWaitingFor = "backlog.waiting_for"

const maxBacklogWaitingForRunes = 80

// backlogWaitingForRejection is the 400 an agent gets for putting a ticket in
// backlog without saying what it waits for. People are not asked; a staged
// child is exempt because its stage gate already says what it waits for.
func backlogWaitingForRejection(waitingFor *string, actorType string, staged bool) string {
	if actorType != "agent" || staged {
		return ""
	}
	reason := strings.TrimSpace(deref(waitingFor))
	if reason == "" {
		return "没在等什么就用 todo：路由不碰 backlog，放进去就没人接。确实要停着，用 --waiting-for 写一句在等什么（80 字以内），例如 --waiting-for \"等公司注册办好\"。"
	}
	if len([]rune(reason)) > maxBacklogWaitingForRunes {
		return "--waiting-for 超过 80 字，写一句话就够。"
	}
	return ""
}

func (h *Handler) isBacklogStatus(ctx context.Context, workspaceID pgtype.UUID, status string) bool {
	return issuestatus.Effective(ctx, h.Queries, workspaceID, status) == issuestatus.Backlog
}

// syncBacklogWaitingFor records the reason when a write leaves the ticket in
// backlog with one, and drops it once the ticket is out of backlog. It returns
// the issue with the metadata the write left, so the response and the
// broadcast carry the line.
func (h *Handler) syncBacklogWaitingFor(ctx context.Context, issue db.Issue, waitingFor *string) db.Issue {
	if !h.isBacklogStatus(ctx, issue.WorkspaceID, issue.Status) {
		if _, ok := parseIssueMetadata(issue.Metadata)[metaKeyBacklogWaitingFor]; !ok {
			return issue
		}
		row, err := h.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: metaKeyBacklogWaitingFor})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("backlog waiting_for: metadata delete failed", "error", err, "issue_id", uuidToString(issue.ID))
			}
			return issue
		}
		issue.Metadata, issue.Revision = row.Metadata, row.Revision
		return issue
	}
	reason := strings.TrimSpace(deref(waitingFor))
	if reason == "" {
		return issue
	}
	raw, _ := json.Marshal(truncateRunes(reason, maxBacklogWaitingForRunes))
	row, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: metaKeyBacklogWaitingFor, Value: raw})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("backlog waiting_for: metadata write failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return issue
	}
	issue.Metadata, issue.Revision = row.Metadata, row.Revision
	return issue
}
