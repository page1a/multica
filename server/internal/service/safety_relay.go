package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// relaySafetyRefusal moves a ticket off a seat whose provider's safety
// classifier refused the run, onto a seat of another provider. Before this the
// failure landed in agent_error.unknown: no retry, no relay, the ticket sat in
// todo until a person noticed (DENE-1048, DENE-990).
//
// Unlike the quota relay it opens no breaker: the seat is healthy and keeps
// every other ticket. Only this ticket moves, and only when a different house
// can take it on the same tier or one down; otherwise the raw failure comment
// stays the last word, as before.
//
// The bool reports that the ticket was handed over, so the caller skips the
// provider's sentence in favour of the handoff notice.
func (s *TaskService) relaySafetyRefusal(ctx context.Context, task db.AgentTaskQueue, errMsg string) bool {
	if s == nil || s.Queries == nil || !task.IssueID.Valid || task.AutopilotRunID.Valid || !taskfailure.IsSafetyRefusal(errMsg) {
		return false
	}
	failed, err := s.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return false
	}
	house, ok := routing.DefaultLadder.ProviderFor(failed.Name, failed.Model.String)
	if !ok || house == "" {
		return false
	}

	var (
		issue    db.Issue
		prev     string
		replaced quotarelay.Choice
		moved    bool
	)
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		locked, err := qtx.LockIssueForQuotaRelay(ctx, task.IssueID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		switch locked.Status {
		case "todo", "in_progress", "blocked":
		default:
			return nil
		}
		if locked.TriageState.Valid || locked.AssigneeType.String != "agent" ||
			util.UUIDToString(locked.AssigneeID) != util.UUIDToString(failed.ID) {
			return nil
		}
		active, err := qtx.HasActiveTaskForIssue(ctx, locked.ID)
		if err != nil || active {
			return err
		}
		seat, roster, err := quotaRoster(ctx, qtx, task, failed, locked.WorkspaceID)
		if err != nil {
			return err
		}
		seat.AvoidHouse = house
		seat.StrictHouse = true
		seat.Exclude = reviewerSeatExclusion(locked)
		choice, found := quotarelay.Pick(seat, roster, routing.DefaultLadder.TierKeys())
		if !found || choice.Seat.Provider == house {
			return nil
		}
		reassigned, err := qtx.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
			AssigneeID:        util.MustParseUUID(choice.Seat.ID),
			ID:                locked.ID,
			WorkspaceID:       locked.WorkspaceID,
			CurrentAssigneeID: failed.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		prev = reassigned.Status
		if reassigned.Status != "in_progress" {
			if reassigned, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID: reassigned.ID, Status: "in_progress", WorkspaceID: reassigned.WorkspaceID,
			}); err != nil {
				return err
			}
		}
		if prev == "blocked" {
			if err := dropBlockWaitKeys(ctx, qtx, reassigned); err != nil {
				return err
			}
		}
		issue, replaced, moved = reassigned, choice, true
		return nil
	})
	if err != nil {
		slog.Warn("safety refusal relay failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return false
	}
	if !moved {
		return false
	}
	s.broadcastIssueUpdated(ctx, issue, prev)

	note := fmt.Sprintf("上一棒 %s 的模型被供应商安全审查拦下（误判也会拦），换你接着做。先读票和已有评论、提交，从上一棒停下的地方继续。", failed.Name)
	actor := task.AccountableUserID
	if !actor.Valid {
		actor = task.OriginatorUserID
	}
	_, enqErr := s.enqueueIssueTask(ctx, issue, task.TriggerCommentID, false, note, actor, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived)
	if enqErr != nil && !errors.Is(enqErr, ErrDuplicatePendingTask) {
		slog.Warn("safety refusal relay: enqueue failed", "issue_id", util.UUIDToString(issue.ID), "error", enqErr)
	}
	s.createSystemNotice(ctx, issue, safetyRelayNotice(failed.Name, replaced, enqErr))
	return true
}

func safetyRelayNotice(from string, to quotarelay.Choice, enqErr error) string {
	tier := "同档"
	if to.SteppedDown {
		tier = "低一档"
	}
	body := fmt.Sprintf("**%s 这一轮被模型供应商的安全审查拦下了**（不是这张票做错了，这类审查会误判正常的安全、逆向类工作）。\n\n已换给%s另一家模型的 **%s** 接着做，%s 本身没有关停，其它票照常。", from, tier, to.Seat.Name, from)
	if enqErr != nil && !errors.Is(enqErr, ErrDuplicatePendingTask) {
		body += "\n\n新执行人的运行没有排上：" + enqErr.Error() + "。在票下 @ 它即可重新开始。"
	}
	return body
}
