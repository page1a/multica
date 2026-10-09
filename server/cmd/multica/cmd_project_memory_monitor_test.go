package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectMemoryMonitorCLI(t *testing.T) {
	projectID := "33333333-3333-3333-3333-333333333333"
	var gotPath, gotDays string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/memory/monitor") {
			gotPath, gotDays = r.URL.Path, r.URL.Query().Get("days")
			_ = json.NewEncoder(w).Encode(map[string]any{"project_id": projectID, "days": 30})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": projectID, "title": "P"})
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "11111111-1111-1111-1111-111111111111")

	cmd := projectMemoryMonitorCmd
	_ = cmd.Flags().Set("days", "30")
	_ = cmd.Flags().Set("output", "json")
	if err := runProjectMemoryMonitor(cmd, []string{projectID}); err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if gotPath != "/api/projects/"+projectID+"/memory/monitor" || gotDays != "30" {
		t.Fatalf("requested %s days=%q", gotPath, gotDays)
	}
}

func TestPrintMemoryMonitor(t *testing.T) {
	var result map[string]any
	_ = json.Unmarshal([]byte(`{
		"days": 14, "since": "2026-09-24T00:00:00Z",
		"writes": [{"issue_identifier": "DENE-1", "layer": "boss", "created_at": "t",
			"changes": [{"location": "agents", "action": "supersede", "entry": "旧派单"}],
			"superseded": ["旧派单"], "deleted_lines": 2}],
		"unsettled": [{"identifier": "DENE-2", "title": "没沉淀", "reason": "none", "closed_at": "t"}],
		"rounds": {"opened": 2, "open": 1, "idle": 1, "items": [{"identifier": "DENE-3", "layer": "worker", "status": "done", "idle": true, "title": "轮次"}]},
		"chats": [{"chat_session_id": "c1", "accessible": false, "dispatched": 1, "reported": 0, "no_conclusion": 1, "open": 0,
			"tickets": [{"identifier": "DENE-4", "flow": "no_conclusion", "status": "done", "title": "派出"}]}]
	}`), &result)
	var out bytes.Buffer
	printMemoryMonitor(&out, result)
	for _, want := range []string{"superseded: 旧派单", "deleted lines: 2", "none       DENE-2", "2 opened, 1 open, 1 idle", "done (idle)", "(chat you cannot open)", "DENE-4  no_conclusion"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.String())
		}
	}
}
