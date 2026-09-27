package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DENE-901: the board reads everything on arrival, opening a ticket reads
// that ticket, and the rows an open call hangs on stay unread until the call
// is answered or closed.

func boardRequestAs(t *testing.T, userID, method, path string) *http.Request {
	t.Helper()
	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID: util.MustParseUUID(userID), WorkspaceID: util.MustParseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member: %v", err)
	}
	req := newRequestAs(userID, method, path, nil)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
}

func boardMarkAllRead(t *testing.T) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.MarkAllInboxRead(w, boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/mark-all-read"))
	if w.Code != http.StatusOK {
		t.Fatalf("mark all read: %d %s", w.Code, w.Body.String())
	}
}

func boardMarkIssueRead(t *testing.T, issueID string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(boardRequestAs(t, testUserID, http.MethodPost, "/api/inbox/issues/"+issueID+"/read"), "issueId", issueID)
	// withURLParam replaces the context's route values only; the member
	// context set above survives because it lives under its own key.
	testHandler.MarkIssueInboxRead(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mark issue read: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Count
}

func boardUnreadIssues(t *testing.T) map[string]UnreadInboxIssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ListUnreadInboxIssues(w, boardRequestAs(t, testUserID, http.MethodGet, "/api/inbox/unread-issues"))
	if w.Code != http.StatusOK {
		t.Fatalf("unread issues: %d %s", w.Code, w.Body.String())
	}
	var rows []UnreadInboxIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]UnreadInboxIssueResponse{}
	for _, r := range rows {
		out[r.IssueID] = r
	}
	return out
}

// insertBoardInboxRow writes a plain notification to the test user, the way
// a listener would, optionally pointing at a comment.
func insertBoardInboxRow(t *testing.T, issueID, typ, commentID string) string {
	t.Helper()
	details := "{}"
	if commentID != "" {
		details = fmt.Sprintf(`{"comment_id":%q}`, commentID)
	}
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, details)
		VALUES ($1, 'member', $2, $3, 'info', $4, 'board test', $5::jsonb) RETURNING id`,
		testWorkspaceID, testUserID, typ, issueID, details).Scan(&id); err != nil {
		t.Fatalf("insert inbox row: %v", err)
	}
	return id
}

func inboxRowRead(t *testing.T, id string) bool {
	t.Helper()
	var read bool
	if err := testPool.QueryRow(context.Background(), `SELECT read FROM inbox_item WHERE id = $1`, id).Scan(&read); err != nil {
		t.Fatalf("load inbox row: %v", err)
	}
	return read
}

func boardUnreadBadge(t *testing.T) int64 {
	t.Helper()
	rows, err := testHandler.Queries.CountUnreadInboxByWorkspace(context.Background(), util.MustParseUUID(testUserID))
	if err != nil {
		t.Fatalf("badge: %v", err)
	}
	for _, r := range rows {
		if util.UUIDToString(r.WorkspaceID) == testWorkspaceID {
			return r.Count
		}
	}
	return 0
}

func boardCleanup(t *testing.T, issueIDs ...string) {
	t.Cleanup(func() {
		for _, id := range issueIDs {
			testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM issue_summon WHERE issue_id = $1`, id)
		}
	})
}

