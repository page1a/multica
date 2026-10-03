package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestEnqueueTaskForIssueWaitsForGoalConfirmation is the database-backed
// regression for the human gate: an assigned goal issue must not create a run
// while its completion line is editable, and the same assignment is enqueueable
// immediately after the goal becomes active.
func TestEnqueueTaskForIssueWaitsForGoalConfirmation(t *testing.T) {
	pool := newResolveOriginatorPool(t)
	ctx := context.Background()
	q := db.New(pool)
	workspaceID, userID, agentID, issueID := seedAttributionFixture(t, pool)
	_, err := pool.Exec(ctx, `INSERT INTO issue_goal (issue_id, workspace_id, created_by_type, created_by_id) VALUES ($1, $2, 'member', $3)`, issueID, workspaceID, userID)
	if err != nil {
		t.Fatalf("seed draft goal: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID) })

	svc := &TaskService{Queries: q, TxStarter: pool, Bus: events.New()}
	issue := db.Issue{
		ID: util.MustParseUUID(issueID), WorkspaceID: util.MustParseUUID(workspaceID),
		AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: util.MustParseUUID(agentID),
		CreatorType: "member", CreatorID: util.MustParseUUID(userID), Priority: "medium",
	}
	if _, err := svc.EnqueueTaskForIssue(ctx, issue); !errors.Is(err, ErrGoalDraftNotConfirmed) {
		t.Fatalf("draft enqueue error = %v, want ErrGoalDraftNotConfirmed", err)
	}
	if _, err := svc.EnqueueTaskForMention(ctx, issue, util.MustParseUUID(agentID), pgtype.UUID{}, OriginNamed); !errors.Is(err, ErrGoalDraftNotConfirmed) {
		t.Fatalf("draft mention enqueue error = %v, want ErrGoalDraftNotConfirmed", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE issue_goal SET status = 'active' WHERE issue_id = $1`, issueID); err != nil {
		t.Fatalf("activate goal: %v", err)
	}
	if _, err := svc.EnqueueTaskForIssue(ctx, issue); err != nil {
		t.Fatalf("active enqueue: %v", err)
	}
}
