package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// chatAs sends the request as a workspace member other than the fixture owner.
func chatAs(t *testing.T, userID string, req *http.Request) *http.Request {
	t.Helper()
	req.Header.Set("X-User-ID", userID)
	memberRow, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load member %s: %v", userID, err)
	}
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, memberRow))
}

func insertChatPerson(t *testing.T, name, role string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	email := fmt.Sprintf("%s-%d@multica.test", name, time.Now().UnixNano())
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, name, email).Scan(&id); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, testWorkspaceID, id, role); err != nil {
		t.Fatalf("insert member %s: %v", name, err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, id)
		testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, id)
	})
	return id
}

func listChatsAs(t *testing.T, userID string) []ChatSessionResponse {
	t.Helper()
	req := chatAs(t, userID, newRequest("GET", "/api/chat/sessions", nil))
	w := httptest.NewRecorder()
	testHandler.ListChatSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list chats as %s: %d %s", userID, w.Code, w.Body.String())
	}
	var resp []ChatSessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode chat list: %v", err)
	}
	return resp
}

func chatListed(sessions []ChatSessionResponse, id string) (ChatSessionResponse, bool) {
	for _, session := range sessions {
		if session.ID == id {
			return session, true
		}
	}
	return ChatSessionResponse{}, false
}

