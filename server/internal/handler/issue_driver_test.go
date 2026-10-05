package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// quietIssue ages an issue past QuietAfter and forgets the patrol's last
// visit, so the next patrolOne judges it afresh.
func quietIssue(t *testing.T, issueID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE issue SET last_activity_at = now() - interval '2 hours', updated_at = now() - interval '2 hours',
		       metadata = metadata - 'block.patrol_at'
		WHERE id = $1`, issueID); err != nil {
		t.Fatalf("quiet issue: %v", err)
	}
}

// failIssueRuns ends every open run on an issue the way a failed run does.
func failIssueRuns(t *testing.T, issueID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue SET status = 'failed', completed_at = now()
		WHERE issue_id = $1 AND status IN ('queued', 'dispatched', 'running')`, issueID); err != nil {
		t.Fatalf("fail runs: %v", err)
	}
}

// ageRevive moves the last rerun an hour back, past the grace that keeps the
// patrol from burning the count before the rerun could start.
func ageRevive(t *testing.T, issueID string) {
	t.Helper()
	setIssueMetadataString(t, issueID, blockwait.KeyRevivedAt, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
}

func setParentDirect(t *testing.T, childID, parentID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET parent_issue_id = $2 WHERE id = $1`, childID, parentID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
}

func metaOf(t *testing.T, issueID, key string) string {
	t.Helper()
	var v *string
	if err := testPool.QueryRow(context.Background(), `SELECT metadata->>$2 FROM issue WHERE id = $1`, issueID, key).Scan(&v); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func patrolIssue(t *testing.T, issueID string) bool {
	t.Helper()
	loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issueID))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return testHandler.patrolOne(context.Background(), loaded)
}

// TestPatrolRecoversWaiterWhoseBlockerNobodyDrives is the DENE-1342 scenario:
// a parent, B waiting on A, and A's run failed with nothing queued. The
// patrol reruns A on its own seat twice, then wakes the parent's agent with
// the disposition command, then stops asking.
func TestPatrolRecoversWaiterWhoseBlockerNobodyDrives(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	parent := createIssueHTTP(t, "undriven parent", "in_progress")
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	a := createIssueHTTP(t, "undriven blocker A", "todo")
	b := createIssueHTTP(t, "waiter B", "blocked")
	for _, child := range []IssueResponse{a, b} {
		setParentDirect(t, child.ID, parent.ID)
		setIssueAssigneeDirect(t, child.ID, "agent", agentID)
	}
	setIssueMetadataString(t, b.ID, blockwait.KeyBlockedBy, a.Identifier)
	insertIssueTaskWithStatus(t, agentID, a.ID, "running")
	failIssueRuns(t, a.ID)

	for round := 1; round <= blockwait.ReviveLimit; round++ {
		quietIssue(t, b.ID)
		if !patrolIssue(t, b.ID) {
			t.Fatalf("round %d: the patrol did not act on B", round)
		}
		if got := countPendingTasksForAgent(t, a.ID, agentID); got != 1 {
			t.Fatalf("round %d: runs on A = %d, want 1", round, got)
		}
		if got := metaOf(t, a.ID, blockwait.KeyRevives); got != string(rune('0'+round)) {
			t.Fatalf("round %d: A revives = %q", round, got)
		}
		body, _, _, _ := systemCommentOn(t, a.ID)
		if !strings.Contains(body, "没人在推进") {
			t.Fatalf("round %d: A comment = %s", round, body)
		}
		failIssueRuns(t, a.ID)
		ageRevive(t, a.ID)
	}

	before := countSystemCommentsOn(t, parent.ID)
	quietIssue(t, b.ID)
	if !patrolIssue(t, b.ID) {
		t.Fatal("the patrol did not escalate once the reruns were spent")
	}
	if got := countPendingTasksForAgent(t, a.ID, agentID); got != 0 {
		t.Fatalf("escalation reran A again: runs = %d", got)
	}
	if got := countSystemCommentsOn(t, parent.ID); got != before+1 {
		t.Fatalf("parent comments = %d, want %d", got, before+1)
	}
	body, _, _, _ := systemCommentOn(t, parent.ID)
	if !strings.Contains(body, "multica issue dispose "+a.Identifier) {
		t.Fatalf("parent comment = %s", body)
	}
	if got := countPendingTasksForAgent(t, parent.ID, agentID); got != 1 {
		t.Fatalf("parent runs = %d, want the parent's agent woken", got)
	}
	if metaOf(t, a.ID, blockwait.KeyEscalatedAt) == "" {
		t.Fatal("A carries no escalation stamp")
	}

	quietIssue(t, b.ID)
	if patrolIssue(t, b.ID) {
		t.Fatal("an escalated blocker was escalated again")
	}

	// The parent's agent answers with the one command; A has a driver again.
	w := disposeHTTP(t, a.ID, map[string]any{"action": "rerun"})
	if w.Code != http.StatusOK {
		t.Fatalf("dispose rerun = %d %s", w.Code, w.Body.String())
	}
	if got := countPendingTasksForAgent(t, a.ID, agentID); got != 1 {
		t.Fatalf("rerun runs on A = %d", got)
	}
	if metaOf(t, a.ID, blockwait.KeyRevives) != "" {
		t.Fatal("a disposition must reset the patrol's count")
	}
}

func TestPatrolRerunsUndrivenTodo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := handlerTestAgentID(t)
	issue := createIssueHTTP(t, "undriven todo", "todo")
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	quietIssue(t, issue.ID)

	rows, err := testHandler.Queries.ListBlockPatrolCandidates(ctx, db.ListBlockPatrolCandidatesParams{
		QuietBefore: pgtype.Timestamptz{Time: time.Now().Add(-blockwait.QuietAfter), Valid: true},
		TodoSince:   pgtype.Timestamptz{Time: time.Now().Add(-undrivenTodoHorizon), Valid: true},
		RowLimit:    10000,
	})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	found := false
	for _, row := range rows {
		if uuidToString(row.ID) == issue.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("an undriven todo is not a patrol candidate")
	}

	if !patrolIssue(t, issue.ID) {
		t.Fatal("the patrol did not rerun an undriven todo")
	}
	if got := countPendingTasksForAgent(t, issue.ID, agentID); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}
	// A running seat is a driver: the next pass leaves it alone.
	quietIssue(t, issue.ID)
	if patrolIssue(t, issue.ID) {
		t.Fatal("the patrol acted on a todo that has a run")
	}
}

func disposeHTTP(t *testing.T, issueID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("POST", "/api/issues/"+issueID+"/dispose", body), "id", issueID)
	testHandler.DisposeIssue(w, req)
	return w
}

func TestDisposeIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	parent := createIssueHTTP(t, "dispose parent", "in_progress")
	undriven := func(title string) IssueResponse {
		child := createIssueHTTP(t, title, "todo")
		setParentDirect(t, child.ID, parent.ID)
		setIssueAssigneeDirect(t, child.ID, "agent", agentID)
		return child
	}

	t.Run("refuses a driven issue", func(t *testing.T) {
		child := undriven("dispose driven")
		insertIssueTaskWithStatus(t, agentID, child.ID, "queued")
		w := disposeHTTP(t, child.ID, map[string]any{"action": "rerun"})
		if w.Code != http.StatusConflict {
			t.Fatalf("status = %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("checks the arguments", func(t *testing.T) {
		child := undriven("dispose args")
		for _, body := range []map[string]any{
			{"action": "explode"},
			{"action": "cancel"},
			{"action": "split"},
			{"action": "rerun", "into": []string{"x"}},
		} {
			if w := disposeHTTP(t, child.ID, body); w.Code != http.StatusBadRequest {
				t.Fatalf("%v: status = %d %s", body, w.Code, w.Body.String())
			}
		}
	})

	t.Run("an agent that does not hold the parent is refused", func(t *testing.T) {
		child := undriven("dispose foreign agent")
		other := createIssueHTTP(t, "dispose caller run", "in_progress")
		taskID := insertIssueTaskWithStatus(t, agentID, other.ID, "running")
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/issues/"+child.ID+"/dispose", map[string]any{"action": "rerun"}), "id", child.ID)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		testHandler.DisposeIssue(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("split blocks on the new children", func(t *testing.T) {
		child := undriven("dispose split")
		w := disposeHTTP(t, child.ID, map[string]any{"action": "split", "into": []string{"first half", "second half"}})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d %s", w.Code, w.Body.String())
		}
		var out DisposeIssueResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if len(out.Created) != 2 || out.Status != "blocked" {
			t.Fatalf("split = %+v", out)
		}
		if got := metaOf(t, child.ID, blockwait.KeyBlockedBy); !strings.Contains(got, out.Created[0]) || !strings.Contains(got, out.Created[1]) {
			t.Fatalf("blocked_by = %q", got)
		}
		if out.Driver == nil || out.Driver.Kind == blockwait.DriverNone {
			t.Fatalf("split left no driver: %+v", out.Driver)
		}
	})

	t.Run("reroute empties the seat", func(t *testing.T) {
		child := undriven("dispose reroute")
		w := disposeHTTP(t, child.ID, map[string]any{"action": "reroute"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d %s", w.Code, w.Body.String())
		}
		loaded, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(child.ID))
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if loaded.AssigneeID.Valid && uuidToString(loaded.AssigneeID) == agentID && countPendingTasksForAgent(t, child.ID, agentID) == 0 {
			t.Fatal("reroute kept the old seat without starting anybody")
		}
	})

	t.Run("cancel needs and keeps a reason", func(t *testing.T) {
		child := undriven("dispose cancel")
		w := disposeHTTP(t, child.ID, map[string]any{"action": "cancel", "reason": "需求取消"})
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d %s", w.Code, w.Body.String())
		}
		var out DisposeIssueResponse
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out.Status != "cancelled" || out.Driver != nil {
			t.Fatalf("cancel = %+v", out)
		}
		body, _, _, _ := systemCommentOn(t, child.ID)
		if !strings.Contains(body, "需求取消") {
			t.Fatalf("cancel comment = %s", body)
		}
	})
}

func TestIssueResponsesCarryDriver(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := handlerTestAgentID(t)
	parent := createIssueHTTP(t, "driver parent", "in_progress")
	setIssueAssigneeDirect(t, parent.ID, "agent", agentID)
	child := createIssueHTTP(t, "driver child", "todo")
	setParentDirect(t, child.ID, parent.ID)
	setIssueAssigneeDirect(t, child.ID, "agent", agentID)

	w := httptest.NewRecorder()
	testHandler.GetIssue(w, withURLParam(newRequest("GET", "/api/issues/"+child.ID, nil), "id", child.ID))
	var got IssueResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Driver == nil || got.Driver.Kind != blockwait.DriverNone || got.Driver.Reason == "" {
		t.Fatalf("child driver = %+v", got.Driver)
	}

	w = httptest.NewRecorder()
	testHandler.ListChildIssues(w, withURLParam(newRequest("GET", "/api/issues/"+parent.ID+"/children", nil), "id", parent.ID))
	var list struct {
		Issues []IssueResponse `json:"issues"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Issues) != 1 || list.Issues[0].Driver == nil || list.Issues[0].Driver.Kind != blockwait.DriverNone {
		t.Fatalf("children = %+v", list.Issues)
	}

	w = httptest.NewRecorder()
	testHandler.GetIssue(w, withURLParam(newRequest("GET", "/api/issues/"+parent.ID, nil), "id", parent.ID))
	got = IssueResponse{}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Driver == nil || got.Driver.Kind != blockwait.DriverWait {
		t.Fatalf("parent driver = %+v, want waiting on its child", got.Driver)
	}
}
