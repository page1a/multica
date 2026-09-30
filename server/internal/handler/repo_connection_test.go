package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestAgentCannotSaveSomeoneElsesRepoToken is the server-side rule: an agent
// task token belongs to the runtime owner, who is often an admin, so the
// admin path would otherwise accept a token for any repository. The body
// flag is absent on purpose. The caller is still limited to repositories the
// task initiator registered.
func TestAgentCannotSaveSomeoneElsesRepoToken(t *testing.T) {
	if testPool == nil || testHandler == nil {
		t.Skip("no test database")
	}
	withVCSBox(t)
	const (
		someoneElse = "11111111-1111-4111-8111-111111111111"
		notMine     = "https://github.com/acme/not-mine"
		mine        = "https://github.com/acme/mine"
		secret      = "ghs_AGENT_TOKEN_MUST_NOT_LEAK"
	)
	setHandlerTestWorkspaceRepos(t, []map[string]string{
		{"url": notMine, "created_by": someoneElse, "visibility": "workspace"},
		{"url": mine, "created_by": testUserID, "visibility": "private"},
	})
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM vcs_connection WHERE workspace_id = $1 AND repo_url IN ('github.com/acme/not-mine', 'github.com/acme/mine')`,
			testWorkspaceID)
	})

	var providerCalls atomic.Int32
	prevClient := testHandler.deliveryHTTP
	testHandler.deliveryHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		body := []byte(`{}`)
		if strings.HasSuffix(r.URL.Path, "/user") {
			body = []byte(`{"login":"octocat"}`)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
		}, nil
	})}
	t.Cleanup(func() { testHandler.deliveryHTTP = prevClient })

	member, err := testHandler.Queries.GetMemberByUserAndWorkspace(context.Background(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(testUserID),
		WorkspaceID: parseUUID(testWorkspaceID),
	})
	if err != nil {
		t.Fatalf("load owner member: %v", err)
	}
	agentID := createHandlerTestAgent(t, "repo-connection-agent", nil)
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id":        handlerTestRuntimeID(t),
		"status":            "running",
		"started_at":        testutil.Raw("now()"),
		"initiator_user_id": testUserID,
	})
	bareTaskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})

	post := func(repoURL, task string, agentYes *bool) *httptest.ResponseRecorder {
		t.Helper()
		body := map[string]any{
			"repo_url":     repoURL,
			"provider":     "github",
			"access_token": secret,
		}
		if agentYes != nil {
			body["agent_yes"] = *agentYes
		}
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/repos/connections", body)
		req = withURLParam(req, "id", testWorkspaceID)
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", task)
		req.Header.Set("X-Actor-Source", "task_token")
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, member))
		w := httptest.NewRecorder()
		testHandler.UpsertRepoConnection(w, req)
		return w
	}
	count := func(key string) int {
		t.Helper()
		var n int
		if err := testPool.QueryRow(context.Background(),
			`SELECT count(*) FROM vcs_connection WHERE workspace_id = $1 AND repo_url = $2`,
			testWorkspaceID, key,
		).Scan(&n); err != nil {
			t.Fatalf("count connections: %v", err)
		}
		return n
	}

	no := false
	denied := post(notMine, taskID, &no)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("someone else's repo: status = %d, want 403: %s", denied.Code, denied.Body.String())
	}
	if strings.Contains(denied.Body.String(), secret) {
		t.Fatalf("refusal leaked the token: %s", denied.Body.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("refused save called the provider %d times", providerCalls.Load())
	}
	if n := count("github.com/acme/not-mine"); n != 0 {
		t.Fatalf("stored %d connections for someone else's repo", n)
	}

	missing := post(mine, bareTaskID, nil)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("task without initiator: status = %d, want 400: %s", missing.Code, missing.Body.String())
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("missing initiator called the provider %d times", providerCalls.Load())
	}

	allowed := post(mine, taskID, nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("initiator's repo: status = %d, want 200: %s", allowed.Code, allowed.Body.String())
	}
	if strings.Contains(allowed.Body.String(), secret) {
		t.Fatalf("success response leaked the token: %s", allowed.Body.String())
	}
	if n := count("github.com/acme/mine"); n != 1 {
		t.Fatalf("initiator repo connections = %d, want 1", n)
	}
	var connectedBy string
	if err := testPool.QueryRow(context.Background(),
		`SELECT connected_by_id::text FROM vcs_connection WHERE workspace_id = $1 AND repo_url = 'github.com/acme/mine'`,
		testWorkspaceID,
	).Scan(&connectedBy); err != nil {
		t.Fatalf("read connected_by: %v", err)
	}
	if connectedBy != testUserID {
		t.Fatalf("connected_by = %s, want initiator %s", connectedBy, testUserID)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
