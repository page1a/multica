package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// DENE-1019: `multica inbox board --project` turns an id, an id prefix or a
// name into project_id on the board request.
func TestInboxBoardProjectFlag(t *testing.T) {
	const projA = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const projB = "22222222-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	var gotProject string
	var boardCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{
				{"id": projA, "title": "Multica 魔改", "status": "in_progress"},
				{"id": projB, "title": "Other", "status": "planned"},
			}})
		case "/api/inbox/board":
			boardCalls++
			gotProject = r.URL.Query().Get("project_id")
			_ = json.NewEncoder(w).Encode(map[string]any{"waiting": []any{}, "viewer_id": "u"})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	run := func(project string) error {
		cmd := &cobra.Command{}
		cmd.PersistentFlags().String("profile", "", "")
		cmd.Flags().String("tz", "UTC", "")
		cmd.Flags().String("project", "", "")
		cmd.Flags().String("output", "json", "")
		cmd.SetOut(&bytes.Buffer{})
		if project != "" {
			_ = cmd.Flags().Set("project", project)
		}
		return runInboxBoard(cmd, nil)
	}

	for _, tc := range []struct{ ref, want string }{
		{"", ""},
		{projB, projB},
		{"multica 魔改", projA},
		{"11111111", projA},
	} {
		gotProject = "unset"
		if err := run(tc.ref); err != nil {
			t.Fatalf("--project %q: %v", tc.ref, err)
		}
		if gotProject != tc.want {
			t.Fatalf("--project %q sent project_id=%q, want %q", tc.ref, gotProject, tc.want)
		}
	}

	before := boardCalls
	err := run("no such project")
	if err == nil || !strings.Contains(err.Error(), "no project named") {
		t.Fatalf("unknown project: err = %v", err)
	}
	if boardCalls != before {
		t.Fatalf("an unresolved project must not read the board")
	}
}

func TestInboxDismissPostsIssueAndReason(t *testing.T) {
	const issueID = "11111111-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/inbox/issues/"+issueID+"/dismiss" {
			t.Fatalf("unexpected request %s", r.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotReason = body["reason"]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issue_id": issueID,
			"reason":   gotReason,
		})
	}))
	defer srv.Close()
	setCopyTestEnv(t, srv.URL)

	cmd := &cobra.Command{}
	cmd.PersistentFlags().String("profile", "", "")
	cmd.Flags().String("reason", "已在别处定案", "")
	cmd.Flags().String("output", "json", "")
	if err := runInboxDismiss(cmd, []string{issueID}); err != nil {
		t.Fatalf("runInboxDismiss: %v", err)
	}
	if gotReason != "已在别处定案" {
		t.Fatalf("reason = %q", gotReason)
	}
}
