package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/titling"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// recapLLM stubs the completion endpoint, answering each recap prompt by its
// system prompt and recording the user turn it was given.
type recapLLM struct {
	mu       sync.Mutex
	title    string
	topic    string
	progress string
	prompts  map[string]string
}

func (s *recapLLM) prompt(kind string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prompts[kind]
}

func withRecapLLM(t *testing.T, stub *recapLLM) {
	t.Helper()
	stub.prompts = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var system, user string
		for _, m := range body.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				user = m.Content
			}
		}
		kind, answer := "", ""
		switch system {
		case titling.ChatRecapTitleSystemPrompt:
			kind, answer = "title", stub.title
		case titling.ChatTopicSystemPrompt:
			kind, answer = "topic", stub.topic
		case titling.ChatProgressSystemPrompt:
			kind, answer = "progress", stub.progress
		}
		stub.mu.Lock()
		stub.prompts[kind] = user
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"cmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":`+jsonString(answer)+`},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	withStubLLM(t, srv)
}

// addChatTurns inserts alternating user / assistant messages, oldest first,
// one second apart so the recap's ordering is deterministic.
func addChatTurns(t *testing.T, sessionID pgtype.UUID, contents ...string) {
	t.Helper()
	for i, content := range contents {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO chat_message (chat_session_id, role, content, created_at)
			VALUES ($1, $2, $3, now() - make_interval(secs => $4))
		`, uuidToString(sessionID), role, content, len(contents)-i); err != nil {
			t.Fatalf("insert chat message: %v", err)
		}
	}
}

func loadRecapSession(t *testing.T, sessionID pgtype.UUID) db.ChatSession {
	t.Helper()
	s, err := testHandler.Queries.GetChatSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	return s
}

func TestChatRecap_FirstReplyNamesChat(t *testing.T) {
	requireDB(t)
	stub := &recapLLM{title: "Billing · retry failed invoices", progress: "Found the retry bug, fixing it"}
	withRecapLLM(t, stub)
	session := newChatTitleTestSession(t, "um so the invoices")
	addChatTurns(t, session.ID, "um so the invoices, the failed ones, can you", "I'll retry the failed invoices and fix the retry job.")

	changed, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID)
	if err != nil || !changed {
		t.Fatalf("recap: changed=%v err=%v", changed, err)
	}
	got := loadRecapSession(t, session.ID)
	if got.Title != "Billing · retry failed invoices" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.ProgressText != "Found the retry bug, fixing it" || got.ProgressSource != progress.SourceModel {
		t.Fatalf("progress = %q (%s), want the model line", got.ProgressText, got.ProgressSource)
	}
	prompt := stub.prompt("title")
	if !strings.Contains(prompt, "Opening message:\num so the invoices") || !strings.Contains(prompt, "Assistant's first reply:\nI'll retry") {
		t.Fatalf("title prompt must combine opening and reply:\n%s", prompt)
	}
}

// A title the user renamed is locked; the recap must never overwrite it.
func TestChatRecap_LockedTitleIsNotRenamed(t *testing.T) {
	requireDB(t)
	stub := &recapLLM{title: "Model Title", topic: "Drifted Title", progress: "line"}
	withRecapLLM(t, stub)
	session := newChatTitleTestSession(t, "seed")
	if _, err := testHandler.Queries.UpdateChatSessionTitle(context.Background(), db.UpdateChatSessionTitleParams{ID: session.ID, Title: "My own name"}); err != nil {
		t.Fatalf("manual rename: %v", err)
	}
	if !loadRecapSession(t, session.ID).TitleLocked {
		t.Fatal("manual rename did not lock the title")
	}
	// First reply, then a fourth reply (the topic check) — neither may rename.
	addChatTurns(t, session.ID, "fix the invoices", "On it.")
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	addChatTurns(t, session.ID, "q2", "a2", "q3", "a3", "now something else", "a4")
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	if got := loadRecapSession(t, session.ID).Title; got != "My own name" {
		t.Fatalf("locked title overwritten: %q", got)
	}
	if stub.prompt("title") != "" || stub.prompt("topic") != "" {
		t.Fatal("recap asked the model to name a locked chat")
	}
}

func TestChatRecap_TopicDriftRenamesAndKeepHolds(t *testing.T) {
	requireDB(t)
	stub := &recapLLM{topic: titling.TopicKeep, progress: "line"}
	withRecapLLM(t, stub)
	session := newChatTitleTestSession(t, "Invoices · retry")
	addChatTurns(t, session.ID, "q1", "a1", "q2", "a2", "q3", "a3", "q4", "a4")
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	if got := loadRecapSession(t, session.ID).Title; got != "Invoices · retry" {
		t.Fatalf("KEEP renamed the chat: %q", got)
	}
	stub.topic = "Invoices · export CSV"
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	if got := loadRecapSession(t, session.ID).Title; got != "Invoices · export CSV" {
		t.Fatalf("topic drift not applied: %q", got)
	}
}

func TestChatRecap_HandOverLinkNamesAfterIssue(t *testing.T) {
	requireDB(t)
	stub := &recapLLM{title: "Login redirect fix", progress: "line"}
	withRecapLLM(t, stub)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	issueID := fx.Issue(t, "登录后页面来回跳转")
	session := newChatTitleTestSession(t, "seed")
	addChatTurns(t, session.ID, "接管这个：mention://issue/"+issueID, "收到，我来接。")
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	if prompt := stub.prompt("title"); !strings.Contains(prompt, "Linked issues:") || !strings.Contains(prompt, "登录后页面来回跳转") {
		t.Fatalf("title prompt missing the linked issue:\n%s", prompt)
	}
}

// Without a model (the self-host default), the title stays as derived and the
// progress line falls back to the reply's first line.
func TestChatRecap_NoModelUsesReplyLine(t *testing.T) {
	requireDB(t)
	session := newChatTitleTestSession(t, "derived title")
	addChatTurns(t, session.ID, "please check", "## Checked the logs\n\nThe job is stuck on a lock.")
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	got := loadRecapSession(t, session.ID)
	if got.Title != "derived title" {
		t.Fatalf("title changed without a model: %q", got.Title)
	}
	if got.ProgressText != "Checked the logs" || got.ProgressSource != progress.SourceReply {
		t.Fatalf("progress = %q (%s), want the reply's first line", got.ProgressText, got.ProgressSource)
	}
}

// The agent's own `multica chat progress` line for this turn beats the recap.
func TestChatRecap_AgentLineWins(t *testing.T) {
	requireDB(t)
	withRecapLLM(t, &recapLLM{title: "T", progress: "model line"})
	session := newChatTitleTestSession(t, "seed")
	addChatTurns(t, session.ID, "do it", "done")
	if _, _, err := testHandler.recordChatProgress(context.Background(), session, progressEntry{
		Text: "agent line", Source: progress.SourceAgent, AuthorType: "agent",
	}, false, pgtype.Timestamptz{}); err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.recapChatSession(context.Background(), testWorkspaceID, session.ID); err != nil {
		t.Fatal(err)
	}
	if got := loadRecapSession(t, session.ID); got.ProgressText != "agent line" {
		t.Fatalf("recap overwrote the agent line: %q", got.ProgressText)
	}
}

func TestReplyProgressLine(t *testing.T) {
	cases := map[string]string{
		"## Heading\n\nbody":      "Heading",
		"\n\n- **first** item":    "first item",
		"```go\ncode\n```\nafter": "after",
		"plain":                   "plain",
		strings.Repeat("字", 100):  strings.Repeat("字", chatReplyProgressMax) + "…",
	}
	for in, want := range cases {
		if got := replyProgressLine(in); got != want {
			t.Errorf("replyProgressLine(%q) = %q, want %q", in, got, want)
		}
	}
}
