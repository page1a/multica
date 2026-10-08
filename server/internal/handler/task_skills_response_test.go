package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// TestListTasksByIssueHydratesSkillsUsed guards the 「用到 skill」 line on the
// execution log (DENE-1573): each run carries the skills its `skill`
// transcript rows name, once each, in first-use order, and a run without any
// carries no field at all.
func TestListTasksByIssueHydratesSkillsUsed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agentID := createHandlerTestAgent(t, "SkillsUsedListAgent", []byte("[]"))

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'skills-used-issue', 'todo', 'medium', $2, 'member', 92779, 0)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	newTask := func() string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id)
			VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'completed', 0, $2)
			RETURNING id
		`, agentID, issueID).Scan(&id); err != nil {
			t.Fatalf("create task: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, id) })
		return id
	}

	usedTask := newTask()
	plainTask := newTask()

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, tool, content)
		VALUES ($1, 1, 'tool_use', 'Read', ''),
		       ($1, 2, 'skill', 'jev', ''),
		       ($1, 5, 'skill', 'code-review', ''),
		       ($1, 9, 'skill', 'jev', ''),
		       ($2, 1, 'tool_use', 'Bash', '')
	`, usedTask, plainTask); err != nil {
		t.Fatalf("insert task messages: %v", err)
	}

	req := newRequest("GET", "/api/issues/"+issueID+"/task-runs", nil)
	req = withURLParam(req, "id", issueID)
	w := httptest.NewRecorder()
	testHandler.ListTasksByIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp []AgentTaskResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	byID := make(map[string]AgentTaskResponse, len(resp))
	for _, task := range resp {
		byID[task.ID] = task
	}

	if got, want := byID[usedTask].SkillsUsed, []string{"jev", "code-review"}; !slices.Equal(got, want) {
		t.Errorf("skills_used = %v, want %v", got, want)
	}
	if got := byID[plainTask].SkillsUsed; got != nil {
		t.Errorf("plain run skills_used = %v, want none", got)
	}

	var raw []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, task := range raw {
		if task["id"] == plainTask {
			if _, ok := task["skills_used"]; ok {
				t.Errorf("plain run carries skills_used on the wire: %v", task["skills_used"])
			}
		}
	}
}
