package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// Consult (DENE-1721): a run working a ticket asks a strong-tier seat one
// question and waits for the advice. The advisor gets a short issue-less run
// whose context names the consult; the ticket, its executor and its status
// never change. The run's tokens land on the advisor's own usage (so its
// quota sees them) and, when the ticket has a goal, on the goal budget.
const (
	ConsultContextType = "consult"
	// MaxConsultQuestionRunes bounds what an asker may send; paste the code
	// that matters, not the repository.
	MaxConsultQuestionRunes = 12000
	// MaxConsultAnswerRunes is the longest advice kept and shown. The brief
	// asks for less; this only clips a runaway reply.
	MaxConsultAnswerRunes = 8000

	ConsultActivityAnswered = "consult_answered"
	ConsultActivityFailed   = "consult_failed"
)

var (
	// ErrConsultLimitReached: the ticket already used its consults.
	ErrConsultLimitReached = errors.New("consult limit reached")
	// ErrConsultAdvisorsBusy: every strong seat that routing allowed is
	// offline, on a daemon too old to answer, or has no free slot.
	ErrConsultAdvisorsBusy = errors.New("no strong seat can answer right now")
)

// ConsultContext is the advisor run's task context.
type ConsultContext struct {
	Type         string `json:"type"`
	ConsultID    string `json:"consult_id"`
	WorkspaceID  string `json:"workspace_id"`
	IssueID      string `json:"issue_id"`
	AskerAgentID string `json:"asker_agent_id"`
	AskerTaskID  string `json:"asker_task_id"`
}

// ParseConsultContext reports whether task is a consult run.
func ParseConsultContext(task db.AgentTaskQueue) (ConsultContext, bool) {
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || len(task.Context) == 0 {
		return ConsultContext{}, false
	}
	var cc ConsultContext
	if err := json.Unmarshal(task.Context, &cc); err != nil || cc.Type != ConsultContextType || cc.ConsultID == "" {
		return ConsultContext{}, false
	}
	return cc, true
}

// ConsultRequest is one ask. Candidates come from routing, best first; Ready
// is the handler's live check (daemon online and able to run a consult).
type ConsultRequest struct {
	Issue      db.Issue
	AskerTask  db.AgentTaskQueue
	Question   string
	Limit      int
	Candidates []pgtype.UUID
	Ready      func(ctx context.Context, agent db.Agent) bool
}

