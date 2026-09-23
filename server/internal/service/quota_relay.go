package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const quotaRelayPendingBatch = 50

// RelayQuotaFailure opens a recoverable breaker for a quota failure and, when
// the issue is still unfinished, hands it to a same-tier seat or exactly one
// tier down. The bool tells HandleFailedTasks not to reset the issue to todo:
// a replacement is queued, or the issue was marked blocked on purpose.
//
// Transient errors, including provider capacity, return false and change
// nothing. Quota failures are not retried; this is the path that runs instead.
func (s *TaskService) RelayQuotaFailure(ctx context.Context, task db.AgentTaskQueue) (bool, error) {
	if s == nil || s.Queries == nil || !quotaFailureWorthRelay(task) {
		return false, nil
	}
	prepared, err := s.prepareQuotaRelay(ctx, task)
	if err != nil || prepared == nil {
		return false, err
	}
	if prepared.enqueue {
		if err := s.finishQuotaRelay(ctx, prepared.relay); err != nil {
			return true, err
		}
	}
	s.publishQuotaRelaySideEffects(ctx, prepared)
	return prepared.hold, nil
}

// FinishPendingQuotaRelays retries handoffs whose replacement was chosen but
// whose task insert did not land. Safe to run on every sweeper tick.
func (s *TaskService) FinishPendingQuotaRelays(ctx context.Context) (int, error) {
	if s == nil || s.Queries == nil {
		return 0, nil
	}
	pending, err := s.Queries.ListPendingQuotaRelays(ctx, quotaRelayPendingBatch)
	if err != nil {
		return 0, err
	}
	finished := 0
	for _, relay := range pending {
		before := relay.Outcome
		if err := s.finishQuotaRelay(ctx, relay); err != nil {
			slog.Warn("quota relay: finish pending failed",
				"source_task_id", util.UUIDToString(relay.SourceTaskID),
				"error", err,
			)
			continue
		}
		current, err := s.Queries.GetQuotaRelayBySourceTask(ctx, relay.SourceTaskID)
		if err != nil {
			continue
		}
		if current.Outcome != before {
			finished++
		}
	}
	return finished, nil
}

// RecoverExpiredQuotaBreakers closes breakers whose recovery time has passed
// and turns work back on for a seat only when no open breaker remains and
// nobody has edited that seat since the breaker disabled it.
func (s *TaskService) RecoverExpiredQuotaBreakers(ctx context.Context) (int, error) {
	if s == nil || s.Queries == nil {
		return 0, nil
	}
	released := 0
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		due, err := qtx.LockDueQuotaBreakers(ctx, 100)
		if err != nil {
			return err
		}
		agents := make(map[string]pgtype.UUID, len(due))
		for _, row := range due {
			n, err := qtx.MarkQuotaBreakerRecovered(ctx, row.ID)
			if err != nil {
				return err
			}
			if n > 0 {
				agents[util.UUIDToString(row.AgentID)] = row.AgentID
			}
		}
		for _, agentID := range agents {
			n, err := qtx.ReleaseAgentQuotaSuppression(ctx, agentID)
			if err != nil {
				return err
			}
			released += int(n)
		}
		return nil
	})
	return released, err
}

type quotaRelayPrepared struct {
	hold       bool
	enqueue    bool
	relay      db.AgentQuotaRelay
	comment    *db.Comment
	issue      *db.Issue
	prevStatus string
}

func quotaFailureWorthRelay(task db.AgentTaskQueue) bool {
	reason := ""
	if task.FailureReason.Valid {
		reason = task.FailureReason.String
	}
	text := ""
	if task.Error.Valid {
		text = task.Error.String
	}
	return quotarelay.ShouldInspect(reason, text)
}

