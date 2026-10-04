package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

type chatSpawnFixture struct {
	runtime, carrier, target, parent, task string
}

func newChatSpawnFixture(t *testing.T) chatSpawnFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("test database not available")
	}
	var stored []byte
	if err := testPool.QueryRow(context.Background(), `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, stored)
	})
	runtime := dbfx.Runtime(t, "chat-spawn-runtime", testutil.Cols{"workspace_id": testWorkspaceID})
	carrier := dbfx.Agent(t, "chat-spawn-carrier", runtime, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID})
	target := dbfx.Agent(t, "chat-spawn-target", runtime, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID})
	parent := dbfx.Insert(t, "chat_session", testutil.Cols{
		"workspace_id": testWorkspaceID, "agent_id": carrier, "creator_id": testUserID,
		"title": "parent chat", "status": "active", "visibility": "private",
		"explicitly_created_at": testutil.Raw("now()"),
	})
	task := dbfx.Task(t, carrier, testutil.Cols{
		"runtime_id": runtime, "status": "running", "chat_session_id": parent,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_spawn_record WHERE task_id = $1`, task)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id IN (SELECT id FROM chat_session WHERE origin_session_id = $1)`, parent)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE origin_session_id = $1`, parent)
	})
	return chatSpawnFixture{runtime: runtime, carrier: carrier, target: target, parent: parent, task: task}
}

