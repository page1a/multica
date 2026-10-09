package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// createChatRunTask seeds a running chat task whose input is userMessage, the
// run an agent creates issues from.
func createChatRunTask(t *testing.T, agentID, sessionID, userMessage string) (taskID, messageID string) {
	t.Helper()
	ctx := context.Background()
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, chat_session_id, started_at)
		VALUES ($1, (SELECT runtime_id FROM agent WHERE id = $1), 'running', 0, $2, now())
		RETURNING id
	`, agentID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("create chat task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, 'user', $2, $3) RETURNING id
	`, sessionID, userMessage, taskID).Scan(&messageID); err != nil {
		t.Fatalf("seed chat input: %v", err)
	}
	return taskID, messageID
}

// chatTicketDescription is what a chat-opened issue must carry (DENE-1665).
const chatTicketDescription = "## 目标\n修好登录\n\n## 验收\n能登录"

func createIssueFromChatRun(t *testing.T, agentID, taskID, title string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"title": title, "status": "todo", "description": chatTicketDescription}
	for k, v := range extra {
		body[k] = v
	}
	req := asTaskAgent(newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body), agentID)
	req.Header.Set("X-Task-ID", taskID)
	w := httptest.NewRecorder()
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID) })
	return created.ID
}

func setIssueStatusForTest(t *testing.T, issueID, status string) {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": status}), "id", issueID))
	if w.Code != http.StatusOK {
		t.Fatalf("update to %s: %d %s", status, w.Code, w.Body.String())
	}
}

func countReceipts(t *testing.T, sessionID, issueID string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_message
		WHERE chat_session_id = $1 AND message_kind = 'issue_receipt' AND content LIKE '%mention://issue/' || $2 || '%'
	`, sessionID, issueID).Scan(&n); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	return n
}

func TestChatTicketPostsReceiptAndQuotesItsSource(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Receipt Chat Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "把登录改成新令牌")
	issueID := createIssueFromChatRun(t, agentID, taskID, "Receipt dispatched task", nil)

	// The state card names the chat and quotes what was asked.
	card := getIssueContextHTTP(t, issueID, "", "")
	if card.Source == nil || card.Source.ChatSessionID != sessionID || card.Source.Excerpt != "把登录改成新令牌" {
		t.Fatalf("card source = %+v", card.Source)
	}
	if !strings.Contains(card.Text, "来源：聊天「Handler Test Chat Session」") {
		t.Fatalf("card text misses the source line:\n%s", card.Text)
	}

	// Moving to in_progress is not reportable; done is, once.
	setIssueStatusForTest(t, issueID, "in_progress")
	if n := countReceipts(t, sessionID, issueID); n != 0 {
		t.Fatalf("receipts after in_progress = %d", n)
	}
	setIssueStatusForTest(t, issueID, "done")
	if n := countReceipts(t, sessionID, issueID); n != 1 {
		t.Fatalf("receipts after done = %d", n)
	}

	// The chat page carries it as a receipt card.
	page := fetchChatMessagesPageForTest(t, sessionID, nil)
	var receiptMsg *ChatMessageResponse
	for i := range page.Messages {
		if page.Messages[i].MessageKind == "issue_receipt" {
			receiptMsg = &page.Messages[i]
		}
	}
	if receiptMsg == nil || !strings.Contains(receiptMsg.Content, "已完成") {
		t.Fatalf("receipt card in chat page = %+v", receiptMsg)
	}

	// The next chat turn opens with the ticket and where it stands.
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	lines := testHandler.chatDispatchedLines(ctx, session)
	if len(lines) != 1 || !strings.Contains(lines[0], "已完成") {
		t.Fatalf("dispatched lines = %q", lines)
	}
}

func TestSubIssueOfChatTicketLeavesTheReceiptToItsParent(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Receipt Child Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "拆三张子票")
	parentID := createIssueFromChatRun(t, agentID, taskID, "Receipt parent", nil)
	childID := createIssueFromChatRun(t, agentID, taskID, "Receipt child", map[string]any{"parent_issue_id": parentID})

	setIssueStatusForTest(t, childID, "done")
	if n := countReceipts(t, sessionID, childID); n != 0 {
		t.Fatalf("child receipts = %d, want 0: the parent reports", n)
	}
}

func TestAlignmentIssueRecordsItsChat(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Receipt Draft Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	res, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID: parseUUID(testWorkspaceID), Title: "Receipt alignment issue",
		Status: "todo", Priority: "none", CreatorType: "member", CreatorID: parseUUID(testUserID),
		OriginType: pgtype.Text{String: "issue_draft", Valid: true}, OriginID: parseUUID(sessionID),
	}, service.IssueCreateOpts{ActorID: testUserID})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, res.Issue.ID) })
	if uuidToString(res.Issue.OriginChatSessionID) != sessionID {
		t.Fatalf("origin chat = %s, want %s", uuidToString(res.Issue.OriginChatSessionID), sessionID)
	}
}

// A person who can see the issue but not the private chat it came from gets
// no source: no title, no quote, no message id. The run brief reads with the
// eyes of the person the run acts for.
func TestStateCardSourceFollowsChatAccess(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Receipt Private Chat Agent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID, _ := createChatRunTask(t, agentID, sessionID, "私聊里的原话")
	issueID := createIssueFromChatRun(t, agentID, taskID, "Receipt private source", nil)
	otherID := createPermissionTestMember(t, "receipt-outsider@multica.test")

	contextAs := func(userID string) IssueContextResponse {
		t.Helper()
		req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/context", nil), "id", issueID)
		req.Header.Set("X-User-ID", userID)
		rec := httptest.NewRecorder()
		testHandler.GetIssueContext(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("context as %s = %d %s", userID, rec.Code, rec.Body.String())
		}
		var resp IssueContextResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	if card := contextAs(testUserID); card.Source == nil || card.Source.Excerpt != "私聊里的原话" {
		t.Fatalf("owner source = %+v", card.Source)
	}
	outsider := contextAs(otherID)
	if outsider.Source != nil {
		t.Fatalf("outsider sees the private chat: %+v", outsider.Source)
	}
	if strings.Contains(outsider.Text, "私聊里的原话") || strings.Contains(outsider.Text, "Handler Test Chat Session") {
		t.Fatalf("outsider text leaks the chat:\n%s", outsider.Text)
	}

	issue, err := testHandler.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	runFor := func(originator string) sourceViewer {
		return taskSourceViewer(db.AgentTaskQueue{OriginatorUserID: parseUUID(originator)})
	}
	if testHandler.stateCardSource(ctx, issue, runFor(otherID)) != nil {
		t.Fatal("a run acting for the outsider sees the private chat")
	}
	if testHandler.stateCardSource(ctx, issue, sourceViewer{}) != nil {
		t.Fatal("a run with no originator sees the private chat")
	}
	if testHandler.stateCardSource(ctx, issue, runFor(testUserID)) == nil {
		t.Fatal("a run acting for the owner lost the source")
	}
	if testHandler.stateCardSource(ctx, issue, sourceViewer{OwnChat: parseUUID(sessionID)}) == nil {
		t.Fatal("the chat's own run lost the source")
	}

	// Shared with the workspace, the same person reads it.
	if _, err := testPool.Exec(ctx, `UPDATE chat_session SET visibility = 'workspace' WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("share chat: %v", err)
	}
	if card := contextAs(otherID); card.Source == nil || card.Source.Excerpt != "私聊里的原话" {
		t.Fatalf("shared viewer source = %+v", card.Source)
	}
}
