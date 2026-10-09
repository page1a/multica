package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// DENE-1667: where a person has heard a project up to is kept per person +
// project. Two chats bound to one project each open tickets; hearing the
// report in chat A means chat B only hears what moved afterwards — from both
// chats, each named as its source.

type projectReportFixture struct {
	projectID string
	agentID   string
}

func newProjectReportFixture(t *testing.T) projectReportFixture {
	t.Helper()
	ctx := context.Background()
	var projectID string
	if err := testPool.QueryRow(ctx, `INSERT INTO project (workspace_id, title) VALUES ($1, 'report-test') RETURNING id`, testWorkspaceID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM project_report_heard WHERE project_id = $1`, projectID)
		testPool.Exec(bg, `DELETE FROM issue WHERE project_id = $1`, projectID)
		testPool.Exec(bg, `DELETE FROM project WHERE id = $1`, projectID)
	})
	return projectReportFixture{projectID: projectID, agentID: createHandlerTestAgent(t, "report-agent-"+projectID[:8], nil)}
}

// chat opens a chat in the project and returns it with the chat run that will
// open its tickets.
func (f projectReportFixture) chat(t *testing.T, title string) (chatID, taskID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status, project_id)
		VALUES ($1, $2, $3, $4, 'active', $5) RETURNING id
	`, testWorkspaceID, f.agentID, testUserID, title, f.projectID).Scan(&chatID); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id, originator_user_id, accountable_user_id, originator_source)
		VALUES ($1, $2, 'completed', 0, $3, $4, $4, 'direct_human') RETURNING id
	`, f.agentID, testRuntimeID, chatID, testUserID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(bg, `DELETE FROM chat_session WHERE id = $1`, chatID)
	})
	return chatID, taskID
}

// ticket opens an issue from a chat run, created at `at`.
func (f projectReportFixture) ticket(t *testing.T, taskID, title, status string, at time.Time, assignedToUser bool) string {
	t.Helper()
	var issueID string
	assigneeType, assigneeID := any(nil), any(nil)
	if assignedToUser {
		assigneeType, assigneeID = "member", testUserID
	}
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, description, status, priority, creator_type, creator_id, number,
		                   project_id, origin_type, origin_id, assignee_type, assignee_id, created_at)
		VALUES ($1, $2, E'## 目标\n讲清楚 '||$2, $3, 'medium', 'agent', $4,
		        COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1,
		        $5, 'agent_create', $6, $7, $8, $9)
		RETURNING id
	`, testWorkspaceID, title, status, f.agentID, f.projectID, taskID, assigneeType, assigneeID, at).Scan(&issueID); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return issueID
}

func moveProjectReportTicket(t *testing.T, issueID, from, to string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = $2 WHERE id = $1`, issueID, to); err != nil {
		t.Fatalf("move ticket: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO activity_log (workspace_id, issue_id, actor_type, action, details, created_at)
		VALUES ($1, $2, 'system', 'status_changed', jsonb_build_object('from', $3::text, 'to', $4::text), $5)
	`, testWorkspaceID, issueID, from, to, at); err != nil {
		t.Fatalf("record move: %v", err)
	}
}

