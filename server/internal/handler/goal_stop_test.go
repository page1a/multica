package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

// goalStopFixture creates a goal-mode issue with a drafted line, plus an agent
// running a task on it whose human originator is the test user.
func goalStopFixture(t *testing.T, title string) (issueID, agentID, taskID string) {
	t.Helper()
	createW := httptest.NewRecorder()
	testHandler.CreateIssue(createW, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": title, "status": "todo", "priority": "none", "goal_mode": true,
	}))
	if createW.Code != http.StatusCreated {
		t.Fatalf("goal issue create status = %d: %s", createW.Code, createW.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("decode created issue: %v body=%s", err, createW.Body.String())
	}
	draftW := httptest.NewRecorder()
	testHandler.CreateIssueGoal(draftW, withURLParam(newRequest(http.MethodPost, "/api/issues/"+created.ID+"/goal", map[string]any{
		"checks": []map[string]any{{"description": "Line holds", "method": "test"}},
	}), "id", created.ID))
	if draftW.Code != http.StatusCreated {
		t.Fatalf("draft status = %d: %s", draftW.Code, draftW.Body.String())
	}
	runtimeID := handlerTestRuntimeID(t)
	agentID = dbfx.Agent(t, title+" agent", runtimeID)
	taskID = seedTaskOnIssue(t, agentID, created.ID, runtimeID, testUserID)
	return created.ID, agentID, taskID
}

