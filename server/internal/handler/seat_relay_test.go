package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestStageAdvancePromotesWhenParentSeatIsOff(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	fx := newChildDoneFixture(t, "in_progress")
	off := createHandlerTestAgent(t, "席位停用父票-孙悟饭", nil)
	worker := createHandlerTestAgent(t, "席位停用子票-布尔玛", nil)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET work_enabled = false WHERE id = $1`, off); err != nil {
		t.Fatal(err)
	}
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", off)
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET stage = 1 WHERE id = $1`, fx.child.ID); err != nil {
		t.Fatal(err)
	}

	w := testutil.Call(t, testHandler.CreateIssue, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "stage two clear",
		"status":          "backlog",
		"parent_issue_id": fx.parent.ID,
		"assignee_type":   "agent",
		"assignee_id":     worker,
	})).Want(http.StatusCreated)
	var stageTwo IssueResponse
	w.JSON(&stageTwo)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, fx.parent.ID, stageTwo.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, stageTwo.ID)
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET stage = 2, status = 'backlog' WHERE id = $1`, stageTwo.ID); err != nil {
		t.Fatal(err)
	}

	updateChildStatus(t, fx.child.ID, "done")

	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, stageTwo.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "todo" {
		t.Fatalf("stage 2 status = %q, want todo", status)
	}
	var queued int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, stageTwo.ID, worker).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("stage 2 queued tasks = %d, want 1", queued)
	}
	var parentTasks int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, fx.parent.ID, off).Scan(&parentTasks); err != nil {
		t.Fatal(err)
	}
	if parentTasks != 0 {
		t.Fatalf("disabled parent was woken %d times", parentTasks)
	}
	content := parentSystemCommentContent(t, fx.parent.ID)
	if !strings.Contains(content, "提到待办") || !strings.Contains(content, "已停用") {
		t.Fatalf("comment = %s", content)
	}
}

func TestStageAdvancePromotesClearNextStageWithoutWakingParent(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	fx := newChildDoneFixture(t, "in_progress")
	parentSeat := createHandlerTestAgent(t, "阶段推进父票-孙悟天", nil)
	worker := createHandlerTestAgent(t, "阶段推进子票-布尔玛", nil)
	setIssueAssigneeDirect(t, fx.parent.ID, "agent", parentSeat)
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET stage = 1 WHERE id = $1`, fx.child.ID); err != nil {
		t.Fatal(err)
	}

	w := testutil.Call(t, testHandler.CreateIssue, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           "stage two no extra dependency",
		"description":     "没有额外依赖",
		"status":          "backlog",
		"parent_issue_id": fx.parent.ID,
		"assignee_type":   "agent",
		"assignee_id":     worker,
	})).Want(http.StatusCreated)
	var stageTwo IssueResponse
	w.JSON(&stageTwo)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1 OR issue_id = $2`, fx.parent.ID, stageTwo.ID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, stageTwo.ID)
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET stage = 2, status = 'backlog' WHERE id = $1`, stageTwo.ID); err != nil {
		t.Fatal(err)
	}

	updateChildStatus(t, fx.child.ID, "done")

	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, stageTwo.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "todo" {
		t.Fatalf("stage 2 status = %q, want todo", status)
	}
	if got := countPendingTasksForAgent(t, stageTwo.ID, worker); got != 1 {
		t.Fatalf("stage 2 pending runs = %d, want 1", got)
	}
	if got := countPendingTasksForAgent(t, fx.parent.ID, parentSeat); got != 0 {
		t.Fatalf("parent seat was woken %d times, want 0", got)
	}
	content := parentSystemCommentContent(t, fx.parent.ID)
	if !strings.Contains(content, "提到待办") {
		t.Fatalf("comment = %s", content)
	}
}

func TestMentionOfDisabledSeatRelaysToAnotherFamily(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	off := createHandlerTestAgent(t, "孙悟饭", nil)
	cover := createHandlerTestAgent(t, "布尔玛", nil)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET work_enabled = false, routing_tier = 'strongest' WHERE id = $1
	`, off); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET routing_tier = 'strongest', work_enabled = true WHERE id = $1`, cover); err != nil {
		t.Fatal(err)
	}

	var number int
	if err := testPool.QueryRow(context.Background(), `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter
	`, testWorkspaceID).Scan(&number); err != nil {
		t.Fatal(err)
	}
	issueID := dbfxInsertIssue(t, number)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM comment WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	req := newRequest("POST", "/api/issues/"+issueID+"/comments", map[string]any{
		"content": fmt.Sprintf("[@孙悟饭](mention://agent/%s) 看一下", off),
	})
	req = withURLParam(req, "id", issueID)
	testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated)

	var queuedOff, queuedCover int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, off).Scan(&queuedOff); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, issueID, cover).Scan(&queuedCover); err != nil {
		t.Fatal(err)
	}
	if queuedOff != 0 || queuedCover != 1 {
		t.Fatalf("queued off=%d cover=%d, want 0 and 1", queuedOff, queuedCover)
	}
	var bodies string
	if err := testPool.QueryRow(context.Background(), `SELECT coalesce(string_agg(content, '\n'), '') FROM comment WHERE issue_id = $1`, issueID).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodies, "已停用") || !strings.Contains(bodies, "布尔玛") {
		t.Fatalf("relay comment = %s", bodies)
	}
}

func dbfxInsertIssue(t *testing.T, number int) string {
	t.Helper()
	return dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"creator_type": "member",
		"creator_id":   testUserID,
		"title":        "disabled mention relay",
		"number":       number,
	})
}
