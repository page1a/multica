package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type reviewerRelayMeta struct {
	OriginalID      string `json:"original_id"`
	OriginalName    string `json:"original_name"`
	ReplacementID   string `json:"replacement_id"`
	ReplacementName string `json:"replacement_name"`
	Designated      bool   `json:"designated"`
	CoveredAt       string `json:"covered_at"`
}

// ReclaimDesignatedReviews gives acceptance back to a seat that was switched
// off and has just been switched on, only when that seat is the ticket's
// designated reviewer and the cover has not started. A cover that already
// ran stays where it is. Execution-ticket recovery is not this function.
func (s *TaskService) ReclaimDesignatedReviews(ctx context.Context, agentID pgtype.UUID) error {
	if s == nil || s.Queries == nil || !agentID.Valid {
		return nil
	}
	agent, err := s.Queries.GetAgent(ctx, agentID)
	if err != nil || !agent.WorkEnabled || agent.ArchivedAt.Valid {
		return err
	}
	issues, err := s.Queries.ListIssuesRelayedFromReviewer(ctx, db.ListIssuesRelayedFromReviewerParams{
		WorkspaceID: agent.WorkspaceID,
		OriginalID:  util.UUIDToString(agent.ID),
	})
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if err := s.reclaimOneReview(ctx, agent, issue); err != nil {
			slog.Warn("reviewer relay: reclaim failed",
				"issue_id", util.UUIDToString(issue.ID),
				"agent_id", util.UUIDToString(agent.ID),
				"error", err,
			)
		}
	}
	return nil
}

func (s *TaskService) reclaimOneReview(ctx context.Context, agent db.Agent, issue db.Issue) error {
	var wrapped struct {
		ReviewerRelay reviewerRelayMeta `json:"reviewer_relay"`
	}
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &wrapped) != nil || wrapped.ReviewerRelay.OriginalID == "" {
		return nil
	}
	meta := wrapped.ReviewerRelay
	if !meta.Designated || meta.OriginalID != util.UUIDToString(agent.ID) {
		return nil
	}
	if !issue.ReviewerID.Valid || util.UUIDToString(issue.ReviewerID) != meta.ReplacementID {
		return nil
	}
	since, err := time.Parse(time.RFC3339Nano, meta.CoveredAt)
	if err != nil {
		since, err = time.Parse(time.RFC3339, meta.CoveredAt)
		if err != nil {
			return nil
		}
	}
	replacementID, err := util.ParseUUID(meta.ReplacementID)
	if err != nil {
		return nil
	}
	begun, err := s.Queries.AgentHasBegunWorkOnIssue(ctx, db.AgentHasBegunWorkOnIssueParams{
		IssueID: issue.ID,
		AgentID: replacementID,
		Since:   pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil || begun {
		return err
	}
	if _, err := s.Queries.CancelPendingTasksByIssueAndAgent(ctx, db.CancelPendingTasksByIssueAndAgentParams{
		IssueID: issue.ID,
		AgentID: replacementID,
	}); err != nil {
		return err
	}
	restored, err := s.Queries.ReplaceIssueReviewerIfCurrent(ctx, db.ReplaceIssueReviewerIfCurrentParams{
		ReviewerID:        agent.ID,
		ID:                issue.ID,
		WorkspaceID:       issue.WorkspaceID,
		CurrentReviewerID: replacementID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if restored.AssigneeType.String == "agent" && restored.AssigneeID == replacementID {
		moved, err := s.Queries.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
			AssigneeID:        agent.ID,
			ID:                restored.ID,
			WorkspaceID:       restored.WorkspaceID,
			CurrentAssigneeID: replacementID,
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			restored = moved
		}
	}
	if _, err := s.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{
		Key: "reviewer_relay", ID: restored.ID, WorkspaceID: restored.WorkspaceID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	body := fmt.Sprintf("验收席 %s 已恢复接活，而且 %s 还没开跑，这张票的指定验收人换回去了。", agent.Name, emptyName(meta.ReplacementName))
	created, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     restored.ID,
		WorkspaceID: restored.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     body,
		Type:        "system",
	})
	if err != nil {
		return err
	}
	if _, err := s.EnqueueTaskForIssue(ctx, restored, created.Comment().ID); err != nil {
		slog.Warn("reviewer relay: restored reviewer did not start",
			"issue_id", util.UUIDToString(restored.ID),
			"error", err,
		)
	}
	return nil
}

func emptyName(name string) string {
	if name == "" {
		return "顶上的席位"
	}
	return name
}