func goalCallAs(t *testing.T, fn http.HandlerFunc, method, path, issueID, agentID, taskID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := withURLParam(newRequest(method, path, body), "id", issueID)
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func enqueueGoalErr(t *testing.T, issueID string) error {
	t.Helper()
	_, err := testHandler.TaskService.EnqueueTaskForIssue(context.Background(), issueForGuardTest(t, issueID))
	return err
}

// DENE-1583: an agent may stop a draft goal on a person's word; the record
// names the agent, the person it acted for, and the words, and the draft no
// longer gates dispatch.
func TestFinishIssueGoal_AgentStopsDraft(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, agentID, taskID := goalStopFixture(t, "Agent stops draft goal")
	finish := func(body any) *httptest.ResponseRecorder {
		return goalCallAs(t, testHandler.FinishIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/finish", issueID, agentID, taskID, body)
	}

	if w := goalCallAs(t, testHandler.ConfirmIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/confirm", issueID, agentID, taskID, nil); w.Code != http.StatusForbidden {
		t.Fatalf("agent confirm status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := goalCallAs(t, testHandler.CreateIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal", issueID, agentID, taskID, map[string]any{
		"checks": []map[string]any{{"description": "Lower bar"}},
	}); w.Code != http.StatusConflict {
		t.Fatalf("agent redraft status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if w := finish(map[string]any{"status": "achieved"}); w.Code != http.StatusForbidden {
		t.Fatalf("agent achieved on draft status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := finish(map[string]any{"status": "stopped"}); w.Code != http.StatusBadRequest {
		t.Fatalf("agent stop without reason status = %d, want 400: %s", w.Code, w.Body.String())
	}

	dbfx.Exec(t, `UPDATE issue SET assignee_type = 'agent', assignee_id = $1 WHERE id = $2`, agentID, issueID)
	if err := enqueueGoalErr(t, issueID); !errors.Is(err, service.ErrGoalDraftNotConfirmed) {
		t.Fatalf("enqueue before stop err = %v, want ErrGoalDraftNotConfirmed", err)
	}

	w := finish(map[string]any{"status": "stopped", "reason": "把这张票的目标模式去掉"})
	if w.Code != http.StatusOK {
		t.Fatalf("agent stop status = %d: %s", w.Code, w.Body.String())
	}
	var goal GoalResponse
	if err := json.Unmarshal(w.Body.Bytes(), &goal); err != nil {
		t.Fatalf("decode goal: %v", err)
	}
	if goal.Status != "stopped" || goal.StoppedBy == nil || goal.StoppedBy.Type != "agent" || goal.StoppedBy.ID == nil || *goal.StoppedBy.ID != agentID {
		t.Fatalf("stop record = %+v / %+v, want stopped by agent %s", goal, goal.StoppedBy, agentID)
	}
	if goal.StoppedOnBehalfOf == nil || *goal.StoppedOnBehalfOf != testUserID || goal.StopReason != "把这张票的目标模式去掉" || goal.StoppedAt == nil {
		t.Fatalf("stop record on_behalf=%v reason=%q at=%v, want test user, the quote, a time", goal.StoppedOnBehalfOf, goal.StopReason, goal.StoppedAt)
	}
	if err := enqueueGoalErr(t, issueID); errors.Is(err, service.ErrGoalDraftNotConfirmed) {
		t.Fatalf("enqueue after stop still gated by the goal: %v", err)
	}
	if w := finish(map[string]any{"status": "stopped", "reason": "again"}); w.Code != http.StatusConflict {
		t.Fatalf("second stop of a deliberately stopped goal status = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// An agent may stop a locked, running goal too, while confirm, budget and
// achieved stay human-only.
func TestFinishIssueGoal_AgentStopsActiveButCannotAchieve(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, agentID, taskID := goalStopFixture(t, "Agent stops active goal")
	if w := goalCallAs(t, testHandler.ConfirmIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/confirm", issueID, "", "", nil); w.Code != http.StatusOK {
		t.Fatalf("human confirm status = %d: %s", w.Code, w.Body.String())
	}
	finish := func(body any) *httptest.ResponseRecorder {
		return goalCallAs(t, testHandler.FinishIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/finish", issueID, agentID, taskID, body)
	}
	if w := finish(map[string]any{"status": "achieved"}); w.Code != http.StatusForbidden {
		t.Fatalf("agent achieved status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := goalCallAs(t, testHandler.AppendIssueGoalBudget, http.MethodPost, "/api/issues/"+issueID+"/goal/budget", issueID, agentID, taskID, map[string]any{"run_limit": 5}); w.Code != http.StatusForbidden {
		t.Fatalf("agent budget on locked goal status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := goalCallAs(t, testHandler.CreateIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal", issueID, agentID, taskID, map[string]any{
		"checks": []map[string]any{{"description": "Lower bar"}},
	}); w.Code != http.StatusConflict {
		t.Fatalf("agent rewrite of locked line status = %d, want 409: %s", w.Code, w.Body.String())
	}
	w := finish(map[string]any{"status": "stopped", "reason": "Kun: 目标模式先关掉"})
	if w.Code != http.StatusOK {
		t.Fatalf("agent stop active status = %d: %s", w.Code, w.Body.String())
	}
	var goal GoalResponse
	_ = json.Unmarshal(w.Body.Bytes(), &goal)
	if goal.Status != "stopped" || goal.StoppedBy == nil || goal.StoppedBy.Type != "agent" {
		t.Fatalf("goal after agent stop = %+v", goal)
	}
	if w := finish(map[string]any{"status": "achieved"}); w.Code != http.StatusForbidden {
		t.Fatalf("agent achieved after stop status = %d, want 403", w.Code)
	}
}

func TestFinishIssueGoal_AgentWithoutActiveHumanOriginatorIsRejected(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, agentID, taskID := goalStopFixture(t, "Agent needs active human originator")
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, taskID)
	w := goalCallAs(t, testHandler.FinishIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/finish", issueID, agentID, taskID, map[string]any{
		"status": "stopped", "reason": "把这张票的目标模式去掉",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent stop without active human originator status = %d, want 403: %s", w.Code, w.Body.String())
	}
}

// A goal the brake paused can be stopped on purpose, which replaces the
// system stop record with the person who turned goal mode off.
func TestFinishIssueGoal_MemberStopsBrakePausedGoal(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID, _, _ := goalStopFixture(t, "Member stops paused goal")
	dbfx.Exec(t, `UPDATE issue_goal SET status = 'stopped', stopped_at = now(), stopped_by_type = 'system', stop_reason = 'budget_exhausted' WHERE issue_id = $1`, issueID)
	w := goalCallAs(t, testHandler.FinishIssueGoal, http.MethodPost, "/api/issues/"+issueID+"/goal/finish", issueID, "", "", map[string]any{"status": "stopped"})
	if w.Code != http.StatusOK {
		t.Fatalf("member stop of paused goal status = %d: %s", w.Code, w.Body.String())
	}
	var goal GoalResponse
	_ = json.Unmarshal(w.Body.Bytes(), &goal)
	if goal.StoppedBy == nil || goal.StoppedBy.Type != "member" || goal.StoppedBy.ID == nil || *goal.StoppedBy.ID != testUserID || goal.StopReason != "" {
		t.Fatalf("stop record = %+v reason=%q, want member %s", goal.StoppedBy, goal.StopReason, testUserID)
	}
}
