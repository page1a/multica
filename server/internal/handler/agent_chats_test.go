package handler

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestListAgentChats_BusyFirstAndRedactsChatsTheViewerCannotOpen(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	runtimeID := dbfx.Runtime(t, "agent chats runtime")
	agentID := dbfx.Agent(t, "agent chats agent", runtimeID)
	otherUserID := dbfx.User(t, "Agent Chats Other", fmt.Sprintf("agent-chats-%d@example.com", time.Now().UnixNano()), nil)
	dbfx.Member(t, testWorkspaceID, otherUserID, "member")

	base := time.Now().UTC().Add(-time.Hour)
	chat := func(creator, title, visibility string, updated time.Time) string {
		return dbfx.Insert(t, "chat_session", testutil.Cols{
			"workspace_id": testWorkspaceID, "agent_id": agentID, "creator_id": creator,
			"title": title, "status": "active", "visibility": visibility,
			"explicitly_created_at": updated, "updated_at": updated,
		})
	}
	mineIdle := chat(testUserID, "My idle chat", "private", base.Add(30*time.Minute))
	mineBusy := chat(testUserID, "My busy chat", "private", base)
	secret := chat(otherUserID, "Secret takeover plan", "private", base.Add(20*time.Minute))
	shared := chat(otherUserID, "Open workspace topic", "workspace", base.Add(10*time.Minute))
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "running", "chat_session_id": mineBusy})
	dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "queued", "chat_session_id": secret})

	read := func(query string) ListAgentChatsResponse {
		t.Helper()
		req := withURLParam(newRequest(http.MethodGet, "/api/agents/"+agentID+"/chats"+query, nil), "id", agentID)
		var resp ListAgentChatsResponse
		response := testutil.Call(t, testHandler.ListAgentChats, req).Want(http.StatusOK)
		if strings.Contains(response.Text(), "Secret takeover plan") {
			t.Fatalf("response leaked another member's private title: %s", response.Text())
		}
		response.JSON(&resp)
		return resp
	}

	resp := read("?limit=10")
	if resp.HasMore || len(resp.Chats) != 4 {
		t.Fatalf("chats = %+v, has_more = %v", resp.Chats, resp.HasMore)
	}
	wantOrder := []string{mineBusy, secret, mineIdle, shared}
	for i, id := range wantOrder {
		if resp.Chats[i].ID != id {
			t.Fatalf("chat %d = %s, want %s (busy first, then latest activity)", i, resp.Chats[i].ID, id)
		}
	}
	if c := resp.Chats[0]; c.Status != "running" || !c.Visible || c.Title == nil || *c.Title != "My busy chat" || c.CreatorID == nil || *c.CreatorID != testUserID {
		t.Fatalf("own busy chat = %+v", c)
	}
	if c := resp.Chats[1]; c.Status != "queued" || c.Visible || c.Title != nil || c.CreatorID != nil {
		t.Fatalf("other member's private chat = %+v, want redacted", c)
	}
	if c := resp.Chats[2]; c.Status != "idle" || !c.Visible {
		t.Fatalf("own idle chat = %+v", c)
	}
	if c := resp.Chats[3]; !c.Visible || c.Title == nil || *c.Title != "Open workspace topic" {
		t.Fatalf("workspace chat = %+v, want visible", c)
	}

	page := read("?limit=1")
	if !page.HasMore || len(page.Chats) != 1 || page.Chats[0].ID != mineBusy {
		t.Fatalf("limit=1 page = %+v has_more=%v", page.Chats, page.HasMore)
	}
}
