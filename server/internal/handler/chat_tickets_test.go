package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestChatTicketDescriptionProblem(t *testing.T) {
	cases := []struct {
		name, desc string
		ok         bool
	}{
		{"headings", "## 目标\n做开单卡\n\n## 验收\n- [ ] 卡片出现", true},
		{"inline labels", "目标：做开单卡\n验收：卡片出现", true},
		{"bold labels", "**目标**：做开单卡\n**验收标准**\n- 卡片出现", true},
		{"english", "## Goal\nShip it\n## Acceptance\n- works", true},
		{"heading with gloss", "## 目标（Goal）\nShip it\n## 验收", true},
		{"missing acceptance", "## 目标\n做开单卡", false},
		{"missing goal", "## 验收\n- 卡片出现", false},
		{"word in prose is not a heading", "这个目标任务要验收\n", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := chatTicketDescriptionProblem(tc.desc)
			if (msg == "") != tc.ok {
				t.Fatalf("chatTicketDescriptionProblem(%q) = %q, want ok=%v", tc.desc, msg, tc.ok)
			}
		})
	}
	if msg := chatTicketDescriptionProblem("## 目标\nx"); !strings.Contains(msg, "验收") || strings.Contains(msg, "目标 (goal)") {
		t.Fatalf("refusal should name only the missing part, got %q", msg)
	}
}

func TestChatTicketGoal(t *testing.T) {
	cases := map[string]string{
		"## 目标\n\n- 做开单卡\n## 验收\n- x": "做开单卡",
		"目标：聊天里能看到开了哪些单\n验收：x":        "聊天里能看到开了哪些单",
		"**目标**: **让单子显示来源**":         "让单子显示来源",
		"## 目标\n## 验收\n- x":           "",
		"没有目标段":                       "",
	}
	for desc, want := range cases {
		if got := chatTicketGoal(desc); got != want {
			t.Errorf("chatTicketGoal(%q) = %q, want %q", desc, got, want)
		}
	}
}

