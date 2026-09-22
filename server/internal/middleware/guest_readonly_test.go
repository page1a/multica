package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// guestFixture inserts one workspace holding one member per tier and returns
// the workspace id plus a user id per role. The slug and emails are derived
// from the test name so two tests in this package never collide on them.
//
// The pool is opened here as well, and closed after the rows are removed:
// `defer pool.Close()` in the test body would run before t.Cleanup and leave
// the fixture behind for the next test to trip over.
func guestFixture(t *testing.T) (queries *db.Queries, workspaceID string, users map[string]string) {
	t.Helper()
	ctx := context.Background()

	pool := openPool(t)
	t.Cleanup(pool.Close)

	name := strings.ToLower(t.Name())
	slug := "mw-guest-" + name

	var wsID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1, $2, '', 'MGT') RETURNING id`,
		"Middleware Guest Test", slug,
	).Scan(&wsID); err != nil {
		t.Fatalf("setup: insert workspace %q: %v", slug, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1`, wsID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	users = map[string]string{}
	for _, role := range []string{"owner", "member", "guest"} {
		var userID string
		if err := pool.QueryRow(ctx,
			`INSERT INTO "user" (email, name) VALUES ($1, $2) RETURNING id`,
			slug+"-"+role+"@multica.ai", "Guest Test "+role,
		).Scan(&userID); err != nil {
			t.Fatalf("setup: insert %s user: %v", role, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
		})
		if _, err := pool.Exec(ctx,
			`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
			wsID, userID, role,
		); err != nil {
			t.Fatalf("setup: insert %s member: %v", role, err)
		}
		users[role] = userID
	}
	return db.New(pool), wsID, users
}

// TestGuestReadOnly is the acceptance for DENE-697: a guest's writes are
// refused by the server, on every route behind the interceptor, without the
// route knowing anything about tiers.
//
// The route under test is a stand-in on purpose. What the interceptor
// promises is that it does not need to know which route it is in front of —
// asserting that against one real endpoint would test that endpoint instead.
func TestGuestReadOnly(t *testing.T) {
	queries, workspaceID, users := guestFixture(t)

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := GuestReadOnly(queries)(next)

	cases := []struct {
		name        string
		method      string
		path        string
		userID      string
		workspaceID string
		wantStatus  int
		wantReached bool
	}{
		{
			name: "guest write is refused", method: http.MethodPost, path: "/api/issues",
			userID: users["guest"], workspaceID: workspaceID,
			wantStatus: http.StatusForbidden, wantReached: false,
		},
		{
			name: "guest delete is refused", method: http.MethodDelete, path: "/api/issues/abc",
			userID: users["guest"], workspaceID: workspaceID,
			wantStatus: http.StatusForbidden, wantReached: false,
		},
		{
			name: "guest read passes through", method: http.MethodGet, path: "/api/issues",
			userID: users["guest"], workspaceID: workspaceID,
			wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "guest may still write their own account state", method: http.MethodPatch, path: "/api/me",
			userID: users["guest"], workspaceID: workspaceID,
			wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "member write passes through", method: http.MethodPost, path: "/api/issues",
			userID: users["member"], workspaceID: workspaceID,
			wantStatus: http.StatusOK, wantReached: true,
		},
		{
			name: "owner write passes through", method: http.MethodPost, path: "/api/issues",
			userID: users["owner"], workspaceID: workspaceID,
			wantStatus: http.StatusOK, wantReached: true,
		},
		{
			// No workspace on the request means no tier to judge. The
			// route's own guard answers; the interceptor must not
			// invent a decision here.
			name: "write with no workspace context falls through", method: http.MethodPost, path: "/api/issues",
			userID: users["guest"], workspaceID: "",
			wantStatus: http.StatusOK, wantReached: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-User-ID", tc.userID)
			if tc.workspaceID != "" {
				req.Header.Set("X-Workspace-ID", tc.workspaceID)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s as this tier: status %d, want %d (body %s)",
					tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if reached != tc.wantReached {
				t.Fatalf("%s %s as this tier: downstream handler reached=%v, want %v",
					tc.method, tc.path, reached, tc.wantReached)
			}
		})
	}
}

// TestGuestReadOnlyPrefetchesMemberForDownstream pins the reason the
// interceptor is free: RequireWorkspaceMember behind it reuses the row it
// already loaded instead of querying again. If the handoff breaks, the suite
// stays green and every write request silently costs an extra round-trip —
// so the handoff is asserted directly.
func TestGuestReadOnlyPrefetchesMemberForDownstream(t *testing.T) {
	queries, workspaceID, users := guestFixture(t)

	var got db.Member
	inner := RequireWorkspaceMember(queries)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = MemberFromContext(r.Context())
	}))
	handler := GuestReadOnly(queries)(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/issues", nil)
	req.Header.Set("X-User-ID", users["member"])
	req.Header.Set("X-Workspace-ID", workspaceID)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("member write: status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got.Role != "member" {
		t.Fatalf("downstream member role = %q, want %q: the prefetched row did not reach RequireWorkspaceMember", got.Role, "member")
	}
}

// TestPrefetchedMemberIsPairBound: a row loaded for one user+workspace must
// never answer for another. The prefetch lives in the request context, and a
// context value that ignored which pair it was loaded for would hand one
// caller's tier to a different caller's request.
func TestPrefetchedMemberIsPairBound(t *testing.T) {
	const (
		userA = "11111111-1111-1111-1111-111111111111"
		userB = "22222222-2222-2222-2222-222222222222"
		wsA   = "33333333-3333-3333-3333-333333333333"
		wsB   = "44444444-4444-4444-4444-444444444444"
	)
	parse := func(s string) pgtype.UUID {
		var u pgtype.UUID
		if err := u.Scan(s); err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return u
	}
	ctx := withPrefetchedMember(context.Background(), parse(userA), parse(wsA), db.Member{Role: "owner"})

	if _, ok := prefetchedMemberFor(ctx, parse(userA), parse(wsA)); !ok {
		t.Fatal("same user and workspace: want the prefetched row back")
	}
	if _, ok := prefetchedMemberFor(ctx, parse(userB), parse(wsA)); ok {
		t.Fatal("different user, same workspace: the prefetched row must not answer")
	}
	if _, ok := prefetchedMemberFor(ctx, parse(userA), parse(wsB)); ok {
		t.Fatal("same user, different workspace: the prefetched row must not answer")
	}
	if _, ok := prefetchedMemberFor(context.Background(), parse(userA), parse(wsA)); ok {
		t.Fatal("no prefetch on the context: want a miss")
	}
}
