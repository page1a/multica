package handler

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

// seedDoneClose writes a done close whose evidence opens with summary, the
// record `multica issue close` leaves behind.
func seedDoneClose(t *testing.T, issueID, summary string) {
	t.Helper()
	ctx := context.Background()
	var commentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
		VALUES ($1, $2, 'member', $3, $4, 'comment') RETURNING id
	`, issueID, testWorkspaceID, testUserID, summary+"\n\n## 证据\n测试").Scan(&commentID); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	meta := map[string]string{}
	for _, k := range closeprotocol.Keys {
		meta[k] = ""
	}
	meta[closeprotocol.KeyConclusion] = "done"
	meta[closeprotocol.KeyStatus] = "done"
	meta[closeprotocol.KeyEvidenceCommentID] = commentID
	meta[closeprotocol.KeyNextOwnerType] = closeprotocol.OwnerNone
	meta[closeprotocol.KeyAt] = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(meta)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb WHERE id = $1`, issueID, raw); err != nil {
		t.Fatalf("seed close: %v", err)
	}
}

// A parent with three sub-tasks hears each one's conclusion (DENE-1679): the
// child-done comment lists them, the state card carries them, and the
// parent's receipt card in its source chat repeats them.
func TestParentReceivesChildReceipts(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Child Receipt Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "拆三张子票做登录")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child receipt parent", nil)
	if _, err := testPool.Exec(ctx, `UPDATE issue SET status = 'in_progress', assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, parentID, agentID); err != nil {
		t.Fatalf("assign parent: %v", err)
	}
	conclusions := []string{"接口改走新令牌", "界面加了登录页", "文档写进 AGENTS.md"}
	var children []string
	for i, c := range conclusions {
		id := createIssueFromChatRun(t, agentID, taskID, "Child receipt sub "+string(rune('A'+i)), map[string]any{"parent_issue_id": parentID})
		children = append(children, id)
		seedDoneClose(t, id, c)
	}
	for _, id := range children {
		setIssueStatusForTest(t, id, "done")
	}

	// The child-done comment carries every conclusion, not only a count.
	content := parentSystemCommentContent(t, parentID)
	if !strings.Contains(content, "子任务回执：") {
		t.Fatalf("child-done comment has no receipts:\n%s", content)
	}
	for i, c := range conclusions {
		if !strings.Contains(content, "mention://issue/"+children[i]+") 已完成："+c) {
			t.Fatalf("child-done comment misses %q:\n%s", c, content)
		}
	}

	// The state card lists them, in JSON and in text.
	card := getIssueContextHTTP(t, parentID, "", "")
	if len(card.Children) != 3 {
		t.Fatalf("card children = %d, want 3: %+v", len(card.Children), card.Children)
	}
	for i, c := range conclusions {
		if card.Children[i].Summary != c || card.Children[i].Status != "done" {
			t.Fatalf("card child %d = %+v", i, card.Children[i])
		}
		if !strings.Contains(card.Text, c) {
			t.Fatalf("card text misses %q:\n%s", c, card.Text)
		}
	}

	// The parent's receipt in the source chat carries them; children stay quiet.
	setIssueStatusForTest(t, parentID, "done")
	var receiptBody string
	if err := testPool.QueryRow(ctx, `
		SELECT content FROM chat_message
		WHERE chat_session_id = $1 AND message_kind = 'issue_receipt' AND content LIKE '%mention://issue/' || $2 || ')%'
	`, sessionID, parentID).Scan(&receiptBody); err != nil {
		t.Fatalf("parent receipt: %v", err)
	}
	for _, c := range conclusions {
		if !strings.Contains(receiptBody, c) {
			t.Fatalf("parent receipt misses %q:\n%s", c, receiptBody)
		}
	}
	for _, id := range children {
		if n := countReceipts(t, sessionID, id); n != 1 {
			t.Fatalf("child %s appears %d times in chat receipts, want once inside the parent's", id, n)
		}
	}
}

// A private sub-task stays off a workspace parent's comment and off the
// card of someone who cannot see it.
func TestChildReceiptsHidePrivateChildren(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Child Receipt Private Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "私有子票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child receipt private parent", nil)
	openID := createIssueFromChatRun(t, agentID, taskID, "Child receipt open sub", map[string]any{"parent_issue_id": parentID})
	privateID := createIssueFromChatRun(t, agentID, taskID, "Child receipt private sub", map[string]any{"parent_issue_id": parentID})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET visibility = 'private' WHERE id = $1`, privateID); err != nil {
		t.Fatalf("make private: %v", err)
	}
	parent, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(parentID))
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	got := testHandler.childReceipts(context.Background(), parent, testHandler.visibleWithParent(context.Background(), parent))
	if len(got) != 1 || got[0].IssueID != openID {
		t.Fatalf("receipts with parent scope = %+v, want only the open child", got)
	}
}

