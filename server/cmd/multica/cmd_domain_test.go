package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// DENE-1451: rename and delete take the name people see, not only the id.
func TestDomainRenameResolvesTheName(t *testing.T) {
	var patchedPath, patchedName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/domains":
			json.NewEncoder(w).Encode(map[string]any{"domains": []map[string]any{
				{"id": "d-sea", "name": "出海"}, {"id": "d-relay", "name": "中转"},
			}})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/api/domains/"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			patchedPath, patchedName = r.URL.Path, body["name"]
			json.NewEncoder(w).Encode(map[string]any{"id": "d-relay", "name": body["name"]})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", routingProjectsTestWorkspace)

	if _, err := captureStdout(t, func() error {
		return runDomainRename(newRoutingProjectsTestCmd(), []string{"中转", "中转站"})
	}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if patchedPath != "/api/domains/d-relay" || patchedName != "中转站" {
		t.Errorf("patched %s with %q", patchedPath, patchedName)
	}

	err := runDomainDelete(newRoutingProjectsTestCmd(), []string{"学术"})
	if err == nil || !strings.Contains(err.Error(), `no domain "学术"`) {
		t.Errorf("delete of an unknown domain: err = %v", err)
	}
}
