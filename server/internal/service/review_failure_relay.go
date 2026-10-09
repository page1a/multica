package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Review-stuck ask options (DENE-1647). The ids are the contract between the
// ask, the answer handler and the patrol: an open ask carrying ReviewStuckClose
// is "the acceptance seat failed and nobody can cover it".
const (
	ReviewStuckRetry  = "review_stuck.retry"
	ReviewStuckMember = "review_stuck.member"
	ReviewStuckClose  = "review_stuck.close"
	reviewStuckTitle  = "验收席失败，需要你决定"
)

// stageReviewRelay is the relay for a seat that failed while the issue is in
// review (DENE-1647). The role does not change: the seat that failed was
// accepting, so another seat accepts. It never picks a seat that already
// worked on the issue, because the executor does not accept its own work.
// The issue stays in review; with nobody to cover, a person gets an ask.
func (s *TaskService) stageReviewRelay(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan, handoff quotarelay.Handoff, dest **quotaRelayPrepared) error {
	failedID := util.UUIDToString(agent.ID)
	if issue.AssigneeType.String != "agent" || !issue.AssigneeID.Valid || util.UUIDToString(issue.AssigneeID) != failedID {
		return s.skipQuotaRelay(ctx, qtx, task, agent, issue, plan, handoff, "skipped_reassigned", "票已经不在失败的这一席手上")
	}
	reviewing := issue.ReviewerType.Valid && issue.ReviewerType.String == "agent" &&
		issue.ReviewerID.Valid && util.UUIDToString(issue.ReviewerID) == failedID
	active, err := qtx.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil {
		return err
	}
	if active {
		return s.skipQuotaRelay(ctx, qtx, task, agent, issue, plan, handoff, "skipped_active", "这张票已经有别的进行中的任务")
	}
	if !reviewing {
		// An executor run on a ticket already in review (a comment woke it).
		// It is still execution work, so the ordinary relay applies.
		choice, found, err := pickQuotaReplacement(ctx, qtx, task, agent, issue, reviewerSeatExclusion(issue))
		if err != nil {
			return err
		}
		if !found {
			return s.waitQuotaRelay(ctx, qtx, task, agent, issue, plan, handoff, dest)
		}
		return s.stageQuotaReplacement(ctx, qtx, task, agent, issue, plan, handoff, choice, dest)
	}

	handoff.Review = true
	workers, err := issueWorkerIDs(ctx, qtx, issue.ID, failedID)
	if err != nil {
		return err
	}
	choice, found, err := pickQuotaReplacement(ctx, qtx, task, agent, issue, workers)
	if err != nil {
		return err
	}
	if !found {
		return s.waitReviewRelay(ctx, qtx, task, agent, issue, plan, handoff, dest)
	}
	replacementID := util.MustParseUUID(choice.Seat.ID)
	updated, err := qtx.ReplaceIssueReviewerIfCurrent(ctx, db.ReplaceIssueReviewerIfCurrentParams{
		ReviewerID:        replacementID,
		ID:                issue.ID,
		WorkspaceID:       issue.WorkspaceID,
		CurrentReviewerID: agent.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.skipQuotaRelay(ctx, qtx, task, agent, issue, plan, handoff, "skipped_reassigned", "验收席已经被人换过")
		}
		return err
	}
	updated, err = qtx.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
		AssigneeID:        replacementID,
		ID:                issue.ID,
		WorkspaceID:       issue.WorkspaceID,
		CurrentAssigneeID: agent.ID,
	})
	if err != nil {
		return err
	}
	if err := rememberReviewerCover(ctx, qtx, updated, agent, choice.Seat); err != nil {
		return err
	}
	handoff.ReplacementName = choice.Seat.Name
	handoff.ReplacementTier = choice.Seat.Tier
	handoff.SteppedDown = choice.SteppedDown
	relay, err := insertQuotaRelay(ctx, qtx, task, agent, plan, db.InsertQuotaRelayParams{
		Outcome:          "pending",
		IssueID:          updated.ID,
		ToAgentID:        replacementID,
		TierFrom:         planSeatTier(agent),
		TierTo:           choice.Seat.Tier,
		HandoffNote:      quotarelay.AgentNote(handoff),
		AuditComment:     quotarelay.AuditRelay(handoff),
		TriggerCommentID: task.TriggerCommentID,
	})
	if err != nil || relay == nil {
		return err
	}
	*dest = &quotaRelayPrepared{hold: true, enqueue: true, relay: *relay, prevStatus: issue.Status, issue: &updated}
	return nil
}

