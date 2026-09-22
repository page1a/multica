package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The requirement-alignment carrier (`agent.system_key = 'issue_draft:*'`) is
// the one task whose entire job is to talk: it produces a structured draft, and
// the server creates the issue from that draft when the user confirms. Its task
// token is therefore scoped down to reads in the auth middleware.
//
// These tests are what make that a boundary rather than a prompt instruction.
// The interesting ones are DB-backed — the middleware learns the scope from
// `task_token ⋈ agent`, and "this credential cannot write, and an ordinary
// task's credential still can" is a property of what that lookup returns — but
// the predicate boundaries are pinned directly too, because a prefix check is
// exactly the kind of thing that silently starts matching more than intended.

// assertScope drives one request through the middleware with `token` and
// reports the status the downstream handler produced, plus the headers it saw.
// A status of http.StatusNoContent is the handler having run.
func assertScope(t *testing.T, queries *db.Queries, method, token string) (int, http.Header) {
	t.Helper()
	return assertScopePath(t, queries, method, "/api/issues", token)
}

// assertScopePath is assertScope for a specific path — the allowlisted upload
// endpoint is a property of the path as much as the method.
func assertScopePath(t *testing.T, queries *db.Queries, method, path, token string) (int, http.Header) {
	t.Helper()
	var seen http.Header
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Auth(queries, nil, nil, nil)(next).ServeHTTP(rec, req)
	return rec.Code, seen
}

// scopeRig is one workspace with the two carriers these tests compare, wired
// to the real query layer.
type scopeRig struct {
	queries     *db.Queries
	fixture     *testutil.Fixture
	workspaceID string
	userID      string
	runtimeID   string
	carrierID   string
	ordinaryID  string
}

// mintTaskToken queues a task for agentID and returns a live `mat_` token bound
// to it, exactly as the claim path would.
func (rig scopeRig) mintTaskToken(t *testing.T, agentID string, expires string) string {
	t.Helper()
	taskID := rig.fixture.Task(t, agentID, testutil.Cols{
		"runtime_id": rig.runtimeID,
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	raw := "mat_" + uuid.NewString()
	rig.fixture.Insert(t, "task_token", testutil.Cols{
		"token_hash":   auth.HashToken(raw),
		"task_id":      taskID,
		"agent_id":     agentID,
		"workspace_id": rig.workspaceID,
		"user_id":      rig.userID,
		"expires_at":   testutil.Raw(expires),
	})
	return raw
}

// scopeFixture builds the workspace, the runtime and the two carriers the scope
// distinguishes: a hidden alignment carrier, and an ordinary agent doing
// ordinary task work.
func scopeFixture(t *testing.T) scopeRig {
	t.Helper()
	pool := openPool(t)
	t.Cleanup(pool.Close)

	fx := testutil.New(pool, "", "")
	userID := fx.User(t, "Task Scope Test", "task-scope-test@multica.ai")
	workspaceID := fx.Workspace(t, "Task Scope Test", "task-scope-test")
	runtimeID := fx.Runtime(t, "scope-test-runtime", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
	})

	carrierID := fx.Agent(t, ".multica-issue-draft-scope", runtimeID, testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
		"kind":         "system",
		"system_key":   "issue_draft:scope-test-flow",
	})
	ordinaryID := fx.Agent(t, "scope-test-ordinary", runtimeID, testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     userID,
	})
	return scopeRig{
		queries:     db.New(pool),
		fixture:     fx,
		workspaceID: workspaceID,
		userID:      userID,
		runtimeID:   runtimeID,
		carrierID:   carrierID,
		ordinaryID:  ordinaryID,
	}
}

// The headline guarantee: an alignment carrier's token cannot write, on any
// method, and the refusal happens in the middleware — before any handler.
func TestIssueDraftCarrierTaskTokenCannotWrite(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")

	for _, method := range []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			status, seen := assertScope(t, rig.queries, method, token)
			if status != http.StatusForbidden {
				t.Fatalf("%s with an alignment carrier token: status = %d, want %d", method, status, http.StatusForbidden)
			}
			// The handler must not have run: a 403 is a refusal, not a
			// refusal *after* the write path was reached.
			if seen != nil {
				t.Fatal("downstream handler ran for a refused write")
			}
		})
	}
}

