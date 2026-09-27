package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func planStage(n int32) *int32 { return &n }

func callApplyPlan(t *testing.T, req ApplyPlanRequest) (int, ApplyPlanResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler.ApplyPlan(rec, newRequest(http.MethodPost, "/api/issues/plan-apply", req))
	var resp ApplyPlanResponse
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode plan response: %v", err)
		}
	}
	return rec.Code, resp, rec.Body.String()
}

func callStageAdvance(t *testing.T, parentID string) (int, StageAdvanceResponse) {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+parentID+"/stage-advance", map[string]any{}), "id", parentID)
	rec := httptest.NewRecorder()
	testHandler.AdvanceIssueStage(rec, req)
	var resp StageAdvanceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stage advance response (%d): %v", rec.Code, err)
	}
	return rec.Code, resp
}

func cleanupPlanIssues(t *testing.T, key string) {
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id IN (SELECT id FROM issue WHERE origin_type = 'plan' AND title LIKE $1)`, key+"%")
		testPool.Exec(ctx, `DELETE FROM issue WHERE origin_type = 'plan' AND title LIKE $1`, key+"%")
	})
}

// DENE-864: one apply builds the whole staged tree with executors seated at
// create time — stage 1 in todo, later stages in backlog — and a second apply
// of the same plan creates nothing.
func TestApplyPlanBuildsStagedTreeAndReapplyIsIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentName := "plan-apply-executor"
	agentID := createHandlerTestAgent(t, agentName, []byte("[]"))
	agentType := "agent"
	title := "planidem"
	cleanupPlanIssues(t, title)

	req := ApplyPlanRequest{
		Key:    "test-plan-idempotent",
		Parent: &ApplyPlanNode{Title: title + " parent", Priority: "high"},
		Children: []ApplyPlanNode{
			{Key: "a", Title: title + " stage 1", Stage: planStage(1), AssigneeType: &agentType, AssigneeID: &agentID},
			{Key: "b", Title: title + " stage 2", Stage: planStage(2), AssigneeType: &agentType, AssigneeID: &agentID},
			{Key: "c", Title: title + " stage 3", Stage: planStage(3)},
		},
	}
	code, first, body := callApplyPlan(t, req)
	if code != http.StatusOK {
		t.Fatalf("first apply = %d: %s", code, body)
	}
	if first.Created != 4 || first.Existing != 0 {
		t.Fatalf("first apply created=%d existing=%d, want 4/0", first.Created, first.Existing)
	}
	if first.Parent.Status != issueDraftCoordinatorStatus {
		t.Errorf("parent status = %q, want %q", first.Parent.Status, issueDraftCoordinatorStatus)
	}
	wantStatus := map[string]string{title + " stage 1": "todo", title + " stage 2": "backlog", title + " stage 3": "backlog"}
	for _, c := range first.Children {
		if c.Status != wantStatus[c.Title] {
			t.Errorf("%s status = %q, want %q", c.Title, c.Status, wantStatus[c.Title])
		}
		if c.Title != title+" stage 3" && (c.AssigneeID == nil || *c.AssigneeID != agentID) {
			t.Errorf("%s assignee = %v, want the plan's executor seated at create", c.Title, c.AssigneeID)
		}
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, first.Children[0].ID); n != 1 {
		t.Errorf("stage 1 child has %d queued runs, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, first.Children[1].ID); n != 0 {
		t.Errorf("stage 2 child has %d queued runs, want 0 until advance", n)
	}

	code, second, body := callApplyPlan(t, req)
	if code != http.StatusOK {
		t.Fatalf("second apply = %d: %s", code, body)
	}
	if second.Created != 0 || second.Existing != 4 || second.Parent.ID != first.Parent.ID {
		t.Fatalf("second apply created=%d existing=%d parent=%s, want 0/4 and the same parent %s", second.Created, second.Existing, second.Parent.ID, first.Parent.ID)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE origin_type = 'plan' AND title LIKE $1`, title+"%"); n != 4 {
		t.Fatalf("plan issues after re-apply = %d, want 4", n)
	}

	// A plan that grew one node adds exactly that node.
	req.Children = append(req.Children, ApplyPlanNode{Key: "d", Title: title + " stage 3b", Stage: planStage(3)})
	code, third, body := callApplyPlan(t, req)
	if code != http.StatusOK {
		t.Fatalf("grown apply = %d: %s", code, body)
	}
	if third.Created != 1 || third.Existing != 4 {
		t.Fatalf("grown apply created=%d existing=%d, want 1/4", third.Created, third.Existing)
	}
}

// Under an existing parent the plan is scoped to that parent: re-applying
// adds nothing, the same key under another parent builds its own children.
func TestApplyPlanUnderExistingParentIsScopedToThatParent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	title := "planscoped"
	cleanupPlanIssues(t, title)
	parentA := dbfx.Issue(t, title+" parent A", testutil.Cols{"status": "in_progress"})
	parentB := dbfx.Issue(t, title+" parent B", testutil.Cols{"status": "in_progress"})
	children := []ApplyPlanNode{{Key: "x", Title: title + " child", Stage: planStage(1)}}

	for i, want := range []int{1, 0} {
		code, resp, body := callApplyPlan(t, ApplyPlanRequest{Key: "scoped", ParentIssueID: &parentA, Children: children})
		if code != http.StatusOK || resp.Created != want || resp.Parent.ID != parentA {
			t.Fatalf("apply %d under A = %d created=%d parent=%s (%s), want %d under A", i+1, code, resp.Created, resp.Parent.ID, body, want)
		}
	}
	code, resp, body := callApplyPlan(t, ApplyPlanRequest{Key: "scoped", ParentIssueID: &parentB, Children: children})
	if code != http.StatusOK || resp.Created != 1 || len(resp.Children) != 1 {
		t.Fatalf("apply under B = %d created=%d children=%d (%s), want its own child", code, resp.Created, len(resp.Children), body)
	}
}

func TestApplyPlanRejectsChildWithoutStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	code, _, body := callApplyPlan(t, ApplyPlanRequest{
		Key:      "test-plan-nostage",
		Parent:   &ApplyPlanNode{Title: "plannostage parent"},
		Children: []ApplyPlanNode{{Title: "plannostage child"}},
	})
	if code != http.StatusBadRequest || !strings.Contains(body, "stage") {
		t.Fatalf("apply without stage = %d %s, want 400 naming the stage", code, body)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE title LIKE 'plannostage%'`); n != 0 {
		t.Fatalf("a refused plan wrote %d issues", n)
	}
}

// DENE-864: advance refuses while the running stage has open work and names
// the ticket, then promotes the next stage's backlog once it is terminal.
func TestStageAdvanceRefusesUntilStageTerminalThenPromotes(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "stage-advance-executor", []byte("[]"))
	parent := dbfx.Issue(t, "advance parent", testutil.Cols{"status": "in_progress"})
	dbfx.Issue(t, "advance s1 done", testutil.Cols{"status": "done", "parent_issue_id": parent, "stage": 1})
	s1open := dbfx.Issue(t, "advance s1 open", testutil.Cols{"status": "in_progress", "parent_issue_id": parent, "stage": 1})
	s2 := dbfx.Issue(t, "advance s2", testutil.Cols{
		"status": "backlog", "parent_issue_id": parent, "stage": 2,
		"assignee_type": "agent", "assignee_id": agentID,
	})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, s2)
	})

	code, refused := callStageAdvance(t, parent)
	if code != http.StatusConflict || refused.Advanced {
		t.Fatalf("advance with open stage 1 = %d %+v, want 409 not advanced", code, refused)
	}
	if len(refused.Pending) != 1 || refused.Pending[0].ID != s1open || !strings.Contains(refused.Message, "in_progress") {
		t.Fatalf("refusal = %+v, want it to name the open stage-1 ticket and its status", refused)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, s2).Scan(&status)
	if status != "backlog" {
		t.Fatalf("refused advance moved stage 2 to %q", status)
	}

	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, s1open)
	code, advanced := callStageAdvance(t, parent)
	if code != http.StatusOK || !advanced.Advanced || advanced.Stage != 2 {
		t.Fatalf("advance after stage 1 done = %d %+v, want stage 2 advanced", code, advanced)
	}
	if len(advanced.Promoted) != 1 || advanced.Promoted[0].ID != s2 || !advanced.Promoted[0].RunStarted {
		t.Fatalf("promoted = %+v, want s2 with its run started", advanced.Promoted)
	}
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, s2).Scan(&status)
	if status != "todo" {
		t.Fatalf("stage 2 status after advance = %q, want todo", status)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, s2, agentID); n != 1 {
		t.Fatalf("stage 2 executor has %d runs, want 1", n)
	}

	// Stage 2 is now running: a second advance refuses and names it.
	code, again := callStageAdvance(t, parent)
	if code != http.StatusConflict || len(again.Pending) != 1 || again.Pending[0].ID != s2 {
		t.Fatalf("second advance = %d %+v, want 409 naming the running stage-2 ticket", code, again)
	}
}

type testStageChild struct {
	stage  int32
	status string
}

func testStageChildren(spec []testStageChild) ([]db.Issue, resolvedChildStatuses) {
	issues := make([]db.Issue, 0, len(spec))
	statuses := resolvedChildStatuses{}
	for i, c := range spec {
		issue := db.Issue{
			ID:     pgtype.UUID{Bytes: [16]byte{byte(i + 1)}, Valid: true},
			Stage:  pgtype.Int4{Int32: c.stage, Valid: true},
			Status: c.status,
		}
		issues = append(issues, issue)
		statuses[issue.ID] = c.status
	}
	return issues, statuses
}

func TestPlanStageAdvancePicksLowestOpenStage(t *testing.T) {
	issues, statuses := testStageChildren([]testStageChild{{1, "done"}, {2, "backlog"}, {2, "todo"}, {3, "backlog"}})
	stage, promote, pending := planStageAdvance(issues, statuses)
	if stage != 2 || len(promote) != 1 || promote[0].ID != issues[1].ID || len(pending) != 1 || pending[0].ID != issues[2].ID {
		t.Fatalf("stage=%d promote=%d pending=%d, want stage 2 promoting the held backlog item and reporting the running one", stage, len(promote), len(pending))
	}
	done, doneStatuses := testStageChildren([]testStageChild{{1, "done"}, {2, "cancelled"}})
	if stage, _, _ := planStageAdvance(done, doneStatuses); stage != 0 {
		t.Fatalf("all-terminal stage = %d, want 0", stage)
	}
}