// RequestConsult counts the ask against the ticket's limit, takes the first
// candidate with a free slot and queues its run, all under one per-ticket
// lock so two parallel asks cannot both take the last consult.
func (s *TaskService) RequestConsult(ctx context.Context, req ConsultRequest) (db.IssueConsult, db.AgentTaskQueue, error) {
	if s.TxStarter == nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, fmt.Errorf("consult needs a transaction starter")
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	if err := qtx.LockIssueConsults(ctx, util.UUIDToString(req.Issue.ID)); err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	used, err := qtx.CountIssueConsultsAgainstLimit(ctx, req.Issue.ID)
	if err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	if used >= int64(req.Limit) {
		return db.IssueConsult{}, db.AgentTaskQueue{}, ErrConsultLimitReached
	}

	var advisor db.Agent
	for _, id := range req.Candidates {
		agent, err := qtx.GetAgent(ctx, id)
		if err != nil || agent.ArchivedAt.Valid || !agent.WorkEnabled || !agent.RuntimeID.Valid || agent.WorkspaceID != req.Issue.WorkspaceID {
			continue
		}
		running, err := qtx.CountRunningTasks(ctx, agent.ID)
		if err != nil || (agent.MaxConcurrentTasks > 0 && running >= int64(agent.MaxConcurrentTasks)) {
			continue
		}
		if req.Ready != nil && !req.Ready(ctx, agent) {
			continue
		}
		advisor = agent
		break
	}
	if !advisor.ID.Valid {
		return db.IssueConsult{}, db.AgentTaskQueue{}, ErrConsultAdvisorsBusy
	}

	consult, err := qtx.CreateIssueConsult(ctx, db.CreateIssueConsultParams{
		ID: dbid.NewV7(), WorkspaceID: req.Issue.WorkspaceID, IssueID: req.Issue.ID,
		AskerAgentID: req.AskerTask.AgentID, AskerTaskID: req.AskerTask.ID,
		AdvisorAgentID: advisor.ID, Question: req.Question,
	})
	if err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	contextJSON, _ := json.Marshal(ConsultContext{
		Type: ConsultContextType, ConsultID: util.UUIDToString(consult.ID),
		WorkspaceID: util.UUIDToString(req.Issue.WorkspaceID), IssueID: util.UUIDToString(req.Issue.ID),
		AskerAgentID: util.UUIDToString(req.AskerTask.AgentID), AskerTaskID: util.UUIDToString(req.AskerTask.ID),
	})
	// The advisor works for whoever the asking run works for: the asker's
	// human is copied down, as for a sub-issue an agent creates.
	attr := attribution.ClassifyDirect(attribution.DirectFacts{
		IssueID: req.Issue.ID, OriginType: "agent_create", OriginTaskID: req.AskerTask.ID,
		OriginOriginator: req.AskerTask.OriginatorUserID, OriginAccountable: req.AskerTask.AccountableUserID,
	})
	if attr, err = s.applyAttributionFallback(ctx, attr, advisor); err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	attrSource, _, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	task, err := qtx.CreateQuickCreateTask(ctx, db.CreateQuickCreateTaskParams{
		ID: dbid.NewV7(), AgentID: advisor.ID, RuntimeID: advisor.RuntimeID,
		Priority: priorityToInt("urgent"), Context: contextJSON,
		OriginatorUserID: attr.UserID, AccountableUserID: attr.AccountableUserID,
		OriginatorSource: attrSource, TriggerEvidenceKind: attrEvidenceKind, TriggerEvidenceRefID: attrEvidenceRef,
	})
	if err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, fmt.Errorf("queue consult run: %w", err)
	}
	if err := qtx.SetIssueConsultTask(ctx, db.SetIssueConsultTaskParams{ID: consult.ID, AdvisorTaskID: task.ID}); err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	consult.AdvisorTaskID = task.ID
	if err := tx.Commit(ctx); err != nil {
		return db.IssueConsult{}, db.AgentTaskQueue{}, err
	}
	slog.Info("consult queued", "consult_id", util.UUIDToString(consult.ID), "issue_id", util.UUIDToString(req.Issue.ID),
		"asker_agent_id", util.UUIDToString(req.AskerTask.AgentID), "advisor_agent_id", util.UUIDToString(advisor.ID))
	s.NotifyTaskEnqueued(ctx, task)
	return consult, task, nil
}

// RenderConsultPrompt is the advisor's whole brief: who asks, the ticket, the
// question and the rules of the reply.
func RenderConsultPrompt(identifier, title, description, stateCard, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are a senior advisor. The agent working %s is asking you one question. It keeps the ticket; you only advise.\n\n", identifier)
	b.WriteString("Rules:\n")
	b.WriteString("- Reply with your advice as your final message. That message is the only thing the asker receives.\n")
	b.WriteString("- Do not change code, files, tickets, comments or status. Your platform credential is read-only; `multica issue get/context/comment list` work if you need more background.\n")
	fmt.Fprintf(&b, "- Be direct and specific: a recommendation, the risks you see, and what to check. Stay under %d characters.\n\n", MaxConsultAnswerRunes/2)
	fmt.Fprintf(&b, "## Ticket %s: %s\n\n", identifier, title)
	if desc := strings.TrimSpace(description); desc != "" {
		b.WriteString(truncateFallbackCommentBody(desc, 6000))
		b.WriteString("\n\n")
	}
	if card := strings.TrimSpace(stateCard); card != "" {
		b.WriteString("## Where it stands\n\n")
		b.WriteString(truncateFallbackCommentBody(card, 6000))
		b.WriteString("\n\n")
	}
	b.WriteString("## Question\n\n")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n")
	return b.String()
}

