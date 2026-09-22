package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// 验收席 (the acceptance slot) is a native reference pair — reviewer_type plus
// reviewer_id — rather than a workspace property holding a seat NAME, so that
// renaming or archiving an agent cannot leave a stale copy behind. These tests
// pin the three things that design depends on: the pair crosses the API
// boundary, it is validated as a reference into THIS workspace, and it
// survives writes that are about something else. (DENE-633)

// readIssueReviewer returns the stored pair as the two strings a test can
// compare, with "" standing for NULL in either column.
func readIssueReviewer(t *testing.T, issueID string) (string, string) {
	t.Helper()
	var reviewerType, reviewerID *string
	dbfx.QueryRow(t,
		`SELECT reviewer_type, reviewer_id::text FROM issue WHERE id = $1`, issueID,
	).Scan(&reviewerType, &reviewerID)
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return deref(reviewerType), deref(reviewerID)
}

func TestGetIssue_EmitsTheReviewerPair(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "reviewer-response-agent", nil)
	issueID := dbfx.Issue(t, "reviewer response issue", testutil.Cols{
		"reviewer_type": "agent",
		"reviewer_id":   agentID,
	})

	var got IssueResponse
	testutil.Call(t, testHandler.GetIssue, testutil.WithURLParams(
		newRequest(http.MethodGet, "/api/issues/"+issueID, nil), "id", issueID,
	)).Want(http.StatusOK).JSON(&got)

	if got.ReviewerType == nil || *got.ReviewerType != "agent" {
		t.Errorf("reviewer_type = %v, want agent", got.ReviewerType)
	}
	if got.ReviewerID == nil || *got.ReviewerID != agentID {
		t.Errorf("reviewer_id = %v, want the agent's id %s", got.ReviewerID, agentID)
	}
}

func TestUpdateIssue_AcceptsEveryReviewerForm(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "reviewer-accept-agent", nil)

	cases := []struct {
		name     string
		body     map[string]any
		wantType string
		wantID   string
	}{
		{
			// "needs no acceptance pass" is a written ANSWER, not an empty
			// slot: routing re-judges an empty slot on every pass.
			name:     "none carries no id",
			body:     map[string]any{"reviewer_type": "none", "reviewer_id": nil},
			wantType: "none",
		},
		{
			name:     "an agent of this workspace",
			body:     map[string]any{"reviewer_type": "agent", "reviewer_id": agentID},
			wantType: "agent",
			wantID:   agentID,
		},
		{
			name:     "a member of this workspace",
			body:     map[string]any{"reviewer_type": "member", "reviewer_id": testUserID},
			wantType: "member",
			wantID:   testUserID,
		},
		{
			// Explicit nulls put the issue back to undecided, which is a
			// different state from "none" and must stay reachable.
			name: "explicit nulls clear it back to undecided",
			body: map[string]any{"reviewer_type": nil, "reviewer_id": nil},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issueID := dbfx.Issue(t, "reviewer accept issue", testutil.Cols{
				"reviewer_type": "none",
			})

			var got IssueResponse
			testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(
				newRequest(http.MethodPut, "/api/issues/"+issueID, tc.body), "id", issueID,
			)).Want(http.StatusOK).JSON(&got)

			gotType, gotID := readIssueReviewer(t, issueID)
			if gotType != tc.wantType || gotID != tc.wantID {
				t.Errorf("stored pair = (%q, %q), want (%q, %q)", gotType, gotID, tc.wantType, tc.wantID)
			}
		})
	}
}

func TestUpdateIssue_RejectsAReviewerItCannotResolve(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	archivedID := createHandlerTestAgent(t, "reviewer-archived-agent", nil)
	testutil.Call(t, testHandler.ArchiveAgent, testutil.WithURLParams(
		newRequest(http.MethodPost, "/api/agents/"+archivedID+"/archive", nil), "id", archivedID,
	)).Want(http.StatusOK)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"id without a type", map[string]any{"reviewer_id": testUserID}},
		{"none with an id", map[string]any{"reviewer_type": "none", "reviewer_id": testUserID}},
		{"agent without an id", map[string]any{"reviewer_type": "agent"}},
		{"an unknown type", map[string]any{"reviewer_type": "squad", "reviewer_id": testUserID}},
		{"an archived agent", map[string]any{"reviewer_type": "agent", "reviewer_id": archivedID}},
		{"a member id that is not a member", map[string]any{"reviewer_type": "member", "reviewer_id": archivedID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issueID := dbfx.Issue(t, "reviewer reject issue")

			testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(
				newRequest(http.MethodPut, "/api/issues/"+issueID, tc.body), "id", issueID,
			)).Want(http.StatusBadRequest)

			if gotType, gotID := readIssueReviewer(t, issueID); gotType != "" || gotID != "" {
				t.Errorf("a rejected update still wrote (%q, %q), want the slot untouched", gotType, gotID)
			}
		})
	}
}

// TestUpdateIssue_UnrelatedWriteKeepsTheReviewer guards the sqlc.narg hazard:
// UpdateIssue treats a bare null parameter as "write NULL", so any caller that
// builds UpdateIssueParams without carrying the current pair over silently
// erases the acceptance slot on a title or status edit.
func TestUpdateIssue_UnrelatedWriteKeepsTheReviewer(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "reviewer-survives-agent", nil)
	issueID := dbfx.Issue(t, "reviewer survives issue", testutil.Cols{
		"reviewer_type": "agent",
		"reviewer_id":   agentID,
	})

	for _, body := range []map[string]any{
		{"title": "renamed while a reviewer is set"},
		{"priority": "high"},
		{"description": "a description edit takes the atomic path"},
	} {
		testutil.Call(t, testHandler.UpdateIssue, testutil.WithURLParams(
			newRequest(http.MethodPut, "/api/issues/"+issueID, body), "id", issueID,
		)).Want(http.StatusOK)

		gotType, gotID := readIssueReviewer(t, issueID)
		if gotType != "agent" || gotID != agentID {
			t.Fatalf("after updating %v the pair is (%q, %q), want (agent, %s)", body, gotType, gotID, agentID)
		}
	}
}

// TestArchiveAgent_ReleasesItsReviewerSlots covers the cleanup this design owes
// the repository's no-foreign-key rule: nothing in the database releases a
// reference, so an archived agent would otherwise stay named on tickets it can
// never accept.
func TestArchiveAgent_ReleasesItsReviewerSlots(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "reviewer-release-agent", nil)
	otherID := createHandlerTestAgent(t, "reviewer-keep-agent", nil)

	released := dbfx.Issue(t, "reviewer release issue", testutil.Cols{
		"reviewer_type": "agent",
		"reviewer_id":   agentID,
	})
	kept := dbfx.Issue(t, "reviewer keep issue", testutil.Cols{
		"reviewer_type": "agent",
		"reviewer_id":   otherID,
	})

	testutil.Call(t, testHandler.ArchiveAgent, testutil.WithURLParams(
		newRequest(http.MethodPost, "/api/agents/"+agentID+"/archive", nil), "id", agentID,
	)).Want(http.StatusOK)

	if gotType, gotID := readIssueReviewer(t, released); gotType != "" || gotID != "" {
		t.Errorf("archived agent still holds the slot: (%q, %q), want it cleared", gotType, gotID)
	}
	if gotType, gotID := readIssueReviewer(t, kept); gotType != "agent" || gotID != otherID {
		t.Errorf("another agent's slot = (%q, %q), want (agent, %s)", gotType, gotID, otherID)
	}
}
