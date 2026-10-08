package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// goalCompletionReport is intentionally a small wire contract. A daemon may
// report a check by id or by zero/one-based position; the server resolves it
// against the locked goal before changing anything.
type goalCompletionReport struct {
	ID       string `json:"id"`
	Position *int   `json:"position"`
	Status   string `json:"status"`
	Evidence []any  `json:"evidence"`
}

type goalCompletionPayload struct {
	GoalChecks []goalCompletionReport `json:"goal_checks"`
}

func budgetExceeded(used int64, limit int64) bool { return limit > 0 && used >= limit }
func budgetWarning(used int64, limit int64) bool  { return limit > 0 && used*100 >= limit*80 }

// reconcileGoalAfterCompletion is the only server-owned continuation gate.
// The executor can report evidence, but it cannot declare the issue complete:
// all locked checks must be passed before the issue is handed to review.
func (s *TaskService) reconcileGoalAfterCompletion(ctx context.Context, task db.AgentTaskQueue, result []byte) {
	if s == nil || s.Queries == nil || !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		return
	}
	goal, err := s.Queries.GetIssueGoal(ctx, db.GetIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil || goal.Status != "active" {
		return
	}

	var payload goalCompletionPayload
	_ = json.Unmarshal(result, &payload)
	for _, report := range payload.GoalChecks {
		status := strings.ToLower(strings.TrimSpace(report.Status))
		if status != "pending" && status != "passed" && status != "failed" {
			continue
		}
		var check db.IssueGoalCheck
		checks, listErr := s.Queries.ListIssueGoalChecks(ctx, goal.ID)
		if listErr != nil {
			break
		}
		for _, candidate := range checks {
			if report.ID != "" && util.UUIDToString(candidate.ID) == report.ID {
				check = candidate
				break
			}
			if report.Position != nil && (candidate.Position == int32(*report.Position) || candidate.Position == int32(*report.Position-1)) {
				check = candidate
				break
			}
		}
		if !check.ID.Valid {
			continue
		}
		evidence, _ := json.Marshal(report.Evidence)
		if _, updateErr := s.Queries.UpdateIssueGoalCheck(ctx, db.UpdateIssueGoalCheckParams{ID: check.ID, GoalID: goal.ID, Status: status, Column4: evidence}); updateErr != nil {
			slog.Warn("goal check update after completion failed", "issue_id", util.UUIDToString(issue.ID), "error", updateErr)
		}
	}

	duration := int64(0)
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		duration = int64(task.CompletedAt.Time.Sub(task.StartedAt.Time).Seconds())
		if duration < 1 {
			duration = 1
		}
	}
	tokens := int64(0)
	if usage, usageErr := s.Queries.GetTaskUsage(ctx, task.ID); usageErr == nil {
		for _, row := range usage {
			tokens += row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheWriteTokens
		}
	}
	completedRound := goal.Round
	if completedRound < 1 {
		completedRound = 1
	}
	goal, err = s.Queries.UpdateIssueGoalUsage(ctx, db.UpdateIssueGoalUsageParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, TokensUsed: tokens, RunsUsed: 1,
		DurationSecondsUsed: duration, Round: completedRound,
	})
	if err != nil {
		return
	}
	checks, err := s.Queries.ListIssueGoalChecks(ctx, goal.ID)
	if err != nil {
		return
	}
	passed, pending := 0, 0
	for _, check := range checks {
		if check.Status == "passed" {
			passed++
		} else {
			pending++
		}
	}

	// Budget accounting is cumulative across all rounds and all relay seats.
	exceeded := budgetExceeded(goal.TokensUsed, goal.TokenLimit) || budgetExceeded(int64(goal.RunsUsed), int64(goal.RunLimit)) || budgetExceeded(goal.DurationSecondsUsed, goal.DurationSeconds)
	warning := budgetWarning(goal.TokensUsed, goal.TokenLimit) || budgetWarning(int64(goal.RunsUsed), int64(goal.RunLimit)) || budgetWarning(goal.DurationSecondsUsed, goal.DurationSeconds)
	if warning && !goal.BudgetWarningAt.Valid {
		if marked, markErr := s.Queries.MarkIssueGoalBudgetWarning(ctx, db.MarkIssueGoalBudgetWarningParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); markErr == nil {
			goal = marked
			s.recordGoalActivity(ctx, issue, task.AgentID, "goal_budget_warning", map[string]any{"round": completedRound, "tokens_used": goal.TokensUsed, "runs_used": goal.RunsUsed, "duration_seconds_used": goal.DurationSecondsUsed})
		}
	}
	if exceeded {
		s.stopGoalForBudget(ctx, issue, task, goal)
		return
	}

	if pending == 0 && len(checks) > 0 {
		if updated, updateErr := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: issuestatus.InReview, WorkspaceID: issue.WorkspaceID}); updateErr == nil {
			s.broadcastIssueUpdated(ctx, updated, issue.Status)
		}
		s.recordGoalActivity(ctx, issue, task.AgentID, "goal_round_completed", map[string]any{"round": completedRound, "passed": passed, "total": len(checks), "next": "review"})
		return
	}

	noProgress := goal.NoProgressRounds
	progressReported := false
	for _, report := range payload.GoalChecks {
		if strings.EqualFold(strings.TrimSpace(report.Status), "passed") {
			progressReported = true
			break
		}
	}
	if progressReported {
		noProgress = 0
	} else {
		noProgress++
	}
	if noProgress >= goal.MaxNoProgressRounds {
		s.stopGoalForNoProgress(ctx, issue, task, goal, noProgress, passed, len(checks))
		return
	}
	nextRound := completedRound + 1
	progress, err := s.Queries.UpdateIssueGoalProgress(ctx, db.UpdateIssueGoalProgressParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, NoProgressRounds: noProgress, Round: nextRound, Column5: pgtype.UUID{}})
	if err != nil {
		return
	}
	if issue.Status == issuestatus.Todo || issue.Status == issuestatus.Blocked {
		if updated, updateErr := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: issuestatus.InProgress, WorkspaceID: issue.WorkspaceID}); updateErr == nil {
			s.broadcastIssueUpdated(ctx, updated, issue.Status)
			issue = updated
		}
	}
	note := fmt.Sprintf("目标第 %d 轮结束，完成线已过 %d/%d 条；服务器自动安排第 %d 轮，只处理尚未通过的检查项。", completedRound, passed, len(checks), progress.Round)
	child, enqueueErr := s.EnqueueTaskForIssueWithHandoff(ctx, issue, note, pgtype.UUID{})
	if enqueueErr == nil {
		_, _ = s.Queries.UpdateIssueGoalProgress(ctx, db.UpdateIssueGoalProgressParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, NoProgressRounds: noProgress, Round: progress.Round, Column5: child.ID})
	}
	s.recordGoalActivity(ctx, issue, task.AgentID, "goal_round_completed", map[string]any{"round": completedRound, "passed": passed, "total": len(checks), "next": "continue", "next_round": progress.Round, "enqueued": enqueueErr == nil})
}

