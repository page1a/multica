package handler

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The sharing model's acceptance criteria (DENE-698), exercised through the
// handlers a client actually calls. The rules under test are product rules,
// not plumbing: a resource nobody shared reaches nobody, a hidden resource is
// indistinguishable from an absent one, a project is the bulk shortcut, and
// every change leaves a row somebody can find.

// visibilityTestMember seeds a workspace member who belongs to no project —
// the "member in no project" the acceptance criteria are written about.
func visibilityTestMember(t *testing.T, name, email string) string {
	t.Helper()
	userID := dbfx.User(t, name, email)
	dbfx.Member(t, testWorkspaceID, userID, "member")
	return userID
}

func issueIDsInList(t *testing.T, userID, path string) map[string]string {
	t.Helper()
	var out struct {
		Issues []struct {
			ID         string `json:"id"`
			Visibility string `json:"visibility"`
		} `json:"issues"`
	}
	testutil.Call(t, testHandler.ListIssues, newRequestAs(userID, "GET", path, nil)).
		Want(200).JSON(&out)
	ids := map[string]string{}
	for _, issue := range out.Issues {
		ids[issue.ID] = issue.Visibility
	}
	return ids
}

// A new issue is private, and private means its creator: the other member's
// list does not hold it, and asking for it directly is answered exactly the
// way a deleted issue is — "not found", never "no permission".
func TestNewIssueIsPrivateAndInvisibleToAnotherMember(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Vis Author", "vis-author@multica.ai")
	stranger := visibilityTestMember(t, "Vis Stranger", "vis-stranger@multica.ai")

	var created struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	testutil.Call(t, testHandler.CreateIssue,
		newRequestAs(author, "POST", "/api/issues", map[string]any{"title": "private by default"})).
		Want(201).JSON(&created)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	if created.Visibility != "private" {
		t.Fatalf("new issue came back %q, want private — nothing was shared yet", created.Visibility)
	}

	listPath := "/api/issues?workspace_id=" + testWorkspaceID
	if _, ok := issueIDsInList(t, author, listPath)[created.ID]; !ok {
		t.Fatal("the author cannot see their own private issue in their list")
	}
	if _, ok := issueIDsInList(t, stranger, listPath)[created.ID]; ok {
		t.Fatal("a member the issue was never shared with found it in their list")
	}

	get := func(userID string) *testutil.Response {
		req := withURLParam(newRequestAs(userID, "GET", "/api/issues/"+created.ID, nil), "id", created.ID)
		return testutil.Call(t, testHandler.GetIssue, req)
	}
	get(author).Want(200)
	denied := get(stranger).Want(404)
	if body := denied.Text(); !strings.Contains(body, "not found") || strings.Contains(body, "permission") {
		t.Fatalf("hidden issue answered %q; it must read the same as an issue that does not exist", body)
	}
}

// An issue in no project cannot be given 'project' scope: that tier names a
// project's people, so without a project it names nobody. The API says so
// before the CHECK constraint has to.
func TestProjectScopeIsRejectedForAnIssueInNoProject(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Vis Loner", "vis-loner@multica.ai")
	issueID := dbfx.Issue(t, "no project of its own", testutil.Cols{
		"creator_type": "member",
		"creator_id":   author,
		"visibility":   "private",
	})

	req := withURLParam(
		newRequestAs(author, "PUT", "/api/issues/"+issueID+"/visibility", map[string]any{"visibility": "project"}),
		"id", issueID)
	testutil.Call(t, testHandler.SetIssueVisibility, req).Want(400)

	// The constraint behind the API rejects the same write, so a client that
	// skips the handler cannot produce the row either.
	_, err := testPool.Exec(context.Background(),
		`UPDATE issue SET visibility = 'project' WHERE id = $1`, issueID)
	if err == nil {
		t.Fatal("the database accepted 'project' scope on an issue that belongs to no project")
	}
}

