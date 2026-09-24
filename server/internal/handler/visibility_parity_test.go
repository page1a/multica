package handler

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/permission"
)

// canSeeIssue and issueVisibilitySQL answer the same question in two
// languages, and the split is not optional: a paginated list has to filter
// inside the query or its pages and totals describe rows the caller cannot
// see, while an unpaginated read filters in Go. If the two ever disagree, one
// surface leaks what the other hides — and the disagreement would be silent,
// because each is covered by tests that only exercise its own side.
//
// This test runs the SQL predicate against PostgreSQL over a synthetic issue
// row and compares it with the Go answer for every combination of tier,
// creator, project and viewer role.
func TestIssueVisibilitySQLAgreesWithCanSeeIssue(t *testing.T) {
	requireDB(t)

	me := mustUUID(t, "11111111-1111-4111-8111-111111111111")
	other := mustUUID(t, "22222222-2222-4222-8222-222222222222")
	myProject := mustUUID(t, "33333333-3333-4333-8333-333333333333")
	otherProject := mustUUID(t, "44444444-4444-4444-8444-444444444444")
	// sharedIssue is shared directly with me (resource_share); plainIssue is not.
	sharedIssue := mustUUID(t, "55555555-5555-4555-8555-555555555555")
	plainIssue := mustUUID(t, "66666666-6666-4666-8666-666666666666")
	zeroWorkspace := mustUUID(t, "00000000-0000-0000-0000-000000000000")
	ctx := context.Background()
	if _, err := testPool.Exec(ctx,
		`INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id) VALUES ($1, 'issue', $2, $3)`,
		zeroWorkspace, uuidToString(sharedIssue), me); err != nil {
		t.Fatalf("seed share: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM resource_share WHERE workspace_id = $1`, zeroWorkspace)
	})
	sharedWithMe := map[string]struct{}{uuidToString(sharedIssue): {}}

	viewers := map[string]visibilityViewer{
		"member in no project": {userID: me, role: permission.RoleMember},
		"guest in no project":  {userID: me, role: permission.RoleGuest},
		"member in a project": {
			userID: me, role: permission.RoleMember,
			projectIDs: []pgtype.UUID{myProject},
			projectSet: map[[16]byte]struct{}{myProject.Bytes: {}},
		},
		"guest in a project": {
			userID: me, role: permission.RoleGuest,
			projectIDs: []pgtype.UUID{myProject},
			projectSet: map[[16]byte]struct{}{myProject.Bytes: {}},
		},
		"admin": {
			userID: me, role: permission.RoleAdmin,
			projectIDs: []pgtype.UUID{myProject, otherProject},
			projectSet: map[[16]byte]struct{}{myProject.Bytes: {}, otherProject.Bytes: {}},
		},
		"unidentified caller": {},
	}

	// The viewer loads its own shares; with the seeded row, every viewer that
	// is me has the shared issue in its set.
	for name, v := range viewers {
		if v.userID.Valid {
			v.sharedIssues = sharedWithMe
			viewers[name] = v
		}
	}

	type issueRow struct {
		id           pgtype.UUID
		visibility   string
		creatorType  string
		creatorID    pgtype.UUID
		projectID    pgtype.UUID
		assigneeType string
		assigneeID   pgtype.UUID
	}
	rows := []issueRow{}
	for _, vis := range []string{"private", "project", "workspace", "something_new"} {
		for _, creator := range []pgtype.UUID{me, other} {
			for _, project := range []pgtype.UUID{myProject, otherProject, {}} {
				// Unassigned, assigned to the viewer, assigned to somebody
				// else, and assigned to an agent: the assignee is a sharing
				// relation of its own, so every tier is crossed with it.
				for _, id := range []pgtype.UUID{plainIssue, sharedIssue} {
					rows = append(rows,
						issueRow{id, vis, "member", creator, project, "", pgtype.UUID{}},
						issueRow{id, vis, "member", creator, project, "member", me},
						issueRow{id, vis, "member", creator, project, "member", other},
						issueRow{id, vis, "member", creator, project, "agent", me},
					)
				}
			}
		}
		// An agent-created issue: creator_id points at an agent, so no human
		// is ever its creator.
		rows = append(rows, issueRow{plainIssue, vis, "agent", other, myProject, "", pgtype.UUID{}})
	}

	for viewerName, viewer := range viewers {
		for _, row := range rows {
			var args []any
			addArg := func(v any) string {
				args = append(args, v)
				// $1..$7 carry the synthetic row, so the predicate's own
				// arguments start at $8.
				return fmt.Sprintf("$%d", len(args)+7)
			}
			predicate := viewer.issueVisibilitySQL("i", addArg)
			// EXISTS, not SELECT <predicate>: the predicate lives in a WHERE
			// clause, where an UNKNOWN (a NULL project_id compared with ANY)
			// excludes the row. Reading the bare expression would compare Go's
			// false against SQL's NULL and call a match a disagreement.
			query := fmt.Sprintf(`
				SELECT EXISTS(
					SELECT 1 FROM (
						SELECT $1::text AS visibility, $2::text AS creator_type,
						       $3::uuid AS creator_id, $4::uuid AS project_id,
						       $5::text AS assignee_type, $6::uuid AS assignee_id,
						       $7::uuid AS id,
						       '00000000-0000-0000-0000-000000000000'::uuid AS workspace_id
					) AS i WHERE %s
				)`, predicate)

			params := append([]any{
				row.visibility, row.creatorType, row.creatorID, row.projectID,
				row.assigneeType, row.assigneeID, row.id,
			}, args...)
			var inSQL bool
			if err := testPool.QueryRow(ctx, query, params...).Scan(&inSQL); err != nil {
				t.Fatalf("%s / %+v: predicate %q failed: %v", viewerName, row, predicate, err)
			}
			inGo := viewer.canSeeIssueFields(row.id,
				row.visibility, row.creatorType, row.creatorID, row.projectID,
				row.assigneeType, row.assigneeID)
			if inSQL != inGo {
				t.Fatalf("%s disagrees about %s issue (creator_type=%s, mine=%t, in my project=%t, shared with me=%t, assigned to me=%t): SQL says %t, Go says %t",
					viewerName, row.visibility, row.creatorType,
					row.creatorID == me, row.projectID == myProject, row.id == sharedIssue,
					row.assigneeType == "member" && row.assigneeID == me, inSQL, inGo)
			}
		}
	}
}

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := parseUUIDSafe(s)
	if err != nil {
		t.Fatalf("bad fixture uuid %q: %v", s, err)
	}
	return id
}
