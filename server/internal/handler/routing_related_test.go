package handler

import (
	"context"
	"sort"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
)

// TestRoutingStoreListsRelatedTickets checks the snapshot half of 接着做
// (DENE-1202): the store lists the previous stage, the parent, and the same
// batch — agent-held only, never the ticket itself, never a stage two back.
func TestRoutingStoreListsRelatedTickets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	prevSeat := createHandlerTestAgent(t, "Related Prev Seat", []byte("[]"))
	parentSeat := createHandlerTestAgent(t, "Related Parent Seat", []byte("[]"))
	batchSeat := createHandlerTestAgent(t, "Related Batch Seat", []byte("[]"))
	farSeat := createHandlerTestAgent(t, "Related Far Seat", []byte("[]"))

	var batch string
	dbfx.QueryRow(t, `SELECT gen_random_uuid()::text`).Scan(&batch)

	parent := heldIssue(t, "related parent", "in_progress", parentSeat)
	stage1 := heldIssue(t, "related stage 1", "done", farSeat)
	stage2 := heldIssue(t, "related stage 2", "done", prevSeat)
	stage2human := originIssue(t, "related stage 2 by a person")
	dbfx.Exec(t, `UPDATE issue SET assignee_type = 'member', assignee_id = $2 WHERE id = $1`, stage2human, testUserID)
	me := originIssue(t, "related stage 3")
	sibling := heldIssue(t, "related batch sibling", "todo", batchSeat)
	for id, stage := range map[string]int{stage1: 1, stage2: 2, stage2human: 2, me: 3} {
		dbfx.Exec(t, `UPDATE issue SET parent_issue_id = $2, stage = $3 WHERE id = $1`, id, parent, stage)
	}
	for _, id := range []string{me, sibling} {
		dbfx.Exec(t, `UPDATE issue SET origin_type = 'agent_create', origin_id = $2 WHERE id = $1`, id, batch)
	}

	issue, err := testHandler.RoutingStore().Issue(context.Background(), testWorkspaceID, me)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got := map[routing.Relation][]string{}
	for _, r := range issue.Related {
		if r.Identifier == "" {
			t.Fatalf("related ticket without identifier: %+v", r)
		}
		got[r.Relation] = append(got[r.Relation], r.ExecutorID)
	}
	for _, ids := range got {
		sort.Strings(ids)
	}
	want := map[routing.Relation][]string{
		routing.RelationPreviousStage: {prevSeat},
		routing.RelationParent:        {parentSeat},
		routing.RelationSameBatch:     {batchSeat},
	}
	for rel, ids := range want {
		if len(got[rel]) != len(ids) || got[rel][0] != ids[0] {
			t.Fatalf("%s = %v, want %v (all: %+v)", rel, got[rel], ids, issue.Related)
		}
	}
	if len(issue.Related) != 3 {
		t.Fatalf("related = %+v, want exactly three", issue.Related)
	}
}