func (s *TaskService) prepareQuotaRelay(ctx context.Context, task db.AgentTaskQueue) (*quotaRelayPrepared, error) {
	var prepared *quotaRelayPrepared
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		locked, err := qtx.LockTaskForQuotaRelay(ctx, task.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if locked.Status != "failed" {
			return nil
		}
		if existing, err := qtx.GetQuotaRelayBySourceTask(ctx, locked.ID); err == nil {
			prepared = preparedFromExisting(existing)
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		agent, err := qtx.GetAgentForUpdate(ctx, locked.AgentID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		plan, ok := quotarelay.PlanFor(quotaReason(locked), quotaError(locked), quotaModelBinding(agent), time.Now())
		if !ok {
			return nil
		}
		suppressAt, err := suppressQuotaSeat(ctx, qtx, agent)
		if err != nil {
			return err
		}
		if _, err := qtx.UpsertQuotaBreaker(ctx, db.UpsertQuotaBreakerParams{
			WorkspaceID:            agent.WorkspaceID,
			AgentID:                agent.ID,
			Scope:                  plan.Scope(),
			ModelKey:               plan.ModelKey,
			Reason:                 string(plan.Kind),
			SourceTaskID:           locked.ID,
			Detail:                 quotaAcceptanceText([]byte(quotaError(locked))),
			RecoverAt:              pgtype.Timestamptz{Time: plan.RecoverAt, Valid: true},
			RecoverCondition:       plan.Condition,
			SuppressAgentUpdatedAt: suppressAt,
		}); err != nil {
			return fmt.Errorf("open quota breaker: %w", err)
		}

		if !locked.IssueID.Valid {
			_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_no_issue"})
			return err
		}
		issue, err := qtx.LockIssueForQuotaRelay(ctx, locked.IssueID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_no_issue"})
				return err
			}
			return err
		}
		handoff := quotaHandoff(locked, agent, issue, plan)
		if locked.AutopilotRunID.Valid {
			_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_autopilot", IssueID: issue.ID})
			return err
		}
		switch issue.Status {
		case "done", "cancelled":
			_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_terminal", IssueID: issue.ID})
			return err
		case "backlog":
			_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_parked", IssueID: issue.ID})
			return err
		}
		if issue.TriageState.Valid {
			_, err = insertQuotaRelay(ctx, qtx, locked, agent, plan, db.InsertQuotaRelayParams{Outcome: "skipped_triage", IssueID: issue.ID})
			return err
		}
		if issue.Status == "in_review" {
			return s.skipQuotaRelay(ctx, qtx, locked, agent, issue, plan, handoff, "skipped_review", "票在验收中，不改执行席")
		}
		if !issue.AssigneeID.Valid || util.UUIDToString(issue.AssigneeID) != util.UUIDToString(agent.ID) || issue.AssigneeType.String != "agent" {
			return s.skipQuotaRelay(ctx, qtx, locked, agent, issue, plan, handoff, "skipped_reassigned", "执行席已经不是失败的这一席")
		}
		active, err := qtx.HasActiveTaskForIssue(ctx, issue.ID)
		if err != nil {
			return err
		}
		if active {
			return s.skipQuotaRelay(ctx, qtx, locked, agent, issue, plan, handoff, "skipped_active", "这张票已经有别的进行中的任务")
		}

		choice, found, err := pickQuotaReplacement(ctx, qtx, agent, issue.WorkspaceID)
		if err != nil {
			return err
		}
		if !found {
			return s.waitQuotaRelay(ctx, qtx, locked, agent, issue, plan, handoff, &prepared)
		}
		return s.stageQuotaReplacement(ctx, qtx, locked, agent, issue, plan, handoff, choice, &prepared)
	})
	return prepared, err
}

func preparedFromExisting(existing db.AgentQuotaRelay) *quotaRelayPrepared {
	switch existing.Outcome {
	case "pending":
		return &quotaRelayPrepared{hold: true, enqueue: true, relay: existing}
	case "relayed", "waiting":
		return &quotaRelayPrepared{hold: true, relay: existing}
	default:
		return &quotaRelayPrepared{}
	}
}

func (s *TaskService) skipQuotaRelay(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan, handoff quotarelay.Handoff, outcome, why string) error {
	relay, err := insertQuotaRelay(ctx, qtx, task, agent, plan, db.InsertQuotaRelayParams{
		Outcome:      outcome,
		IssueID:      issue.ID,
		AuditComment: quotarelay.AuditSkip(handoff, why),
	})
	if err != nil || relay == nil {
		return err
	}
	_, err = postQuotaAudit(ctx, qtx, issue, relay.AuditComment, task.ID)
	return err
}

func (s *TaskService) waitQuotaRelay(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan, handoff quotarelay.Handoff, dest **quotaRelayPrepared) error {
	if handoff.WaitReason == "" {
		if planSeatTier(agent) == "" {
			handoff.WaitReason = quotarelay.WaitNoTier
		} else {
			handoff.WaitReason = quotarelay.WaitNoSeat
		}
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
	prev := issue.Status
	updated := issue
	if issue.Status == "todo" || issue.Status == "in_progress" {
		updated, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          issue.ID,
			Status:      "blocked",
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return err
		}
	}
	comment, err := postQuotaAudit(ctx, qtx, updated, audit, task.ID)
	if err != nil {
		return err
	}
	if err := notifyQuotaWait(ctx, qtx, updated, task, audit); err != nil {
		return err
	}
	prepared := &quotaRelayPrepared{hold: true, relay: *relay, prevStatus: prev}
	if comment != nil {
		prepared.comment = comment
	}
	if updated.Status != prev {
		prepared.issue = &updated
	}
	*dest = prepared
	return nil
}

