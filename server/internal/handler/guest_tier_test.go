package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestValidateAssigneePairRejectsGuest: a guest cannot be handed work.
//
// The read-only interceptor stops a guest from writing, but nothing stops a
// Member from parking an issue on one — and an issue whose owner can neither
// move it nor comment on it is an issue nobody is carrying. This is the
// assignment half of "guests cannot be assigned or @-triggered" (DENE-695).
func TestValidateAssigneePairRejectsGuest(t *testing.T) {
	ctx := context.Background()

	guestUserID := dbfx.User(t, "Guest Assignee", "guest-assignee@multica.ai")
	dbfx.Member(t, testWorkspaceID, guestUserID, "guest")

	memberUserID := dbfx.User(t, "Member Assignee", "member-assignee@multica.ai")
	dbfx.Member(t, testWorkspaceID, memberUserID, "member")

	req := httptest.NewRequest(http.MethodPost, "/api/issues", nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)

	status, msg := testHandler.validateAssigneePair(ctx, req,
		testWorkspaceID, pgtype.Text{String: "member", Valid: true}, parseUUID(guestUserID))
	if status != http.StatusBadRequest {
		t.Fatalf("assigning a guest: status %d (%q), want %d — a read-only member must not be able to own an issue",
			status, msg, http.StatusBadRequest)
	}

	status, msg = testHandler.validateAssigneePair(ctx, req,
		testWorkspaceID, pgtype.Text{String: "member", Valid: true}, parseUUID(memberUserID))
	if status != 0 {
		t.Fatalf("assigning a member: status %d (%q), want 0 — the guest rule must not catch ordinary members",
			status, msg)
	}
}

// TestNormalizeMemberRoleAcceptsGuest pins the moment guest became
// selectable. Migration 502 deliberately left this list at three values
// because a guest without the read-only layer held full Member write access
// under a read-only name; the fourth value and that layer ship together.
func TestNormalizeMemberRoleAcceptsGuest(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member", "guest"} {
		got, ok := normalizeMemberRole(role)
		if !ok || got != role {
			t.Errorf("normalizeMemberRole(%q) = (%q, %v), want (%q, true)", role, got, ok, role)
		}
	}
	for _, role := range []string{"viewer", "Guest", "", "  "} {
		if role == "" {
			// An absent role still defaults to member; that predates this change.
			continue
		}
		if _, ok := normalizeMemberRole(role); ok {
			t.Errorf("normalizeMemberRole(%q) accepted an unknown tier", role)
		}
	}
}

// TestDaemonAccessAllowedForTier: the daemon API is workspace infrastructure,
// not content. There is no read half of registering a machine or claiming a
// task worth granting a guest, and MembershipCache — which stores "is a
// member" and nothing else — must never end up holding an entry that stands
// for one.
func TestDaemonAccessAllowedForTier(t *testing.T) {
	for _, role := range []string{"owner", "admin", "member"} {
		if !daemonAccessAllowedForTier(role) {
			t.Errorf("daemonAccessAllowedForTier(%q) = false, want true", role)
		}
	}
	for _, role := range []string{"guest", "", "something-new"} {
		if daemonAccessAllowedForTier(role) {
			t.Errorf("daemonAccessAllowedForTier(%q) = true, want false", role)
		}
	}
}

// TestCanReadIssueViewGuestWorkspaceScope: "the whole workspace" does not
// include guests. A project is the only way to show a guest anything, so a
// workspace-shared view stays invisible to one even though they are a member
// of that workspace. Mirrors permission.CanSee; the full tier x scope matrix
// lives in internal/permission/permission_test.go.
func TestCanReadIssueViewGuestWorkspaceScope(t *testing.T) {
	owner := parseUUID("11111111-1111-1111-1111-111111111111")
	other := parseUUID("22222222-2222-2222-2222-222222222222")
	project := parseUUID("33333333-3333-3333-3333-333333333333")

	workspaceView := db.IssueView{OwnerID: owner, Visibility: "workspace", ScopeType: "all"}
	projectView := db.IssueView{
		OwnerID:    owner,
		Visibility: "project",
		ScopeType:  "project",
		ScopeID:    project,
	}
	privateView := db.IssueView{OwnerID: owner, Visibility: "private", ScopeType: "all"}

	cases := []struct {
		name       string
		view       db.IssueView
		caller     pgtype.UUID
		role       string
		projectIDs []pgtype.UUID
		want       bool
	}{
		{"member sees a workspace view", workspaceView, other, "member", nil, true},
		{"admin sees a workspace view", workspaceView, other, "admin", nil, true},
		{"guest does not see a workspace view", workspaceView, other, "guest", nil, false},
		{"guest still sees a view they own", workspaceView, owner, "guest", nil, true},
		{"guest sees a project view in their project", projectView, other, "guest", []pgtype.UUID{project}, true},
		{"guest does not see a project view outside it", projectView, other, "guest", nil, false},
		{"nobody sees someone else's private view", privateView, other, "admin", []pgtype.UUID{project}, false},
		{"an unknown tier sees nothing shared", workspaceView, other, "auditor", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canReadIssueView(tc.view, tc.caller, tc.role, tc.projectIDs); got != tc.want {
				t.Fatalf("canReadIssueView(visibility=%q, role=%q) = %v, want %v",
					tc.view.Visibility, tc.role, got, tc.want)
			}
		})
	}
}
