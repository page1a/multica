package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// childProgressEntry mirrors the anonymous payload ChildIssueProgress writes.
type childProgressEntry struct {
	ParentIssueID string `json:"parent_issue_id"`
	Total         int64  `json:"total"`
	Done          int64  `json:"done"`
	Blocked       int64  `json:"blocked"`
	Active        int64  `json:"active"`
}

func childProgressFor(t *testing.T, parentID string) childProgressEntry {
	t.Helper()
	var payload struct {
		Progress []childProgressEntry `json:"progress"`
	}
	testutil.Call(t, testHandler.ChildIssueProgress,
		newRequest(http.MethodGet, "/api/issues/child-progress", nil),
	).Want(http.StatusOK).JSON(&payload)
	for _, entry := range payload.Progress {
		if entry.ParentIssueID == parentID {
			return entry
		}
	}
	t.Fatalf("no child-progress row for parent %s", parentID)
	return childProgressEntry{}
}

// The project views bubble a stuck or stalled sub-issue pipeline onto the
// parent card, which only works if the roll-up says more than done/total.
// (DENE-444)
func TestChildIssueProgressReportsBlockedAndActiveCounts(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	parent := dbfx.Issue(t, "rollup parent", testutil.Cols{"status": "in_progress"})
	for _, child := range []struct {
		title  string
		status string
	}{
		{"rollup child done", "done"},
		{"rollup child cancelled", "cancelled"},
		{"rollup child blocked", "blocked"},
		{"rollup child running", "in_progress"},
		{"rollup child reviewing", "in_review"},
		{"rollup child queued", "todo"},
	} {
		dbfx.Issue(t, child.title, testutil.Cols{
			"status":          child.status,
			"parent_issue_id": parent,
		})
	}

	got := childProgressFor(t, parent)

	want := childProgressEntry{ParentIssueID: parent, Total: 6, Done: 2, Blocked: 1, Active: 2}
	if got != want {
		t.Errorf("roll-up = %+v, want %+v (done counts cancelled; active is in_progress + in_review, never todo)", got, want)
	}
}

// A workspace can rename the built-ins away entirely, so the roll-up resolves
// every count through the status CATEGORY rather than the literal key. Since
// MUL-7365 a stored category is one of the four lifecycle categories, so a
// renamed built-in is a custom status carrying that category.
func TestChildIssueProgressCountsCustomStatusesByCategory(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	// issue_status.key caps at 32 chars (migration 332), so the uniquifier is
	// the low digits of the clock rather than the whole nanosecond count.
	suffix := time.Now().UnixNano() % 1_000_000_000
	doneKey := fmt.Sprintf("shipped_%d", suffix)
	activeKey := fmt.Sprintf("qapass_%d", suffix)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue_status (workspace_id, key, name, description, category, color, position)
		VALUES ($1, $2, 'Shipped', '', 'done', '#ff0000', 90),
		       ($1, $3, 'QA pass', '', 'started', '#00ff00', 91)
	`, testWorkspaceID, doneKey, activeKey); err != nil {
		t.Fatalf("create custom statuses: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_status WHERE workspace_id = $1 AND key = ANY($2::text[])`,
			testWorkspaceID, []string{doneKey, activeKey})
	})

	parent := dbfx.Issue(t, "custom rollup parent", testutil.Cols{"status": "in_progress"})
	dbfx.Issue(t, "custom rollup shipped child", testutil.Cols{
		"status": doneKey, "parent_issue_id": parent,
	})
	dbfx.Issue(t, "custom rollup active child", testutil.Cols{
		"status": activeKey, "parent_issue_id": parent,
	})

	got := childProgressFor(t, parent)

	want := childProgressEntry{ParentIssueID: parent, Total: 2, Done: 1, Blocked: 0, Active: 1}
	if got != want {
		t.Errorf("roll-up = %+v, want %+v (a custom status counts under its category; a started custom status is active, not blocked)", got, want)
	}
}

// tableRowTitles posts one Table rows request and returns the titles it got.
func tableRowTitles(t *testing.T, projectID string, filters map[string]any) []string {
	t.Helper()
	var payload struct {
		Rows []struct {
			Issue struct {
				Title string `json:"title"`
			} `json:"issue"`
		} `json:"rows"`
	}
	testutil.Call(t, testHandler.ListIssueTableRows, newRequest(http.MethodPost, "/api/issues/table/rows", map[string]any{
		"query": map[string]any{
			"scope":   map[string]any{"kind": "project", "project_id": projectID},
			"filters": filters,
			"sort":    map[string]any{"field": "position", "direction": "asc"},
		},
		"group":     map[string]any{"kind": "none"},
		"hierarchy": map[string]any{"enabled": false},
	})).Want(http.StatusOK).JSON(&payload)

	titles := make([]string, 0, len(payload.Rows))
	for _, row := range payload.Rows {
		titles = append(titles, row.Issue.Title)
	}
	return titles
}

// `hide_completed_parents` is narrower than hiding the done category: a
// terminal parent that still has an open child is exactly the row a project
// owner must keep seeing, because closing the parent is what buried that work.
// (DENE-444)
func TestIssueTableHideCompletedParentsKeepsTerminalParentsWithOpenChildren(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	projectID := dbfx.Insert(t, "project", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"title":        fmt.Sprintf("hide-completed %d", time.Now().UnixNano()),
	})

	finishedThrough := dbfx.Issue(t, "finished through", testutil.Cols{
		"status": "done", "project_id": projectID,
	})
	dbfx.Issue(t, "finished through child", testutil.Cols{
		"status": "done", "project_id": projectID, "parent_issue_id": finishedThrough,
	})

	closedTooEarly := dbfx.Issue(t, "closed too early", testutil.Cols{
		"status": "done", "project_id": projectID,
	})
	dbfx.Issue(t, "closed too early child", testutil.Cols{
		"status": "blocked", "project_id": projectID, "parent_issue_id": closedTooEarly,
	})

	dbfx.Issue(t, "childless and done", testutil.Cols{
		"status": "done", "project_id": projectID,
	})
	dbfx.Issue(t, "still open", testutil.Cols{
		"status": "in_progress", "project_id": projectID,
	})

	unfiltered := tableRowTitles(t, projectID, map[string]any{})
	if len(unfiltered) != 6 {
		t.Fatalf("unfiltered rows = %v, want all six seeded issues", unfiltered)
	}

	got := tableRowTitles(t, projectID, map[string]any{"hide_completed_parents": true})

	kept := map[string]bool{}
	for _, title := range got {
		kept[title] = true
	}
	for _, title := range []string{"closed too early", "closed too early child", "still open"} {
		if !kept[title] {
			t.Errorf("%q was hidden; it is not finished through", title)
		}
	}
	for _, title := range []string{"finished through", "finished through child", "childless and done"} {
		if kept[title] {
			t.Errorf("%q survived the filter; every part of it is finished", title)
		}
	}
}
