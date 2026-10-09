package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestParseChatSessionRef(t *testing.T) {
	const id = "019ec09d-6222-722b-bdfa-427b105d80be"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "bare id", raw: id, want: id},
		{name: "uppercase id", raw: "019EC09D-6222-722B-BDFA-427B105D80BE", want: id},
		{name: "path url", raw: "https://app.example/acme/chat/" + id, want: id},
		{name: "path with query", raw: "https://app.example/acme/chat/" + id + "?from=copy", want: id},
		{name: "legacy query", raw: "https://app.example/acme/chat?session=" + id, want: id},
		{name: "relative path", raw: "/acme/chat/" + id, want: id},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChatSessionRef(tc.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}

	if _, err := parseChatSessionRef("https://app.example/acme/issues/not-a-session"); err == nil {
		t.Fatal("expected an error for a url with no session id")
	}
}

func TestParseChatSessionLinkRefKeepsWorkspaceSlug(t *testing.T) {
	const id = "019ec09d-6222-722b-bdfa-427b105d80be"
	ref, err := parseChatSessionLinkRef("https://app.example/acme/chat/" + id + "?from=copy")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ref.ID != id || ref.Slug != "acme" {
		t.Fatalf("ref = %+v, want id %s and slug acme", ref, id)
	}
}

func TestRunChatHandoffPostsTarget(t *testing.T) {
	const id = "019ec09d-6222-722b-bdfa-427b105d80be"
	var gotPath, gotTo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotTo = body["to"]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"id": "new-1", "title": "t"}})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_CHAT_SESSION_ID", id)

	cmd := &cobra.Command{Use: "handoff"}
	cmd.Flags().String("session", "", "")
	cmd.Flags().String("to", "", "")
	cmd.Flags().String("output", "json", "")
	if err := runChatHandoff(cmd, nil); err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("missing --to: err = %v", err)
	}
	_ = cmd.Flags().Set("to", "孙悟饭")
	if err := runChatHandoff(cmd, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotPath != "/api/chat/sessions/"+id+"/handoff" || gotTo != "孙悟饭" {
		t.Fatalf("request = %s to=%q", gotPath, gotTo)
	}
}

func TestChatTicketResultShowsReceipt(t *testing.T) {
	ticket := map[string]any{
		"identifier": "DENE-9", "status": "done", "summary": "改走新令牌",
		"pull_requests": []any{map[string]any{"number": float64(12), "url": "https://x/pull/12", "state": "merged"}},
	}
	if got := chatTicketResult(ticket); got != "改走新令牌 · PR #12 merged" {
		t.Fatalf("result = %q", got)
	}
	if got := chatTicketResult(map[string]any{"status": "in_progress"}); got != "-" {
		t.Fatalf("empty result = %q", got)
	}
}
