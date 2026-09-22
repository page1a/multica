package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const routingProjectsTestWorkspace = "55555555-5555-5555-5555-555555555555"

func newRoutingProjectsTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "routing-projects"}
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

// routingProjectsServer serves one workspace's settings and records the PATCH.
func routingProjectsServer(t *testing.T, settings map[string]any, patched *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/"+routingProjectsTestWorkspace {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode patch body: %v", err)
			}
			*patched = body
		}
		json.NewEncoder(w).Encode(map[string]any{"id": routingProjectsTestWorkspace, "settings": settings})
	}))
	t.Cleanup(srv.Close)
	// A fresh cwd keeps a daemon-task marker out of the ancestry, so the CLI
	// accepts the plain test token.
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", routingProjectsTestWorkspace)
	return srv
}

func TestRoutingProjectsSetKeepsEverythingElseInSettings(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"other": "untouched",
		"routing": map[string]any{
			"enabled": true, "model": "jev-1", "confidence_threshold": 0.7,
			"projects": map[string]any{"tarot": "出海"},
		},
	}, &patched)

	if err := runWorkspaceRoutingProjectsSet(newRoutingProjectsTestCmd(), []string{"Multica 魔改", "通用"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	settings, _ := patched["settings"].(map[string]any)
	if settings["other"] != "untouched" {
		t.Errorf("unrelated settings were dropped: %v", settings)
	}
	block, _ := settings["routing"].(map[string]any)
	if block["enabled"] != true || block["model"] != "jev-1" {
		t.Errorf("routing switch/model were not carried through: %v", block)
	}
	// Omitted, not empty: an empty api_key is the explicit "clear the key".
	if _, present := block["api_key"]; present {
		t.Errorf("write carries api_key and would clear the stored routing key: %v", block)
	}
	projects, _ := block["projects"].(map[string]any)
	if projects["tarot"] != "出海" || projects["Multica 魔改"] != "通用" {
		t.Errorf("projects = %v, want the old row kept and the new one added", projects)
	}
}

func TestRoutingProjectsSetRejectsADirectionTheLadderDoesNotHave(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{}, &patched)

	err := runWorkspaceRoutingProjectsSet(newRoutingProjectsTestCmd(), []string{"tarot", "出海海"})
	if err == nil || !strings.Contains(err.Error(), "unknown direction") {
		t.Fatalf("err = %v, want an unknown-direction error", err)
	}
	if patched != nil {
		t.Errorf("a rejected row still reached the server: %v", patched)
	}
}

func TestRoutingProjectsListMergesWorkspaceRowsOverDefaults(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{"projects": map[string]any{"game-relay": "出海"}},
	}, &patched)

	out, err := captureStdout(t, func() error {
		return runWorkspaceRoutingProjectsList(newRoutingProjectsTestCmd(), nil)
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got struct {
		Projects []struct{ Project, Direction, Source string } `json:"projects"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	seen := map[string]string{}
	for _, p := range got.Projects {
		if _, dup := seen[p.Project]; dup {
			t.Errorf("project %q listed twice", p.Project)
		}
		seen[p.Project] = p.Direction + "/" + p.Source
	}
	if seen["game-relay"] != "出海/workspace" || seen["game"] != "游戏/default" {
		t.Errorf("rows = %v", seen)
	}
}

// Matching is case-insensitive in the router, so two rows that differ only in
// case are one row with two spellings — and the router would pick between them
// at random. Re-setting replaces, and unset finds, whatever case was typed.
func TestRoutingProjectsSetReplacesARowTypedInAnotherCase(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{"projects": map[string]any{"Tarot": "出海"}},
	}, &patched)

	if err := runWorkspaceRoutingProjectsSet(newRoutingProjectsTestCmd(), []string{"tarot", "通用"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	projects, _ := block["projects"].(map[string]any)
	if len(projects) != 1 || projects["tarot"] != "通用" {
		t.Errorf("projects = %v, want the single row replaced", projects)
	}
}

func TestRoutingProjectsUnsetFindsARowTypedInAnotherCase(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{"projects": map[string]any{"Tarot": "出海"}},
	}, &patched)

	if err := runWorkspaceRoutingProjectsUnset(newRoutingProjectsTestCmd(), []string{"tarot"}); err != nil {
		t.Fatalf("unset: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	if _, present := block["projects"]; present {
		t.Errorf("row survived unset: %v", block)
	}
}
