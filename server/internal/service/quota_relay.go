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
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

const quotaRelayPendingBatch = 50

// RelayQuotaFailure opens a recoverable breaker for a quota failure and, when
// the issue is still unfinished, hands it to a same-tier seat or exactly one
// tier down. The bool tells HandleFailedTasks not to reset the issue to todo:
// a replacement is queued, or the issue was marked blocked on purpose.
//
// A capacity or rate-limit failure uses the same path only after the in-place
// retry budget is spent. While retryEligible is still true, this returns
// false and changes nothing. Other transient errors stay out.
func (s *TaskService) RelayQuotaFailure(ctx context.Context, task db.AgentTaskQueue) (bool, error) {
	if s == nil || s.Queries == nil || !quotaFailureWorthRelay(task) {
		return false, nil
	}
	prepared, idle, alert, err := s.prepareQuotaRelay(ctx, task)
	if err != nil {
		return false, err
	}
	if prepared != nil && prepared.enqueue {
		if err := s.finishQuotaRelay(ctx, prepared.relay); err != nil {
			return true, err
		}
	}
	s.publishQuotaRelaySideEffects(ctx, prepared)
	s.finishIdleTransfers(ctx, idle)
	if alert != nil {
		moved := len(idle)
		if prepared != nil && prepared.relay.ToAgentID.Valid {
			moved++
		}
		s.alertBalanceOwners(ctx, *alert, moved)
	}
	if prepared == nil {
		return false, nil
	}
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
	var restored []pgtype.UUID
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
			if n > 0 {
				restored = append(restored, agentID)
			}
		}
		return nil
	})
	if err != nil {
		return released, err
	}
	for _, agentID := range restored {
		if recErr := s.ReclaimDesignatedReviews(ctx, agentID); recErr != nil {
			slog.Warn("quota relay: reclaim designated reviewer failed",
				"agent_id", util.UUIDToString(agentID),
				"error", recErr,
			)
		}
	}
	return released, nil
}

type quotaRelayPrepared struct {
	hold       bool
	enqueue    bool
	relay      db.AgentQuotaRelay
	comment    *db.Comment
	issue      *db.Issue
	prevStatus string
}

// idleTransfer is one not-yet-started issue moved off a broken seat.
// Enqueue happens after the breaker transaction commits, because the
// enqueue reads the assignee through a different connection.
type idleTransfer struct {
	issue      db.Issue
	prevStatus string
	comment    *db.Comment
	note       string
	actor      pgtype.UUID
	enqueue    bool
}