// finishConsult records the advisor run's outcome on the consult, charges
// the ticket's goal budget and writes the timeline entry. It runs once per
// consult: FinishIssueConsult only moves a pending row.
func (s *TaskService) finishConsult(ctx context.Context, task db.AgentTaskQueue, cc ConsultContext, answer, failure string) {
	consultID, err := util.ParseUUID(cc.ConsultID)
	if err != nil {
		return
	}
	workspaceID, _ := util.ParseUUID(cc.WorkspaceID)
	duration := int64(0)
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		duration = max(int64(task.CompletedAt.Time.Sub(task.StartedAt.Time).Seconds()), 1)
	}
	tokens := int64(0)
	if usage, err := s.Queries.GetTaskUsage(ctx, task.ID); err == nil {
		for _, row := range usage {
			tokens += row.InputTokens + row.OutputTokens + row.CacheReadTokens + row.CacheWriteTokens
		}
	}
	params := db.FinishIssueConsultParams{ID: consultID, TokensUsed: tokens, DurationSeconds: duration}
	answer = strings.TrimSpace(answer)
	if failure == "" && answer == "" {
		failure = "the advisor finished without a reply"
	}
	if failure == "" {
		params.Status = "answered"
		params.Answer = pgtype.Text{String: truncateFallbackCommentBody(redact.Text(answer), MaxConsultAnswerRunes), Valid: true}
	} else {
		params.Status = "failed"
		params.FailureReason = pgtype.Text{String: truncateFallbackCommentBody(redact.Text(failure), 500), Valid: true}
	}
	consult, err := s.Queries.FinishIssueConsult(ctx, params)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("consult: finish failed", "consult_id", cc.ConsultID, "error", err)
		}
		return
	}
	if consult.WorkspaceID != workspaceID {
		return
	}
	// A consult spends the ticket's goal budget like any run of it, but it is
	// not a round: only tokens and time count, runs_used stays.
	if tokens > 0 || duration > 0 {
		if _, err := s.Queries.UpdateIssueGoalUsage(ctx, db.UpdateIssueGoalUsageParams{
			IssueID: consult.IssueID, WorkspaceID: consult.WorkspaceID, TokensUsed: tokens, DurationSecondsUsed: duration,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("consult: goal usage update failed", "consult_id", cc.ConsultID, "error", err)
		}
	}
	s.recordConsultActivity(ctx, consult)
}

func (s *TaskService) recordConsultActivity(ctx context.Context, c db.IssueConsult) {
	advisorName := ""
	if agent, err := s.Queries.GetAgent(ctx, c.AdvisorAgentID); err == nil {
		advisorName = agent.Name
	}
	action := ConsultActivityAnswered
	details := map[string]any{
		"consult_id": util.UUIDToString(c.ID), "advisor_id": util.UUIDToString(c.AdvisorAgentID), "advisor_name": advisorName,
		"question": c.Question, "tokens_used": c.TokensUsed, "duration_seconds": c.DurationSeconds,
	}
	if c.Status == "answered" {
		details["answer"] = c.Answer.String
	} else {
		action = ConsultActivityFailed
		details["reason"] = c.FailureReason.String
	}
	raw, _ := json.Marshal(details)
	row, err := s.Queries.CreateActivity(ctx, db.CreateActivityParams{
		ID: dbid.NewV7(), WorkspaceID: c.WorkspaceID, IssueID: c.IssueID,
		ActorType: pgtype.Text{String: "agent", Valid: true}, ActorID: c.AskerAgentID,
		Action: action, Details: raw,
	})
	if err != nil {
		slog.Warn("consult: activity write failed", "consult_id", util.UUIDToString(c.ID), "error", err)
		return
	}
	if s.Bus == nil {
		return
	}
	actorID := util.UUIDToString(c.AskerAgentID)
	s.Bus.Publish(events.Event{
		Type: protocol.EventActivityCreated, WorkspaceID: util.UUIDToString(c.WorkspaceID),
		ActorType: "agent", ActorID: actorID,
		Payload: map[string]any{
			"issue_id": util.UUIDToString(c.IssueID),
			"entry": map[string]any{
				"type": "activity", "id": util.UUIDToString(row.ID), "actor_type": "agent", "actor_id": actorID,
				"action": action, "details": json.RawMessage(raw), "created_at": row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
			},
		},
	})
}

// ReconcileConsult settles a consult whose advisor run was cancelled — a
// path that runs no completion hook — so it neither hangs the asker nor
// keeps counting against the ticket. A failed run is left alone: its retry
// may still answer, and the failure hook settles it otherwise.
func (s *TaskService) ReconcileConsult(ctx context.Context, c db.IssueConsult) db.IssueConsult {
	if c.Status != "pending" || !c.AdvisorTaskID.Valid {
		return c
	}
	task, err := s.Queries.GetAgentTask(ctx, c.AdvisorTaskID)
	if err != nil || task.Status != "cancelled" {
		return c
	}
	cc, ok := ParseConsultContext(task)
	if !ok {
		return c
	}
	s.finishConsult(ctx, task, cc, "", "the advisor run was cancelled")
	if fresh, err := s.Queries.GetIssueConsult(ctx, db.GetIssueConsultParams{ID: c.ID, WorkspaceID: c.WorkspaceID}); err == nil {
		return fresh
	}
	return c
}
