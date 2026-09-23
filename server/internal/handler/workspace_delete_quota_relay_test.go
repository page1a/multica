package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestDeleteWorkspace_SweepsQuotaBreakerAndRelay covers the no-FK tables from
// DENE-771. A spent seat's breaker and its relay are keyed only by
// workspace_id, so deleting the workspace is the only thing that removes them.
func TestDeleteWorkspace_SweepsQuotaBreakerAndRelay(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const slug = "handler-tests-delete-quota-relay"
	dbfx.Exec(t, `DELETE FROM workspace WHERE slug = $1`, slug)

	wsID := dbfx.Workspace(t, "Handler Test Delete Quota Relay", slug)
	dbfx.Member(t, wsID, testUserID, "owner")

	dbfx.Exec(t, `
		INSERT INTO agent_quota_breaker (
			workspace_id, agent_id, scope, model_key, reason, recover_at
		) VALUES ($1, gen_random_uuid(), 'agent', '', 'weekly quota spent', now())
	`, wsID)
	dbfx.Exec(t, `
		INSERT INTO agent_quota_relay (
			workspace_id, source_task_id, from_agent_id, scope, outcome
		) VALUES ($1, gen_random_uuid(), gen_random_uuid(), 'agent', 'waiting')
	`, wsID)

	if n := dbfx.Count(t, `SELECT count(*) FROM agent_quota_breaker WHERE workspace_id = $1`, wsID); n != 1 {
		t.Fatalf("fixture wrote %d breaker rows, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_quota_relay WHERE workspace_id = $1`, wsID); n != 1 {
		t.Fatalf("fixture wrote %d relay rows, want 1", n)
	}

	req := newRequest("DELETE", "/api/workspaces/"+wsID, nil)
	req = withURLParam(req, "id", wsID)
	testutil.Call(t, testHandler.DeleteWorkspace, req).Want(http.StatusNoContent)

	if n := dbfx.Count(t, `SELECT count(*) FROM agent_quota_breaker WHERE workspace_id = $1`, wsID); n != 0 {
		t.Fatalf("workspace delete left %d agent_quota_breaker rows", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_quota_relay WHERE workspace_id = $1`, wsID); n != 0 {
		t.Fatalf("workspace delete left %d agent_quota_relay rows", n)
	}
}
