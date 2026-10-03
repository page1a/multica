package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/stallaction"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAllChildrenTerminalRequiresChildrenAndEveryChildTerminal(t *testing.T) {
	terminal := func(issue db.Issue) bool { return issue.Status == "done" || issue.Status == "cancelled" }
	if allChildrenTerminal(nil, terminal) {
		t.Fatal("empty child set must not auto-close a parent")
	}
	children := []db.Issue{{Status: "done"}, {Status: "in_progress"}}
	if allChildrenTerminal(children, terminal) {
		t.Fatal("open child must keep parent open")
	}
	children[1].Status = "cancelled"
	if !allChildrenTerminal(children, terminal) {
		t.Fatal("all terminal children should close the barrier")
	}
}

type stallTestStore struct {
	routing.Store
	settings routing.Settings
	issue    routing.Issue
}

func (s stallTestStore) Settings(context.Context, string) (routing.Settings, error) {
	return s.settings, nil
}

// Issue only answers for the ticket under test: the sweep scans every
// workspace, so quiet tickets left by other tests in the package must not
// reach the counting judge.
func (s stallTestStore) Issue(_ context.Context, _ string, issueID string) (routing.Issue, error) {
	if issueID != s.issue.ID {
		return routing.Issue{}, fmt.Errorf("issue %s is outside this test", issueID)
	}
	return s.issue, nil
}

type countingStallJudge struct {
	calls atomic.Int32
}

func (j *countingStallJudge) Assign(context.Context, routing.Target, routing.JudgeState) (routing.Verdict, error) {
	return routing.Verdict{}, nil
}

func (j *countingStallJudge) Unblock(context.Context, routing.Target, routing.JudgeState) (routing.Advice, error) {
	return routing.Advice{}, nil
}

func (j *countingStallJudge) Stale(context.Context, routing.Target, routing.StaleState) (routing.StaleDecision, error) {
	return routing.StaleDecision{}, nil
}

func (j *countingStallJudge) Candidate(context.Context, routing.Target, routing.StallCandidateState) (routing.StallCandidateDecision, error) {
	j.calls.Add(1)
	return routing.StallCandidateDecision{Candidate: false, Confidence: 0.99, Reason: "不是重复票"}, nil
}

func TestSweepStallActionsDoesNotRejudgeNegativeCandidateUntilActivity(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "negative candidate", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour)})
	judge := &countingStallJudge{}
	previousRouting := testHandler.Routing
	testHandler.Routing = routing.New(stallTestStore{
		settings: routing.Settings{Enabled: true, Model: "test"},
		issue:    routing.Issue{ID: issueID, Title: "negative candidate", Status: "in_progress"},
	}, judge)
	t.Cleanup(func() { testHandler.Routing = previousRouting })

	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := judge.calls.Load(); got != 1 {
		t.Fatalf("model calls after first sweep = %d, want 1", got)
	}
	var judgedAt string
	dbfx.QueryRow(t, `SELECT metadata->>'stall.judged_at' FROM issue WHERE id=$1`, issueID).Scan(&judgedAt)
	if judgedAt == "" {
		t.Fatal("model decision did not persist an activity marker")
	}
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := judge.calls.Load(); got != 1 {
		t.Fatalf("model calls after unchanged sweep = %d, want 1", got)
	}

	dbfx.Exec(t, `UPDATE issue SET last_activity_at=$2 WHERE id=$1`, issueID, time.Now().UTC().Add(-37*time.Hour))
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("post-activity sweep: %v", err)
	}
	if got := judge.calls.Load(); got != 2 {
		t.Fatalf("model calls after new activity = %d, want 2", got)
	}
}

func TestParentAutoCompletionEligibilityRequiresQuietSafeParent(t *testing.T) {
	base := func() bool { return parentAutoCompletionEligible("in_progress", false, false, false, 2, true) }
	if !base() {
		t.Fatal("a quiet parent with terminal children should be eligible")
	}
	for name, got := range map[string]bool{
		"paused":      parentAutoCompletionEligible("in_progress", true, false, false, 2, true),
		"active run":  parentAutoCompletionEligible("in_progress", false, true, false, 2, true),
		"linked PR":   parentAutoCompletionEligible("in_progress", false, false, true, 2, true),
		"open child":  parentAutoCompletionEligible("in_progress", false, false, false, 2, false),
		"no children": parentAutoCompletionEligible("in_progress", false, false, false, 0, true),
		"backlog":     parentAutoCompletionEligible("backlog", false, false, false, 2, true),
	} {
		if got {
			t.Fatalf("%s parent must not be auto-completed", name)
		}
	}
}

