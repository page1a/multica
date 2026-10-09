package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func TestUseLinkedWorkspace(t *testing.T) {
	var linkedHeader, workspaceHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace-links" {
			w.Write([]byte(`{"links":[
				{"id":"l1","side":"viewer","status":"active","managed":true,"source":{"id":"src-1","slug":"wd-game"}},
				{"id":"l2","side":"viewer","status":"active","managed":false,"source":{"id":"src-2","slug":"read-only"}},
				{"id":"l3","side":"source","status":"active","managed":true,"source":{"id":"self","slug":"mine"}},
				{"id":"l4","side":"viewer","status":"pending","managed":true,"source":{"id":"src-4","slug":"pending"}}]}`))
			return
		}
		linkedHeader, workspaceHeader = r.Header.Get("X-Linked-Workspace"), r.Header.Get("X-Workspace-ID")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, c := range []struct{ ref, want string }{
		{"read-only", "--managed on"},
		{"mine", "no active link"},
		{"pending", "no active link"},
		{"nowhere", "no active link"},
	} {
		err := useLinkedWorkspace(cli.NewAPIClient(srv.URL, "viewer", "mat_x"), c.ref)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.ref, err, c.want)
		}
	}

	for _, ref := range []string{"wd-game", "src-1"} {
		client := cli.NewAPIClient(srv.URL, "viewer", "mat_x")
		if err := useLinkedWorkspace(client, ref); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		var out map[string]any
		if err := client.GetJSON(context.Background(), "/api/issues", &out); err != nil {
			t.Fatal(err)
		}
		if linkedHeader != "src-1" || workspaceHeader != "src-1" {
			t.Fatalf("%s: sent X-Linked-Workspace=%q X-Workspace-ID=%q", ref, linkedHeader, workspaceHeader)
		}
	}
}

// TestLinkedFlagOnManagedCommands pins --linked on every command whose
// routes the managed link opens, so a new subcommand cannot quietly miss it.
func TestLinkedFlagOnManagedCommands(t *testing.T) {
	for _, path := range [][]string{
		{"issue", "list"}, {"issue", "get"}, {"issue", "children"}, {"issue", "create"},
		{"issue", "update"}, {"issue", "assign"}, {"issue", "status"}, {"issue", "search"},
		{"issue", "comment", "list"}, {"issue", "comment", "add"},
		{"issue", "label", "list"}, {"issue", "label", "add"}, {"issue", "label", "remove"},
		{"issue", "property", "list"}, {"issue", "property", "set"}, {"issue", "property", "unset"},
		{"autopilot", "list"}, {"autopilot", "get"}, {"autopilot", "create"}, {"autopilot", "update"},
		{"autopilot", "delete"}, {"autopilot", "trigger"}, {"autopilot", "runs"}, {"autopilot", "linked-changes"},
		{"autopilot", "trigger-add"}, {"autopilot", "trigger-list"}, {"autopilot", "trigger-update"}, {"autopilot", "trigger-delete"},
	} {
		cmd, _, err := rootCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Errorf("%v: command not found", path)
			continue
		}
		if cmd.Flags().Lookup(linkedFlag) == nil {
			t.Errorf("multica %s has no --%s", strings.Join(path, " "), linkedFlag)
		}
	}
}

// TestIssuePropertyCommandsGoToLinkedSource runs issue property list, set and
// unset with --linked and checks every request after the link lookup is
// addressed to the source workspace and carries the linked header.
func TestIssuePropertyCommandsGoToLinkedSource(t *testing.T) {
	type call struct{ method, path, workspace, linked string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/workspace-links" {
			w.Write([]byte(`{"links":[{"id":"l1","side":"viewer","status":"active","managed":true,"source":{"id":"src-1","slug":"wd-game"}}]}`))
			return
		}
		calls = append(calls, call{r.Method, r.URL.Path, r.Header.Get("X-Workspace-ID"), r.Header.Get("X-Linked-Workspace")})
		switch {
		case r.URL.Path == "/api/properties":
			json.NewEncoder(w).Encode(map[string]any{"properties": resolvePropertiesTestCatalog()})
		case r.URL.Path == "/api/issues/MUL-1":
			json.NewEncoder(w).Encode(map[string]any{"id": "issue-1", "identifier": "MUL-1"})
		case r.URL.Path == "/api/issues/issue-1":
			json.NewEncoder(w).Encode(testIssue("issue-1", "MUL-1", nil))
		case r.URL.Path == "/api/issues/issue-1/properties/"+testReviewerDefID && r.Method == http.MethodPut:
			json.NewEncoder(w).Encode(map[string]any{"properties": map[string]any{testReviewerDefID: "member:" + testMemberAdaID}})
		case r.URL.Path == "/api/issues/issue-1/properties/"+testReviewerDefID && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/workspaces/src-1/members":
			json.NewEncoder(w).Encode([]map[string]any{{"user_id": testMemberAdaID, "name": "Ada"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "viewer-ws")
	t.Setenv("MULTICA_TOKEN", "test-token")

	for _, c := range []struct {
		cmd   *cobra.Command
		run   func(*cobra.Command, []string) error
		flags map[string]string
		want  string
	}{
		{issuePropertyListCmd, runIssuePropertyList, nil, "GET /api/issues/issue-1"},
		{issuePropertySetCmd, runIssuePropertySet, map[string]string{"name": "Reviewer", "value": "Ada"}, "PUT /api/issues/issue-1/properties/" + testReviewerDefID},
		{issuePropertyUnsetCmd, runIssuePropertyUnset, map[string]string{"name": "Reviewer"}, "DELETE /api/issues/issue-1/properties/" + testReviewerDefID},
	} {
		calls = nil
		cmd := &cobra.Command{Use: c.cmd.Name()}
		cmd.Flags().AddFlagSet(c.cmd.LocalFlags())
		flags := map[string]string{"linked": "wd-game", "output": "json"}
		for k, v := range c.flags {
			flags[k] = v
		}
		for k, v := range flags {
			if err := cmd.Flags().Set(k, v); err != nil {
				t.Fatalf("%s --%s: %v", c.cmd.Name(), k, err)
			}
		}
		t.Cleanup(func() {
			for k := range flags {
				f := c.cmd.Flags().Lookup(k)
				f.Value.Set(f.DefValue)
				f.Changed = false
			}
		})
		if _, err := captureStdout(t, func() error { return c.run(cmd, []string{"MUL-1"}) }); err != nil {
			t.Fatalf("%s: %v", c.cmd.Name(), err)
		}
		hit := false
		for _, got := range calls {
			if got.workspace != "src-1" || got.linked != "src-1" {
				t.Errorf("%s: %s %s went to workspace %q linked %q, want the source", c.cmd.Name(), got.method, got.path, got.workspace, got.linked)
			}
			hit = hit || got.method+" "+got.path == c.want
		}
		if !hit {
			t.Errorf("%s: no %s among %v", c.cmd.Name(), c.want, calls)
		}
	}
}
