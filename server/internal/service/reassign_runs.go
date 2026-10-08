package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ExecutorSeat is who holds an issue's executor slot and whose decision that
// is (routing.Source*).
type ExecutorSeat struct {
	Type   pgtype.Text
	ID     pgtype.UUID
	Source string
}

func (s ExecutorSeat) same(o ExecutorSeat) bool {
	return s.Type.Valid && o.Type.Valid && s.Type.String == o.Type.String && s.ID == o.ID
}

// ReassignVoidsSeatRuns is the one rule for "which runs does changing the
// executor make pointless" (DENE-1613). A reassignment in general cancels
// nothing (MUL-4113): only a seat routing filled, replaced by a person's
// decision (their own hand or their verified words), loses the runs its
// assignment started. Routing's pick was a stand-in until somebody said who;
// once they have, two seats working the same ticket is the bug.
func ReassignVoidsSeatRuns(prev, next ExecutorSeat) bool {
	if prev.Source != routing.SourceRouter || !prev.Type.Valid || !prev.ID.Valid {
		return false
	}
	if next.Source != routing.SourceHuman && next.Source != routing.SourceQuote {
		return false
	}
	return !prev.same(next)
}

// CancelRunsVoidedByReassign cancels the runs ReassignVoidsSeatRuns says the
// reassignment voided: the previous seat's active runs on this issue that its
// assignment started. A squad seat's run belongs to its leader. exceptTaskID
// is the run making the request, if any; it is never cancelled from inside.
// A cancelled run that had started keeps its worktree through the usual
// snapshot branch (agent/agent/<issue>).
func (s *TaskService) CancelRunsVoidedByReassign(ctx context.Context, issueID pgtype.UUID, prev, next ExecutorSeat, exceptTaskID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	if !ReassignVoidsSeatRuns(prev, next) {
		return nil, nil
	}
	agentID := prev.ID
	switch prev.Type.String {
	case "agent":
	case "squad":
		squad, err := s.Queries.GetSquad(ctx, prev.ID)
		if err != nil || !squad.LeaderID.Valid {
			return nil, err
		}
		agentID = squad.LeaderID
	default:
		return nil, nil
	}
	var cancelled []db.AgentTaskQueue
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		cancelled, err = qtx.CancelAssignmentTasksByIssueAndAgent(ctx, db.CancelAssignmentTasksByIssueAndAgentParams{
			IssueID:      issueID,
			AgentID:      agentID,
			ExceptTaskID: exceptTaskID,
		})
		if err != nil {
			return err
		}
		return SettleTerminalTaskState(ctx, qtx, cancelled...)
	}); err != nil {
		return nil, err
	}
	for _, t := range cancelled {
		s.captureTaskCancelled(ctx, t)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, t)
	}
	if len(cancelled) > 0 {
		s.ReconcileAgentStatus(ctx, agentID)
	}
	s.notifyTasksFinished(cancelled)
	return cancelled, nil
}
