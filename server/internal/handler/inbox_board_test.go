package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/inboxboard"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-975: GET /api/inbox/board — the lanes built server-side, read as the
// person behind the request, and a visit's unread snapshot replayed from
// read_at.

func getInboxBoard(t *testing.T, req *http.Request) (*httptest.ResponseRecorder, InboxBoardResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.GetInboxBoard(w, req)
	var out InboxBoardResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decode board: %v", err)
		}
	}
	return w, out
}

// boardRow finds an issue on any lane, children included.
func boardRow(b InboxBoardResponse, issueID string) *inboxboard.Row {
	var walk func(rows []*inboxboard.Row) *inboxboard.Row
	walk = func(rows []*inboxboard.Row) *inboxboard.Row {
		for _, r := range rows {
			if r.IssueID == issueID {
				return r
			}
			if c := walk(r.Children); c != nil {
				return c
			}
		}
		return nil
	}
	for _, lane := range [][]*inboxboard.Row{b.Waiting, b.Stalled, b.Running, b.Todo, b.Fresh, b.Done} {
		if r := walk(lane); r != nil {
			return r
		}
	}
	return nil
}

func insertInboxRowFor(t *testing.T, recipientID, issueID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, details)
		VALUES ($1, 'member', $2, 'status_changed', 'info', $3, 'board test', '{}'::jsonb) RETURNING id`,
		testWorkspaceID, recipientID, issueID).Scan(&id); err != nil {
		t.Fatalf("insert inbox row: %v", err)
	}
	return id
}

type boardAgentFixture struct {
	agent, task, originator string
}

// newBoardAgentFixture is an agent run on the test user's runtime, started by
// another member of the workspace.
func newBoardAgentFixture(t *testing.T, source string) boardAgentFixture {
	t.Helper()
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	originator := dbfx.User(t, "Board originator", slug+"-originator@example.test")
	dbfx.Member(t, testWorkspaceID, originator, "member")
	runtime := dbfx.Runtime(t, "board-runtime-"+slug, testutil.Cols{"workspace_id": testWorkspaceID})
	agent := dbfx.Agent(t, "board-agent-"+slug, runtime, testutil.Cols{"workspace_id": testWorkspaceID})
	task := dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "status": "running", "originator_user_id": originator,
		"accountable_user_id": originator, "originator_source": source,
	})
	return boardAgentFixture{agent: agent, task: task, originator: originator}
}

// request is the board as the agent's CLI calls it: a task token authenticated
// as the runtime owner (the test user).
func (f boardAgentFixture) request(t *testing.T) *http.Request {
	t.Helper()
	req := boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/board")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", f.agent)
	req.Header.Set("X-Task-ID", f.task)
	return req
}

func TestInboxBoard_AgentReadsTheInboxOfWhoStartedTheRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newBoardAgentFixture(t, "direct_human")
	theirs := createIssueHTTP(t, "board: originator's news", "in_progress")
	owners := createIssueHTTP(t, "board: runtime owner's news", "in_progress")
	hidden := createIssueHTTP(t, "board: someone else's private ticket", "in_progress")
	boardCleanup(t, theirs.ID, owners.ID, hidden.ID)
	// Test issues start private to their creator (the runtime owner); the
	// first two are opened to the workspace, the third stays private.
	testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'workspace' WHERE id = ANY($1::uuid[])`,
		[]string{theirs.ID, owners.ID})
	insertInboxRowFor(t, f.originator, theirs.ID)
	insertInboxRowFor(t, testUserID, owners.ID)
	insertInboxRowFor(t, f.originator, hidden.ID)

	w, board := getInboxBoard(t, f.request(t))
	if w.Code != http.StatusOK {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	if board.ViewerID != f.originator {
		t.Fatalf("viewer = %s, want the originator %s", board.ViewerID, f.originator)
	}
	if r := boardRow(board, theirs.ID); r == nil || r.Unread != 1 {
		t.Fatalf("originator's unread ticket missing or unmarked: %+v", r)
	}
	if r := boardRow(board, owners.ID); r != nil && r.Unread > 0 {
		t.Fatalf("runtime owner's unread leaked onto the originator's board: %+v", r)
	}
	// Visibility is the originator's, not the agent's bypass.
	if r := boardRow(board, hidden.ID); r != nil {
		t.Fatalf("a ticket the originator cannot see is on their board: %+v", r)
	}
}