// The other half of the same boundary: reads still work, and the request keeps
// the actor identity the token carries. A scope that broke the carrier's own
// context reads would be an outage, not a security fix.
func TestIssueDraftCarrierTaskTokenCanRead(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			status, seen := assertScope(t, rig.queries, method, token)
			if status != http.StatusNoContent {
				t.Fatalf("%s with an alignment carrier token: status = %d, want %d", method, status, http.StatusNoContent)
			}
			if got := seen.Get("X-Actor-Source"); got != "task_token" {
				t.Fatalf("X-Actor-Source = %q, want %q", got, "task_token")
			}
			if got := seen.Get("X-Agent-ID"); got != rig.carrierID {
				t.Fatalf("X-Agent-ID = %q, want %q", got, rig.carrierID)
			}
			if got := seen.Get("X-Workspace-ID"); got != rig.workspaceID {
				t.Fatalf("X-Workspace-ID = %q, want %q", got, rig.workspaceID)
			}
		})
	}
}

// The scope is narrow by construction: an ordinary task's token keeps every
// write it had. Without this, "read-only" could be implemented by breaking
// agent writes generally and the tests above would still pass.
func TestOrdinaryTaskTokenStillWrites(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.ordinaryID, "now() + interval '1 hour'")

	status, seen := assertScope(t, rig.queries, http.MethodPost, token)
	if status != http.StatusNoContent {
		t.Fatalf("POST with an ordinary task token: status = %d, want %d", status, http.StatusNoContent)
	}
	if got := seen.Get("X-Actor-Source"); got != "task_token" {
		t.Fatalf("X-Actor-Source = %q, want %q", got, "task_token")
	}
}

// system_key is a text column, so the prefix alone is not a scope: a
// user-authored agent whose key happens to start with `issue_draft:` must not
// inherit a hidden carrier's restrictions. The kind check is what prevents it,
// and this pins that it is actually doing something.
func TestUserAuthoredAgentWithCarrierPrefixIsNotScoped(t *testing.T) {
	rig := scopeFixture(t)
	// A user-authored agent deliberately given a carrier-looking key — the
	// shape a bug or a future feature could produce.
	userAgentID := rig.fixture.Agent(t, "scope-test-user-agent", rig.runtimeID, testutil.Cols{
		"workspace_id": rig.workspaceID,
		"owner_id":     rig.userID,
		"kind":         "user",
		"system_key":   "issue_draft:not-a-carrier",
	})
	token := rig.mintTaskToken(t, userAgentID, "now() + interval '1 hour'")

	status, _ := assertScope(t, rig.queries, http.MethodPost, token)
	if status != http.StatusNoContent {
		t.Fatalf("POST with a user-authored agent token: status = %d, want %d", status, http.StatusNoContent)
	}
}

