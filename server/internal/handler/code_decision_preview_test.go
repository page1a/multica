package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestPreviewProjectCodeDecision(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const daemonID = "preview-decision-daemon"
	insertDaemonRuntimeWithCaps(t, daemonID,
		protocol.DaemonCapabilityLocalWorktreeV1,
		protocol.DaemonCapabilityLocalWorktreeUserRootV1,
		protocol.DaemonCapabilityLocalDirectoryMultiV1,
		protocol.DaemonCapabilityLocalSharedV1,
	)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Code decision preview",
	})
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateProject: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var project ProjectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	t.Cleanup(func() {
		req := withURLParam(newRequest("DELETE", "/api/projects/"+project.ID, nil), "id", project.ID)
		testHandler.DeleteProject(httptest.NewRecorder(), req)
	})

	createLocal := func(path, label string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
			"resource_type": "local_directory",
			"label":         label,
			"resource_ref": map[string]any{
				"local_path": path,
				"daemon_id":  daemonID,
				"label":      label,
			},
		}), "id", project.ID)
		testHandler.CreateProjectResource(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateProjectResource %s: expected 201, got %d: %s", label, w.Code, w.Body.String())
		}
	}
	createLocal("/Users/dev/work/first", "first")
	createLocal("/Users/dev/work/second", "second")

	missing := withURLParam(newRequest("GET", "/api/projects/"+project.ID+"/code-decision", nil), "id", project.ID)
	mw := httptest.NewRecorder()
	testHandler.PreviewProjectCodeDecision(mw, missing)
	if mw.Code != http.StatusBadRequest {
		t.Fatalf("missing daemon_id: expected 400, got %d: %s", mw.Code, mw.Body.String())
	}

	got := previewDecision(t, project.ID, daemonID)
	if got["kind"] != "local_in_place" {
		t.Fatalf("kind = %v, want local_in_place (the first directory, as the server ordered it)", got["kind"])
	}
	if got["path"] != "/Users/dev/work/first" {
		t.Fatalf("path = %v, want the first directory; the second must not be chosen here", got["path"])
	}
	if got["display_name"] != "first" {
		t.Fatalf("display_name = %v, want first", got["display_name"])
	}

	// A mode this machine no longer advertises is a failure code, not a quiet
	// rewrite to in-place. The save gate would have refused this resource
	// against the downgraded binary, so the row is written while the binary
	// still advertises the mode and the advertisement is removed afterwards —
	// the same window a daemon downgrade opens.
	ww := httptest.NewRecorder()
	wreq := withURLParam(newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
		"resource_type": "local_directory",
		"label":         "parallel",
		"resource_ref": map[string]any{
			"local_path":     "/Users/dev/work/parallel",
			"daemon_id":      daemonID,
			"label":          "parallel",
			"execution_mode": "worktree",
			"is_git_repo":    true,
		},
		// Below the two in-place rows, so THIS directory is the one the
		// server selects. Position order is the selection rule.
		"position": -1,
	}), "id", project.ID)
	testHandler.CreateProjectResource(ww, wreq)
	if ww.Code != http.StatusCreated {
		t.Fatalf("CreateProjectResource worktree: expected 201, got %d: %s", ww.Code, ww.Body.String())
	}
	dbfx.Exec(t, `UPDATE agent_runtime SET metadata = '{"capabilities":[]}'::jsonb WHERE daemon_id = $1 AND workspace_id = $2`, daemonID, testWorkspaceID)

	downgraded := previewDecision(t, project.ID, daemonID)
	if downgraded["kind"] != "unresolvable" {
		t.Fatalf("kind = %v, want unresolvable after the daemon stopped advertising worktree", downgraded["kind"])
	}
	if downgraded["code"] != "daemon_cannot_run_mode" {
		t.Fatalf("code = %v, want daemon_cannot_run_mode", downgraded["code"])
	}
}

func previewDecision(t *testing.T, projectID, daemonID string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req := withURLParam(
		newRequest("GET", "/api/projects/"+projectID+"/code-decision?daemon_id="+daemonID, nil),
		"id", projectID,
	)
	testHandler.PreviewProjectCodeDecision(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	return got
}