func (s *TaskService) stageQuotaReplacement(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan, handoff quotarelay.Handoff, choice quotarelay.Choice, dest **quotaRelayPrepared) error {
	replacementID := util.MustParseUUID(choice.Seat.ID)
	reassigned, err := qtx.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
		AssigneeID:        replacementID,
		ID:                issue.ID,
		WorkspaceID:       issue.WorkspaceID,
		CurrentAssigneeID: agent.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return s.skipQuotaRelay(ctx, qtx, task, agent, issue, plan, handoff, "skipped_reassigned", "执行席已经不是失败的这一席")
		}
		return err
	}
	prev := reassigned.Status
	if reassigned.Status == "todo" || reassigned.Status == "blocked" {
		reassigned, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          reassigned.ID,
			Status:      "in_progress",
			WorkspaceID: reassigned.WorkspaceID,
		})
		if err != nil {
			return err
		}
	}
	handoff.ReplacementName = choice.Seat.Name
	handoff.ReplacementTier = choice.Seat.Tier
	handoff.SteppedDown = choice.SteppedDown
	note := quotarelay.AgentNote(handoff)
	audit := quotarelay.AuditRelay(handoff)
	relay, err := insertQuotaRelay(ctx, qtx, task, agent, plan, db.InsertQuotaRelayParams{
		Outcome:          "pending",
		IssueID:          reassigned.ID,
		ToAgentID:        replacementID,
		TierFrom:         planSeatTier(agent),
		TierTo:           choice.Seat.Tier,
		HandoffNote:      note,
		AuditComment:     audit,
		TriggerCommentID: task.TriggerCommentID,
	})
	if err != nil || relay == nil {
		return err
	}
	prepared := &quotaRelayPrepared{hold: true, enqueue: true, relay: *relay, prevStatus: prev}
	if reassigned.Status != prev {
		prepared.issue = &reassigned
	}
	*dest = prepared
	return nil
}

func (s *TaskService) finishQuotaRelay(ctx context.Context, relay db.AgentQuotaRelay) error {
	var comment *db.Comment
	var blocked *db.Issue
	var prevStatus string
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		current, err := qtx.LockQuotaRelayBySourceTask(ctx, relay.SourceTaskID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if current.Outcome != "pending" || !current.IssueID.Valid {
			return nil
		}
		issue, err := s.Queries.GetIssue(ctx, current.IssueID)
		if err != nil {
			return err
		}
		actor := quotaRelayActor(current.SourceTaskID, s, ctx)
		_, enqErr := s.enqueueIssueTask(ctx, issue, current.TriggerCommentID, false, current.HandoffNote, actor, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived)
		if quotaRelayGiveUp(enqErr) {
			prevStatus = issue.Status
			blocked, comment, err = s.abandonQuotaRelayTx(ctx, qtx, current, issue, enqErr)
			return err
		}
		if enqErr != nil && !errors.Is(enqErr, ErrDuplicatePendingTask) {
			return enqErr
		}
		created, err := postQuotaAudit(ctx, qtx, issue, current.AuditComment, current.SourceTaskID)
		if err != nil {
			return err
		}
		comment = created
		if _, err := qtx.MarkQuotaRelayRelayed(ctx, current.SourceTaskID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if comment != nil {
		s.publishQuotaComment(ctx, comment)
	}
	if blocked != nil && s.Bus != nil {
		s.broadcastIssueUpdated(ctx, *blocked, prevStatus)
	}
	return nil
}

func (s *TaskService) abandonQuotaRelayTx(ctx context.Context, qtx *db.Queries, relay db.AgentQuotaRelay, issue db.Issue, cause error) (*db.Issue, *db.Comment, error) {
	reason := "replacement seat could not take the task: " + cause.Error()
	if _, err := qtx.AbandonQuotaRelay(ctx, db.AbandonQuotaRelayParams{
		SourceTaskID: relay.SourceTaskID,
		WaitReason:   reason,
	}); err != nil {
		return nil, nil, err
	}
	prev := issue
	updated := issue
	var err error
	if issue.Status == "todo" || issue.Status == "in_progress" {
		updated, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
			ID:          issue.ID,
			Status:      "blocked",
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return nil, nil, err
		}
	}
	audit := fmt.Sprintf("## 额度熔断后接力没有发出去\n\n- 已选的席位没能接上这张票。\n- 等待：%s\n- 本票改为 blocked，避免停在进行中却没有执行者。\n", reason)
	comment, err := postQuotaAudit(ctx, qtx, updated, audit, relay.SourceTaskID)
	if err != nil {
		return nil, nil, err
	}
	task, err := qtx.GetAgentTask(ctx, relay.SourceTaskID)
	if err != nil {
		return nil, nil, err
	}
	if err := notifyQuotaWait(ctx, qtx, updated, task, audit); err != nil {
		return nil, nil, err
	}
	if updated.Status == prev.Status {
		return nil, comment, nil
	}
	return &updated, comment, nil
}

func quotaRelayActor(taskID pgtype.UUID, s *TaskService, ctx context.Context) pgtype.UUID {
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return pgtype.UUID{}
	}
	if task.AccountableUserID.Valid {
		return task.AccountableUserID
	}
	return task.OriginatorUserID
}

