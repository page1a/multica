package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func handoffReq(t *testing.T, sessionID, rawQuery, userID string) *http.Request {
	t.Helper()
	target := "/api/chat/sessions/" + sessionID + "/handoff"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-User-ID", userID)
	req = withURLParam(req, "sessionId", sessionID)
	return withChatTestWorkspaceCtx(t, req)
}

func TestGetChatSessionHandoff_SummaryAndRecentPage(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	agentID := createHandlerTestAgent(t, "ChatHandoffAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	base := time.Date(2026, 9, 24, 1, 0, 0, 0, time.UTC)
	insertChatMessageRole(t, sessionID, "user", "plan the rollout", "message", false, base)
	insertChatMessageRole(t, sessionID, "assistant", "starting with the API", "message", false, base.Add(time.Second))
	for i := 0; i < 4; i++ {
		insertChatMessageRole(t, sessionID, "user", "later "+string(rune('a'+i)), "message", false, base.Add(time.Duration(i+2)*time.Second))
	}

	w := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(w, handoffReq(t, sessionID, "limit=3", testUserID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp ChatSessionHandoffResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.SessionID != sessionID {
		t.Fatalf("session_id = %s", resp.SessionID)
	}
	if resp.MessageCount != 6 {
		t.Fatalf("message_count = %d, want 6", resp.MessageCount)
	}
	if len(resp.Messages) != 3 {
		t.Fatalf("page length = %d, want 3: %+v", len(resp.Messages), resp.Messages)
	}
	if resp.Messages[0].Text != "later b" || resp.Messages[2].Text != "later d" {
		t.Fatalf("page = %+v, want the latest 3 oldest-first", resp.Messages)
	}
	if resp.NextCursor == "" {
		t.Fatal("full page advertised no cursor")
	}
	if resp.Summary == "" || !strings.Contains(resp.Summary, "Handler Test Chat Session") || !strings.Contains(resp.Summary, "plan the rollout") || !strings.Contains(resp.Summary, "Older messages exist") {
		t.Fatalf("summary = %q", resp.Summary)
	}

	older := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(older, handoffReq(t, sessionID, "limit=3&before="+url.QueryEscape(resp.NextCursor), testUserID))
	if older.Code != http.StatusOK {
		t.Fatalf("older status = %d: %s", older.Code, older.Body.String())
	}
	var olderResp ChatSessionHandoffResponse
	if err := json.Unmarshal(older.Body.Bytes(), &olderResp); err != nil {
		t.Fatalf("decode older: %v", err)
	}
	if len(olderResp.Messages) != 3 || olderResp.Messages[0].Text != "plan the rollout" {
		t.Fatalf("older page = %+v", olderResp.Messages)
	}
	if olderResp.NextCursor != "" {
		t.Fatalf("last page advertised a cursor: %q", olderResp.NextCursor)
	}
}

func TestGetChatSessionHandoff_HidesOtherWorkspaceAndOtherOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	agentID := createHandlerTestAgent(t, "ChatHandoffDeniedAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	insertChatMessageRole(t, sessionID, "user", "secret takeover plan", "message", false, time.Date(2026, 9, 24, 2, 0, 0, 0, time.UTC))

	otherUser := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(otherUser, handoffReq(t, sessionID, "", uuid.NewString()))
	if otherUser.Code != http.StatusNotFound {
		t.Fatalf("other owner status = %d, want 404: %s", otherUser.Code, otherUser.Body.String())
	}
	if strings.Contains(otherUser.Body.String(), "secret takeover plan") {
		t.Fatalf("other owner response leaked the transcript: %s", otherUser.Body.String())
	}

	req := handoffReq(t, sessionID, "", testUserID)
	req = req.WithContext(middleware.SetMemberContext(req.Context(), uuid.NewString(), mustTestMember(t)))
	otherWorkspace := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(otherWorkspace, req)
	if otherWorkspace.Code != http.StatusNotFound {
		t.Fatalf("other workspace status = %d, want 404: %s", otherWorkspace.Code, otherWorkspace.Body.String())
	}
	if strings.Contains(otherWorkspace.Body.String(), "secret takeover plan") {
		t.Fatalf("other workspace response leaked the transcript: %s", otherWorkspace.Body.String())
	}
}

func TestGetChatSessionHandoff_TaskTokenOfOwnerCanRead(t *testing.T) {
	if testHandler == nil {
		t.Skip("requires test database")
	}
	agentID := createHandlerTestAgent(t, "ChatHandoffTaskAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	insertChatMessageRole(t, sessionID, "user", "continue the migration", "message", false, time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC))

	req := handoffReq(t, sessionID, "", testUserID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	w := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "continue the migration") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func mustTestMember(t *testing.T) db.Member {
	t.Helper()
	req := withChatTestWorkspaceCtx(t, httptest.NewRequest(http.MethodGet, "/", nil))
	member, ok := middleware.MemberFromContext(req.Context())
	if !ok {
		t.Fatal("test member missing from context")
	}
	return member
}