// TestChatProjectSharing covers the DENE-840 acceptance: a project member can
// see and speak, a private chat disappears, an extra person can be view-only,
// the two people's unread counts diverge, and the launch notice fires once.
func TestChatProjectSharing(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "ChatShareAgent", []byte("[]"))
	bID := insertChatPerson(t, "chat-share-b", "member")
	cID := insertChatPerson(t, "chat-share-c", "member")

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, visibility, created_by)
		VALUES ($1, 'Chat share project', 'project', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project_member WHERE project_id = $1`, projectID)
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_member (workspace_id, project_id, member_id) VALUES ($1, $2, $3)
	`, testWorkspaceID, projectID, bID); err != nil {
		t.Fatalf("add project member: %v", err)
	}

	createReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions", map[string]any{
		"agent_id":   agentID,
		"title":      "shared chat",
		"project_id": projectID,
	}))
	createW := httptest.NewRecorder()
	testHandler.CreateChatSession(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", createW.Code, createW.Body.String())
	}
	var created ChatSessionResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	sessionID := created.ID
	if created.Visibility != "project" || created.Access != "owner" {
		t.Fatalf("creator session = visibility %q access %q", created.Visibility, created.Access)
	}

	bList := listChatsAs(t, bID)
	bSession, ok := chatListed(bList, sessionID)
	if !ok {
		t.Fatal("project member B cannot see A's project chat")
	}
	if bSession.Access != "speak" {
		t.Fatalf("B access = %q, want speak", bSession.Access)
	}

	sendReq := chatAs(t, bID, newRequest("POST", "/api/chat/sessions/"+sessionID+"/messages", map[string]any{
		"content": "from B",
	}))
	sendReq = withURLParam(sendReq, "sessionId", sessionID)
	sendW := httptest.NewRecorder()
	testHandler.SendChatMessage(sendW, sendReq)
	if sendW.Code != http.StatusCreated {
		t.Fatalf("B send: %d %s", sendW.Code, sendW.Body.String())
	}
	var sendResp SendChatMessageResponse
	if err := json.Unmarshal(sendW.Body.Bytes(), &sendResp); err != nil {
		t.Fatalf("decode send: %v", err)
	}
	var sender, originator string
	if err := testPool.QueryRow(ctx, `SELECT sender_user_id::text FROM chat_message WHERE id = $1`, sendResp.MessageID).Scan(&sender); err != nil {
		t.Fatalf("load sender: %v", err)
	}
	if sender != bID {
		t.Fatalf("message sender = %s, want B %s", sender, bID)
	}
	if err := testPool.QueryRow(ctx, `SELECT originator_user_id::text FROM agent_task_queue WHERE id = $1`, sendResp.TaskID).Scan(&originator); err != nil {
		t.Fatalf("load task originator: %v", err)
	}
	if originator != testUserID {
		t.Fatalf("task originator = %s, want creator %s", originator, testUserID)
	}

	// Both people have a cursor. A new reply after that is unread only for
	// whoever has not marked it read.
	_ = listChatsAs(t, testUserID)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'assistant', 'reply')
	`, sessionID); err != nil {
		t.Fatalf("insert assistant reply: %v", err)
	}
	readReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions/"+sessionID+"/read", nil))
	readReq = withURLParam(readReq, "sessionId", sessionID)
	readW := httptest.NewRecorder()
	testHandler.MarkChatSessionRead(readW, readReq)
	if readW.Code != http.StatusNoContent {
		t.Fatalf("A mark read: %d %s", readW.Code, readW.Body.String())
	}
	aSession, ok := chatListed(listChatsAs(t, testUserID), sessionID)
	if !ok {
		t.Fatal("creator lost their own chat")
	}
	bAfter, ok := chatListed(listChatsAs(t, bID), sessionID)
	if !ok {
		t.Fatal("B lost the chat before it was made private")
	}
	if aSession.UnreadCount != 0 {
		t.Fatalf("A unread = %d after marking read, want 0", aSession.UnreadCount)
	}
	if bAfter.UnreadCount == 0 {
		t.Fatal("B unread was cleared by A's read")
	}

	noticeReq := chatAs(t, testUserID, newRequest("GET", "/api/chat/visibility-notice", nil))
	noticeW := httptest.NewRecorder()
	testHandler.GetChatVisibilityNotice(noticeW, noticeReq)
	if noticeW.Code != http.StatusOK {
		t.Fatalf("notice: %d %s", noticeW.Code, noticeW.Body.String())
	}
	var notice chatVisibilityNoticeResponse
	if err := json.Unmarshal(noticeW.Body.Bytes(), &notice); err != nil {
		t.Fatalf("decode notice: %v", err)
	}
	if !notice.Pending || notice.Count < 1 {
		t.Fatalf("notice = pending %v count %d, want a pending count", notice.Pending, notice.Count)
	}
	dismissReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/visibility-notice/dismiss", map[string]any{}))
	dismissW := httptest.NewRecorder()
	testHandler.DismissChatVisibilityNotice(dismissW, dismissReq)
	if dismissW.Code != http.StatusNoContent {
		t.Fatalf("dismiss: %d %s", dismissW.Code, dismissW.Body.String())
	}
	noticeW = httptest.NewRecorder()
	testHandler.GetChatVisibilityNotice(noticeW, chatAs(t, testUserID, newRequest("GET", "/api/chat/visibility-notice", nil)))
	if err := json.Unmarshal(noticeW.Body.Bytes(), &notice); err != nil {
		t.Fatalf("decode notice after dismiss: %v", err)
	}
	if notice.Pending || notice.Count != 0 {
		t.Fatalf("notice after dismiss = pending %v count %d", notice.Pending, notice.Count)
	}

	putReq := chatAs(t, testUserID, newRequest("PUT", "/api/chat/sessions/"+sessionID+"/access", map[string]any{
		"mode": "extra",
		"shares": []map[string]string{
			{"user_id": cID, "access": "view"},
		},
	}))
	putReq = withURLParam(putReq, "sessionId", sessionID)
	putW := httptest.NewRecorder()
	testHandler.PutChatSessionAccess(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("add view-only C: %d %s", putW.Code, putW.Body.String())
	}
	cGet := chatAs(t, cID, newRequest("GET", "/api/chat/sessions/"+sessionID, nil))
	cGet = withURLParam(cGet, "sessionId", sessionID)
	cGetW := httptest.NewRecorder()
	testHandler.GetChatSession(cGetW, cGet)
	if cGetW.Code != http.StatusOK {
		t.Fatalf("C get shared chat: %d %s", cGetW.Code, cGetW.Body.String())
	}
	var cSession ChatSessionResponse
	if err := json.Unmarshal(cGetW.Body.Bytes(), &cSession); err != nil {
		t.Fatalf("decode C session: %v", err)
	}
	if cSession.Access != "view" {
		t.Fatalf("C access = %q, want view", cSession.Access)
	}
	cSend := chatAs(t, cID, newRequest("POST", "/api/chat/sessions/"+sessionID+"/messages", map[string]any{
		"content": "C should not send",
	}))
	cSend = withURLParam(cSend, "sessionId", sessionID)
	cSendW := httptest.NewRecorder()
	testHandler.SendChatMessage(cSendW, cSend)
	if cSendW.Code != http.StatusForbidden {
		t.Fatalf("C send: %d %s, want 403", cSendW.Code, cSendW.Body.String())
	}

	privReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions/make-private", map[string]any{
		"session_ids": []string{sessionID},
	}))
	privW := httptest.NewRecorder()
	testHandler.MakeChatSessionsPrivate(privW, privReq)
	if privW.Code != http.StatusOK {
		t.Fatalf("make private: %d %s", privW.Code, privW.Body.String())
	}
	if _, ok := chatListed(listChatsAs(t, bID), sessionID); ok {
		t.Fatal("B still lists the chat after it was made private")
	}
	if _, ok := chatListed(listChatsAs(t, cID), sessionID); ok {
		t.Fatal("C still lists the chat after it was made private")
	}
	bGet := chatAs(t, bID, newRequest("GET", "/api/chat/sessions/"+sessionID, nil))
	bGet = withURLParam(bGet, "sessionId", sessionID)
	bGetW := httptest.NewRecorder()
	testHandler.GetChatSession(bGetW, bGet)
	if bGetW.Code != http.StatusNotFound {
		t.Fatalf("B direct get after private: %d %s, want 404", bGetW.Code, bGetW.Body.String())
	}
}

// TestBindingUnboundChatFollowsProject: an unbound chat is private. Binding
// the first project promotes it to project visibility, so a project member
// can see it and speak. Creating the chat already bound does the same thing.
func TestBindingUnboundChatFollowsProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "ChatBindAgent", []byte("[]"))
	bID := insertChatPerson(t, "chat-bind-b", "member")

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, visibility, created_by)
		VALUES ($1, 'Chat bind project', 'project', $2)
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project_member WHERE project_id = $1`, projectID)
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	if _, err := testPool.Exec(ctx, `
		INSERT INTO project_member (workspace_id, project_id, member_id) VALUES ($1, $2, $3)
	`, testWorkspaceID, projectID, bID); err != nil {
		t.Fatalf("add project member: %v", err)
	}

	createReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions", map[string]any{
		"agent_id": agentID,
		"title":    "bind later",
	}))
	createW := httptest.NewRecorder()
	testHandler.CreateChatSession(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create chat: %d %s", createW.Code, createW.Body.String())
	}
	var created ChatSessionResponse
	if err := json.Unmarshal(createW.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Visibility != "private" {
		t.Fatalf("unbound visibility = %q, want private", created.Visibility)
	}
	if _, ok := chatListed(listChatsAs(t, bID), created.ID); ok {
		t.Fatal("B can see an unbound chat")
	}

	patchReq := chatAs(t, testUserID, newRequest("PATCH", "/api/chat/sessions/"+created.ID, map[string]any{
		"project_id": projectID,
	}))
	patchReq = withURLParam(patchReq, "sessionId", created.ID)
	patchW := httptest.NewRecorder()
	testHandler.UpdateChatSession(patchW, patchReq)
	if patchW.Code != http.StatusOK {
		t.Fatalf("bind project: %d %s", patchW.Code, patchW.Body.String())
	}
	var bound ChatSessionResponse
	if err := json.Unmarshal(patchW.Body.Bytes(), &bound); err != nil {
		t.Fatalf("decode bind: %v", err)
	}
	if bound.Visibility != "project" {
		t.Fatalf("bound visibility = %q, want project", bound.Visibility)
	}
	bSession, ok := chatListed(listChatsAs(t, bID), created.ID)
	if !ok {
		t.Fatal("project member B cannot see the chat after it was bound")
	}
	if bSession.Access != "speak" {
		t.Fatalf("B access = %q, want speak", bSession.Access)
	}
}
