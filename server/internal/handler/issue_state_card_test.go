package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func getIssueContextHTTP(t *testing.T, issueID, agentID, taskID string) IssueContextResponse {
	t.Helper()
	req := newRequest(http.MethodGet, "/api/issues/"+issueID+"/context", nil)
	req = withURLParam(req, "id", issueID)
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
		if taskID != "" {
			req.Header.Set("X-Task-ID", taskID)
		}
	}
	rec := httptest.NewRecorder()
	testHandler.GetIssueContext(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("context status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp IssueContextResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode context: %v", err)
	}
	return resp
}

func insertStateCardComment(t *testing.T, issueID, parentID, authorType, authorID, content string, at time.Time) string {
	t.Helper()
	var parent any
	if parentID != "" {
		parent = parentID
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, parent_id, author_type, author_id, content, type, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'comment', $7, $7)
		RETURNING id
	`, testWorkspaceID, issueID, parent, authorType, authorID, content, at).Scan(&id); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	return id
}

func setTaskStartedAt(t *testing.T, taskID string, at time.Time) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE agent_task_queue SET started_at = $2 WHERE id = $1`, taskID, at); err != nil {
		t.Fatalf("set started_at: %v", err)
	}
}

func cleanupDecisions(t *testing.T, issueID string) {
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_decision WHERE issue_id = $1`, issueID)
	})
}

// A close that carries --decision is read back by a different agent: the
// decisions, the close conclusion and the close summary all land on the card.
func TestStateCardDerivesFromClose(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "state card close", "in_progress")
	cleanupDecisions(t, issue.ID)
	agentID := handlerTestAgentID(t)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":        "done",
		"evidence":       "状态卡服务端做完了\n\n细节见下",
		"no_code_reason": "测试",
		"decisions":      []string{"拍板单独建表", " 拍板单独建表 ", "手机端先做网页"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("close status = %d: %s", w.Code, w.Body.String())
	}

	reader := createHandlerTestAgent(t, "state card reader "+time.Now().Format(time.RFC3339Nano), []byte("[]"))
	readerTask := insertIssueTaskWithStatus(t, reader, issue.ID, "running")
	card := getIssueContextHTTP(t, issue.ID, reader, readerTask)
	if len(card.Decisions) != 2 || card.Decisions[0].Text != "拍板单独建表" || card.Decisions[1].Text != "手机端先做网页" {
		t.Fatalf("decisions = %+v", card.Decisions)
	}
	if card.Decisions[0].Source != "close" || card.Decisions[0].AuthorType != "agent" || card.Decisions[0].AuthorID != agentID {
		t.Fatalf("decision provenance = %+v", card.Decisions[0])
	}
	if !card.Now.Closed || card.Now.Conclusion != "delivered" || card.Now.Status != "done" {
		t.Fatalf("now = %+v", card.Now)
	}
	if card.Baton == nil || card.Baton.Kind != "close" || !strings.Contains(card.Baton.Summary, "状态卡服务端做完了") {
		t.Fatalf("baton = %+v", card.Baton)
	}
	for _, want := range []string{"已拍板", "拍板单独建表", "现在在哪", "上一棒交代"} {
		if !strings.Contains(card.Text, want) {
			t.Fatalf("text misses %q:\n%s", want, card.Text)
		}
	}
}

// A handoff written after the close becomes the baton; its decisions join
// the close's.
func TestStateCardHandoffSummaryWinsOverOlderClose(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "state card handoff", "in_progress")
	cleanupDecisions(t, issue.ID)
	agentID := handlerTestAgentID(t)
	setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
	taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
		"outcome":   "in_progress",
		"evidence":  "做了一半",
		"wake_at":   time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		"decisions": []string{"先做服务端"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("close status = %d: %s", w.Code, w.Body.String())
	}
	// Let the handoff land strictly after close.at (second precision).
	time.Sleep(1100 * time.Millisecond)

	next := createHandlerTestAgent(t, "state card next "+time.Now().Format(time.RFC3339Nano), []byte("[]"))
	nextName := agentNameDirect(t, next)
	req := newRequest(http.MethodPost, "/api/issues/"+issue.ID+"/handoff", map[string]any{
		"to":        nextName,
		"summary":   "服务端已合，剩前端和 CLI",
		"decisions": []string{"界面用行式布局"},
	})
	req = withURLParam(req, "id", issue.ID)
	rec := httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handoff status = %d: %s", rec.Code, rec.Body.String())
	}

	card := getIssueContextHTTP(t, issue.ID, "", "")
	if card.Baton == nil || card.Baton.Kind != "handoff" || card.Baton.Summary != "服务端已合，剩前端和 CLI" {
		t.Fatalf("baton = %+v", card.Baton)
	}
	if len(card.Decisions) != 2 || card.Decisions[1].Source != "handoff" {
		t.Fatalf("decisions = %+v", card.Decisions)
	}

	// An over-long summary is refused before anything is written.
	req = newRequest(http.MethodPost, "/api/issues/"+issue.ID+"/handoff", map[string]any{
		"to": nextName, "summary": strings.Repeat("字", 301),
	})
	req = withURLParam(req, "id", issue.ID)
	rec = httptest.NewRecorder()
	testHandler.HandoffIssue(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long summary status = %d: %s", rec.Code, rec.Body.String())
	}
}

func agentNameDirect(t *testing.T, agentID string) string {
	t.Helper()
	var name string
	if err := testPool.QueryRow(context.Background(), `SELECT name FROM agent WHERE id = $1`, agentID).Scan(&name); err != nil {
		t.Fatalf("agent name: %v", err)
	}
	return name
}

// The change list belongs to the caller: an agent counts from its previous
// run and never sees its own comments; a person counts from their last word.
func TestStateCardChangesArePerCaller(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "state card changes", "in_progress")
	agentID := handlerTestAgentID(t)
	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)

	prevTask := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
	setTaskStartedAt(t, prevTask, base)
	currentTask := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	old := insertStateCardComment(t, issue.ID, "", "member", testUserID, "老讨论", base.Add(-time.Hour))
	q := insertStateCardComment(t, issue.ID, "", "member", testUserID, "## 能不能先做手机\n正文", base.Add(10*time.Minute))
	insertStateCardComment(t, issue.ID, q, "agent", agentID, "我来回答", base.Add(20*time.Minute))
	own := insertStateCardComment(t, issue.ID, "", "agent", agentID, "我自己的进展", base.Add(30*time.Minute))
	insertStateCardComment(t, issue.ID, old, "member", testUserID, "老讨论续上", base.Add(40*time.Minute))

	agentCard := getIssueContextHTTP(t, issue.ID, agentID, currentTask)
	if agentCard.Changes.Anchor != "last_run" {
		t.Fatalf("agent anchor = %+v", agentCard.Changes)
	}
	got := map[string]int{}
	for _, th := range agentCard.Changes.Threads {
		got[th.ThreadID] = th.NewCount
	}
	if len(got) != 2 || got[q] != 1 || got[old] != 1 {
		t.Fatalf("agent threads = %+v", agentCard.Changes.Threads)
	}
	if _, ok := got[own]; ok {
		t.Fatal("an agent's own comment is not a change for it")
	}
	if agentCard.Changes.Threads[0].ThreadID != old {
		t.Fatalf("newest activity first: %+v", agentCard.Changes.Threads)
	}
	for _, th := range agentCard.Changes.Threads {
		if th.ThreadID == q && th.Title != "能不能先做手机" {
			t.Fatalf("thread title = %q", th.Title)
		}
	}

	// The member's last comment is at +40m; only the agent's later word counts.
	later := insertStateCardComment(t, issue.ID, "", "agent", agentID, "收到", base.Add(50*time.Minute))
	memberCard := getIssueContextHTTP(t, issue.ID, "", "")
	if memberCard.Changes.Anchor != "last_comment" || len(memberCard.Changes.Threads) != 1 || memberCard.Changes.Threads[0].ThreadID != later {
		t.Fatalf("member changes = %+v", memberCard.Changes)
	}
}

// People edit any decision; an agent only its own.
func TestStateCardDecisionCRUD(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "state card decisions", "in_progress")
	cleanupDecisions(t, issue.ID)
	agentID := handlerTestAgentID(t)
	agentTask := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")

	call := func(method, path, decisionID, agent string, body any, fn func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		req := newRequest(method, path, body)
		req = withURLParams(req, "id", issue.ID)
		if decisionID != "" {
			req = withURLParams(req, "decisionId", decisionID)
		}
		if agent != "" {
			req.Header.Set("X-Agent-ID", agent)
			req.Header.Set("X-Task-ID", agentTask)
		}
		rec := httptest.NewRecorder()
		fn(rec, req)
		return rec
	}
	base := "/api/issues/" + issue.ID + "/decisions"

	rec := call(http.MethodPost, base, "", "", map[string]string{"text": "用新表"}, testHandler.CreateIssueDecision)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var memberDecision struct{ ID string }
	json.NewDecoder(rec.Body).Decode(&memberDecision)

	rec = call(http.MethodPost, base, "", "", map[string]string{"text": "  "}, testHandler.CreateIssueDecision)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty decision = %d", rec.Code)
	}

	rec = call(http.MethodPatch, base+"/"+memberDecision.ID, memberDecision.ID, agentID, map[string]string{"text": "改掉"}, testHandler.UpdateIssueDecision)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("agent editing a person's decision = %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(http.MethodPost, base, "", agentID, map[string]string{"text": "智能体的拍板"}, testHandler.CreateIssueDecision)
	if rec.Code != http.StatusCreated {
		t.Fatalf("agent create = %d: %s", rec.Code, rec.Body.String())
	}
	var agentDecision struct{ ID string }
	json.NewDecoder(rec.Body).Decode(&agentDecision)
	rec = call(http.MethodPatch, base+"/"+agentDecision.ID, agentDecision.ID, agentID, map[string]string{"text": "智能体改自己的"}, testHandler.UpdateIssueDecision)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent editing its own = %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(http.MethodPatch, base+"/"+memberDecision.ID, memberDecision.ID, "", map[string]string{"text": "人改成这样"}, testHandler.UpdateIssueDecision)
	if rec.Code != http.StatusOK {
		t.Fatalf("person edit = %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(http.MethodDelete, base+"/"+agentDecision.ID, agentDecision.ID, "", nil, testHandler.DeleteIssueDecision)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("person delete = %d: %s", rec.Code, rec.Body.String())
	}

	card := getIssueContextHTTP(t, issue.ID, "", "")
	if len(card.Decisions) != 1 || card.Decisions[0].Text != "人改成这样" || card.Decisions[0].Source != "manual" {
		t.Fatalf("decisions = %+v", card.Decisions)
	}
}
