package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newRuntimeAgentCLITestCmd(serverURL string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().Bool("update-now", false, "")
	cmd.Flags().String("follow", "", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

func TestRunRuntimeAgentCLIUpdateNowReportsQueuedState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	var requested int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtimes/rt-1/agent-cli/update":
			requested++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "request_id": "req-9"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/runtimes":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "rt-other", "provider": "claude"},
				{"id": "rt-1", "provider": "codex", "metadata": map[string]any{
					"cli_update": map[string]any{
						"phase":         "waiting",
						"waiting_tasks": 2,
						"claims_paused": true,
						"wait_reason":   "tasks",
					},
				}},
			})
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	cmd := newRuntimeAgentCLITestCmd(srv.URL)
	_ = cmd.Flags().Set("update-now", "true")
	out, err := captureRuntimeStdout(t, func() error {
		return runRuntimeAgentCLI(cmd, []string{"rt-1"})
	})
	if err != nil {
		t.Fatalf("runRuntimeAgentCLI: %v", err)
	}
	if requested != 1 {
		t.Fatalf("update requests = %d, want 1", requested)
	}
	var got struct {
		Provider        string         `json:"provider"`
		UpdateRequested bool           `json:"update_requested"`
		RequestID       string         `json:"request_id"`
		CLIUpdate       map[string]any `json:"cli_update"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.Provider != "codex" || !got.UpdateRequested || got.RequestID != "req-9" {
		t.Fatalf("got = %#v", got)
	}
	if got.CLIUpdate["phase"] != "waiting" || got.CLIUpdate["waiting_tasks"] != float64(2) || got.CLIUpdate["claims_paused"] != true {
		t.Fatalf("cli_update = %#v", got.CLIUpdate)
	}
}

func TestRunRuntimeAgentCLIRejectsBadFollow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := newRuntimeAgentCLITestCmd("http://127.0.0.1:1")
	_ = cmd.Flags().Set("follow", "maybe")
	if err := runRuntimeAgentCLI(cmd, []string{"rt-1"}); err == nil {
		t.Fatal("accepted --follow maybe")
	}
}