// An expired token is not a scoped token — it is not a token at all. Pinned
// separately because the scope check sits on the same row that decides expiry.
func TestExpiredCarrierTaskTokenIsUnauthorized(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() - interval '1 hour'")

	status, _ := assertScope(t, rig.queries, http.MethodGet, token)
	if status != http.StatusUnauthorized {
		t.Fatalf("GET with an expired carrier token: status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// The predicate itself, including the cases a DB fixture cannot easily express.
func TestIsIssueDraftReadOnlyScope(t *testing.T) {
	text := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	cases := []struct {
		name      string
		kind      string
		systemKey pgtype.Text
		want      bool
	}{
		{name: "hidden alignment carrier", kind: "system", systemKey: text("issue_draft:flow"), want: true},
		{name: "null system key", kind: "system", systemKey: pgtype.Text{}, want: false},
		{name: "empty system key", kind: "system", systemKey: text(""), want: false},
		{name: "agent builder carrier", kind: "system", systemKey: text("agent_builder:flow"), want: false},
		{name: "prefix without separator", kind: "system", systemKey: text("issue_draft"), want: false},
		{name: "user-authored agent with carrier-looking key", kind: "user", systemKey: text("issue_draft:flow"), want: false},
		{name: "empty kind", kind: "", systemKey: text("issue_draft:flow"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIssueDraftReadOnlyScope(tc.kind, tc.systemKey); got != tc.want {
				t.Fatalf("isIssueDraftReadOnlyScope(%q, %q) = %v, want %v", tc.kind, tc.systemKey.String, got, tc.want)
			}
		})
	}
}

// Method classification is an allowlist, so a verb nobody classified is
// refused rather than treated as a read.
func TestIsReadOnlyMethod(t *testing.T) {
	allowed := []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	for _, method := range allowed {
		if !isReadOnlyMethod(method) {
			t.Fatalf("isReadOnlyMethod(%q) = false, want true", method)
		}
	}
	refused := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodConnect,
		http.MethodTrace,
		"PURGE",
		"",
	}
	for _, method := range refused {
		if isReadOnlyMethod(method) {
			t.Fatalf("isReadOnlyMethod(%q) = true, want false", method)
		}
	}
}

// The one write a carrier is granted: posting the file it produced for its own
// reply (DENE-590). An alignment that can describe a mock-up but cannot hand
// over the file it just made is the bug this closes — the carrier's own
// conversation is where the file belongs, and the upload handler is what keeps
// it there.
func TestIssueDraftCarrierMayUploadForItsOwnReply(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")

	status, seen := assertScopePath(t, rig.queries, http.MethodPost, "/api/upload-file", token)
	if status != http.StatusNoContent {
		t.Fatalf("POST /api/upload-file with a carrier token: status = %d, want %d", status, http.StatusNoContent)
	}
	// The handler needs to know the credential was a carrier's, because the
	// bindings it must refuse (issue_id, comment_id, chat_session_id) are form
	// fields the middleware never sees.
	if got := seen.Get(IssueDraftScopeHeader); got != IssueDraftScopeValue {
		t.Fatalf("%s = %q, want %q", IssueDraftScopeHeader, got, IssueDraftScopeValue)
	}
}

// The allowlist is one path, not a category of "harmless" writes: every other
// POST the carrier could reach still changes something the user has not
// confirmed.
func TestIssueDraftCarrierUploadExceptionIsPathOnly(t *testing.T) {
	rig := scopeFixture(t)
	token := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")

	for _, path := range []string{
		"/api/issues",
		"/api/upload-file/other",
		"/api/upload-files",
	} {
		t.Run(path, func(t *testing.T) {
			status, seen := assertScopePath(t, rig.queries, http.MethodPost, path, token)
			if status != http.StatusForbidden {
				t.Fatalf("POST %s with a carrier token: status = %d, want %d", path, status, http.StatusForbidden)
			}
			if seen != nil {
				t.Fatal("downstream handler ran for a refused write")
			}
		})
	}
}

// The scope marker is server-set, and the handler that reads it treats its
// ABSENCE as "not a carrier". So a client must not be able to clear one the
// middleware would have stamped, nor forge one on an ordinary token.
func TestIssueDraftScopeHeaderIsServerSet(t *testing.T) {
	rig := scopeFixture(t)

	carrier := rig.mintTaskToken(t, rig.carrierID, "now() + interval '1 hour'")
	var seen http.Header
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/api/upload-file", nil)
	req.Header.Set("Authorization", "Bearer "+carrier)
	// A carrier trying to shed its own scope so the handler stops restricting it.
	req.Header.Set(IssueDraftScopeHeader, "")
	rec := httptest.NewRecorder()
	Auth(rig.queries, nil, nil, nil)(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("carrier upload: status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := seen.Get(IssueDraftScopeHeader); got != IssueDraftScopeValue {
		t.Fatalf("a client-cleared %s reached the handler as %q, want %q", IssueDraftScopeHeader, got, IssueDraftScopeValue)
	}

	ordinary := rig.mintTaskToken(t, rig.ordinaryID, "now() + interval '1 hour'")
	seen = nil
	req = httptest.NewRequest(http.MethodPost, "/api/upload-file", nil)
	req.Header.Set("Authorization", "Bearer "+ordinary)
	req.Header.Set(IssueDraftScopeHeader, IssueDraftScopeValue)
	rec = httptest.NewRecorder()
	Auth(rig.queries, nil, nil, nil)(next).ServeHTTP(rec, req)
	if got := seen.Get(IssueDraftScopeHeader); got != "" {
		t.Fatalf("a client-supplied %s survived on an ordinary token: %q", IssueDraftScopeHeader, got)
	}
}

// The predicate itself: exact method, exact path.
func TestIsIssueDraftAllowedWrite(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodPost, path: "/api/upload-file", want: true},
		{method: http.MethodPost, path: "/api/upload-file/", want: true},
		{method: http.MethodPut, path: "/api/upload-file", want: false},
		{method: http.MethodDelete, path: "/api/upload-file", want: false},
		{method: http.MethodPost, path: "/api/upload-file/bulk", want: false},
		{method: http.MethodPost, path: "/api/upload-files", want: false},
		{method: http.MethodPost, path: "/api/issues", want: false},
		{method: http.MethodPost, path: "", want: false},
	}
	for _, tc := range cases {
		if got := isIssueDraftAllowedWrite(tc.method, tc.path); got != tc.want {
			t.Fatalf("isIssueDraftAllowedWrite(%q, %q) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
