package main

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

// TestCliTokenAndPersonalAccessTokenRoutesRejectTaskToken pins the credential
// boundary from DENE-896: a mat_ task token is bound to one workspace, so it
// must not be able to exchange itself for an unbound human JWT (/api/cli-token)
// or mint / list / renew / revoke unbound mul_ tokens (/api/tokens). The guard
// sits on the router, so the test drives the real server.
func TestCliTokenAndPersonalAccessTokenRoutesRejectTaskToken(t *testing.T) {
	if testServer == nil {
		t.Skip("integration server not available")
	}
	ctx := context.Background()
	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load integration-test agent: %v", err)
	}
	token := mintAgentTaskToken(t, agentID, ensureAgentTask(t, agentID), testUserID)

	do := func(t *testing.T, bearer, method, path string) *http.Response {
		t.Helper()
		var body *bytes.Reader
		if method == http.MethodPost {
			body = bytes.NewReader([]byte(`{"name":"dene896","expires_in_days":1}`))
		} else {
			body = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, testServer.URL+path, body)
		if err != nil {
			t.Fatalf("create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		// A forged actor source must be discarded by auth before the guard.
		req.Header.Set("X-Actor-Source", "member")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("perform request: %v", err)
		}
		return resp
	}

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "cli-token", method: http.MethodPost, path: "/api/cli-token"},
		{name: "tokens list", method: http.MethodGet, path: "/api/tokens/"},
		{name: "tokens create", method: http.MethodPost, path: "/api/tokens/"},
		{name: "tokens renew", method: http.MethodPost, path: "/api/tokens/current/renew"},
		{name: "tokens revoke", method: http.MethodDelete, path: "/api/tokens/00000000-0000-0000-0000-000000000001"},
	} {
		t.Run("task token "+tc.name, func(t *testing.T) {
			resp := do(t, token, tc.method, tc.path)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
		})
	}

	// The guard must not lock the human out: the web login page still
	// exchanges a JWT for a CLI token, and `multica login` still mints a PAT.
	jwtToken, err := generateTestJWT(testUserID, "handler-test@multica.ai", "Test")
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}
	t.Run("human jwt cli-token", func(t *testing.T) {
		resp := do(t, jwtToken, http.MethodPost, "/api/cli-token")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
	t.Run("human jwt tokens list", func(t *testing.T) {
		resp := do(t, jwtToken, http.MethodGet, "/api/tokens/")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}