func TestBoardMarkAllReadKeepsOpenCallsUntilAnswered(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Start from a clean slate for this user so the badge counts only ours.
	testPool.Exec(context.Background(), `UPDATE inbox_item SET read = true WHERE recipient_id = $1 AND workspace_id = $2`, testUserID, testWorkspaceID)

	called := createIssueHTTP(t, "board: called", "blocked")
	mentioned := createIssueHTTP(t, "board: mentioned", "in_progress")
	plain := createIssueHTTP(t, "board: plain", "in_progress")
	boardCleanup(t, called.ID, mentioned.ID, plain.ID)

	// A summon writes its own needs_you row.
	code, call := summonIssueHTTP(t, called.ID, testUserID, "等你拍板")
	if code != http.StatusOK || call.InboxItemID == "" {
		t.Fatalf("summon: %d %+v", code, call)
	}
	// A member @ records a call without a row; the mention listener's row
	// (written by hand here) hangs on it through the comment.
	peer := summonSecondMember(t)
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'workspace' WHERE id = $1`, mentioned.ID); err != nil {
		t.Fatalf("visibility: %v", err)
	}
	ask := postMemberComment(t, peer, mentioned.ID, fmt.Sprintf("[@Kun](mention://member/%s) 你来定？", testUserID))
	mentionRow := insertBoardInboxRow(t, mentioned.ID, "mentioned", ask.ID)
	// A notification with no call behind it.
	plainRow := insertBoardInboxRow(t, plain.ID, "new_comment", "")

	if got := boardUnreadBadge(t); got != 3 {
		t.Fatalf("badge before = %d, want 3", got)
	}
	snapshot := boardUnreadIssues(t)
	if s := snapshot[called.ID]; s.UnreadCount != 1 || s.HeldCount != 1 || s.Identifier == "" {
		t.Fatalf("called snapshot = %+v", s)
	}
	if s := snapshot[mentioned.ID]; s.UnreadCount != 1 || s.HeldCount != 1 {
		t.Fatalf("mentioned snapshot = %+v", s)
	}
	if s := snapshot[plain.ID]; s.UnreadCount != 1 || s.HeldCount != 0 {
		t.Fatalf("plain snapshot = %+v", s)
	}

	boardMarkAllRead(t)
	if !inboxRowRead(t, plainRow) {
		t.Fatal("plain row still unread after mark-all")
	}
	if inboxRowRead(t, call.InboxItemID) || inboxRowRead(t, mentionRow) {
		t.Fatal("mark-all read a row an open call hangs on")
	}
	// A newer, read notification on the called ticket must not hide it from
	// the badge.
	newer := insertBoardInboxRow(t, called.ID, "status_changed", "")
	boardMarkAllRead(t)
	if !inboxRowRead(t, newer) {
		t.Fatal("newer row not read")
	}
	if got := boardUnreadBadge(t); got != 2 {
		t.Fatalf("badge after mark-all = %d, want 2", got)
	}

	// The reply answers the call and reads its row.
	postMemberComment(t, testUserID, called.ID, "选 A")
	if !inboxRowRead(t, call.InboxItemID) {
		t.Fatal("answered call's row still unread")
	}
	// The mentioned ticket finishes without a reply: the call closes and its
	// row is read.
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = 'done' WHERE id = $1`, mentioned.ID); err != nil {
		t.Fatalf("finish: %v", err)
	}
	service.CloseSummonsOnProgress(context.Background(), testHandler.Queries, testHandler.Bus,
		util.MustParseUUID(mentioned.ID), "agent", handlerTestAgentID(t), true, false)
	if !inboxRowRead(t, mentionRow) {
		t.Fatal("finished ticket's call row still unread")
	}
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE issue_id = $1 AND answered_at IS NULL`, mentioned.ID); got != 0 {
		t.Fatalf("open calls on a finished ticket = %d, want 0", got)
	}
	if got := boardUnreadBadge(t); got != 0 {
		t.Fatalf("badge at the end = %d, want 0", got)
	}
}

func TestOpeningTicketReadsAllButOpenCall(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := createIssueHTTP(t, "board: open ticket", "blocked")
	boardCleanup(t, issue.ID)
	_, call := summonIssueHTTP(t, issue.ID, testUserID, "等你")
	other := insertBoardInboxRow(t, issue.ID, "new_comment", "")

	if got := boardMarkIssueRead(t, issue.ID); got != 1 {
		t.Fatalf("rows read = %d, want 1", got)
	}
	if !inboxRowRead(t, other) || inboxRowRead(t, call.InboxItemID) {
		t.Fatal("opening the ticket should read the plain row and keep the call row")
	}
}

// Only the person called closes their own call by moving the ticket; an
// agent moving it, or someone else's call, stays open.
func TestCalledPersonMovingTicketClosesOnlyTheirCall(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	issue := createIssueHTTP(t, "board: moved by hand", "blocked")
	boardCleanup(t, issue.ID)
	peer := summonSecondMember(t)
	_, mine := summonIssueHTTP(t, issue.ID, testUserID, "等你")
	_, theirs := summonIssueHTTP(t, issue.ID, peer, "也等你")
	issueUUID := util.MustParseUUID(issue.ID)

	service.CloseSummonsOnProgress(ctx, testHandler.Queries, testHandler.Bus, issueUUID, "agent", handlerTestAgentID(t), true, false)
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE issue_id = $1 AND answered_at IS NULL`, issue.ID); got != 2 {
		t.Fatalf("an agent's status change closed calls: open = %d, want 2", got)
	}

	service.CloseSummonsOnProgress(ctx, testHandler.Queries, testHandler.Bus, issueUUID, "member", testUserID, false, true)
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE id = $1 AND answered_at IS NULL`, mine.SummonID); got != 0 {
		t.Fatal("my reassignment did not close my call")
	}
	if !inboxRowRead(t, mine.InboxItemID) {
		t.Fatal("my closed call's row still unread")
	}
	if got := summonCount(t, `SELECT count(*) FROM issue_summon WHERE id = $1 AND answered_at IS NULL`, theirs.SummonID); got != 1 {
		t.Fatal("my reassignment closed someone else's call")
	}
}
