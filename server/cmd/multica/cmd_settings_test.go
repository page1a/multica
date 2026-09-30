package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSettingsCommandsUseServerMethods(t *testing.T) {
	repoURL := "https://github.com/acme/app.git"
	cases := []struct {
		name       string
		key        string
		write      bool
		value      string
		wantMethod string
		wantPath   string
		wantQuery  string
		wantVis    string
		wantStdout string
	}{
		{
			name: "read repo visibility from the workspace record",
			key:  "repo.visibility", value: `{"url":"` + repoURL + `"}`,
			wantMethod: http.MethodGet, wantPath: "/api/workspaces/ws-123",
			wantStdout: `"visibility": "workspace"`,
		},
		{
			name: "write repo visibility with PUT",
			key:  "repo.visibility", write: true,
			value:      `{"url":"` + repoURL + `","visibility":"project"}`,
			wantMethod: http.MethodPut, wantPath: "/api/repos/visibility", wantVis: "project",
		},
		{
			name: "list repo shares",
			key:  "repo.shares", value: `{"url":"` + repoURL + `"}`,
			wantMethod: http.MethodGet, wantPath: "/api/repos/shares",
			wantQuery: "url=https%3A%2F%2Fgithub.com%2Facme%2Fapp.git",
		},
		{
			name: "add a repo share",
			key:  "repo.shares", write: true,
			value:      `{"url":"` + repoURL + `","member_id":"member-1"}`,
			wantMethod: http.MethodPost, wantPath: "/api/repos/shares",
		},
		{
			name: "revoke a repo share",
			key:  "repo.shares", write: true,
			value:      `{"url":"` + repoURL + `","member_id":"member-1","revoke":true}`,
			wantMethod: http.MethodDelete, wantPath: "/api/repos/shares",
			wantQuery:  "member_id=member-1&url=https%3A%2F%2Fgithub.com%2Facme%2Fapp.git",
			wantStdout: `"revoked": true`,
		},
		{
			name:       "read runtime visibility from the runtime list",
			key:        "runtime.rt-1.visibility",
			wantMethod: http.MethodGet, wantPath: "/api/runtimes",
			wantStdout: `"visibility": "public"`,
		},
		{
			name: "write runtime visibility with PATCH",
			key:  "runtime.rt-1.visibility", write: true, value: `{"visibility":"private"}`,
			wantMethod: http.MethodPatch, wantPath: "/api/runtimes/rt-1", wantVis: "private",
		},
		{
			name:       "read runtime skill switches from the agent",
			key:        "agent.ag-1.runtime-skill",
			wantMethod: http.MethodGet, wantPath: "/api/agents/ag-1",
			wantStdout: `"key": "demo"`,
		},
		{
			name: "write runtime skill switch with PUT",
			key:  "agent.ag-1.runtime-skill", write: true,
			value:      `{"runtime_id":"rt-1","root":"provider","key":"demo","enabled":false}`,
			wantMethod: http.MethodPut, wantPath: "/api/agents/ag-1/runtime-skills/enabled",
			wantStdout: `"ok": true`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
				gotBody = nil
				if r.Body != nil {
					_ = json.NewDecoder(r.Body).Decode(&gotBody)
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/api/workspaces/ws-123":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"repos": []map[string]any{{"url": repoURL, "visibility": "workspace"}},
					})
				case r.URL.Path == "/api/runtimes" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode([]map[string]any{
						{"id": "rt-1", "visibility": "public"},
						{"id": "rt-2", "visibility": "private"},
					})
				case r.URL.Path == "/api/agents/ag-1" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id":                      "ag-1",
						"disabled_runtime_skills": []map[string]any{{"key": "demo", "runtime_id": "rt-1"}},
					})
				case strings.HasSuffix(r.URL.Path, "/runtime-skills/enabled"):
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
				}
			}))
			defer srv.Close()

			t.Setenv("HOME", t.TempDir())
			// A task-scoped mat_ token so this also passes inside an agent
			// workdir, where a daemon task marker rejects a plain token.
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
			t.Setenv("MULTICA_SERVER_URL", srv.URL)
			clearSettingsFlags(t)

			stdout, err := captureSettingsStdout(t, func() error {
				if tc.write {
					if err := settingsSetCmd.Flags().Set("value-json", tc.value); err != nil {
						return err
					}
					return runSettingsSet(settingsSetCmd, []string{tc.key})
				}
				if tc.value != "" {
					if err := settingsGetCmd.Flags().Set("value-json", tc.value); err != nil {
						return err
					}
				}
				return runSettingsGet(settingsGetCmd, []string{tc.key})
			})
			if err != nil {
				t.Fatalf("command: %v", err)
			}
			if gotMethod != tc.wantMethod {
				t.Errorf("method = %s, want %s", gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPath)
			}
			if tc.wantQuery != "" && gotQuery != tc.wantQuery {
				t.Errorf("query = %s, want %s", gotQuery, tc.wantQuery)
			}
			if tc.wantVis != "" && gotBody["visibility"] != tc.wantVis {
				t.Errorf("visibility = %#v, want %s", gotBody["visibility"], tc.wantVis)
			}
			if tc.wantStdout != "" && !strings.Contains(stdout, tc.wantStdout) {
				t.Errorf("stdout = %s, want it to contain %s", stdout, tc.wantStdout)
			}
		})
	}
}

func TestSettingsRejectsUnknownKeyAndTableOutput(t *testing.T) {
	if _, err := planSettings("signing-secret", false, nil, nil, "ws-123"); err == nil {
		t.Fatal("unknown setting was accepted")
	}
	for _, cmd := range []*cobra.Command{settingsGetCmd, settingsSetCmd} {
		usage := cmd.Flags().Lookup("output").Usage
		if strings.Contains(usage, "table") {
			t.Errorf("%s help still promises table output: %s", cmd.Name(), usage)
		}
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	clearSettingsFlags(t)
	if err := settingsGetCmd.Flags().Set("output", "table"); err != nil {
		t.Fatal(err)
	}
	err := runSettingsGet(settingsGetCmd, []string{"repo.visibility"})
	if err == nil || !strings.Contains(err.Error(), "only writes JSON") {
		t.Fatalf("table output error = %v", err)
	}
}

func TestPlanSettingsPinsAccessPassMethods(t *testing.T) {
	read, err := planSettings("agent.ag-1.access-passes", false, nil, nil, "ws-123")
	if err != nil {
		t.Fatal(err)
	}
	if read.Method != http.MethodGet || read.Path != "/api/agents/ag-1/access-passes" {
		t.Fatalf("read = %s %s", read.Method, read.Path)
	}
	revoke, err := planSettings("agent.ag-1.access-passes", true, map[string]any{"revoke_id": "pass-1"}, nil, "ws-123")
	if err != nil {
		t.Fatal(err)
	}
	if revoke.Method != http.MethodDelete || revoke.Path != "/api/agents/ag-1/access-passes/pass-1" {
		t.Fatalf("revoke = %s %s", revoke.Method, revoke.Path)
	}
}

func clearSettingsFlags(t *testing.T) {
	t.Helper()
	_ = settingsGetCmd.Flags().Set("output", "json")
	_ = settingsGetCmd.Flags().Set("value-json", "")
	_ = settingsSetCmd.Flags().Set("output", "json")
	_ = settingsSetCmd.Flags().Set("value-json", "")
	_ = settingsSetCmd.Flags().Set("value-file", "")
	_ = settingsSetCmd.Flags().Set("value-stdin", "false")
}

func captureSettingsStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	cmdErr := fn()
	_ = w.Close()
	os.Stdout = old
	body, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(body), cmdErr
}
