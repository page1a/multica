package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newChatProgressTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "progress"}
	cmd.Flags().String("session", "", "")
	cmd.Flags().String("output", "json", "")
	cmd.Flags().String("tone", "", "")
	cmd.Flags().Bool("history", false, "")
	return cmd
}

func TestChatProgressSendsToneAndReadsHistory(t *testing.T) {
	const session = "11111111-2222-3333-4444-555555555555"
	var method string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat/sessions/"+session+"/progress" {
			http.NotFound(w, r)
			return
		}
		method = r.Method
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": session})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"progress": []map[string]any{{"text": "old", "tone": "working"}}})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_CHAT_SESSION_ID", session)

	cmd := newChatProgressTestCmd()
	_ = cmd.Flags().Set("tone", "waiting")
	if err := runChatProgress(cmd, []string{"need the key"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if method != http.MethodPost || body["text"] != "need the key" || body["tone"] != "waiting" {
		t.Fatalf("report sent %s %v", method, body)
	}

	cmd = newChatProgressTestCmd()
	_ = cmd.Flags().Set("history", "true")
	if err := runChatProgress(cmd, nil); err != nil {
		t.Fatalf("history: %v", err)
	}
	if method != http.MethodGet {
		t.Fatalf("history used %s", method)
	}

	if err := runChatProgress(newChatProgressTestCmd(), nil); err == nil {
		t.Fatal("report without text should fail")
	}
}