func TestSweepStallActionsCompletesQuietParentAndUndoRestoresStatus(t *testing.T) {
	parent := dbfx.Issue(t, "quiet parent", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour)})
	dbfx.Issue(t, "finished child", testutil.Cols{"status": "done", "parent_issue_id": parent})

	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, parent).Scan(&status)
	if status != "done" {
		t.Fatalf("parent status = %q, want done", status)
	}

	req := withURLParam(inboxRequest(http.MethodPost, "/api/issues/"+parent+"/stall/undo", testWorkspaceID), "id", parent)
	rr := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.UndoStallAction).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("undo status = %d, body=%s", rr.Code, rr.Body.String())
	}
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, parent).Scan(&status)
	if status != "in_progress" {
		t.Fatalf("restored parent status = %q, want in_progress", status)
	}
}

func TestSweepStallActionsAnnouncementKeepAndExpiry(t *testing.T) {
	kept := dbfx.Issue(t, "candidate kept", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour), "metadata": rawJSON(`{"stall.candidate":"true"}`)})
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("candidate sweep: %v", err)
	}
	var action string
	dbfx.QueryRow(t, `SELECT metadata->>'stall.action' FROM issue WHERE id = $1`, kept).Scan(&action)
	if action != stallaction.ActionAnnounced {
		t.Fatalf("candidate action = %q, want announced", action)
	}

	req := withURLParam(inboxRequest(http.MethodPost, "/api/issues/"+kept+"/stall/keep", testWorkspaceID), "id", kept)
	rr := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.KeepStallAction).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("keep status = %d, body=%s", rr.Code, rr.Body.String())
	}
	dbfx.QueryRow(t, `SELECT metadata->>'stall.action' FROM issue WHERE id = $1`, kept).Scan(&action)
	if action != stallaction.ActionKept {
		t.Fatalf("kept action = %q, want kept", action)
	}

	expired := dbfx.Issue(t, "candidate expired", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour), "metadata": rawJSON(`{"stall.candidate":"true"}`)})
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("second candidate sweep: %v", err)
	}
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, ARRAY['stall.review_until'], to_jsonb($2::text), true) WHERE id = $1`, expired, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("expiry sweep: %v", err)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, expired).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("expired status = %q, want cancelled", status)
	}
	var reason string
	dbfx.QueryRow(t, `SELECT metadata->>'stall.reason' FROM issue WHERE id = $1`, expired).Scan(&reason)
	if !strings.Contains(reason, "已核对关联 PR") {
		t.Fatalf("expired reason = %q, want artifact check", reason)
	}
}

func TestSweepStallActionsDoesNotTouchIntentionallyPausedTickets(t *testing.T) {
	pausedParent := dbfx.Issue(t, "paused parent", testutil.Cols{
		"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour),
		"metadata": rawJSON(`{"close.conclusion":"continuing"}`),
	})
	dbfx.Issue(t, "paused child", testutil.Cols{"status": "done", "parent_issue_id": pausedParent})
	pausedCandidate := dbfx.Issue(t, "paused candidate", testutil.Cols{
		"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour),
		"metadata": rawJSON(`{"close.conclusion":"deferred","stall.candidate":"true"}`),
	})

	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var status, action string
	dbfx.QueryRow(t, `SELECT status, COALESCE(metadata->>'stall.action','') FROM issue WHERE id = $1`, pausedParent).Scan(&status, &action)
	if status != "in_progress" || action != "" {
		t.Fatalf("paused parent status/action = %q/%q, want in_progress/empty", status, action)
	}
	dbfx.QueryRow(t, `SELECT status, COALESCE(metadata->>'stall.action','') FROM issue WHERE id = $1`, pausedCandidate).Scan(&status, &action)
	if status != "in_progress" || action != "" {
		t.Fatalf("paused candidate status/action = %q/%q, want in_progress/empty", status, action)
	}
}

func TestNotifyStallInboxKeepsActionsBoundToTheirIssue(t *testing.T) {
	first := dbfx.Issue(t, "stall inbox first", testutil.Cols{
		"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour),
		"metadata": rawJSON(`{"stall.candidate":"true"}`),
	})
	second := dbfx.Issue(t, "stall inbox second", testutil.Cols{
		"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour),
		"metadata": rawJSON(`{"stall.candidate":"true"}`),
	})

	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, issueID := range []string{first, second} {
		var detailIssueID string
		if err := testPool.QueryRow(t.Context(), `SELECT details->>'issue_id' FROM inbox_item WHERE issue_id=$1 AND type='issue_stall_action' ORDER BY created_at DESC LIMIT 1`, issueID).Scan(&detailIssueID); err != nil {
			t.Fatalf("action inbox row for %s: %v", issueID, err)
		}
		if detailIssueID != issueID {
			t.Fatalf("action inbox details issue_id = %q, want %q", detailIssueID, issueID)
		}
	}
	var summary string
	if err := testPool.QueryRow(t.Context(), `SELECT body FROM inbox_item WHERE workspace_id=$1 AND recipient_type='member' AND type='issue_stall_action' AND issue_id IS NULL AND body LIKE '%stall inbox first%' ORDER BY created_at DESC LIMIT 1`, testWorkspaceID).Scan(&summary); err != nil {
		t.Fatalf("daily summary: %v", err)
	}
	if !strings.Contains(summary, "stall inbox second") {
		t.Fatalf("daily summary omitted second ticket: %q", summary)
	}
}

func rawJSON(value string) any { return testutil.Raw("'" + value + "'::jsonb") }

func TestSweepStallActionsKeepsAnnouncementWhenWorkResumes(t *testing.T) {
	issueID := dbfx.Issue(t, "announced then resumed", testutil.Cols{"status": "in_progress", "last_activity_at": time.Now().UTC().Add(-37 * time.Hour), "metadata": rawJSON(`{"stall.candidate":"true"}`)})
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("candidate sweep: %v", err)
	}
	dbfx.Comment(t, issueID, "我接着做")
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_set(metadata, ARRAY['stall.review_until'], to_jsonb($2::text), true) WHERE id = $1`, issueID, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	if _, err := testHandler.SweepStallActions(t.Context()); err != nil {
		t.Fatalf("expiry sweep: %v", err)
	}
	var status, action string
	dbfx.QueryRow(t, `SELECT status, metadata->>'stall.action' FROM issue WHERE id = $1`, issueID).Scan(&status, &action)
	if status != "in_progress" || action != stallaction.ActionKept {
		t.Fatalf("resumed ticket status/action = %q/%q, want in_progress/kept", status, action)
	}
}

