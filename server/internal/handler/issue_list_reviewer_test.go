package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The acceptance slot (reviewer_type + reviewer_id, DENE-633) is selected by the
// sqlc source queries but four list read paths build their SQL by hand for
// dynamic filtering. Each of those hand-written SELECT lists once omitted the
// pair while its positional rows.Scan stayed in step, so the columns were
// silently zero — the detail endpoint was right, every list payload was not.
//
// These four tests are the tripwire that was missing: each read path must carry
// the slot for both shapes, an agent reviewer (type 'agent' plus its id) and an
// explicit 'none' (type 'none' with a null id).

// reviewerReadPathFixture is one project whose issues exercise both halves of
// the acceptance slot. The token makes every title unique per run so the
// workspace-wide search assertion cannot see another test's rows.
type reviewerReadPathFixture struct {
	projectID  string
	agentID    string
	agentIssue string
	noneIssue  string
	token      string
}

func seedReviewerReadPathFixture(t *testing.T) reviewerReadPathFixture {
	t.Helper()

	// No dash before the digits: buildSearchQuery treats a "PREFIX-123" query as
	// an issue identifier and binds the number as int4, which a nanosecond
	// token overflows.
	token := fmt.Sprintf("rev%d", time.Now().UnixNano())
	projectID := dbfx.Project(t, "Reviewer read paths "+token)
	agentID := dbfx.Agent(t, "Reviewer read paths agent "+token, "")
	agentIssue := dbfx.Issue(t, token+"-agent-reviewed", testutil.Cols{
		"project_id":    projectID,
		"status":        "todo",
		"reviewer_type": "agent",
		"reviewer_id":   agentID,
	})
	noneIssue := dbfx.Issue(t, token+"-no-reviewer", testutil.Cols{
		"project_id":    projectID,
		"status":        "in_review",
		"reviewer_type": "none",
	})

	return reviewerReadPathFixture{
		projectID:  projectID,
		agentID:    agentID,
		agentIssue: agentIssue,
		noneIssue:  noneIssue,
		token:      token,
	}
}

