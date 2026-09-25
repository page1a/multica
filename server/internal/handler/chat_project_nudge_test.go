package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDismissChatSessionProjectNudge_PersistsAcrossReads(t *testing.T) {
	agentID := createHandlerTestAgent(t, "ChatNudgeAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)

	var updatedBefore time.Time
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT updated_at FROM chat_session WHERE id = $1`,
		sessionID,
	).Scan(&updatedBefore); err != nil {
		t.Fatalf("query updated_at: %v", err)
	}

	dismiss := func(body map[string]any) *httptest.ResponseRecorder {
		req := newRequest("PATCH", "/api/chat/sessions/"+sessionID+"/project-nudge", body)
		req = withURLParam(req, "sessionId", sessionID)
		req = withChatTestWorkspaceCtx(t, req)
		w := httptest.NewRecorder()
		testHandler.DismissChatSessionProjectNudge(w, req)
		return w
	}

	rejected := dismiss(map[string]any{"dismissed": false})
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("dismissed=false: expected 400, got %d: %s", rejected.Code, rejected.Body.String())
	}

	first := dismiss(map[string]any{"dismissed": true})
	if first.Code != http.StatusOK {
		t.Fatalf("dismiss: expected 200, got %d: %s", first.Code, first.Body.String())
	}
	var resp ChatSessionResponse
	if err := json.Unmarshal(first.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.ProjectNudgeDismissed {
		t.Fatal("response project_nudge_dismissed: want true")
	}

	var dismissedAt *time.Time
	var updatedAfter time.Time
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT project_nudge_dismissed_at, updated_at FROM chat_session WHERE id = $1`,
		sessionID,
	).Scan(&dismissedAt, &updatedAfter); err != nil {
		t.Fatalf("query dismissal: %v", err)
	}
	if dismissedAt == nil {
		t.Fatal("project_nudge_dismissed_at: want non-null")
	}
	if !updatedAfter.Equal(updatedBefore) {
		t.Fatalf("updated_at must not change: before %v, after %v", updatedBefore, updatedAfter)
	}

	// A second dismiss keeps the original timestamp, so the choice does not
	// look like fresh activity.
	second := dismiss(map[string]any{"dismissed": true})
	if second.Code != http.StatusOK {
		t.Fatalf("second dismiss: expected 200, got %d: %s", second.Code, second.Body.String())
	}
	var dismissedAgain *time.Time
	if err := testPool.QueryRow(
		context.Background(),
		`SELECT project_nudge_dismissed_at FROM chat_session WHERE id = $1`,
		sessionID,
	).Scan(&dismissedAgain); err != nil {
		t.Fatalf("query dismissal again: %v", err)
	}
	if dismissedAgain == nil || !dismissedAgain.Equal(*dismissedAt) {
		t.Fatalf("dismissal timestamp changed: first %v, second %v", dismissedAt, dismissedAgain)
	}
}