// balanceAlert is the one owner reminder for a seat whose account ran out
// of money. It is decided inside the breaker transaction (first opening
// only) and sent after it commits.
type balanceAlert struct {
	agent    db.Agent
	issue    pgtype.UUID
	detail   string
	siblings []string
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

func (s *TaskService) prepareQuotaRelay(ctx context.Context, task db.AgentTaskQueue) (*quotaRelayPrepared, []idleTransfer, *balanceAlert, error) {
	var prepared *quotaRelayPrepared
	var idle []idleTransfer
	var alert *balanceAlert
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
		// Same predicate the in-place retry uses. A capacity miss that can
		// still spawn a child must not open a breaker or move the issue.
		if capacityRetriesRemain(locked, agent) {
			return nil
		}
		plan, ok := quotarelay.PlanFor(quotaReason(locked), quotaError(locked), quotaModelBinding(agent), time.Now())
		if !ok {
			return nil
		}
		balance := plan.Kind == quotarelay.KindBalanceExhausted
		if balance {
			seen, err := qtx.HasOpenQuotaBreakerForReason(ctx, db.HasOpenQuotaBreakerForReasonParams{
				AgentID: agent.ID,
				Reason:  string(plan.Kind),
			})
			if err != nil {
				return err
			}
			if !seen {
				alert = &balanceAlert{agent: agent, issue: locked.IssueID, detail: quotaAcceptanceText([]byte(quotaError(locked)))}
			}
		}
		// Shut every seat on the empty account first, then move the work:
		// a sibling still enabled would be picked as the replacement and
		// fail on the same account (DENE-870).
		seats, err := quotaAccountSeats(ctx, qtx, agent, plan)
		if err != nil {
			return err
		}
		for _, seat := range seats {
			if err := openQuotaBreaker(ctx, qtx, seat, locked, plan); err != nil {
				return err
			}
		}
		demotion := quotaDemotionLine(ctx, qtx, agent)
		for _, seat := range seats {
			var moved []idleTransfer
			if balance {
				moved, err = s.transferBrokenSeatIssues(ctx, qtx, locked, seat)
			} else {
				moved, err = s.reassignUnstartedIssues(ctx, qtx, locked, seat, quotaDemotionLine(ctx, qtx, seat))
			}
			if err != nil {
				return err
			}
			idle = append(idle, moved...)
		}
		if alert != nil {
			for _, seat := range seats[1:] {
				alert.siblings = append(alert.siblings, seat.Name)
			}
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
		handoff.Demotion = demotion
		handoff.Branch = canonicalBranch(ctx, qtx, issue.ID)
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

		choice, found, err := pickQuotaReplacement(ctx, qtx, locked, agent, issue.WorkspaceID, reviewerSeatExclusion(issue))
		if err != nil {
			return err
		}
		if !found {
			return s.waitQuotaRelay(ctx, qtx, locked, agent, issue, plan, handoff, &prepared)
		}
		return s.stageQuotaReplacement(ctx, qtx, locked, agent, issue, plan, handoff, choice, &prepared)
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return prepared, idle, alert, nil
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
	if prev == "blocked" {
		// A failed-child clock written just before this relay would
		// otherwise fire the moment the issue is blocked again.
		if err := dropBlockWaitKeys(ctx, qtx, reassigned); err != nil {
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

// quotaAccountSeats is the failed seat followed by every other seat that
// goes down with it. An empty balance takes the base role, all its
// specialisations and any seat bound to the same account. A weekly window or
// capacity miss on a base role takes the specialisations running on its
// profile. A specialisation's own window stays on that one seat.
func quotaAccountSeats(ctx context.Context, qtx *db.Queries, agent db.Agent, plan quotarelay.Plan) ([]db.Agent, error) {
	seats := []db.Agent{agent}
	if plan.Scope() == quotarelay.ScopeModel || !agent.RuntimeID.Valid {
		return seats, nil
	}
	if plan.Kind != quotarelay.KindBalanceExhausted {
		if agent.ParentAgentID.Valid {
			return seats, nil
		}
		children, err := qtx.ListInheritingSpecialisations(ctx, db.ListInheritingSpecialisationsParams{
			WorkspaceID:   agent.WorkspaceID,
			ParentAgentID: agent.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("list inheriting specialisations: %w", err)
		}
		return append(seats, children...), nil
	}
	root := agent.ID
	if agent.ParentAgentID.Valid {
		root = agent.ParentAgentID
	}
	env := agent.CustomEnv
	if len(env) == 0 {
		env = []byte("{}")
	}
	siblings, err := qtx.ListQuotaAccountSiblings(ctx, db.ListQuotaAccountSiblingsParams{
		WorkspaceID: agent.WorkspaceID,
		AgentID:     agent.ID,
		RuntimeID:   agent.RuntimeID,
		RootID:      root,
		CustomEnv:   env,
	})
	if err != nil {
		return nil, fmt.Errorf("list quota account siblings: %w", err)
	}
	return append(seats, siblings...), nil
}

// openQuotaBreaker turns one seat's work off and records why.
func openQuotaBreaker(ctx context.Context, qtx *db.Queries, agent db.Agent, task db.AgentTaskQueue, plan quotarelay.Plan) error {
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
		SourceTaskID:           task.ID,
		Detail:                 quotaAcceptanceText([]byte(quotaError(task))),
		RecoverAt:              pgtype.Timestamptz{Time: plan.RecoverAt, Valid: true},
		RecoverCondition:       plan.Condition,
		SuppressAgentUpdatedAt: suppressAt,
	}); err != nil {
		return fmt.Errorf("open quota breaker: %w", err)
	}
	return nil
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

func capacityRetriesRemain(task db.AgentTaskQueue, agent db.Agent) bool {
	reason := quotaReason(task)
	if !quotarelay.IsCapacityFailure(reason, quotaError(task)) {
		return false
	}
	return retryEligible(reason, task, agent)
}

// relayCapacityIfRetriesSpent is the daemon fail path. HandleFailedTasks
// already calls RelayQuotaFailure after MaybeRetryFailedTask declines; a
// failure the daemon reported itself never reaches that sweeper, so the
// exhausted capacity attempt has to enter the relay here or the issue stays
// in progress with the provider's English sentence.
//
// A quota exhaustion the daemon reported takes the same path (DENE-870):
// before this, a 402 that never reached the sweeper left the seat enabled,
// and every wake handed the issue back to the same empty account.
func (s *TaskService) relayCapacityIfRetriesSpent(ctx context.Context, task db.AgentTaskQueue, failureReason, errMsg string) bool {
	if s == nil || !quotarelay.ShouldInspect(failureReason, errMsg) {
		return false
	}
	hold, err := s.RelayQuotaFailure(ctx, task)
	if err != nil {
		slog.Warn("fail task: capacity relay failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return false
	}
	return hold
}

func pickQuotaReplacement(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, failed db.Agent, workspaceID pgtype.UUID, exclude []string) (quotarelay.Choice, bool, error) {
	failedSeat, roster, err := quotaRoster(ctx, qtx, task, failed, workspaceID)
	if err != nil {
		return quotarelay.Choice{}, false, err
	}
	failedSeat.Exclude = exclude
	choice, ok := quotarelay.Pick(failedSeat, roster, routing.DefaultLadder.TierKeys())
	return choice, ok, nil
}

// quotaRoster is the failed seat and every seat the relay may pick from.
// Built once per breaker so a batch transfer does not reread the roster per
// ticket.
func quotaRoster(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, failed db.Agent, workspaceID pgtype.UUID) (quotarelay.Seat, []quotarelay.Seat, error) {
	agents, err := qtx.ListAgents(ctx, workspaceID)
	if err != nil {
		return quotarelay.Seat{}, nil, err
	}
	open, err := qtx.ListOpenQuotaBreakerAgentIDs(ctx, workspaceID)
	if err != nil {
		return quotarelay.Seat{}, nil, err
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
		provider, _ := routing.DefaultLadder.ProviderOf(agent.Name)
		seat := quotarelay.Seat{
			ID:        id,
			Name:      agent.Name,
			Tier:      tier,
			Direction: quotaSeatDirection(agent.Name),
			Provider:  provider,
			Eligible:  agent.WorkEnabled && agent.RuntimeID.Valid && !agent.ArchivedAt.Valid && tier != "" && !broken[id],
		}
		if id == failedID {
			failedSeat = seat
			failedSeat.Eligible = false
			failedSeat.AvoidHouse = capacityAvoidHouse(task, agent.Name)
			failedSeat.StrictHouse = balanceFailure(task)
		}
		roster = append(roster, seat)
	}
	return failedSeat, roster, nil
}

// reviewerSeatExclusion keeps the replacement off the issue's own acceptance
// seat: the executor and the reviewer must not be the same seat.
func reviewerSeatExclusion(issue db.Issue) []string {
	if issue.ReviewerType.Valid && issue.ReviewerType.String == "agent" && issue.ReviewerID.Valid {
		return []string{util.UUIDToString(issue.ReviewerID)}
	}
	return nil
}

func balanceFailure(task db.AgentTaskQueue) bool {
	plan, ok := quotarelay.PlanFor(quotaReason(task), quotaError(task), quotarelay.Binding{}, time.Time{})
	return ok && plan.Kind == quotarelay.KindBalanceExhausted
}

func canonicalBranch(ctx context.Context, qtx *db.Queries, issueID pgtype.UUID) string {
	row, err := qtx.GetIssueCanonicalDeliveryBranch(ctx, issueID)
	if err != nil {
		return ""
	}
	return row.BranchName
}

func dropBlockWaitKeys(ctx context.Context, qtx *db.Queries, issue db.Issue) error {
	for _, key := range blockwait.WaitKeys() {
		if _, err := qtx.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: issue.WorkspaceID,
			Key:         key,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
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

func capacityAvoidHouse(task db.AgentTaskQueue, name string) string {
	// A seat whose account ran out of money leaves its house too: Grok
	// goes to Claude or GPT, GPT to Claude or Grok (DENE-870).
	if !quotarelay.IsCapacityFailure(quotaReason(task), quotaError(task)) && !balanceFailure(task) {
		return ""
	}
	// Any house, not only GPT. A Claude seat that is full should not hand
	// the work to another Claude seat either: the same provider is usually
	// full together. One tier down still may land on the same house when
	// the rung has nobody else.
	provider, ok := routing.DefaultLadder.ProviderOf(name)
	if !ok || provider == "" {
		return ""
	}
	return provider
}

func quotaDemotionLine(ctx context.Context, qtx *db.Queries, agent db.Agent) string {
	n, err := qtx.CountQuotaBreakersSinceSuccess(ctx, agent.ID)
	if err != nil || n < 2 {
		return ""
	}
	return quotarelay.DemotionLine(agent.Name, int(n))
}

// reassignUnstartedIssues moves tickets that are assigned to the broken
// seat but have not started. The failing ticket itself is left to the
// relay: it already has a failed task and its own handoff.
func (s *TaskService) reassignUnstartedIssues(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent, demotion string) ([]idleTransfer, error) {
	choice, found, err := pickQuotaReplacement(ctx, qtx, task, agent, agent.WorkspaceID, nil)
	if err != nil || !found {
		return nil, err
	}
	issues, err := qtx.ListUnstartedIssuesForAgent(ctx, db.ListUnstartedIssuesForAgentParams{
		WorkspaceID: agent.WorkspaceID,
		AssigneeID:  agent.ID,
		Limit:       50,
	})
	if err != nil {
		return nil, err
	}
	sourceID := ""
	if task.IssueID.Valid {
		sourceID = util.UUIDToString(task.IssueID)
	}
	crossHouse := capacityAvoidHouse(task, agent.Name) != ""
	actor := task.AccountableUserID
	if !actor.Valid {
		actor = task.OriginatorUserID
	}
	replacementID := util.MustParseUUID(choice.Seat.ID)
	out := make([]idleTransfer, 0, len(issues))
	for _, listed := range issues {
		if util.UUIDToString(listed.ID) == sourceID {
			continue
		}
		issue, err := qtx.LockIssueForQuotaRelay(ctx, listed.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if issue.Status != "todo" && issue.Status != "in_progress" && issue.Status != "backlog" {
			continue
		}
		reassigned, err := qtx.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
			AssigneeID:        replacementID,
			ID:                issue.ID,
			WorkspaceID:       issue.WorkspaceID,
			CurrentAssigneeID: agent.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if _, err := qtx.CancelPendingTasksByIssueAndAgent(ctx, db.CancelPendingTasksByIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: agent.ID,
		}); err != nil {
			return nil, err
		}
		tierLabel := choice.Seat.Tier
		if tier, ok := routing.DefaultLadder.TierByKey(choice.Seat.Tier); ok && tier.Label != "" {
			tierLabel = tier.Label
		}
		audit := quotarelay.AuditIdle(agent.Name, choice.Seat.Name, tierLabel, crossHouse, choice.SteppedDown, demotion)
		if reassigned.Status == "backlog" {
			audit += "\n这张票还在待排期，先改了执行人，轮到开跑时由新席位接。\n"
		}
		comment, err := postQuotaAudit(ctx, qtx, reassigned, audit, task.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, idleTransfer{
			issue:      reassigned,
			prevStatus: issue.Status,
			comment:    comment,
			note:       quotarelay.IdleHandoffNote(agent.Name, choice.Seat.Name, choice.SteppedDown),
			actor:      actor,
			enqueue:    reassigned.Status == "todo" || reassigned.Status == "in_progress",
		})
	}
	return out, nil
}

// transferBrokenSeatIssues moves every todo, in-progress, or blocked ticket
// off a seat whose account ran out of money, started or not, to a seat of
// another house. Each ticket gets its own pick so the replacement is never
// that ticket's acceptance seat. The failing ticket itself is left to the
// relay. Blocked tickets change hands but are not started: the patrol wakes
// the new seat when their clock is due.
func (s *TaskService) transferBrokenSeatIssues(ctx context.Context, qtx *db.Queries, task db.AgentTaskQueue, agent db.Agent) ([]idleTransfer, error) {
	failedSeat, roster, err := quotaRoster(ctx, qtx, task, agent, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}
	issues, err := qtx.ListOpenIssuesForBrokenSeat(ctx, db.ListOpenIssuesForBrokenSeatParams{
		WorkspaceID: agent.WorkspaceID,
		AssigneeID:  agent.ID,
		Limit:       50,
	})
	if err != nil {
		return nil, err
	}
	sourceID := ""
	if task.IssueID.Valid {
		sourceID = util.UUIDToString(task.IssueID)
	}
	actor := task.AccountableUserID
	if !actor.Valid {
		actor = task.OriginatorUserID
	}
	out := make([]idleTransfer, 0, len(issues))
	for _, listed := range issues {
		if util.UUIDToString(listed.ID) == sourceID {
			continue
		}
		issue, err := qtx.LockIssueForQuotaRelay(ctx, listed.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if issue.Status != "todo" && issue.Status != "in_progress" && issue.Status != "blocked" {
			continue
		}
		seat := failedSeat
		seat.Exclude = reviewerSeatExclusion(issue)
		choice, found := quotarelay.Pick(seat, roster, routing.DefaultLadder.TierKeys())
		if !found {
			continue
		}
		replacementID := util.MustParseUUID(choice.Seat.ID)
		reassigned, err := qtx.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
			AssigneeID:        replacementID,
			ID:                issue.ID,
			WorkspaceID:       issue.WorkspaceID,
			CurrentAssigneeID: agent.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if _, err := qtx.CancelPendingTasksByIssueAndAgent(ctx, db.CancelPendingTasksByIssueAndAgentParams{
			IssueID: issue.ID,
			AgentID: agent.ID,
		}); err != nil {
			return nil, err
		}
		started, err := qtx.HasIssueRunHistory(ctx, issue.ID)
		if err != nil {
			return nil, err
		}
		branch := canonicalBranch(ctx, qtx, issue.ID)
		tierLabel := choice.Seat.Tier
		if tier, ok := routing.DefaultLadder.TierByKey(choice.Seat.Tier); ok && tier.Label != "" {
			tierLabel = tier.Label
		}
		audit := quotarelay.AuditBalanceTransfer(agent.Name, choice.Seat.Name, tierLabel, choice.SteppedDown, started, branch)
		comment, err := postQuotaAudit(ctx, qtx, reassigned, audit, task.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, idleTransfer{
			issue:      reassigned,
			prevStatus: issue.Status,
			comment:    comment,
			note:       quotarelay.BalanceTransferNote(agent.Name, choice.Seat.Name, branch, started),
			actor:      actor,
			enqueue:    reassigned.Status == "todo" || reassigned.Status == "in_progress",
		})
	}
	return out, nil
}

// alertBalanceOwners tells each workspace owner, once per episode, that a
// seat ran out of money and needs a top-up or another account.
func (s *TaskService) alertBalanceOwners(ctx context.Context, alert balanceAlert, moved int) {
	if s == nil || s.Queries == nil {
		return
	}
	members, err := s.Queries.ListMembers(ctx, alert.agent.WorkspaceID)
	if err != nil {
		slog.Warn("quota relay: list owners for balance alert failed", "agent_id", util.UUIDToString(alert.agent.ID), "error", err)
		return
	}
	body := quotarelay.BalanceOwnerAlert(alert.agent.Name, alert.detail, moved, alert.siblings...)
	details, _ := json.Marshal(map[string]string{
		"wait_reason": "seat_balance_exhausted",
		"agent_id":    util.UUIDToString(alert.agent.ID),
	})
	for _, member := range members {
		if member.Role != "owner" {
			continue
		}
		item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			WorkspaceID:   alert.agent.WorkspaceID,
			RecipientType: "member",
			RecipientID:   member.UserID,
			Type:          "seat_balance_exhausted",
			Severity:      "action_required",
			IssueID:       alert.issue,
			Title:         fmt.Sprintf("「%s」余额用完了，请充值或换账号", alert.agent.Name),
			Body:          pgtype.Text{String: body, Valid: true},
			ActorType:     pgtype.Text{String: "system", Valid: true},
			Details:       details,
			ID:            dbid.NewV7(),
		})
		if err != nil {
			slog.Warn("quota relay: balance alert failed", "agent_id", util.UUIDToString(alert.agent.ID), "error", err)
			continue
		}
		if s.Bus == nil {
			continue
		}
		s.Bus.Publish(events.Event{
			Type:        protocol.EventInboxNew,
			WorkspaceID: util.UUIDToString(item.WorkspaceID),
			ActorType:   "system",
			Payload: map[string]any{"item": map[string]any{
				"id":             util.UUIDToString(item.ID),
				"workspace_id":   util.UUIDToString(item.WorkspaceID),
				"recipient_type": item.RecipientType,
				"recipient_id":   util.UUIDToString(item.RecipientID),
				"type":           item.Type,
				"severity":       item.Severity,
				"issue_id":       util.UUIDToPtr(item.IssueID),
				"title":          item.Title,
				"body":           util.TextToPtr(item.Body),
				"read":           item.Read,
				"archived":       item.Archived,
				"created_at":     util.TimestampToString(item.CreatedAt),
				"actor_type":     util.TextToPtr(item.ActorType),
				"actor_id":       util.UUIDToPtr(item.ActorID),
				"details":        json.RawMessage(item.Details),
			}},
		})
	}
}

func freshSessionNoticeReason(reason string) bool {
	switch reason {
	case string(taskfailure.ReasonAgentProviderCapacityOrRateLimit),
		string(taskfailure.ReasonAgentProviderNetwork),
		string(taskfailure.ReasonAgentProviderServerError),
		string(taskfailure.ReasonTimeout),
		string(taskfailure.ReasonRuntimeOffline),
		string(taskfailure.ReasonRuntimeRecovery),
		serverInterruptFailureReason:
		return true
	default:
		return false
	}
}

// noteUnresumedRetry says the automatic retry opened a new CLI session
// because the failed run left nothing that can be resumed.
func (s *TaskService) noteUnresumedRetry(ctx context.Context, task db.AgentTaskQueue, rolloutMissing bool) {
	why := "上一轮在留下可恢复的 CLI session 之前就失败了。"
	if rolloutMissing {
		why = "上一轮的 Codex 会话文件没有落到本机，续不上。"
	}
	s.NoteSessionRestart(ctx, task, why)
}

// NoteSessionRestart posts the human explanation when a run had to open a
// new CLI session. Empty reason posts nothing. The same task reports at
// most once.
func (s *TaskService) NoteSessionRestart(ctx context.Context, task db.AgentTaskQueue, reason string) {
	reason = strings.TrimSpace(reason)
	if s == nil || s.Queries == nil || reason == "" || !task.IssueID.Valid {
		return
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if err != nil {
		slog.Warn("session restart notice: load issue failed",
			"task_id", util.UUIDToString(task.ID),
			"error", err,
		)
		return
	}
	comment, err := s.Queries.CreateRoutingComment(ctx, db.CreateRoutingCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     "这次运行新开了 CLI 对话，没有续上原来的会话。原因：" + reason,
		RoutingKind: "session_restart:" + util.UUIDToString(task.ID),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("session restart notice: comment failed",
				"task_id", util.UUIDToString(task.ID),
				"error", err,
			)
		}
		return
	}
	s.publishQuotaComment(ctx, &comment)
}

func (s *TaskService) finishIdleTransfers(ctx context.Context, idle []idleTransfer) {
	for _, item := range idle {
		if item.comment != nil {
			s.publishQuotaComment(ctx, item.comment)
		}
		if s != nil && s.Bus != nil {
			s.broadcastIssueUpdated(ctx, item.issue, item.prevStatus)
		}
		if !item.enqueue {
			continue
		}
		if _, err := s.enqueueIssueTask(ctx, item.issue, pgtype.UUID{}, false, item.note, item.actor, pgtype.UUID{}, pgtype.Timestamptz{}, OriginDerived); err != nil && !errors.Is(err, ErrDuplicatePendingTask) {
			slog.Warn("quota relay: unstarted issue enqueue failed",
				"issue_id", util.UUIDToString(item.issue.ID),
				"error", err,
			)
		}
	}
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