func quotaRelayGiveUp(err error) bool {
	if err == nil || errors.Is(err, ErrDuplicatePendingTask) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not accepting work") ||
		strings.Contains(msg, "archived") ||
		strings.Contains(msg, "no runtime") ||
		strings.Contains(msg, "fail-closed")
}

func suppressQuotaSeat(ctx context.Context, qtx *db.Queries, agent db.Agent) (pgtype.Timestamptz, error) {
	updated, err := qtx.SuppressAgentWorkForQuota(ctx, db.SuppressAgentWorkForQuotaParams{
		ID:          agent.ID,
		WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.Timestamptz{}, nil
		}
		return pgtype.Timestamptz{}, err
	}
	return updated, nil
}

func pickQuotaReplacement(ctx context.Context, qtx *db.Queries, failed db.Agent, workspaceID pgtype.UUID) (quotarelay.Choice, bool, error) {
	agents, err := qtx.ListAgents(ctx, workspaceID)
	if err != nil {
		return quotarelay.Choice{}, false, err
	}
	open, err := qtx.ListOpenQuotaBreakerAgentIDs(ctx, workspaceID)
	if err != nil {
		return quotarelay.Choice{}, false, err
	}
	broken := make(map[string]bool, len(open))
	for _, id := range open {
		broken[util.UUIDToString(id)] = true
	}
	roster := make([]quotarelay.Seat, 0, len(agents))
	var failedSeat quotarelay.Seat
	failedID := util.UUIDToString(failed.ID)
	for _, agent := range agents {
		id := util.UUIDToString(agent.ID)
		tier := quotaTierKey(agent.RoutingTier)
		seat := quotarelay.Seat{
			ID:        id,
			Name:      agent.Name,
			Tier:      tier,
			Direction: quotaSeatDirection(agent.Name),
			Eligible:  agent.WorkEnabled && agent.RuntimeID.Valid && !agent.ArchivedAt.Valid && tier != "" && !broken[id],
		}
		if id == failedID {
			failedSeat = seat
			failedSeat.Eligible = false
		}
		roster = append(roster, seat)
	}
	choice, ok := quotarelay.Pick(failedSeat, roster, routing.DefaultLadder.TierKeys())
	return choice, ok, nil
}

func insertQuotaRelay(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, plan quotarelay.Plan, extra db.InsertQuotaRelayParams) (*db.AgentQuotaRelay, error) {
	extra.WorkspaceID = agent.WorkspaceID
	extra.SourceTaskID = task.ID
	extra.FromAgentID = agent.ID
	extra.Scope = plan.Scope()
	extra.ModelKey = plan.ModelKey
	if extra.TierFrom == "" {
		extra.TierFrom = planSeatTier(agent)
	}
	if !extra.TriggerCommentID.Valid {
		extra.TriggerCommentID = task.TriggerCommentID
	}
	row, err := qtx.InsertQuotaRelay(ctx, extra)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func postQuotaAudit(ctx context.Context, qtx *db.Queries, issue db.Issue, content string, taskID pgtype.UUID) (*db.Comment, error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	comment, err := qtx.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		RoutingKind: "quota_relay:" + util.UUIDToString(taskID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &comment, nil
}

func notifyQuotaWait(ctx context.Context, qtx *db.Queries, issue db.Issue, task db.AgentTaskQueue, body string) error {
	recipient, ok := quotaWaitRecipient(issue, task)
	if !ok {
		return nil
	}
	details, _ := json.Marshal(map[string]string{
		"wait_reason": "quota_relay",
		"issue_id":    util.UUIDToString(issue.ID),
	})
	_, err := qtx.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   recipient,
		Type:          "quota_relay_waiting",
		Severity:      "action_required",
		IssueID:       issue.ID,
		Title:         "额度熔断后没有可接力的席位",
		Body:          pgtype.Text{String: body, Valid: true},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		Details:       details,
		ID:            dbid.NewV7(),
	})
	return err
}

