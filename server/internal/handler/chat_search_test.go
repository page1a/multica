package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func searchChatsAs(t *testing.T, userID, q string) []ChatMessageSearchHit {
	t.Helper()
	req := chatAs(t, userID, newRequest("GET", "/api/chat/sessions/search?q="+url.QueryEscape(q), nil))
	w := httptest.NewRecorder()
	testHandler.SearchChatMessages(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("search chats as %s: %d %s", userID, w.Code, w.Body.String())
	}
	var hits []ChatMessageSearchHit
	if err := json.Unmarshal(w.Body.Bytes(), &hits); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	return hits
}

// TestSearchChatMessages: content search finds a chat by what was said in it,
// needs every word in one message, and never surfaces a chat the viewer's
// chat list would hide.
func TestSearchChatMessages(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "ChatSearchAgent", []byte("[]"))
	memberID := insertChatPerson(t, "chat-search-member", "member")
	outsiderID := insertChatPerson(t, "chat-search-outsider", "member")

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, visibility, created_by)
		VALUES ($1, 'Chat search project', 'project', $2)
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
	`, testWorkspaceID, projectID, memberID); err != nil {
		t.Fatalf("add project member: %v", err)
	}

	createReq := chatAs(t, testUserID, newRequest("POST", "/api/chat/sessions", map[string]any{
		"agent_id":   agentID,
		"title":      "untitled",
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
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE chat_session_id = $1`, created.ID)
		testPool.Exec(context.Background(), `DELETE FROM chat_session WHERE id = $1`, created.ID)
	})
	long := strings.Repeat("padding ", 40) + "the Quokka release plan was approved" + strings.Repeat(" trailing", 40)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content) VALUES ($1, 'user', $2), ($1, 'assistant', 'unrelated reply')
	`, created.ID, long); err != nil {
		t.Fatalf("insert messages: %v", err)
	}

	hits := searchChatsAs(t, testUserID, "quokka RELEASE")
	if len(hits) != 1 || hits[0].SessionID != created.ID || hits[0].Role != "user" {
		t.Fatalf("owner hits = %+v, want the one chat", hits)
	}
	if !strings.Contains(hits[0].Snippet, "Quokka") || !strings.HasPrefix(hits[0].Snippet, "…") || !strings.HasSuffix(hits[0].Snippet, "…") {
		t.Fatalf("snippet = %q, want clipped window around the hit", hits[0].Snippet)
	}
	if got := searchChatsAs(t, memberID, "quokka"); len(got) != 1 {
		t.Fatalf("project member hits = %+v, want 1", got)
	}
	if got := searchChatsAs(t, outsiderID, "quokka"); len(got) != 0 {
		t.Fatalf("outsider hits = %+v, want none", got)
	}
	// Words must co-occur in one message.
	if got := searchChatsAs(t, testUserID, "quokka unrelated"); len(got) != 0 {
		t.Fatalf("split-word hits = %+v, want none", got)
	}
	if got := searchChatsAs(t, testUserID, "   "); len(got) != 0 {
		t.Fatalf("blank query hits = %+v, want none", got)
	}
}
