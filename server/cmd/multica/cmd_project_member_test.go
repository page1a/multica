package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// DENE-1706: `project member list` prints role and lead from the server, and
// the roster table shows each member's projects.
func TestRunProjectMemberListPrintsRoleAndLead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-123")

	const projectID = "11111111-1111-4111-8111-111111111111"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"member_id": "u-owner", "name": "Owner", "role": "owner", "is_lead": false},
			{"member_id": "u-lead", "name": "Lead", "role": "", "is_lead": true},
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("output", "table", "")
	out, err := captureStdout(t, func() error { return runProjectMemberList(cmd, []string{projectID}) })
	if err != nil {
		t.Fatalf("runProjectMemberList: %v", err)
	}
	if gotPath != "/api/projects/"+projectID+"/members" {
		t.Fatalf("path = %q", gotPath)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "owner") || !strings.Contains(lines[2], "-") || !strings.Contains(lines[2], "lead") {
		t.Fatalf("unexpected table:\n%s", out)
	}
}

func TestMemberProjectTitles(t *testing.T) {
	row := map[string]any{"projects": []any{
		map[string]any{"id": "p1", "title": "Alpha"},
		map[string]any{"id": "p2", "title": "Beta"},
	}}
	if got := memberProjectTitles(row); got != "Alpha, Beta" {
		t.Fatalf("memberProjectTitles = %q", got)
	}
	if got := memberProjectTitles(map[string]any{}); got != "" {
		t.Fatalf("no projects = %q, want empty", got)
	}
}