func callProjectReport(t *testing.T, projectID, taskID string, mark bool) ProjectReportResponse {
	t.Helper()
	method, path := http.MethodGet, "/api/projects/"+projectID+"/report"
	if mark {
		method, path = http.MethodPost, path+"/heard"
	}
	req := withURLParam(boardRequestAs(t, testUserID, method, path), "id", projectID)
	if taskID != "" {
		req.Header.Set("X-Task-ID", taskID)
	}
	w := httptest.NewRecorder()
	if mark {
		testHandler.MarkProjectReportHeard(w, req)
	} else {
		testHandler.GetProjectReport(w, req)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("project report: %d %s", w.Code, w.Body.String())
	}
	var out ProjectReportResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func reportItems(r ProjectReportResponse) map[string]ProjectReportItem {
	out := map[string]ProjectReportItem{}
	for _, item := range r.Items {
		out[item.IssueID] = item
	}
	return out
}

func TestProjectReportCursorIsPerPersonAndProject(t *testing.T) {
	if testPool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	f := newProjectReportFixture(t)
	chatA, taskA := f.chat(t, "聊天 A")
	chatB, taskB := f.chat(t, "聊天 B")

	now := time.Now().UTC()
	early := now.Add(-2 * time.Hour)
	a1 := f.ticket(t, taskA, "A1", "todo", early, false)
	a2 := f.ticket(t, taskA, "A2", "todo", early, true)
	b1 := f.ticket(t, taskB, "B1", "todo", early, false)
	b2 := f.ticket(t, taskB, "B2", "todo", early, false)
	moveProjectReportTicket(t, a1, "todo", "done", early.Add(10*time.Minute))
	moveProjectReportTicket(t, b1, "todo", "in_progress", early.Add(20*time.Minute))

	// An unread notification on A1: hearing the report reads it.
	var inboxID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, created_at)
		VALUES ($1, 'member', $2, 'status_changed', 'info', $3, 'A1 done', $4) RETURNING id
	`, testWorkspaceID, testUserID, a1, early.Add(11*time.Minute)).Scan(&inboxID); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE id = $1`, inboxID) })

	// First time: everything from the last week, nothing heard yet.
	first := callProjectReport(t, f.projectID, taskA, false)
	if first.LastHeardAt != nil || first.Marked {
		t.Fatalf("a preview must not be heard yet: %+v", first)
	}
	if len(first.Items) != 4 {
		t.Fatalf("first report = %d items, want the 4 tickets", len(first.Items))
	}

	// Hear it in chat A.
	heard := callProjectReport(t, f.projectID, taskA, true)
	if !heard.Marked || heard.InboxRead != 1 {
		t.Fatalf("heard = marked %v inbox_read %d, want marked and 1 read", heard.Marked, heard.InboxRead)
	}
	items := reportItems(heard)
	if items[a1].Phase != projectReportPhaseDone || items[a2].Phase != projectReportPhaseWaitingYou || items[b1].Phase != projectReportPhaseInProgress {
		t.Fatalf("phases wrong: %+v", heard.Items)
	}
	if heard.Items[0].IssueID != a2 {
		t.Fatalf("waiting_you must lead, got %s first", heard.Items[0].Identifier)
	}
	if src := items[b2].SourceChat; src == nil || src.ID != chatB || src.Title != "聊天 B" || !src.Accessible {
		t.Fatalf("B2 source chat = %+v, want 聊天 B", src)
	}
	if items[a1].Gist != "讲清楚 A1" {
		t.Fatalf("gist = %q, want the 目标 line", items[a1].Gist)
	}
	var read bool
	testPool.QueryRow(ctx, `SELECT read FROM inbox_item WHERE id = $1`, inboxID).Scan(&read)
	if !read {
		t.Fatal("hearing the report must read the inbox row on a covered ticket")
	}
	// A2 waits on the person without being in review: ask what blocks it.
	if len(heard.Actions) != 2 || heard.Actions[0].Label != items[a2].Identifier+" 卡在哪" || heard.Actions[1].Label != service.ChatReportAckLabel {
		t.Fatalf("actions = %+v, want 卡在哪 for the one waiting ticket and 都知道了", heard.Actions)
	}

	// Nothing moved since: chat B hears nothing new.
	again := callProjectReport(t, f.projectID, taskB, false)
	if again.LastHeardAt == nil || len(again.Items) != 0 || len(again.Actions) != 0 {
		t.Fatalf("after hearing in A, B must start from A's cursor: %+v", again)
	}

	// After A was heard, one ticket of each chat moves. B hears exactly those two.
	later := time.Now().UTC().Add(time.Second)
	moveProjectReportTicket(t, a2, "todo", "in_review", later)
	moveProjectReportTicket(t, b2, "todo", "done", later)
	// A move that came back to where it was is no news.
	moveProjectReportTicket(t, b1, "in_progress", "blocked", later)
	moveProjectReportTicket(t, b1, "blocked", "in_progress", later.Add(time.Millisecond))

	// The cursor ends at `until`; the moves above sit after it only once the
	// clock passes them.
	time.Sleep(1100 * time.Millisecond)
	fromB := callProjectReport(t, f.projectID, taskB, true)
	got := reportItems(fromB)
	if len(got) != 2 || got[a2].FromStatus != "todo" || got[b2].Status != "done" {
		t.Fatalf("B's report = %+v, want just A2 and B2 since A was heard", fromB.Items)
	}
	if got[a2].SourceChat == nil || got[a2].SourceChat.ID != chatA {
		t.Fatalf("A2 must name chat A as its source: %+v", got[a2].SourceChat)
	}
	// A2 is now in review: the person's verdict, either way.
	if len(fromB.Actions) != 3 || fromB.Actions[0].Label != got[a2].Identifier+" 看过了，没问题" || fromB.Actions[1].Label != got[a2].Identifier+" 要改" {
		t.Fatalf("actions = %+v, want approve / change for the ticket in review", fromB.Actions)
	}

	// The run that heard it is recorded with the report's buttons, which its
	// chat reply takes over the suggestion pass.
	rows, err := testHandler.Queries.ListProjectReportHeardByTask(ctx, parseUUID(taskB))
	if err != nil || len(rows) != 1 {
		t.Fatalf("heard rows for B's run = %d (%v), want 1", len(rows), err)
	}
	if rows[0].ChatSessionID != parseUUID(chatB) || rows[0].ItemCount != 2 {
		t.Fatalf("heard row = %+v", rows[0])
	}
	var actions []map[string]any
	_ = json.Unmarshal(rows[0].Actions, &actions)
	if len(actions) == 0 || actions[len(actions)-1]["label"] != service.ChatReportAckLabel {
		t.Fatalf("recorded actions = %v", actions)
	}
}