// waitReviewRelay leaves the issue in review with nobody to cover it and asks
// a person which way to go. Blocking it would hide that the work is done.
func (s *TaskService) waitReviewRelay(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan, handoff quotarelay.Handoff, dest **quotaRelayPrepared) error {
	if planSeatTier(agent) == "" {
		handoff.WaitReason = quotarelay.WaitNoTier
	} else {
		handoff.WaitReason = quotarelay.WaitNoSeat
	}
	audit := quotarelay.AuditWait(handoff)
	relay, err := insertQuotaRelay(ctx, qtx, task, agent, plan, db.InsertQuotaRelayParams{
		Outcome:      "waiting",
		IssueID:      issue.ID,
		WaitReason:   handoff.WaitReason,
		AuditComment: audit,
	})
	if err != nil || relay == nil {
		return err
	}
	comment, err := postQuotaAudit(ctx, qtx, issue, audit, task.ID)
	if err != nil {
		return err
	}
	recipient, _ := quotaWaitRecipient(issue, task)
	if _, err := OpenReviewStuckAsk(ctx, qtx, issue, agent.ID, recipient); err != nil {
		return err
	}
	*dest = &quotaRelayPrepared{hold: true, relay: *relay, prevStatus: issue.Status, comment: comment}
	return nil
}

// issueWorkerIDs lists the seats that finished a run on this issue, minus the
// seat being replaced.
func issueWorkerIDs(ctx context.Context, qtx *db.Queries, issueID pgtype.UUID, except string) ([]string, error) {
	ids, err := qtx.ListIssueWorkerAgentIDs(ctx, issueID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if s := util.UUIDToString(id); s != except {
			out = append(out, s)
		}
	}
	return out, nil
}

// rememberReviewerCover writes the same reviewer_relay record the routing
// substitute writes, so recovery and the UI read one shape.
func rememberReviewerCover(ctx context.Context, qtx *db.Queries, issue db.Issue, from db.Agent, to quotarelay.Seat) error {
	raw, err := json.Marshal(reviewerRelayMeta{
		OriginalID:      util.UUIDToString(from.ID),
		OriginalName:    from.Name,
		ReplacementID:   to.ID,
		ReplacementName: to.Name,
		Designated:      true,
		CoveredAt:       time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	_, err = qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		Key: "reviewer_relay", Value: raw, ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// OpenReviewStuckAsk asks a person what to do with an issue whose acceptance
// seat failed and that no other seat can cover: switch seats, review it
// themselves, or close it. One open ask per issue; created is false when one
// is already waiting. recipient may be invalid, in which case the ask still
// shows on the issue but no inbox item is written.
func OpenReviewStuckAsk(ctx context.Context, q *db.Queries, issue db.Issue, askerID, recipient pgtype.UUID) (bool, error) {
	open, err := q.HasOpenReviewStuckAsk(ctx, issue.ID)
	if err != nil {
		return false, err
	}
	if open {
		return false, nil
	}
	questions, err := json.Marshal([]map[string]any{{
		"text": "验收席失败了，这张票怎么处理？",
		"options": []map[string]any{
			{"id": ReviewStuckRetry, "label": "换席位", "recommended": true},
			{"id": ReviewStuckMember, "label": "我来验"},
			{"id": ReviewStuckClose, "label": "直接关票"},
		},
	}})
	if err != nil {
		return false, err
	}
	askID, err := q.CreateReviewStuckAsk(ctx, db.CreateReviewStuckAskParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		AskerID:     askerID,
		Title:       reviewStuckTitle,
		Questions:   questions,
	})
	if err != nil {
		return false, err
	}
	if !recipient.Valid {
		return true, nil
	}
	details, _ := json.Marshal(map[string]string{"ask_id": util.UUIDToString(askID)})
	_, err = q.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   recipient,
		Type:          InboxTypeNeedsYou,
		Severity:      "action_required",
		IssueID:       issue.ID,
		Title:         reviewStuckTitle,
		Body:          pgtype.Text{String: "验收席失败，没有可接的席位", Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       askerID,
		Details:       details,
	})
	return true, err
}