func (f chatSpawnFixture) spawn(t *testing.T, task string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	if body["agent_id"] == nil {
		body["agent_id"] = f.target
	}
	if body["brief"] == nil {
		body["brief"] = "Dig into the flaky deploy and report back."
	}
	r := newRequest(http.MethodPost, "/api/chat/sessions/spawn", body)
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Agent-ID", f.carrier)
	r.Header.Set("X-Task-ID", task)
	r = withChatTestWorkspaceCtx(t, r)
	w := httptest.NewRecorder()
	testHandler.SpawnChatSession(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func setAgentSpawn(t *testing.T, policy string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) || jsonb_build_object('agent_spawn', $2::jsonb) WHERE id = $1`,
		testWorkspaceID, policy); err != nil {
		t.Fatal(err)
	}
}

func nextTestIssueNumber(t *testing.T) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(),
		`UPDATE workspace SET issue_counter = issue_counter + 1 WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func lastParentCard(t *testing.T, parent string) (kind, content string, linked *string) {
	t.Helper()
	if err := testPool.QueryRow(context.Background(),
		`SELECT message_kind, content, linked_session_id::text FROM chat_message WHERE chat_session_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		parent).Scan(&kind, &content, &linked); err != nil {
		t.Fatal(err)
	}
	return
}

func TestChatSpawn_CreatesChatWithBriefOriginAndCard(t *testing.T) {
	f := newChatSpawnFixture(t)
	w, out := f.spawn(t, f.task, map[string]any{"title": "Deploy dig"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	session := out["session"].(map[string]any)
	child := session["id"].(string)
	if session["creator_id"] != testUserID {
		t.Fatalf("creator = %v, want originator %s", session["creator_id"], testUserID)
	}
	if session["origin_session_id"] != f.parent || session["origin_type"] != "chat" {
		t.Fatalf("origin = %v/%v", session["origin_type"], session["origin_session_id"])
	}
	if session["origin_title"] != "parent chat" {
		t.Fatalf("origin_title = %v", session["origin_title"])
	}
	if session["visibility"] != "private" {
		t.Fatalf("visibility = %v, want private like the parent", session["visibility"])
	}
	var role, content string
	var queued int
	if err := testPool.QueryRow(context.Background(),
		`SELECT m.role, m.content, (SELECT count(*) FROM agent_task_queue q WHERE q.chat_session_id = $1 AND q.agent_id = $2)
		   FROM chat_message m WHERE m.chat_session_id = $1 ORDER BY m.created_at LIMIT 1`, child, f.target).Scan(&role, &content, &queued); err != nil {
		t.Fatal(err)
	}
	if role != "user" || content != "Dig into the flaky deploy and report back." || queued != 1 {
		t.Fatalf("first message = %s %q, tasks = %d", role, content, queued)
	}
	kind, cardContent, linked := lastParentCard(t, f.parent)
	if kind != "chat_spawn" || linked == nil || *linked != child {
		t.Fatalf("parent card = %s %q linked=%v", kind, cardContent, linked)
	}
}

func TestChatSpawn_ClientKeyRetryReturnsSameSession(t *testing.T) {
	f := newChatSpawnFixture(t)
	w1, out1 := f.spawn(t, f.task, map[string]any{"client_key": "k1"})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body=%s", w1.Code, w1.Body.String())
	}
	w2, out2 := f.spawn(t, f.task, map[string]any{"client_key": "k1"})
	if w2.Code != http.StatusOK || out2["created"] != false {
		t.Fatalf("retry status = %d, body=%s", w2.Code, w2.Body.String())
	}
	if out1["session"].(map[string]any)["id"] != out2["session"].(map[string]any)["id"] {
		t.Fatal("retry opened a second chat")
	}
	var n int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session WHERE origin_session_id = $1`, f.parent).Scan(&n)
	if n != 1 {
		t.Fatalf("spawned chats = %d, want 1", n)
	}
}

func TestChatSpawn_DepthExceeded(t *testing.T) {
	f := newChatSpawnFixture(t)
	w, out := f.spawn(t, f.task, map[string]any{})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	child := out["session"].(map[string]any)["id"].(string)
	childTask := dbfx.Task(t, f.carrier, testutil.Cols{
		"runtime_id": f.runtime, "status": "running", "chat_session_id": child,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	w, out = f.spawn(t, childTask, map[string]any{})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedDepth {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if kind, _, _ := lastParentCard(t, child); kind != "chat_spawn_refused" {
		t.Fatalf("refusal card kind = %s", kind)
	}
}

func TestChatSpawn_PerRunBudget(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"chat_chat":{"enabled":true,"per_chat":5,"per_run":1}}`)
	if w, _ := f.spawn(t, f.task, map[string]any{}); w.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body=%s", w.Code, w.Body.String())
	}
	w, out := f.spawn(t, f.task, map[string]any{})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedBudget || out["scope"] != "run" {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSpawn_PerChatBudget(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"chat_chat":{"enabled":true,"per_chat":1,"per_run":0}}`)
	if w, _ := f.spawn(t, f.task, map[string]any{}); w.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body=%s", w.Code, w.Body.String())
	}
	other := dbfx.Task(t, f.carrier, testutil.Cols{
		"runtime_id": f.runtime, "status": "running", "chat_session_id": f.parent,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	w, out := f.spawn(t, other, map[string]any{})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedBudget || out["scope"] != "chat" {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if kind, content, _ := lastParentCard(t, f.parent); kind != "chat_spawn_refused" || content == "" {
		t.Fatalf("refusal card = %s %q", kind, content)
	}
}

func TestChatSpawn_Disabled(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"chat_chat":{"enabled":false}}`)
	w, out := f.spawn(t, f.task, map[string]any{})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedDisabled {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSpawn_TaskModeRejected(t *testing.T) {
	f := newChatSpawnFixture(t)
	issue := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "spawn from issue", "status": "todo", "priority": "none",
		"creator_type": "member", "creator_id": testUserID, "number": nextTestIssueNumber(t),
	})
	issueTask := dbfx.Task(t, f.carrier, testutil.Cols{
		"runtime_id": f.runtime, "status": "running", "issue_id": issue,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	w, out := f.spawn(t, issueTask, map[string]any{})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedTaskMode {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSpawn_ScopeCannotWiden(t *testing.T) {
	f := newChatSpawnFixture(t)
	w, out := f.spawn(t, f.task, map[string]any{"visibility": "workspace"})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedScope {
		t.Fatalf("visibility widen status = %d, body=%s", w.Code, w.Body.String())
	}
	project := dbfx.Project(t, "Spawn scope project", testutil.Cols{"workspace_id": testWorkspaceID})
	w, out = f.spawn(t, f.task, map[string]any{"project_id": project})
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedScope {
		t.Fatalf("project widen status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatSpawn_PersonCannotCall(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	r := withChatTestWorkspaceCtx(t, newRequest(http.MethodPost, "/api/chat/sessions/spawn", map[string]any{"agent_id": testUserID, "brief": "x"}))
	w := httptest.NewRecorder()
	testHandler.SpawnChatSession(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestAgentSpawn_IssueFromIssueDisabled(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"issue_issue":{"enabled":false}}`)
	issue := dbfx.Insert(t, "issue", testutil.Cols{
		"workspace_id": testWorkspaceID, "title": "parent for spawn gate", "status": "todo", "priority": "none",
		"creator_type": "member", "creator_id": testUserID, "number": nextTestIssueNumber(t),
	})
	issueTask := dbfx.Task(t, f.carrier, testutil.Cols{
		"runtime_id": f.runtime, "status": "running", "issue_id": issue,
		"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
	})
	r := newRequest(http.MethodPost, "/api/issues", map[string]any{"title": "child from agent"})
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Agent-ID", f.carrier)
	r.Header.Set("X-Task-ID", issueTask)
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusForbidden || out["code"] != SpawnRefusedDisabled {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// The chat cell is separate: a chat run may still create issues, and the
	// budget counts them.
	setAgentSpawn(t, `{"issue_issue":{"enabled":false},"chat_issue":{"enabled":true,"per_run":1}}`)
	create := func() *httptest.ResponseRecorder {
		r := newRequest(http.MethodPost, "/api/issues", map[string]any{"title": "issue from chat", "allow_duplicate": true})
		r.Header.Set("X-Actor-Source", "task_token")
		r.Header.Set("X-Agent-ID", f.carrier)
		r.Header.Set("X-Task-ID", f.task)
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, r)
		return w
	}
	w1 := create()
	if w1.Code != http.StatusCreated {
		t.Fatalf("chat run create status = %d, body=%s", w1.Code, w1.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &created)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created["id"]) })
	w2 := create()
	_ = json.Unmarshal(w2.Body.Bytes(), &out)
	if w2.Code != http.StatusForbidden || out["code"] != SpawnRefusedBudget {
		t.Fatalf("second create status = %d, body=%s", w2.Code, w2.Body.String())
	}
}

// Concurrent creates from one run must not all pass on a stale count: the
// check and the reservation are one transaction under a per-run lock.
func TestAgentSpawn_ConcurrentIssueCreatesRespectPerRun(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"chat_issue":{"enabled":true,"per_run":2}}`)
	const attempts = 8
	codes := make([]int, attempts)
	bodies := make([]map[string]any, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := newRequest(http.MethodPost, "/api/issues", map[string]any{"title": "concurrent issue from chat", "allow_duplicate": true})
			r.Header.Set("X-Actor-Source", "task_token")
			r.Header.Set("X-Agent-ID", f.carrier)
			r.Header.Set("X-Task-ID", f.task)
			w := httptest.NewRecorder()
			testHandler.CreateIssue(w, r)
			codes[i] = w.Code
			_ = json.Unmarshal(w.Body.Bytes(), &bodies[i])
		}(i)
	}
	wg.Wait()
	created := 0
	for i, code := range codes {
		switch {
		case code == http.StatusCreated:
			created++
			id := bodies[i]["id"]
			t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
		case code == http.StatusForbidden && bodies[i]["code"] == SpawnRefusedBudget:
		default:
			t.Fatalf("attempt %d: status = %d, body = %v", i, code, bodies[i])
		}
	}
	if created != 2 {
		t.Fatalf("created %d issues, want exactly per_run=2", created)
	}
	var records int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_spawn_record WHERE task_id = $1 AND target_kind = 'issue' AND target_id <> id`, f.task).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 2 {
		t.Fatalf("ledger has %d filled issue records, want 2", records)
	}
}