func issueRowsByID(rows []IssueResponse) map[string]IssueResponse {
	out := make(map[string]IssueResponse, len(rows))
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

// reviewerPtrString renders a nullable reviewer field for comparison, so a nil
// and an empty string stay distinguishable from a set value.
func reviewerPtrString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

func reviewerRow(t *testing.T, rows map[string]IssueResponse, id, label string) IssueResponse {
	t.Helper()
	row, ok := rows[id]
	if !ok {
		t.Fatalf("%s: issue %s missing from the response", label, id)
	}
	return row
}

func wantAgentReviewer(t *testing.T, label string, got IssueResponse, agentID string) {
	t.Helper()
	if got.ReviewerType == nil || *got.ReviewerType != "agent" {
		t.Errorf("%s: reviewer_type = %v, want agent", label, got.ReviewerType)
	}
	if got.ReviewerID == nil || *got.ReviewerID != agentID {
		t.Errorf("%s: reviewer_id = %v, want %s", label, got.ReviewerID, agentID)
	}
}

func wantNoReviewer(t *testing.T, label string, got IssueResponse) {
	t.Helper()
	if got.ReviewerType == nil || *got.ReviewerType != "none" {
		t.Errorf("%s: reviewer_type = %v, want none", label, got.ReviewerType)
	}
	if got.ReviewerID != nil {
		t.Errorf("%s: reviewer_id = %v, want null", label, got.ReviewerID)
	}
}

func TestListIssuesIncludesReviewer(t *testing.T) {
	fx := seedReviewerReadPathFixture(t)

	list := func(query string) map[string]IssueResponse {
		t.Helper()
		path := fmt.Sprintf("/api/issues?workspace_id=%s&project_id=%s%s",
			testWorkspaceID, fx.projectID, query)
		var out struct {
			Issues []IssueResponse `json:"issues"`
		}
		testutil.Call(t, testHandler.ListIssues, newRequest(http.MethodGet, path, nil)).
			Want(http.StatusOK).
			JSON(&out)
		return issueRowsByID(out.Issues)
	}

	// Baseline: both slot shapes come back on the unfiltered list.
	baseline := list("&limit=500")
	if len(baseline) != 2 {
		t.Fatalf("baseline rows = %d, want 2", len(baseline))
	}
	wantAgentReviewer(t, "baseline agent row", reviewerRow(t, baseline, fx.agentIssue, "baseline"), fx.agentID)
	wantNoReviewer(t, "baseline none row", reviewerRow(t, baseline, fx.noneIssue, "baseline"))

	// The reported symptom was the list payload disagreeing with the detail
	// endpoint for the same issue, so pin the two against each other rather
	// than only against the fixture's expected value.
	for _, id := range []string{fx.agentIssue, fx.noneIssue} {
		var detail IssueResponse
		testutil.Call(t, testHandler.GetIssue,
			withURLParam(newRequest(http.MethodGet, "/api/issues/"+id, nil), "id", id)).
			Want(http.StatusOK).
			JSON(&detail)

		listed := reviewerRow(t, baseline, id, "list-vs-detail")
		if reviewerPtrString(listed.ReviewerType) != reviewerPtrString(detail.ReviewerType) ||
			reviewerPtrString(listed.ReviewerID) != reviewerPtrString(detail.ReviewerID) {
			t.Errorf("list/detail reviewer disagree for %s: list=(%v,%v) detail=(%v,%v)",
				id, reviewerPtrString(listed.ReviewerType), reviewerPtrString(listed.ReviewerID),
				reviewerPtrString(detail.ReviewerType), reviewerPtrString(detail.ReviewerID))
		}
	}

	// ?status= takes a different filter arm; the projection must not drift with it.
	todo := list("&status=todo&limit=500")
	if len(todo) != 1 {
		t.Fatalf("status=todo rows = %d, want 1", len(todo))
	}
	wantAgentReviewer(t, "status=todo agent row", reviewerRow(t, todo, fx.agentIssue, "status=todo"), fx.agentID)

	inReview := list("&status=in_review&limit=500")
	if len(inReview) != 1 {
		t.Fatalf("status=in_review rows = %d, want 1", len(inReview))
	}
	wantNoReviewer(t, "status=in_review none row", reviewerRow(t, inReview, fx.noneIssue, "status=in_review"))

	// Offset paging: every page carries the slot, not just the first.
	page1 := list("&limit=1&offset=0")
	page2 := list("&limit=1&offset=1")
	if len(page1) != 1 || len(page2) != 1 {
		t.Fatalf("paged rows = %d and %d, want 1 each", len(page1), len(page2))
	}
	paged := map[string]IssueResponse{}
	for _, page := range []map[string]IssueResponse{page1, page2} {
		for id, row := range page {
			paged[id] = row
		}
	}
	if len(paged) != 2 {
		t.Fatalf("paged rows across both pages = %d, want 2", len(paged))
	}
	wantAgentReviewer(t, "paged agent row", reviewerRow(t, paged, fx.agentIssue, "paged"), fx.agentID)
	wantNoReviewer(t, "paged none row", reviewerRow(t, paged, fx.noneIssue, "paged"))
}

func TestListGroupedIssuesIncludesReviewer(t *testing.T) {
	fx := seedReviewerReadPathFixture(t)

	path := fmt.Sprintf("/api/issues/grouped?workspace_id=%s&group_by=assignee&project_id=%s&limit=100",
		testWorkspaceID, fx.projectID)
	var response GroupedIssuesResponse
	testutil.Call(t, testHandler.ListGroupedIssues, newRequest(http.MethodGet, path, nil)).
		Want(http.StatusOK).
		JSON(&response)

	var rows []IssueResponse
	for _, group := range response.Groups {
		rows = append(rows, group.Issues...)
	}
	byID := issueRowsByID(rows)
	wantAgentReviewer(t, "grouped agent row", reviewerRow(t, byID, fx.agentIssue, "grouped"), fx.agentID)
	wantNoReviewer(t, "grouped none row", reviewerRow(t, byID, fx.noneIssue, "grouped"))
}

func TestListIssueTableRowsIncludesReviewer(t *testing.T) {
	fx := seedReviewerReadPathFixture(t)

	var response issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows,
		newRequest(http.MethodPost, "/api/issues/table/rows", issueTableRowsRequest{
			Query: issueTableQuerySpec{
				Scope: issueTableScope{Kind: "project", ProjectID: fx.projectID},
				Sort:  issueTableSortRequest{Field: "position", Direction: "asc"},
			},
			Group:     issueTableGroupSpec{Kind: "none"},
			Hierarchy: issueTableHierarchyRequest{Enabled: false},
			Page:      issueTablePageRequest{Limit: 10},
		})).
		Want(http.StatusOK).
		JSON(&response)

	byID := make(map[string]IssueResponse, len(response.Rows))
	for _, row := range response.Rows {
		byID[row.Issue.ID] = row.Issue
	}
	wantAgentReviewer(t, "table row agent issue", reviewerRow(t, byID, fx.agentIssue, "table"), fx.agentID)
	wantNoReviewer(t, "table row none issue", reviewerRow(t, byID, fx.noneIssue, "table"))
}

func TestSearchIssuesIncludesReviewer(t *testing.T) {
	fx := seedReviewerReadPathFixture(t)

	path := fmt.Sprintf("/api/issues/search?workspace_id=%s&q=%s&include_closed=true&limit=50",
		testWorkspaceID, url.QueryEscape(fx.token))
	var response struct {
		Issues []SearchIssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.SearchIssues, newRequest(http.MethodGet, path, nil)).
		Want(http.StatusOK).
		JSON(&response)

	rows := make([]IssueResponse, 0, len(response.Issues))
	for _, issue := range response.Issues {
		rows = append(rows, issue.IssueResponse)
	}
	byID := issueRowsByID(rows)
	wantAgentReviewer(t, "search agent row", reviewerRow(t, byID, fx.agentIssue, "search"), fx.agentID)
	wantNoReviewer(t, "search none row", reviewerRow(t, byID, fx.noneIssue, "search"))
}
