package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// seedReviewFailure turns the quota world into DENE-1644's shape: the ticket
// is in review, the failed seat is its acceptance seat (and so its assignee),
// and the same-tier seat already worked on the ticket.
func seedReviewFailure(t *testing.T) quotaWorld {
	t.Helper()
	w := seedQuotaWorld(t, string(taskfailure.ReasonAgentProviderAuthOrAccess), "API Error: 401 authentication_error", true)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE issue SET status = 'in_review', reviewer_id = $2 WHERE id = $1`, w.issueID, w.failedID); err != nil {
		t.Fatalf("move to review: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, originator_user_id, accountable_user_id)
		VALUES ($1, $2, $3, 'completed', 0, $4, $4)`, w.sameID, w.runtimeID, w.issueID, w.userID); err != nil {
		t.Fatalf("seed executor run: %v", err)
	}
	return w
}

// DENE-1647: an acceptance seat that fails on a 401 hands acceptance to
// another seat at once, never to the seat that did the work, and the ticket
// stays in review.
func TestReviewAuthFailureRelaysToAnotherAcceptanceSeat(t *testing.T) {
	w := seedReviewFailure(t)
	ctx := context.Background()

	hold, err := w.service().RelayQuotaFailure(ctx, w.task(t))
	if err != nil || !hold {
		t.Fatalf("relay hold=%v err=%v", hold, err)
	}
	var status, assignee, reviewer, kind string
	var enabled bool
	if err := w.pool.QueryRow(ctx, `SELECT status, assignee_id::text, reviewer_id::text FROM issue WHERE id = $1`, w.issueID).Scan(&status, &assignee, &reviewer); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if status != "in_review" || reviewer != w.mediumID || assignee != w.mediumID {
		t.Fatalf("status/assignee/reviewer = %s/%s/%s, want in_review on %s (the executor %s must not accept its own work)",
			status, assignee, reviewer, w.mediumID, w.sameID)
	}
	if err := w.pool.QueryRow(ctx, `SELECT reason FROM agent_quota_breaker WHERE agent_id = $1 AND recovered_at IS NULL`, w.failedID).Scan(&kind); err != nil {
		t.Fatalf("breaker: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT work_enabled FROM agent WHERE id = $1`, w.failedID).Scan(&enabled); err != nil {
		t.Fatalf("seat: %v", err)
	}
	if kind != "auth_failure" || enabled {
		t.Fatalf("breaker=%s enabled=%v, want auth_failure and the seat paused", kind, enabled)
	}
	var queued int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, w.issueID, w.mediumID).Scan(&queued); err != nil {
		t.Fatalf("queued: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued acceptance runs = %d, want 1", queued)
	}
	relays, err := w.service().Queries.ListIssueQuotaRelays(ctx, w.task(t).IssueID)
	if err != nil || len(relays) != 1 || relays[0].Outcome != "relayed" || relays[0].Reason != "auth_failure" || relays[0].ToAgentName == "" {
		t.Fatalf("relay record = %+v err=%v, want relayed auth_failure naming the new seat", relays, err)
	}
}

// With nobody left to accept, the ticket stays in review and a person gets
// one options ask.
func TestReviewAuthFailureWithNoSeatAsksAPerson(t *testing.T) {
	w := seedReviewFailure(t)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE agent SET routing_tier = NULL WHERE id = $1`, w.mediumID); err != nil {
		t.Fatalf("clear ladder: %v", err)
	}
	for i := 0; i < 2; i++ {
		hold, err := w.service().RelayQuotaFailure(ctx, w.task(t))
		if err != nil || !hold {
			t.Fatalf("relay %d hold=%v err=%v", i, hold, err)
		}
	}
	var status string
	var asks, inbox int
	if err := w.pool.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1`, w.issueID).Scan(&status); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if status != "in_review" {
		t.Fatalf("status = %s, want in_review", status)
	}
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM agent_ask WHERE issue_id = $1 AND status = 'open'`, w.issueID).Scan(&asks); err != nil {
		t.Fatalf("asks: %v", err)
	}
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM inbox_item WHERE issue_id = $1 AND recipient_id = $2 AND type = $3`, w.issueID, w.userID, InboxTypeNeedsYou).Scan(&inbox); err != nil {
		t.Fatalf("inbox: %v", err)
	}
	if asks != 1 || inbox != 1 {
		t.Fatalf("asks=%d inbox=%d, want exactly one of each", asks, inbox)
	}
	has, err := w.service().Queries.HasOpenReviewStuckAsk(ctx, w.task(t).IssueID)
	if err != nil || !has {
		t.Fatalf("open review-stuck ask = %v err=%v", has, err)
	}
}
