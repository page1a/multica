package handler

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// reviewRoundFailure reads whether this review round's newest run failed with
// nothing after it, how many of its runs failed, and whether a person is
// already being asked about it (DENE-1647).
func (h *Handler) reviewRoundFailure(ctx context.Context, issue db.Issue, round time.Time, hasRound bool) (failed bool, failures int, asked bool) {
	if issue.Status != issuestatus.InReview {
		return false, 0, false
	}
	since := time.Unix(0, 0)
	if hasRound {
		since = round
	}
	state, err := h.Queries.ReviewRoundRunState(ctx, db.ReviewRoundRunStateParams{
		IssueID: issue.ID,
		Since:   pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		slog.Warn("block wait: review run state failed", "error", err, "issue_id", uuidToString(issue.ID))
		return false, 0, false
	}
	if state.LastStatus != "failed" {
		return false, int(state.FailedRuns), false
	}
	asked, err = h.Queries.HasOpenReviewStuckAsk(ctx, issue.ID)
	if err != nil {
		asked = true
	}
	return true, int(state.FailedRuns), asked
}

// coverFailedReview moves a stalled acceptance on after its run failed: the
// same seat once, then another seat that did not work on the ticket, then a
// person through an ask. Routing owns the seat choice when it is on; without
// it the patrol can only retry a seat that is still switched on.
func (h *Handler) coverFailedReview(ctx context.Context, issue db.Issue, force bool) {
	if !issue.ReviewerType.Valid || issue.ReviewerType.String != "agent" || !issue.ReviewerID.Valid {
		return
	}
	ws, id := uuidToString(issue.WorkspaceID), uuidToString(issue.ID)
	if h.Routing != nil {
		out, err := h.Routing.CoverFailedReview(ctx, ws, id, force)
		if err != nil {
			slog.Warn("block wait: cover failed review", "error", err, "issue_id", id)
			return
		}
		switch out.Action {
		case routing.ActionAdvised:
			h.askReviewStuck(ctx, issue, false)
			return
		case routing.ActionSkipped:
		default:
			return
		}
	}
	agent, err := h.Queries.GetAgent(ctx, issue.ReviewerID)
	if err == nil && !force && agent.WorkEnabled && !agent.ArchivedAt.Valid {
		if err := (routingStore{h: h}).Handoff(ctx, ws, id, "agent", uuidToString(agent.ID)); err != nil {
			slog.Warn("block wait: retry failed review", "error", err, "issue_id", id)
		}
		return
	}
	h.askReviewStuck(ctx, issue, true)
}

// askReviewStuck opens the one "acceptance seat failed" ask on the issue.
// comment adds the issue note; routing already wrote its own when it gave up.
func (h *Handler) askReviewStuck(ctx context.Context, issue db.Issue, comment bool) {
	recipient := issue.CreatorID
	if issue.CreatorType != "member" || !recipient.Valid {
		recipient = pgtype.UUID{}
		if managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID); err == nil && len(managers) > 0 {
			recipient = managers[0]
		}
	}
	created, err := service.OpenReviewStuckAsk(ctx, h.Queries, issue, issue.ReviewerID, recipient)
	if err != nil {
		slog.Warn("block wait: review stuck ask failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	if created && comment {
		mention := ""
		if recipient.Valid {
			mention = h.memberWakeMention(ctx, recipient)
		}
		h.postBlockComment(ctx, issue, mention+"验收 run 失败后没有能接的验收席。这张票停在待验收，已发选项提问：换席位 / 我来验 / 直接关票。")
	}
}

// applyReviewStuckAnswer carries out a person's answer to the
// "acceptance seat failed" ask. The answer is the instruction; each branch
// leaves one note on the issue so the timeline says who decided.
func (h *Handler) applyReviewStuckAnswer(ctx context.Context, workspaceID pgtype.UUID, issueID pgtype.UUID, userID, choice string) {
	if !strings.HasPrefix(choice, "review_stuck.") || !issueID.Valid {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issueID, WorkspaceID: workspaceID})
	if err != nil || issue.Status != issuestatus.InReview {
		return
	}
	member, err := util.ParseUUID(userID)
	if err != nil {
		return
	}
	// The person who answered is named, not @-ed: they just made the call.
	who := "有人"
	if user, err := h.Queries.GetUser(ctx, member); err == nil && strings.TrimSpace(user.Name) != "" {
		who = strings.TrimSpace(user.Name) + " "
	}
	switch choice {
	case service.ReviewStuckRetry:
		h.postBlockComment(ctx, issue, who+"选了换席位，平台重新找验收席。")
		h.coverFailedReview(ctx, issue, true)
	case service.ReviewStuckMember:
		updated, err := h.Queries.SetIssueReviewerToMember(ctx, db.SetIssueReviewerToMemberParams{
			ReviewerID: member, ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			slog.Warn("review stuck: set member reviewer failed", "error", err, "issue_id", uuidToString(issue.ID))
			return
		}
		h.publishBlockStatus(issue, updated)
		h.postBlockComment(ctx, updated, who+"选了自己验收，验收人改成本人，平台不再派验收席。")
	case service.ReviewStuckClose:
		done, err := (routingStore{h: h}).CompleteFromReview(ctx, uuidToString(workspaceID), uuidToString(issueID))
		if err != nil || !done {
			slog.Warn("review stuck: close failed", "error", err, "issue_id", uuidToString(issue.ID))
			return
		}
		h.postBlockComment(ctx, issue, who+"选了直接关票：验收席失败后不再验收，票改为已完成。")
	}
}