// A project is the bulk shortcut: changing its scope overwrites everything it
// holds, and the response reports how much it moved and how much of that only
// its creator could see — the two numbers a confirm dialog is built from.
func TestProjectScopeSweepsItsIssuesAndReportsTheCounts(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Vis Owner", "vis-owner@multica.ai")
	stranger := visibilityTestMember(t, "Vis Outsider", "vis-outsider@multica.ai")

	projectID := dbfx.Project(t, "sweep me", testutil.Cols{
		"visibility": "private",
		"created_by": author,
	})
	private := dbfx.Issue(t, "held, private", testutil.Cols{
		"creator_type": "member", "creator_id": author,
		"project_id": projectID, "visibility": "private",
	})
	alreadyShared := dbfx.Issue(t, "held, already workspace", testutil.Cols{
		"creator_type": "member", "creator_id": author,
		"project_id": projectID, "visibility": "workspace",
	})

	preview := testutil.Call(t, testHandler.PreviewProjectVisibility,
		withURLParam(newRequestAs(author, "GET", "/api/projects/"+projectID+"/visibility/preview", nil), "id", projectID),
	).Want(200).Map()
	if got := preview["affected_count"]; got != float64(2) {
		t.Fatalf("preview says %v resources would move, want 2", got)
	}
	if got := preview["previously_private_count"]; got != float64(1) {
		t.Fatalf("preview says %v were private, want 1 — the other is already workspace-wide", got)
	}

	applied := testutil.Call(t, testHandler.SetProjectVisibility,
		withURLParam(newRequestAs(author, "PUT", "/api/projects/"+projectID+"/visibility",
			map[string]any{"visibility": "workspace"}), "id", projectID),
	).Want(200).Map()
	if applied["affected_count"] != preview["affected_count"] ||
		applied["previously_private_count"] != preview["previously_private_count"] {
		t.Fatalf("the sweep moved %v/%v but the preview promised %v/%v",
			applied["affected_count"], applied["previously_private_count"],
			preview["affected_count"], preview["previously_private_count"])
	}

	listPath := "/api/issues?workspace_id=" + testWorkspaceID
	seen := issueIDsInList(t, stranger, listPath)
	for _, id := range []string{private, alreadyShared} {
		if seen[id] != "workspace" {
			t.Fatalf("issue %s reads %q to another member after the project was shared workspace-wide", id, seen[id])
		}
	}

	// Narrowing to the project tier takes the outsider's access away again,
	// and adding them to the project gives it back with no further write to
	// the issues.
	testutil.Call(t, testHandler.SetProjectVisibility,
		withURLParam(newRequestAs(author, "PUT", "/api/projects/"+projectID+"/visibility",
			map[string]any{"visibility": "project"}), "id", projectID),
	).Want(200)

	if _, ok := issueIDsInList(t, stranger, listPath)[private]; ok {
		t.Fatal("a non-member still sees a project-scoped issue")
	}

	testutil.Call(t, testHandler.AddProjectMember,
		withURLParam(newRequest("POST", "/api/projects/"+projectID+"/members",
			map[string]any{"member_id": stranger}), "id", projectID),
	).Want(201)
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM project_member WHERE project_id = $1 AND member_id = $2`, projectID, stranger)
	})

	if _, ok := issueIDsInList(t, stranger, listPath)[private]; !ok {
		t.Fatal("joining the project did not make its issues visible on the next read")
	}
}

// Every sharing change is locatable afterwards: the direct change and each
// resource a project sweep carried with it.
func TestSharingChangesAreLocatableInTheAuditLog(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Vis Auditor", "vis-auditor@multica.ai")
	projectID := dbfx.Project(t, "audited", testutil.Cols{
		"visibility": "private", "created_by": author,
	})
	issueID := dbfx.Issue(t, "audited issue", testutil.Cols{
		"creator_type": "member", "creator_id": author,
		"project_id": projectID, "visibility": "private",
	})
	dbfx.Cleanup(t, `DELETE FROM visibility_audit WHERE workspace_id = $1 AND actor_id = $2`,
		testWorkspaceID, author)

	testutil.Call(t, testHandler.SetProjectVisibility,
		withURLParam(newRequestAs(author, "PUT", "/api/projects/"+projectID+"/visibility",
			map[string]any{"visibility": "workspace"}), "id", projectID),
	).Want(200)

	var previous, next, source string
	dbfx.QueryRow(t, `
		SELECT previous_visibility, new_visibility, source
		FROM visibility_audit
		WHERE resource_type = 'issue' AND resource_id = $1`, issueID,
	).Scan(&previous, &next, &source)
	if previous != "private" || next != "workspace" || source != "project_bulk" {
		t.Fatalf("the swept issue's audit row reads %s -> %s via %s, want private -> workspace via project_bulk",
			previous, next, source)
	}

	var projectRows int
	if projectRows = dbfx.Count(t,
		`SELECT count(*) FROM visibility_audit WHERE resource_type = 'project' AND resource_id = $1`,
		projectID); projectRows != 1 {
		t.Fatalf("the project's own change left %d audit rows, want 1", projectRows)
	}
}

// Assigning work is itself an act of sharing: the assignee opens and updates
// the issue even though nobody widened its scope, and it is in the list they
// read. Without this, a member could be handed work they cannot see — the
// assignment would silently do nothing.
func TestAssigneeSeesAPrivateIssueTheyWereGiven(t *testing.T) {
	requireDB(t)

	author := visibilityTestMember(t, "Vis Assigner", "vis-assigner@multica.ai")
	worker := visibilityTestMember(t, "Vis Worker", "vis-worker@multica.ai")
	stranger := visibilityTestMember(t, "Vis Bystander", "vis-bystander@multica.ai")

	issueID := dbfx.Issue(t, "yours to do", testutil.Cols{
		"creator_type":  "member",
		"creator_id":    author,
		"visibility":    "private",
		"assignee_type": "member",
		"assignee_id":   worker,
	})

	get := func(userID string) *testutil.Response {
		req := withURLParam(newRequestAs(userID, "GET", "/api/issues/"+issueID, nil), "id", issueID)
		return testutil.Call(t, testHandler.GetIssue, req)
	}
	get(worker).Want(200)
	get(stranger).Want(404)

	listPath := "/api/issues?workspace_id=" + testWorkspaceID
	if _, ok := issueIDsInList(t, worker, listPath)[issueID]; !ok {
		t.Fatal("the assignee cannot find their own assigned issue in the list")
	}
	if _, ok := issueIDsInList(t, stranger, listPath)[issueID]; ok {
		t.Fatal("an uninvolved member sees an issue assigned to somebody else")
	}

	// The scope itself did not move: assignment names one person, it does not
	// widen the tier.
	var visibility string
	dbfx.QueryRow(t, `SELECT visibility FROM issue WHERE id = $1`, issueID).Scan(&visibility)
	if visibility != "private" {
		t.Fatalf("assignment changed the scope to %q; it must stay private", visibility)
	}
}