// A sub-task scoped exactly like its parent still stays off what the
// parent's readers share when one of them reaches the parent some other way:
// the parent's own assignee, or a share naming the parent alone.
func TestChildReceiptsFollowEveryParentReader(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Child Receipt Reader Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "私有父票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child receipt reader parent", nil)
	openID := createIssueFromChatRun(t, agentID, taskID, "Child receipt reader open sub", map[string]any{"parent_issue_id": parentID})
	secretID := createIssueFromChatRun(t, agentID, taskID, "Child receipt reader secret sub", map[string]any{"parent_issue_id": parentID})
	bID := createPlainMember(t, "child-receipt-reader-b@multica.test")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE issue SET visibility = 'private' WHERE id = ANY($1::uuid[])`, []string{parentID, secretID})
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM resource_share WHERE resource_id = ANY($1::text[])`, []string{parentID, openID})
	})
	keeps := func() (open, secret bool) {
		t.Helper()
		parent, err := testHandler.Queries.GetIssue(ctx, parseUUID(parentID))
		if err != nil {
			t.Fatalf("load parent: %v", err)
		}
		keep := testHandler.visibleWithParent(ctx, parent)
		for _, id := range []string{openID, secretID} {
			c, err := testHandler.Queries.GetIssue(ctx, parseUUID(id))
			if err != nil {
				t.Fatalf("load child: %v", err)
			}
			if id == openID {
				open = keep(c)
			} else {
				secret = keep(c)
			}
		}
		return open, secret
	}

	// Same creator, both private, nobody else reads the parent: listed.
	if open, secret := keeps(); !open || !secret {
		t.Fatalf("sole reader: open=%v secret=%v, want both", open, secret)
	}

	// The parent's assignee reads the parent but not the secret child.
	exec(`UPDATE issue SET assignee_type = 'member', assignee_id = $2 WHERE id = $1`, parentID, bID)
	if open, secret := keeps(); !open || secret {
		t.Fatalf("parent assigned to B: open=%v secret=%v, want open only", open, secret)
	}

	// A share naming the parent (and the open child) but not the secret one
	// does the same at project scope.
	projectID := createChatProjectTestProject(t, testWorkspaceID, "Child receipt reader project", "")
	exec(`UPDATE issue SET status = 'in_progress', assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, parentID, agentID)
	exec(`UPDATE issue SET visibility = 'project', project_id = $2 WHERE id = ANY($1::uuid[])`, []string{parentID, openID, secretID}, projectID)
	for _, id := range []string{parentID, openID} {
		exec(`INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, access, added_by) VALUES ($1, 'issue', $2, $3, 'view', $4)`,
			testWorkspaceID, id, bID, testUserID)
	}
	if open, secret := keeps(); !open || secret {
		t.Fatalf("parent shared with B: open=%v secret=%v, want open only", open, secret)
	}

	// End to end: the parent's comment, read by B too, leaves the secret
	// child out. It finishes first, so the "last one" line names the open
	// child and the receipts are the only place it could leak.
	seedDoneClose(t, openID, "公开子票的结论")
	seedDoneClose(t, secretID, "不该外泄的结论")
	setIssueStatusForTest(t, secretID, "done")
	setIssueStatusForTest(t, openID, "done")
	var comments []string
	rows, err := testPool.Query(ctx, `SELECT content FROM comment WHERE issue_id = $1`, parentID)
	if err != nil {
		t.Fatalf("read parent comments: %v", err)
	}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			t.Fatalf("scan: %v", err)
		}
		comments = append(comments, body)
	}
	rows.Close()
	all := strings.Join(comments, "\n---\n")
	if strings.Contains(all, secretID) || strings.Contains(all, "不该外泄") {
		t.Fatalf("parent comment names the secret child:\n%s", all)
	}
	if !strings.Contains(all, "公开子票的结论") {
		t.Fatalf("parent comment misses the open child:\n%s", all)
	}

	// The chat card follows the chat's readers: once the chat is open to the
	// workspace, only workspace-wide children are named.
	exec(`UPDATE chat_session SET visibility = 'workspace' WHERE id = $1`, sessionID)
	setIssueStatusForTest(t, parentID, "done")
	var card string
	if err := testPool.QueryRow(ctx, `
		SELECT content FROM chat_message
		WHERE chat_session_id = $1 AND message_kind = 'issue_receipt' AND content LIKE '%mention://issue/' || $2 || ')%'
	`, sessionID, parentID).Scan(&card); err != nil {
		t.Fatalf("parent receipt: %v", err)
	}
	if strings.Contains(card, secretID) || strings.Contains(card, "不该外泄") {
		t.Fatalf("workspace chat card names the secret child:\n%s", card)
	}
}

// The "last one" line names the child that closed the barrier; when a reader
// of the parent cannot see that child, the line leaves it unnamed
// (DENE-1687). A member-assigned parent gets no line at all, so the reader
// here is a member the parent alone is shared with.
func TestChildDoneLineSkipsChildHiddenFromParentReaders(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Child Done Line Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "私有父票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Child done line parent", nil)
	openID := createIssueFromChatRun(t, agentID, taskID, "Child done line open sub", map[string]any{"parent_issue_id": parentID})
	secretID := createIssueFromChatRun(t, agentID, taskID, "Child done line secret sub", map[string]any{"parent_issue_id": parentID})
	bID := createPlainMember(t, "child-done-line-b@multica.test")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	projectID := createChatProjectTestProject(t, testWorkspaceID, "Child done line project", "")
	exec(`UPDATE issue SET visibility = 'project', project_id = $2 WHERE id = ANY($1::uuid[])`, []string{parentID, openID, secretID}, projectID)
	exec(`UPDATE issue SET status = 'in_progress', assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, parentID, agentID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM resource_share WHERE resource_id = ANY($1::text[])`, []string{parentID, openID})
	})
	for _, id := range []string{parentID, openID} {
		exec(`INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, access, added_by) VALUES ($1, 'issue', $2, $3, 'view', $4)`,
			testWorkspaceID, id, bID, testUserID)
	}
	var secretNumber int
	if err := testPool.QueryRow(ctx, `SELECT number FROM issue WHERE id = $1`, secretID).Scan(&secretNumber); err != nil {
		t.Fatalf("load secret number: %v", err)
	}

	setIssueStatusForTest(t, openID, "done")
	setIssueStatusForTest(t, secretID, "done")

	var all string
	if err := testPool.QueryRow(ctx, `SELECT COALESCE(string_agg(content, E'\n---\n'), '') FROM comment WHERE issue_id = $1`, parentID).Scan(&all); err != nil {
		t.Fatalf("read parent comments: %v", err)
	}
	if !strings.Contains(all, "All sub-issues are complete — the last one, (not visible to everyone here), just finished.") {
		t.Fatalf("parent comment misses the unnamed last-child line:\n%s", all)
	}
	secretIdentifier := testHandler.getIssuePrefix(ctx, parseUUID(testWorkspaceID)) + "-" + strconv.Itoa(secretNumber)
	if strings.Contains(all, secretID) || strings.Contains(all, "secret sub") || strings.Contains(all, "["+secretIdentifier+"]") {
		t.Fatalf("parent comment names the secret child:\n%s", all)
	}
}