// DENE-1691: each item carries the --summary of its latest close or handoff,
// verbatim, and nothing when that close or handoff carried none.
func TestProjectReportCarriesLatestSummary(t *testing.T) {
	if testPool == nil {
		t.Skip("no database")
	}
	ctx := context.Background()
	f := newProjectReportFixture(t)
	agentID := handlerTestAgentID(t)
	inProject := func(title string) string {
		issue := createIssueHTTP(t, title, "in_progress")
		if _, err := testPool.Exec(ctx, `UPDATE issue SET project_id = $2 WHERE id = $1`, issue.ID, f.projectID); err != nil {
			t.Fatalf("move into project: %v", err)
		}
		setIssueAssigneeDirect(t, issue.ID, "agent", agentID)
		return issue.ID
	}
	closeIt := func(issueID string, body map[string]any) {
		taskID := insertIssueTaskWithStatus(t, agentID, issueID, "running")
		body["outcome"] = "in_progress"
		body["wake_at"] = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		if w := closeIssueHTTP(t, issueID, agentID, taskID, body); w.Code != http.StatusOK {
			t.Fatalf("close status = %d: %s", w.Code, w.Body.String())
		}
	}

	handoff := func(issueID, summary string) {
		next := createHandlerTestAgent(t, "report next "+time.Now().Format(time.RFC3339Nano), []byte("[]"))
		body := map[string]any{"to": agentNameDirect(t, next)}
		if summary != "" {
			body["summary"] = summary
		}
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/handoff", body), "id", issueID)
		rec := httptest.NewRecorder()
		testHandler.HandoffIssue(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("handoff status = %d: %s", rec.Code, rec.Body.String())
		}
	}

	// Longer than the progress line keeps, with inner spacing: the report
	// still says it word for word.
	long := strings.Repeat("结", 500) + "  论"
	withSummary := inProject("report summary")
	closeIt(withSummary, map[string]any{"summary": long, "evidence": "证据：测试全绿"})
	noSummary := inProject("report no summary")
	closeIt(noSummary, map[string]any{"evidence": "只有证据没有结论"})
	handedOff := inProject("report handoff")
	closeIt(handedOff, map[string]any{"summary": "旧结论", "evidence": "证据"})
	handoff(handedOff, "卡在前端，要验收席看截图")
	// The latest handoff said nothing: no older line stands in, whether the
	// one before it was a close or a handoff. Same-second on purpose.
	closeThenBare := inProject("report close then bare handoff")
	closeIt(closeThenBare, map[string]any{"summary": "旧结论", "evidence": "证据"})
	handoff(closeThenBare, "")
	handoffThenBare := inProject("report handoff then bare handoff")
	handoff(handoffThenBare, "上一棒结论")
	handoff(handoffThenBare, "")

	items := reportItems(callProjectReport(t, f.projectID, "", false))
	if got := items[withSummary].LatestSummary; got != long {
		t.Fatalf("closed with summary: latest_summary = %q", got)
	}
	if got, ok := items[noSummary]; !ok || got.LatestSummary != "" {
		t.Fatalf("closed without summary must be reported without latest_summary: %+v (present %v)", got, ok)
	}
	if got := items[handedOff].LatestSummary; got != "卡在前端，要验收席看截图" {
		t.Fatalf("handoff after close: latest_summary = %q", got)
	}
	for name, id := range map[string]string{"close → bare handoff": closeThenBare, "handoff → bare handoff": handoffThenBare} {
		if got, ok := items[id]; !ok || got.LatestSummary != "" {
			t.Fatalf("%s: latest_summary = %q (present %v), want none", name, got.LatestSummary, ok)
		}
	}
}