func TestInboxBoard_TodoLaneIsWhoStartedTheRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newBoardAgentFixture(t, "direct_human")
	theirs := createIssueHTTP(t, "board: originator's todo", "todo")
	owners := createIssueHTTP(t, "board: runtime owner's todo", "todo")
	started := createIssueHTTP(t, "board: originator's work already started", "in_progress")
	boardCleanup(t, theirs.ID, owners.ID, started.ID)
	ctx := context.Background()
	testPool.Exec(ctx, `UPDATE issue SET visibility = 'workspace', assignee_type = 'member', assignee_id = $2
		WHERE id = ANY($1::uuid[])`, []string{theirs.ID, started.ID}, f.originator)
	testPool.Exec(ctx, `UPDATE issue SET visibility = 'workspace', assignee_type = 'member', assignee_id = $2
		WHERE id = $1`, owners.ID, testUserID)

	w, board := getInboxBoard(t, f.request(t))
	if w.Code != http.StatusOK {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	if r := boardRow(board, theirs.ID); r == nil || r.Lane != inboxboard.LaneTodo {
		t.Fatalf("originator's todo ticket should be in the todo lane: %+v", r)
	}
	if r := boardRow(board, owners.ID); r != nil {
		t.Fatalf("runtime owner's todo leaked onto the originator's board: %+v", r)
	}
	if r := boardRow(board, started.ID); r != nil && r.Lane == inboxboard.LaneTodo {
		t.Fatalf("an in-progress ticket is in the todo lane: %+v", r)
	}
}

func TestInboxBoard_AutomationRunIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := newBoardAgentFixture(t, "trigger_owner")
	w, _ := getInboxBoard(t, f.request(t))
	if w.Code != http.StatusForbidden {
		t.Fatalf("automation run: %d %s, want 403", w.Code, w.Body.String())
	}
	var body map[string]string
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["code"] != inboxBoardErrNoPerson || !strings.Contains(body["error"], "automation") {
		t.Fatalf("refusal should name the reason: %v", body)
	}
}

func TestInboxBoard_UnreadSinceReplaysTheVisitSnapshot(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	testPool.Exec(context.Background(), `UPDATE inbox_item SET read = true WHERE recipient_id = $1 AND workspace_id = $2`, testUserID, testWorkspaceID)
	news := createIssueHTTP(t, "board: news on arrival", "in_progress")
	boardCleanup(t, news.ID)
	row := insertInboxRowFor(t, testUserID, news.ID)

	// Arrival: live read. Reading the board changes nothing.
	w, first := getInboxBoard(t, boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/board?tz=Asia/Shanghai"))
	if w.Code != http.StatusOK {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	if r := boardRow(first, news.ID); r == nil || r.Unread != 1 || r.Lane != inboxboard.LaneFresh {
		t.Fatalf("arrival: want a fresh row with 1 unread, got %+v", r)
	}
	if first.UnreadMarkable < 1 || first.TZ != "Asia/Shanghai" || first.AsOf == "" {
		t.Fatalf("arrival metadata: %+v", first)
	}
	if inboxRowRead(t, row) {
		t.Fatal("reading the board marked the row read")
	}

	boardMarkAllRead(t)

	// Live, the news is gone; replayed from the arrival mark it is still new.
	_, live := getInboxBoard(t, boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/board"))
	if r := boardRow(live, news.ID); r != nil && r.Unread > 0 {
		t.Fatalf("live read after mark-all-read still unread: %+v", r)
	}
	path := "/api/inbox/board?unread_since=" + url.QueryEscape(first.AsOf)
	_, replay := getInboxBoard(t, boardRequestAs(t, testUserID, http.MethodGet, path))
	if r := boardRow(replay, news.ID); r == nil || r.Unread != 1 {
		t.Fatalf("replay lost the arrival marker: %+v", r)
	}
	if replay.UnreadSince == nil {
		t.Fatal("replay should echo unread_since")
	}

	// A mark from after the read no longer counts it.
	_, later := getInboxBoard(t, boardRequestAs(t, testUserID, http.MethodGet,
		"/api/inbox/board?unread_since="+url.QueryEscape(live.AsOf)))
	if r := boardRow(later, news.ID); r != nil && r.Unread > 0 {
		t.Fatalf("a mark after the read still replays it: %+v", r)
	}
}

func TestInboxBoard_BadParams(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, q := range []string{"tz=Mars/Olympus", "unread_since=yesterday"} {
		w, _ := getInboxBoard(t, boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/board?"+q))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", q, w.Code)
		}
	}
}
