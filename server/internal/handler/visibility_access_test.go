package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The share button reads GET /access before anyone clicks it (DENE-1214). Its
// answer must match what the save does: if access says "you may change it",
// the PUT goes through, and if it says no, the PUT is refused for the reason
// access names.
func TestIssueAccessAgreesWithTheScopeChange(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Access Author", "access-author@multica.ai")
	other := visibilityTestMember(t, "Access Other", "access-other@multica.ai")
	guest := dbfx.User(t, "Access Guest", "access-guest@multica.ai")
	dbfx.Member(t, testWorkspaceID, guest, "guest")
	issueID := dbfx.Issue(t, "shared with the workspace", testutil.Cols{
		"creator_type": "member",
		"creator_id":   author,
		"visibility":   "workspace",
	})
	dbfx.Cleanup(t, `DELETE FROM visibility_audit WHERE workspace_id = $1 AND resource_id = $2`,
		testWorkspaceID, issueID)

	access := func(userID string) SharingAccessResponse {
		var out SharingAccessResponse
		testutil.Call(t, testHandler.GetIssueAccess,
			withURLParam(newRequestAs(userID, "GET", "/api/issues/"+issueID+"/access", nil), "id", issueID)).
			Want(200).JSON(&out)
		return out
	}
	change := func(userID string) *testutil.Response {
		return testutil.Call(t, testHandler.SetIssueVisibility,
			withURLParam(newRequestAs(userID, "PUT", "/api/issues/"+issueID+"/visibility",
				map[string]any{"visibility": "workspace"}), "id", issueID))
	}

	mine := access(author)
	if !mine.CanChange || mine.Reason != nil || mine.Visibility != "workspace" || mine.AudienceSize < 2 {
		t.Fatalf("author access = %+v, want can_change with the workspace audience", mine)
	}
	change(author).Want(200)

	theirs := access(other)
	if theirs.CanChange || theirs.Reason == nil || *theirs.Reason != accessReasonNotCreator {
		t.Fatalf("other member access = %+v, want can_change=false reason=not_creator", theirs)
	}
	change(other).Want(403)

	// A guest cannot see a workspace-scoped issue at all, so share it first.
	testutil.Call(t, testHandler.SetIssueVisibility,
		withURLParam(newRequestAs(author, "PUT", "/api/issues/"+issueID+"/visibility",
			map[string]any{"visibility": "project"}), "id", issueID)).Want(200)
	testutil.Call(t, testHandler.AddIssueShare,
		withURLParam(newRequestAs(author, "POST", "/api/issues/"+issueID+"/shares",
			map[string]any{"member_id": guest}), "id", issueID)).Want(201)
	visitor := access(guest)
	if visitor.CanChange || visitor.Reason == nil || *visitor.Reason != accessReasonGuest {
		t.Fatalf("guest access = %+v, want can_change=false reason=guest", visitor)
	}

	stranger := visibilityTestMember(t, "Access Stranger", "access-stranger@multica.ai")
	testutil.Call(t, testHandler.SetIssueVisibility,
		withURLParam(newRequestAs(author, "PUT", "/api/issues/"+issueID+"/visibility",
			map[string]any{"visibility": "private"}), "id", issueID)).Want(200)
	testutil.Call(t, testHandler.GetIssueAccess,
		withURLParam(newRequestAs(stranger, "GET", "/api/issues/"+issueID+"/access", nil), "id", issueID)).
		Want(404)
}

func TestProjectAccessFollowsTheProjectRule(t *testing.T) {
	requireDB(t)

	member := visibilityTestMember(t, "Access Project Member", "access-project-member@multica.ai")
	projectID := dbfx.Project(t, "access read", testutil.Cols{"visibility": "workspace"})

	var owner SharingAccessResponse
	testutil.Call(t, testHandler.GetProjectAccess,
		withURLParam(newRequest("GET", "/api/projects/"+projectID+"/access", nil), "id", projectID)).
		Want(200).JSON(&owner)
	if !owner.CanChange || owner.Visibility != "workspace" {
		t.Fatalf("owner project access = %+v, want can_change", owner)
	}

	var plain SharingAccessResponse
	testutil.Call(t, testHandler.GetProjectAccess,
		withURLParam(newRequestAs(member, "GET", "/api/projects/"+projectID+"/access", nil), "id", projectID)).
		Want(200).JSON(&plain)
	if plain.CanChange || plain.Reason == nil || *plain.Reason != accessReasonNotCreator {
		t.Fatalf("member project access = %+v, want can_change=false reason=not_creator", plain)
	}
	testutil.Call(t, testHandler.SetProjectVisibility,
		withURLParam(newRequestAs(member, "PUT", "/api/projects/"+projectID+"/visibility",
			map[string]any{"visibility": "workspace"}), "id", projectID)).Want(403)
}
