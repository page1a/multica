package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DENE-1022: member management is owner-only. The roster stays readable by
// every member (assignee pickers, @mentions, comment authors read it) but the
// management fields are for the owner.

func rosterAs(t *testing.T, userID string) map[string]MemberWithUserResponse {
	t.Helper()
	req := newRequest("GET", "/api/workspaces/"+testWorkspaceID+"/members", nil)
	req.Header.Set("X-User-ID", userID)
	req = withURLParam(req, "id", testWorkspaceID)
	var rows []MemberWithUserResponse
	testutil.Call(t, testHandler.ListMembersWithUser, req).Want(http.StatusOK).JSON(&rows)
	byUser := make(map[string]MemberWithUserResponse, len(rows))
	for _, row := range rows {
		byUser[row.UserID] = row
	}
	return byUser
}

func TestListMembersWithUser_OwnerSeesManagementFields(t *testing.T) {
	adminID := dbfx.User(t, "Roster Admin", "roster-admin-"+uuid.NewString()+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")

	roster := rosterAs(t, testUserID)
	row, ok := roster[adminID]
	if !ok {
		t.Fatalf("admin missing from owner roster")
	}
	if row.Role != "admin" || row.Email == "" || row.CreatedAt == "" {
		t.Fatalf("owner should see role/email/created_at, got %+v", row)
	}
}

func TestListMembersWithUser_NonOwnerGetsNameAndAvatarOnly(t *testing.T) {
	for _, role := range []string{"admin", "member", "guest"} {
		t.Run(role, func(t *testing.T) {
			email := "roster-" + role + "-" + uuid.NewString() + "@multica.ai"
			userID := dbfx.User(t, "Roster "+role, email)
			dbfx.Member(t, testWorkspaceID, userID, role)

			roster := rosterAs(t, userID)

			owner, ok := roster[testUserID]
			if !ok {
				t.Fatalf("owner missing from %s roster", role)
			}
			if owner.ID == "" || owner.Name == "" {
				t.Fatalf("%s must still get id and name for pickers/mentions, got %+v", role, owner)
			}
			if owner.Role != "" || owner.Email != "" || owner.CreatedAt != "" {
				t.Fatalf("%s must not see the owner's role/email/created_at, got %+v", role, owner)
			}
			for uid, row := range roster {
				if uid == userID {
					continue
				}
				if row.Role != "" || row.Email != "" || row.CreatedAt != "" {
					t.Fatalf("%s sees management fields of %s: %+v", role, uid, row)
				}
			}
			// The caller's own row keeps its role: the UI decides which
			// buttons to show from it.
			self := roster[userID]
			if self.Role != role || self.Email != email {
				t.Fatalf("own row should stay whole, got %+v", self)
			}
		})
	}
}

func TestMemberManagement_NonOwnerGets403(t *testing.T) {
	for _, role := range []string{"admin", "member", "guest"} {
		t.Run(role, func(t *testing.T) {
			userID := dbfx.User(t, "Manage "+role, "manage-"+role+"-"+uuid.NewString()+"@multica.ai")
			dbfx.Member(t, testWorkspaceID, userID, role)
			targetID := dbfx.User(t, "Manage Target", "manage-target-"+uuid.NewString()+"@multica.ai")
			targetMemberID := dbfx.Member(t, testWorkspaceID, targetID, "member")

			as := func(method, path string, body any, params ...string) *http.Request {
				req := newRequest(method, path, body)
				req.Header.Set("X-User-ID", userID)
				return testutil.WithURLParams(req, append([]string{"id", testWorkspaceID}, params...)...)
			}

			testutil.Call(t, testHandler.ListWorkspaceInvitations,
				as("GET", "/api/workspaces/"+testWorkspaceID+"/invitations", nil)).Want(http.StatusForbidden)
			testutil.Call(t, testHandler.CreateInvitation,
				as("POST", "/api/workspaces/"+testWorkspaceID+"/members", CreateMemberRequest{Email: "nobody-" + uuid.NewString() + "@multica.ai", Role: "member"})).Want(http.StatusForbidden)
			testutil.Call(t, testHandler.UpdateMember,
				as("PATCH", "/api/workspaces/"+testWorkspaceID+"/members/"+targetMemberID, UpdateMemberRequest{Role: "admin"}, "memberId", targetMemberID)).Want(http.StatusForbidden)
			testutil.Call(t, testHandler.DeleteMember,
				as("DELETE", "/api/workspaces/"+testWorkspaceID+"/members/"+targetMemberID, nil, "memberId", targetMemberID)).Want(http.StatusForbidden)
			testutil.Call(t, testHandler.RevokeInvitation,
				as("DELETE", "/api/workspaces/"+testWorkspaceID+"/invitations/"+uuid.NewString(), nil, "invitationId", uuid.NewString())).Want(http.StatusForbidden)

			if n := dbfx.Count(t, `SELECT count(*) FROM member WHERE id = $1 AND role = 'member'`, parseUUID(targetMemberID)); n != 1 {
				t.Fatalf("target member was changed or removed by a %s", role)
			}
		})
	}
}

func TestMemberManagement_OwnerStillCan(t *testing.T) {
	targetID := dbfx.User(t, "Owner Manage Target", "owner-manage-"+uuid.NewString()+"@multica.ai")
	targetMemberID := dbfx.Member(t, testWorkspaceID, targetID, "member")

	testutil.Call(t, testHandler.ListWorkspaceInvitations,
		testutil.WithURLParams(newRequest("GET", "/x", nil), "id", testWorkspaceID)).Want(http.StatusOK)
	testutil.Call(t, testHandler.UpdateMember,
		testutil.WithURLParams(newRequest("PATCH", "/x", UpdateMemberRequest{Role: "admin"}), "id", testWorkspaceID, "memberId", targetMemberID)).Want(http.StatusOK)
	testutil.Call(t, testHandler.DeleteMember,
		testutil.WithURLParams(newRequest("DELETE", "/x", nil), "id", testWorkspaceID, "memberId", targetMemberID)).Want(http.StatusNoContent)
}