// A reservation the create never used goes back to the budget.
func TestAgentSpawn_ReleasedReservationReturnsBudget(t *testing.T) {
	f := newChatSpawnFixture(t)
	setAgentSpawn(t, `{"chat_issue":{"enabled":true,"per_run":1}}`)
	ctx := context.Background()
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(f.task))
	if err != nil {
		t.Fatal(err)
	}
	ws := parseUUID(testWorkspaceID)
	res, refusal, err := testHandler.reserveAgentIssueSpawn(ctx, ws, task, 1)
	if err != nil || refusal != nil {
		t.Fatalf("first reserve: err=%v refusal=%v", err, refusal)
	}
	if _, refusal, _ := testHandler.reserveAgentIssueSpawn(ctx, ws, task, 1); refusal == nil || refusal.Code != SpawnRefusedBudget {
		t.Fatalf("second reserve while held: refusal=%v", refusal)
	}
	res.release()
	again, refusal, err := testHandler.reserveAgentIssueSpawn(ctx, ws, task, 1)
	if err != nil || refusal != nil {
		t.Fatalf("reserve after release: err=%v refusal=%v", err, refusal)
	}
	again.release()
}

func TestAgentSpawn_PolicyDefaultsAndAgentCannotUpdate(t *testing.T) {
	p := parseAgentSpawnPolicy([]byte(`{}`))
	if !p.ChatIssue.Enabled || p.ChatIssue.PerRun != 0 || !p.IssueIssue.Enabled || p.IssueIssue.PerRun != 0 {
		t.Fatalf("issue defaults changed behavior: %+v", p)
	}
	if !p.ChatChat.Enabled || p.ChatChat.PerChat != 5 || p.ChatChat.PerRun != 3 {
		t.Fatalf("chat defaults = %+v", p.ChatChat)
	}
	p = parseAgentSpawnPolicy([]byte(`{"agent_spawn":{"chat_chat":{"per_run":7}}}`))
	if !p.ChatChat.Enabled || p.ChatChat.PerRun != 7 || p.ChatChat.PerChat != 5 {
		t.Fatalf("partial stored value = %+v", p.ChatChat)
	}

	r := newRequest(http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/agent-spawn", map[string]any{"chat_chat": map[string]any{"per_run": 100}})
	r.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	testHandler.UpdateWorkspaceAgentSpawn(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("agent update status = %d", w.Code)
	}
	merged := keepStoredAgentSpawn(map[string]any{"agent_spawn": map[string]any{"chat_chat": map[string]any{"per_run": 100}}, "x": 1}, []byte(`{}`))
	if _, ok := merged.(map[string]any)["agent_spawn"]; ok {
		t.Fatal("agent settings write smuggled agent_spawn in")
	}
}
