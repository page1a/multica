package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
)

// DENE-870: a seat that is off is not woken when its clock comes due. Before
// this, a seat turned off for a spent account was @-mentioned every minute,
// failed again, and was blocked again with a fresh clock.
func TestPatrolDoesNotWakeADisabledSeat(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	off := createHandlerTestAgent(t, "孙悟天", nil)
	reviewer := createHandlerTestAgent(t, "孙悟空", nil)
	cover := createHandlerTestAgent(t, "特兰克斯", nil)
	if _, err := testPool.Exec(ctx, `UPDATE agent SET routing_tier = 'strong', work_enabled = (id <> $1) WHERE id = ANY($2::uuid[])`,
		off, []string{off, reviewer, cover}); err != nil {
		t.Fatal(err)
	}

	due := func(title string) string {
		t.Helper()
		issue := createIssueHTTP(t, title, "blocked")
		setIssueAssigneeDirect(t, issue.ID, "agent", off)
		if _, err := testPool.Exec(ctx, `UPDATE issue SET reviewer_type = 'agent', reviewer_id = $2 WHERE id = $1`, issue.ID, reviewer); err != nil {
			t.Fatal(err)
		}
		setIssueMetadataString(t, issue.ID, blockwait.KeyWakeAt, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
		return issue.ID
	}
	patrol := func(id string) {
		t.Helper()
		loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		testHandler.patrolOne(ctx, loaded)
	}

	// Another house takes it, never the ticket's own reviewer.
	moved := due("patrol off seat relays")
	patrol(moved)
	if got := countPendingTasksForAgent(t, moved, off); got != 0 {
		t.Fatalf("disabled seat got %d runs", got)
	}
	if got := countPendingTasksForAgent(t, moved, reviewer); got != 0 {
		t.Fatalf("reviewer seat got %d runs, executor and reviewer must differ", got)
	}
	if got := countPendingTasksForAgent(t, moved, cover); got != 1 {
		t.Fatalf("cover seat runs = %d, want 1", got)
	}
	var assignee string
	if err := testPool.QueryRow(ctx, `SELECT assignee_id::text FROM issue WHERE id = $1`, moved).Scan(&assignee); err != nil {
		t.Fatal(err)
	}
	if assignee != cover {
		t.Fatalf("assignee = %s, want 特兰克斯 %s", assignee, cover)
	}
	body, _, _, _ := systemCommentOn(t, moved)
	if !strings.Contains(body, "已停用") || !strings.Contains(body, "特兰克斯") {
		t.Fatalf("relay comment = %s", body)
	}

	// Nobody can take it: a plain comment, nobody dispatched, and the clock
	// is spent so the next patrol stays quiet.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET work_enabled = false WHERE id = $1`, cover); err != nil {
		t.Fatal(err)
	}
	stuck := due("patrol off seat stuck")
	patrol(stuck)
	for _, id := range []string{off, reviewer, cover} {
		if got := countPendingTasksForAgent(t, stuck, id); got != 0 {
			t.Fatalf("seat %s got %d runs with nobody able to take the ticket", id, got)
		}
	}
	body, _, _, _ = systemCommentOn(t, stuck)
	if !strings.Contains(body, "已停用") || strings.Contains(body, "mention://agent/") {
		t.Fatalf("stuck comment = %s", body)
	}
	loaded, err := testHandler.Queries.GetIssue(ctx, parseUUID(stuck))
	if err != nil {
		t.Fatal(err)
	}
	if testHandler.patrolOne(ctx, loaded) {
		t.Fatal("the patrol woke the stuck ticket a second time")
	}
}
