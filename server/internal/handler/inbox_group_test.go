package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestGroupInboxResponsesMirrorsClientDedup(t *testing.T) {
	issueA, issueB := "issue-a", "issue-b"
	rows := []InboxItemResponse{
		{ID: "a3", IssueID: &issueA, Title: "a newest", Read: true, Details: json.RawMessage(`{"from":"todo","to":"done"}`)},
		{ID: "x1", Title: "issue-less", Read: false, Details: json.RawMessage(`{}`)},
		{ID: "a2", IssueID: &issueA, Read: false, Details: json.RawMessage(`{"comment_id":"c2"}`)},
		{ID: "b1", IssueID: &issueB, Read: true, Details: json.RawMessage(`{"comment_id":"c9"}`)},
		{ID: "a1", IssueID: &issueA, Read: false, Details: json.RawMessage(`{"comment_id":"c1"}`)},
	}

	got := groupInboxResponses(rows)

	if len(got) != 3 || got[0].ID != "a3" || got[1].ID != "x1" || got[2].ID != "b1" {
		t.Fatalf("groups = %+v, want newest row of a, x1, b1 in order", got)
	}
	if got[0].Read || *got[0].UnreadCount != 2 {
		t.Errorf("issue a: read=%v unread=%d, want unread 2", got[0].Read, *got[0].UnreadCount)
	}
	if inboxDetailsCommentID(got[0].Details) != "c2" {
		t.Errorf("issue a anchor = %s, want newest comment c2", got[0].Details)
	}
	var details map[string]string
	if err := json.Unmarshal(got[0].Details, &details); err != nil || details["to"] != "done" {
		t.Errorf("issue a details lost its own fields: %s", got[0].Details)
	}
	if got[1].Read || *got[1].UnreadCount != 1 {
		t.Errorf("issue-less: read=%v unread=%d, want unread 1", got[1].Read, *got[1].UnreadCount)
	}
	if !got[2].Read || *got[2].UnreadCount != 0 || string(got[2].Details) != `{"comment_id":"c9"}` {
		t.Errorf("issue b = %+v, want read with its own anchor untouched", got[2])
	}
}

// Seeds a busy inbox — many notifications per issue, the way agent comments
// pile up — and checks the grouped list carries the same groups the client
// builds from the ungrouped one, at a fraction of the bytes.
func TestListInboxGroupedByIssue(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Grouped inbox", "inbox-grouped-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Cleanup(t, `DELETE FROM inbox_item WHERE workspace_id = $1`, workspaceID)

	const issues, perIssue = 40, 25
	base := time.Now().UTC().Add(-time.Hour)
	for n := 0; n < issues; n++ {
		issueID := dbfx.Issue(t, "Busy issue", testutil.Cols{"workspace_id": workspaceID})
		// Row 0 is the newest: a status change without an anchor. The rest are
		// comment notifications with a full preview-length body; the newest
		// eight rows (the status row included) are unread.
		dbfx.Exec(t, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, body, read, created_at, details)
			SELECT $1, 'member', $2,
			       CASE WHEN i = 0 THEN 'status_changed' ELSE 'new_comment' END, 'info', $3,
			       'Busy issue', repeat('评论正文 ', 60), i > 7,
			       $4::timestamptz - (i * interval '1 second') - ($5 * interval '1 minute'),
			       CASE WHEN i = 0 THEN '{"from":"todo","to":"in_progress"}'::jsonb
			            ELSE jsonb_build_object('comment_id', 'comment-' || i) END
			FROM generate_series(0, $6 - 1) AS i
		`, workspaceID, testUserID, issueID, base, n, perIssue)
	}

	var flat, grouped []InboxItemResponse
	flatRec := testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox", workspaceID)).Want(http.StatusOK)
	flatRec.JSON(&flat)
	groupedRec := testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		inboxRequest(http.MethodGet, "/api/inbox?group=issue", workspaceID)).Want(http.StatusOK)
	groupedRec.JSON(&grouped)

	if len(flat) != issues*perIssue {
		t.Fatalf("ungrouped rows = %d, want %d (contract unchanged)", len(flat), issues*perIssue)
	}
	if flat[0].UnreadCount != nil {
		t.Errorf("ungrouped row carries unread_count; old clients must see the old shape")
	}
	if len(grouped) != issues {
		t.Fatalf("grouped rows = %d, want one per issue (%d)", len(grouped), issues)
	}
	for _, row := range grouped {
		if row.Type != "status_changed" || row.Read || row.UnreadCount == nil || *row.UnreadCount != 8 {
			t.Fatalf("group = %+v, want newest status row with 8 unread", row)
		}
		if inboxDetailsCommentID(row.Details) != "comment-1" {
			t.Fatalf("group anchor = %s, want newest comment-1", row.Details)
		}
	}

	flatBytes, groupedBytes := flatRec.Body.Len(), groupedRec.Body.Len()
	t.Logf("GET /api/inbox for %d issues × %d notifications: ungrouped %d bytes, grouped %d bytes (%.1f%%)",
		issues, perIssue, flatBytes, groupedBytes, 100*float64(groupedBytes)/float64(flatBytes))
	if groupedBytes*10 > flatBytes {
		t.Errorf("grouped %d bytes, want under a tenth of ungrouped %d", groupedBytes, flatBytes)
	}
}

func TestMarkInboxReadIssueScopeReadsActiveGroup(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Grouped inbox read", "inbox-group-read-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	dbfx.Cleanup(t, `DELETE FROM inbox_item WHERE workspace_id = $1`, workspaceID)
	issueID := dbfx.Issue(t, "Group read", testutil.Cols{"workspace_id": workspaceID})
	otherIssueID := dbfx.Issue(t, "Other", testutil.Cols{"workspace_id": workspaceID})

	insert := func(issue string, archived bool) string {
		return dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id": workspaceID, "recipient_type": "member", "recipient_id": testUserID,
			"type": "new_comment", "severity": "info", "issue_id": issue, "title": "t", "archived": archived,
		})
	}
	newest := insert(issueID, false)
	sibling := insert(issueID, false)
	archivedSibling := insert(issueID, true)
	other := insert(otherIssueID, false)

	read := func(id, query string) {
		req := withURLParam(inboxRequest(http.MethodPost, "/api/inbox/"+id+"/read"+query, workspaceID), "id", id)
		testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkInboxRead), req).Want(http.StatusOK)
	}
	isRead := func(id string) bool {
		var r bool
		dbfx.QueryRow(t, `SELECT read FROM inbox_item WHERE id = $1`, id).Scan(&r)
		return r
	}

	read(newest, "")
	if isRead(sibling) {
		t.Fatalf("plain read marked a sibling; the default scope must stay the single item")
	}
	read(newest, "?scope=issue")
	if !isRead(newest) || !isRead(sibling) {
		t.Errorf("scope=issue left an active row of the group unread")
	}
	if isRead(archivedSibling) || isRead(other) {
		t.Errorf("scope=issue reached an archived row or another issue")
	}
}