func quotaWaitRecipient(issue db.Issue, task db.AgentTaskQueue) (pgtype.UUID, bool) {
	if task.AccountableUserID.Valid {
		return task.AccountableUserID, true
	}
	if task.OriginatorUserID.Valid {
		return task.OriginatorUserID, true
	}
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		return issue.CreatorID, true
	}
	return pgtype.UUID{}, false
}

func (s *TaskService) publishQuotaRelaySideEffects(ctx context.Context, prepared *quotaRelayPrepared) {
	if prepared == nil {
		return
	}
	if prepared.comment != nil {
		s.publishQuotaComment(ctx, prepared.comment)
	}
	if prepared.issue != nil && s.Bus != nil {
		s.broadcastIssueUpdated(ctx, *prepared.issue, prepared.prevStatus)
	}
}

func (s *TaskService) publishQuotaComment(ctx context.Context, comment *db.Comment) {
	if s == nil || s.Bus == nil || comment == nil {
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(comment.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"comment": commentEventFields(*comment),
		},
	})
}

func quotaModelBinding(agent db.Agent) quotarelay.Binding {
	model := ""
	if agent.Model.Valid {
		model = strings.TrimSpace(agent.Model.String)
	}
	return quotarelay.Binding{
		Model:    model,
		OwnModel: agent.ParentAgentID.Valid && !agent.RuntimeInherited && model != "",
	}
}

func quotaTierKey(raw pgtype.Text) string {
	if !raw.Valid {
		return ""
	}
	key, ok := routing.DefaultLadder.NormalizeTier(raw.String)
	if !ok {
		return ""
	}
	return key
}

func planSeatTier(agent db.Agent) string {
	return quotaTierKey(agent.RoutingTier)
}

func quotaSeatDirection(name string) string {
	for _, direction := range routing.DefaultLadder.Directions {
		if direction != "" && strings.HasSuffix(name, direction) {
			return direction
		}
	}
	return ""
}

func quotaReason(task db.AgentTaskQueue) string {
	if task.FailureReason.Valid {
		return task.FailureReason.String
	}
	return ""
}

func quotaError(task db.AgentTaskQueue) string {
	if task.Error.Valid {
		return task.Error.String
	}
	return ""
}

func quotaHandoff(task db.AgentTaskQueue, agent db.Agent, issue db.Issue, plan quotarelay.Plan) quotarelay.Handoff {
	thread := ""
	if task.TriggerCommentID.Valid {
		thread = util.UUIDToString(task.TriggerCommentID)
	}
	reviewerType := ""
	if issue.ReviewerType.Valid {
		reviewerType = issue.ReviewerType.String
	}
	reviewerID := ""
	if issue.ReviewerID.Valid {
		reviewerID = util.UUIDToString(issue.ReviewerID)
	}
	return quotarelay.Handoff{
		FailedName:      agent.Name,
		FailedID:        util.UUIDToString(agent.ID),
		TaskID:          util.UUIDToString(task.ID),
		IssueNumber:     issue.Number,
		IssueTitle:      issue.Title,
		ThreadCommentID: thread,
		ReviewerType:    reviewerType,
		ReviewerID:      reviewerID,
		Acceptance:      quotaAcceptanceText(issue.AcceptanceCriteria),
		Kind:            plan.Kind,
		ModelKey:        plan.ModelKey,
		RecoverAt:       plan.RecoverAt,
		Condition:       plan.Condition,
	}
}

func quotaAcceptanceText(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "[]" || s == "{}" {
		return ""
	}
	if utf8.RuneCountInString(s) <= 500 {
		return s
	}
	rs := []rune(s)
	return string(rs[:500]) + "…"
}