func (s *TaskService) stopGoalForBudget(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, goal db.IssueGoal) {
	stopped, err := s.Queries.StopIssueGoalForBudget(ctx, db.StopIssueGoalForBudgetParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, StopReason: "budget_exhausted"})
	if err != nil {
		return
	}
	if updated, updateErr := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: issuestatus.Blocked, WorkspaceID: issue.WorkspaceID}); updateErr == nil {
		s.broadcastIssueUpdated(ctx, updated, issue.Status)
	}
	s.recordGoalActivity(ctx, issue, task.AgentID, "goal_budget_exhausted", map[string]any{"round": stopped.Round, "tokens_used": stopped.TokensUsed, "runs_used": stopped.RunsUsed, "duration_seconds_used": stopped.DurationSecondsUsed})
	s.createGoalBudgetAsk(ctx, issue, task.AgentID)
	s.createSystemNotice(ctx, issue, "目标预算已用完，任务已暂停。请在提问卡片里选择再给预算、调整完成线或结束目标。")
}

func (s *TaskService) stopGoalForNoProgress(ctx context.Context, issue db.Issue, task db.AgentTaskQueue, goal db.IssueGoal, rounds int32, passed, total int) {
	_, err := s.Queries.StopIssueGoalForBudget(ctx, db.StopIssueGoalForBudgetParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, StopReason: "no_progress"})
	if err != nil {
		return
	}
	if updated, updateErr := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: issuestatus.Blocked, WorkspaceID: issue.WorkspaceID}); updateErr == nil {
		s.broadcastIssueUpdated(ctx, updated, issue.Status)
	}
	s.recordGoalActivity(ctx, issue, task.AgentID, "goal_no_progress", map[string]any{"rounds": rounds, "passed": passed, "total": total, "limit": goal.MaxNoProgressRounds})
	s.createGoalBudgetAsk(ctx, issue, task.AgentID)
	s.createSystemNotice(ctx, issue, "目标连续多轮没有新的完成线进展，任务已暂停。请在提问卡片里选择下一步。")
}

func (s *TaskService) createGoalBudgetAsk(ctx context.Context, issue db.Issue, agentID pgtype.UUID) {
	recipient := issue.CreatorID
	if !recipient.Valid || issue.CreatorType != "member" {
		if goal, err := s.Queries.GetIssueGoal(ctx, db.GetIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err == nil && goal.CreatedByID.Valid && goal.CreatedByType == "member" {
			recipient = goal.CreatedByID
		}
	}
	questions, _ := json.Marshal([]map[string]any{{"text": "目标需要怎么继续？", "options": []map[string]any{{"id": "extend_budget", "label": "再给预算", "recommended": true}, {"id": "change_goal", "label": "调整完成线"}, {"id": "stop", "label": "结束目标"}}}})
	askID, err := s.Queries.CreateGoalBudgetAsk(ctx, db.CreateGoalBudgetAskParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, AskerID: agentID, Title: "目标暂停，需要你的决定", Questions: questions})
	if err != nil || !recipient.Valid {
		return
	}
	details, _ := json.Marshal(map[string]string{"ask_id": util.UUIDToString(askID)})
	_, _ = s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, RecipientType: "member", RecipientID: recipient, Type: InboxTypeNeedsYou, Severity: "action_required", IssueID: issue.ID, Title: "目标暂停，需要你的决定", Body: pgtype.Text{String: "需要回答提问", Valid: true}, ActorType: pgtype.Text{String: "agent", Valid: true}, ActorID: agentID, Details: details})
}

func (s *TaskService) recordGoalActivity(ctx context.Context, issue db.Issue, agentID pgtype.UUID, action string, details map[string]any) {
	raw, _ := json.Marshal(details)
	_, err := s.Queries.CreateActivity(ctx, db.CreateActivityParams{ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, ActorType: pgtype.Text{String: "agent", Valid: true}, ActorID: agentID, Action: action, Details: raw})
	if err != nil {
		slog.Warn("goal activity write failed", "issue_id", util.UUIDToString(issue.ID), "action", action, "error", err)
	}
}
