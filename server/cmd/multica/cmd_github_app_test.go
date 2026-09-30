package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubAppStatusAndSetupLink(t *testing.T) {
	t.Setenv("MULTICA_TOKEN", "mat_test")
	t.Setenv("MULTICA_WORKSPACE_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_DAEMON_PORT", "")

	var postedOrg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workspaces/11111111-1111-4111-8111-111111111111/github/app" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"source":     "none",
				"configured": false,
				"read_only":  false,
				"can_create": true,
			})
			return
		}
		var body struct {
			Org string `json:"org"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		postedOrg = body.Org
		_ = json.NewEncoder(w).Encode(map[string]any{
			"action_url": "https://github.com/organizations/acme/settings/apps/new",
			"manifest":   map[string]any{"public": true},
			"launch_url": "https://api.example.test/api/github/app/launch?state=abc",
		})
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)

	statusCmd := &cobra.Command{Use: "status", RunE: runGitHubAppStatus}
	statusCmd.Flags().String("output", "json", "")
	statusOut, err := captureStdout(t, func() error {
		return runGitHubAppStatus(statusCmd, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOut, `"can_create": true`) {
		t.Fatalf("status output = %s", statusOut)
	}

	linkCmd := &cobra.Command{Use: "setup-link", RunE: runGitHubAppSetupLink}
	linkCmd.Flags().String("output", "json", "")
	linkCmd.Flags().String("org", "acme", "")
	if err := linkCmd.Flags().Set("org", "acme"); err != nil {
		t.Fatal(err)
	}
	linkOut, err := captureStdout(t, func() error {
		return runGitHubAppSetupLink(linkCmd, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if postedOrg != "acme" {
		t.Fatalf("posted org = %q", postedOrg)
	}
	if !strings.Contains(linkOut, `/api/github/app/launch?state=abc`) {
		t.Fatalf("setup-link output = %s", linkOut)
	}
}

func TestGitHubAppCommandRegistered(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "github-app" {
			return
		}
	}
	t.Fatal("github-app is not registered")
}
