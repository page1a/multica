package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The 从结果里学 half of the routing store (DENE-1722).

func (s routingStore) RecordOutcome(ctx context.Context, workspaceID, issueID string, c routing.OutcomeClass) error {
	wsID, id, err := parseWorkspaceIssue(workspaceID, issueID)
	if err != nil {
		return err
	}
	return s.h.Queries.RecordIssueRoutingOutcome(ctx, db.RecordIssueRoutingOutcomeParams{
		IssueID: id, WorkspaceID: wsID,
		Direction: c.Direction, Tier: c.Tier, Scope: c.Scope, Clarity: c.Clarity, Risk: c.Risk,
	})
}

func (s routingStore) MarkUnderjudged(ctx context.Context, workspaceID, issueID, signal string) error {
	wsID, id, err := parseWorkspaceIssue(workspaceID, issueID)
	if err != nil {
		return err
	}
	return markUnderjudged(ctx, s.h.Queries, wsID, id, signal)
}

// markUnderjudged is shared with the comment path, which records a hold
// whether or not a router is wired.
func markUnderjudged(ctx context.Context, q *db.Queries, wsID, issueID pgtype.UUID, signal string) error {
	switch signal {
	case routing.SignalEscalated:
		return q.MarkIssueRoutingEscalated(ctx, db.MarkIssueRoutingEscalatedParams{IssueID: issueID, WorkspaceID: wsID})
	case routing.SignalHeld:
		return q.MarkIssueRoutingHeld(ctx, db.MarkIssueRoutingHeldParams{IssueID: issueID, WorkspaceID: wsID})
	}
	return fmt.Errorf("unknown routing signal %q", signal)
}

func (s routingStore) OutcomeStats(ctx context.Context, workspaceID string, since time.Time) ([]routing.ClassStats, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.h.Queries.ListRoutingOutcomeStats(ctx, db.ListRoutingOutcomeStatsParams{
		WorkspaceID: wsID, Since: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]routing.ClassStats, 0, len(rows))
	for _, r := range rows {
		out = append(out, routing.ClassStats{
			Class: routing.OutcomeClass{Direction: r.Direction, Tier: r.Tier, Scope: r.Scope, Clarity: r.Clarity, Risk: r.Risk},
			Total: int(r.Total), Escalated: int(r.Escalated), Held: int(r.Held), Low: int(r.Low),
		})
	}
	return out, nil
}

func parseWorkspaceIssue(workspaceID, issueID string) (pgtype.UUID, pgtype.UUID, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	id, err := util.ParseUUID(issueID)
	return wsID, id, err
}

// GetRoutingLearning backs GET /api/workspaces/{id}/routing/learning: the
// 从结果里学 switch, its thresholds, and every class tiered in the window
// with what the rule would do to its next ticket. Member-visible, like the
// health report: everyone can see how routing is deciding.
func (h *Handler) GetRoutingLearning(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		writeError(w, http.StatusConflict, "routing is not enabled on this server")
		return
	}
	rep, err := h.Routing.Learning(r.Context(), workspaceID)
	if err != nil {
		slog.Warn("routing learning report failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read routing learning report")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