// TestChatTickets_LinkBothWays: an issue a chat run creates points back at
// the chat, the chat lists it, and the issue names the chat (DENE-1665).
func TestChatTickets_LinkBothWays(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, "Handler Test Agent",
	).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("find test agent: %v", err)
	}
	var sessionID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, runtime_id, explicitly_created_at)
		 VALUES ($1, $2, $3, 'Multica · 聊天开单', $4, now()) RETURNING id`,
		testWorkspaceID, agentID, testUserID, runtimeID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id, chat_session_id)
		 VALUES ($1, $2, 'running', 0, $3, $3, $4) RETURNING id`,
		agentID, runtimeID, testUserID, sessionID,
	).Scan(&taskID); err != nil {
		t.Fatalf("seed chat task: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM issue WHERE origin_chat_session_id = $1`, sessionID)
		testPool.Exec(bg, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(bg, `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})

	createFromChat := func(title, description string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title": title, "description": description,
		})
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		testHandler.CreateIssue(w, req)
		return w
	}

	if w := createFromChat("No acceptance (DENE-1665)", "## 目标\n只有目标"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "验收") {
		t.Fatalf("chat ticket without 验收: want 400 naming it, got %d: %s", w.Code, w.Body.String())
	}

	w := createFromChat("Chat ticket (DENE-1665)", "## 目标\n聊天能看到开了哪些单\n\n## 验收\n- [ ] 列表出现")
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue from chat: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}

	var origin string
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(origin_chat_session_id::text, '') FROM issue WHERE id = $1`, created.ID).Scan(&origin); err != nil {
		t.Fatalf("load origin chat: %v", err)
	}
	if origin != sessionID {
		t.Fatalf("origin_chat_session_id = %q, want %q", origin, sessionID)
	}

	// The chat lists it.
	w = httptest.NewRecorder()
	testHandler.ListChatSessionTickets(w, withChatTestWorkspaceCtx(t, withURLParam(newRequest("GET", "/api/chat/sessions/"+sessionID+"/tickets", nil), "sessionId", sessionID)))
	if w.Code != http.StatusOK {
		t.Fatalf("ListChatSessionTickets: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var tickets ChatTicketsResponse
	if err := json.NewDecoder(w.Body).Decode(&tickets); err != nil {
		t.Fatalf("decode tickets: %v", err)
	}
	if len(tickets.Tickets) != 1 || tickets.Tickets[0].ID != created.ID {
		t.Fatalf("tickets = %+v, want just %s", tickets.Tickets, created.ID)
	}
	if got := tickets.Tickets[0].Goal; got != "聊天能看到开了哪些单" {
		t.Fatalf("ticket goal = %q", got)
	}
	if tk := tickets.Tickets[0]; tk.Phase != projectReportPhaseInProgress || tk.FromStatus != "" || tk.ChangedAt != tk.CreatedAt {
		t.Fatalf("fresh ticket progress = %+v, want in progress since creation", tk)
	}

	// The progress bar (DENE-1667): a move hands it to the person — the latest
	// move and their bucket come with the ticket.
	if _, err := testPool.Exec(ctx, `UPDATE issue SET assignee_type = 'member', assignee_id = $2 WHERE id = $1`, created.ID, testUserID); err != nil {
		t.Fatalf("assign to person: %v", err)
	}
	// A minute on: changed_at is second-precise, like created_at.
	moveProjectReportTicket(t, created.ID, "todo", "in_review", time.Now().UTC().Add(time.Minute))
	w = httptest.NewRecorder()
	testHandler.ListChatSessionTickets(w, withChatTestWorkspaceCtx(t, withURLParam(newRequest("GET", "/api/chat/sessions/"+sessionID+"/tickets", nil), "sessionId", sessionID)))
	tickets = ChatTicketsResponse{}
	if err := json.NewDecoder(w.Body).Decode(&tickets); err != nil {
		t.Fatalf("decode tickets: %v", err)
	}
	if tk := tickets.Tickets[0]; tk.Phase != projectReportPhaseWaitingYou || !tk.NeedsYou || tk.FromStatus != "todo" || tk.ChangedAt == tk.CreatedAt {
		t.Fatalf("moved ticket progress = %+v, want waiting on the person, from todo", tk)
	}

	// The chat run reads its own chat's tickets through its task token.
	w = httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/chat/sessions/"+sessionID+"/tickets", nil), "sessionId", sessionID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	testHandler.ListChatSessionTickets(w, withChatTestWorkspaceCtx(t, req))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), created.ID) {
		t.Fatalf("task-token tickets read: got %d: %s", w.Code, w.Body.String())
	}

	// The issue names the chat.
	w = httptest.NewRecorder()
	testHandler.GetIssue(w, withURLParam(newRequest("GET", "/api/issues/"+created.ID, nil), "id", created.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	if got.SourceChat == nil || got.SourceChat.ID != sessionID || got.SourceChat.Title != "Multica · 聊天开单" || !got.SourceChat.Accessible {
		t.Fatalf("source_chat = %+v, want the seeded chat", got.SourceChat)
	}

	// A member's own create carries no chat and no 目标/验收 requirement.
	w = httptest.NewRecorder()
	testHandler.CreateIssue(w, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{"title": "Member create (DENE-1665)"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("member create: want 201, got %d: %s", w.Code, w.Body.String())
	}
	var member IssueResponse
	_ = json.NewDecoder(w.Body).Decode(&member)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, member.ID) })
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(origin_chat_session_id::text, '') FROM issue WHERE id = $1`, member.ID).Scan(&origin); err != nil || origin != "" {
		t.Fatalf("member create origin chat = %q (%v), want empty", origin, err)
	}
}

// TestChatTickets_PlanApplyFromChat: `plan apply` from a chat run stamps every
// node with the chat and refuses a node without 目标/验收.
func TestChatTickets_PlanApplyFromChat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, "Handler Test Agent",
	).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("find test agent: %v", err)
	}
	var sessionID, taskID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, runtime_id)
		 VALUES ($1, $2, $3, 'plan chat', $4) RETURNING id`,
		testWorkspaceID, agentID, testUserID, runtimeID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id, chat_session_id)
		 VALUES ($1, $2, 'running', 0, $3, $3, $4) RETURNING id`,
		agentID, runtimeID, testUserID, sessionID,
	).Scan(&taskID); err != nil {
		t.Fatalf("seed chat task: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM issue WHERE origin_chat_session_id = $1 AND parent_issue_id IS NOT NULL`, sessionID)
		testPool.Exec(bg, `DELETE FROM issue WHERE origin_chat_session_id = $1`, sessionID)
		testPool.Exec(bg, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(bg, `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})
	stage := int32(1)
	good := "## 目标\nx\n## 验收\n- y"
	apply := func(childDesc string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/plans/apply?workspace_id="+testWorkspaceID, ApplyPlanRequest{
			Key:      "dene-1665-" + sessionID,
			Parent:   &ApplyPlanNode{Title: "Plan root (DENE-1665)", Description: good},
			Children: []ApplyPlanNode{{Key: "a", Title: "Plan child (DENE-1665)", Description: childDesc, Stage: &stage}},
		})
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		testHandler.ApplyPlan(w, req)
		return w
	}
	if w := apply("no sections"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Plan child") {
		t.Fatalf("plan child without sections: want 400 naming it, got %d: %s", w.Code, w.Body.String())
	}
	if w := apply(good); w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("plan apply from chat: got %d: %s", w.Code, w.Body.String())
	}
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE origin_chat_session_id = $1`, sessionID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("plan issues stamped with chat = %d (%v), want 2", n, err)
	}
}

// TestChatTickets_FollowAndPin (DENE-1719): a chat lists the issues its run
// acted on and the ones pinned by hand, each with its source; reading an issue
// does not follow it, and a take-down sticks against later auto-follow.
func TestChatTickets_FollowAndPin(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	var agentID, runtimeID string
	if err := testPool.QueryRow(ctx,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, "Handler Test Agent",
	).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("find test agent: %v", err)
	}
	var sessionID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, runtime_id, explicitly_created_at)
		 VALUES ($1, $2, $3, 'Multica · 聊天跟进', $4, now()) RETURNING id`,
		testWorkspaceID, agentID, testUserID, runtimeID,
	).Scan(&sessionID); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id, accountable_user_id, chat_session_id)
		 VALUES ($1, $2, 'running', 0, $3, $3, $4) RETURNING id`,
		agentID, runtimeID, testUserID, sessionID,
	).Scan(&taskID); err != nil {
		t.Fatalf("seed chat task: %v", err)
	}
	var issueIDs []string
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM chat_followed_issue WHERE chat_session_id = $1`, sessionID)
		for _, id := range issueIDs {
			testPool.Exec(bg, `DELETE FROM issue WHERE id = $1`, id)
		}
		testPool.Exec(bg, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(bg, `DELETE FROM chat_session WHERE id = $1`, sessionID)
	})

	asChatRun := func(req *http.Request) *http.Request {
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		return req
	}
	memberIssue := func(title string) string {
		w := httptest.NewRecorder()
		testHandler.CreateIssue(w, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{"title": title, "status": "backlog"}))
		if w.Code != http.StatusCreated {
			t.Fatalf("member create: want 201, got %d: %s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		_ = json.NewDecoder(w.Body).Decode(&issue)
		issueIDs = append(issueIDs, issue.ID)
		return issue.ID
	}
	list := func() map[string]string {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.ListChatSessionTickets(w, withChatTestWorkspaceCtx(t, withURLParam(newRequest("GET", "/api/chat/sessions/"+sessionID+"/tickets", nil), "sessionId", sessionID)))
		if w.Code != http.StatusOK {
			t.Fatalf("ListChatSessionTickets: want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp ChatTicketsResponse
		_ = json.NewDecoder(w.Body).Decode(&resp)
		out := map[string]string{}
		for _, tk := range resp.Tickets {
			if tk.LinkedAt == "" {
				t.Fatalf("ticket %s has no linked_at", tk.Identifier)
			}
			out[tk.ID] = tk.Source
		}
		return out
	}
	pin := func(issueRef string, remove bool) {
		t.Helper()
		w := httptest.NewRecorder()
		if remove {
			req := withURLParams(newRequest("DELETE", "/api/chat/sessions/"+sessionID+"/tickets/"+issueRef, nil), "sessionId", sessionID, "issueId", issueRef)
			testHandler.RemoveChatSessionTicket(w, withChatTestWorkspaceCtx(t, req))
		} else {
			req := withURLParam(newRequest("POST", "/api/chat/sessions/"+sessionID+"/tickets", map[string]any{"issue": issueRef}), "sessionId", sessionID)
			testHandler.AddChatSessionTicket(w, withChatTestWorkspaceCtx(t, req))
		}
		if w.Code != http.StatusNoContent {
			t.Fatalf("pin %s (remove=%v): want 204, got %d: %s", issueRef, remove, w.Code, w.Body.String())
		}
	}

	moved := memberIssue("Moved from chat (DENE-1719)")
	commented := memberIssue("Commented from chat (DENE-1719)")
	pinned := memberIssue("Pinned by hand (DENE-1719)")

	// Reading an issue is not acting on it.
	w := httptest.NewRecorder()
	testHandler.GetIssue(w, asChatRun(withURLParam(newRequest("GET", "/api/issues/"+moved, nil), "id", moved)))
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue as chat run: got %d: %s", w.Code, w.Body.String())
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("after a read, tickets = %v, want none", got)
	}

	// A status change from the chat's run follows the issue.
	w = httptest.NewRecorder()
	testHandler.UpdateIssue(w, asChatRun(withURLParam(newRequest(http.MethodPut, "/api/issues/"+moved, map[string]any{"status": "todo", "suppress_run": true}), "id", moved)))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue as chat run: got %d: %s", w.Code, w.Body.String())
	}
	// So does a comment.
	w = httptest.NewRecorder()
	testHandler.CreateComment(w, asChatRun(withURLParam(newRequest("POST", "/api/issues/"+commented+"/comments", map[string]any{"content": "聊天里跟进一下"}), "id", commented)))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateComment as chat run: got %d: %s", w.Code, w.Body.String())
	}
	// A member's own comment follows nothing.
	w = httptest.NewRecorder()
	testHandler.CreateComment(w, withURLParam(newRequest("POST", "/api/issues/"+pinned+"/comments", map[string]any{"content": "人自己评论"}), "id", pinned))
	if w.Code != http.StatusCreated {
		t.Fatalf("member comment: got %d: %s", w.Code, w.Body.String())
	}
	if got := list(); len(got) != 2 || got[moved] != chatTicketSourceAuto || got[commented] != chatTicketSourceAuto {
		t.Fatalf("after acting, tickets = %v, want moved and commented as auto", got)
	}

	// Pin by hand, by identifier.
	var number int
	if err := testPool.QueryRow(ctx, `SELECT number FROM issue WHERE id = $1`, pinned).Scan(&number); err != nil {
		t.Fatalf("load number: %v", err)
	}
	prefix := testHandler.getIssuePrefix(ctx, util.MustParseUUID(testWorkspaceID))
	pin(fmt.Sprintf("%s-%d", prefix, number), false)
	if got := list(); got[pinned] != chatTicketSourceManual {
		t.Fatalf("after pin, tickets = %v, want pinned as manual", got)
	}

	// A take-down sticks: acting on the issue again does not bring it back.
	pin(moved, true)
	w = httptest.NewRecorder()
	testHandler.UpdateIssue(w, asChatRun(withURLParam(newRequest(http.MethodPut, "/api/issues/"+moved, map[string]any{"status": "in_progress", "suppress_run": true}), "id", moved)))
	if w.Code != http.StatusOK {
		t.Fatalf("UpdateIssue after take-down: got %d: %s", w.Code, w.Body.String())
	}
	if got := list(); len(got) != 2 || got[moved] != "" {
		t.Fatalf("after take-down, tickets = %v, want moved gone", got)
	}
	// Pinning it again brings it back.
	pin(moved, false)
	if got := list(); got[moved] != chatTicketSourceManual {
		t.Fatalf("after re-pin, tickets = %v, want moved as manual", got)
	}

	// The birthplace is untouched by following.
	var origin string
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(origin_chat_session_id::text, '') FROM issue WHERE id = $1`, moved).Scan(&origin); err != nil || origin != "" {
		t.Fatalf("followed issue origin = %q (%v), want empty", origin, err)
	}
}