func TestUndoStallActionRefusesAfterManualStatusChange(t *testing.T) {
	issueID := dbfx.Issue(t, "cancelled then reopened", testutil.Cols{"status": "todo", "metadata": rawJSON(fmt.Sprintf(`{"stall.action":"cancelled","stall.previous_status":"in_progress","stall.revert_until":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)))})
	req := withURLParam(inboxRequest(http.MethodPost, "/api/issues/"+issueID+"/stall/undo", testWorkspaceID), "id", issueID)
	rr := httptest.NewRecorder()
	inboxWorkspaceHandler(testHandler.UndoStallAction).ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("undo status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	if status != "todo" {
		t.Fatalf("status = %q, want the manual todo kept", status)
	}
}

// The patrol filters in SQL and re-checks each row in Go. Both are built from
// stallaction's Eligibility (DENE-1183); this pins them to the same answer
// on a matrix of status, close.* and block.* values.
func TestStallPatrolEligibilitySQLMatchesGo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		status string
		meta   string
	}{
		{"todo", `{}`},
		{"in_progress", `{}`},
		{"blocked", `{}`},
		{"backlog", `{}`},
		{"done", `{}`},
		{"cancelled", `{}`},
		{"todo", `{"close.conclusion":"deferred"}`},
		{"in_progress", `{"close.conclusion":"continuing"}`},
		{"in_progress", `{"close.conclusion":"delivered"}`},
		{"blocked", `{"close.conclusion":"blocked"}`},
		{"in_progress", `{"close.conclusion":"awaiting_human"}`},
		{"blocked", `{"block.blocked_by":"DENE-1"}`},
		{"blocked", `{"block.wait_condition":"CI green"}`},
		{"blocked", `{"block.wake_at":"2026-10-04T00:00:00Z"}`},
		{"todo", `{"block.blocked_by":""}`},
		{"todo", `{"close.waiting_on":"DENE-2"}`},
	}
	ids := make([]string, len(cases))
	for i, tc := range cases {
		ids[i] = dbfx.Issue(t, fmt.Sprintf("eligibility %d", i), testutil.Cols{"status": tc.status, "metadata": rawJSON(tc.meta)})
	}
	rows, err := testPool.Query(t.Context(), `SELECT id::text FROM issue WHERE id = ANY($1::uuid[]) AND `+stallaction.PatrolStatusSQL()+` AND `+stallaction.NotWaitingSQL(), ids)
	if err != nil {
		t.Fatal(err)
	}
	inSQL := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		inSQL[id] = true
	}
	rows.Close()
	for i, id := range ids {
		issue, err := testHandler.Queries.GetIssue(t.Context(), parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		if got := stallTicket(issue).PatrolEligible(); got != inSQL[id] {
			t.Fatalf("case %d %s %s: Go eligible=%v, SQL selected=%v", i, cases[i].status, cases[i].meta, got, inSQL[id])
		}
	}
}
