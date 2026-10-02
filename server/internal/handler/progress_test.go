package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func loadProgressIssue(t *testing.T, id string) db.Issue {
	t.Helper()
	issue, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(id))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

// A progress write must not bump revision: a background report would
// otherwise fail a person's in-flight title/description save.
func TestWriteIssueProgress_DoesNotBumpRevision(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	issueID := fx.Issue(t, "progress revision")
	before := loadProgressIssue(t, issueID)

	w := httptest.NewRecorder()
	testHandler.WriteIssueProgress(w, withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/progress", map[string]any{"text": "halfway", "tone": "waiting"}), "id", issueID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	after := loadProgressIssue(t, issueID)
	if after.Revision != before.Revision || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("revision %d→%d updated_at changed=%v", before.Revision, after.Revision, after.UpdatedAt != before.UpdatedAt)
	}
	if after.ProgressText != "halfway" || after.ProgressSource != progress.SourceAgent || after.ProgressTone != progress.ToneWaiting {
		t.Fatalf("progress = %q/%s/%s", after.ProgressText, after.ProgressSource, after.ProgressTone)
	}
	if n := fx.Count(t, `SELECT count(*) FROM issue_progress WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("history rows = %d", n)
	}
}

func TestWriteIssueProgress_RejectsUnknownTone(t *testing.T) {
	requireDB(t)
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	issueID := fx.Issue(t, "progress tone")
	w := httptest.NewRecorder()
	testHandler.WriteIssueProgress(w, withURLParam(newRequest(http.MethodPost, "/", map[string]any{"text": "x", "tone": "red"}), "id", issueID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
}

// The parking summary is the last-priority fallback: it fills an empty line
// but never replaces an agent report or a close summary.
func TestIssueProgress_ParkingFallbackNeverBeatsExplicitLines(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	issue := loadProgressIssue(t, fx.Issue(t, "progress fallback"))
	parked := func(text string) bool {
		_, err := testHandler.Queries.UpdateIssueProgress(ctx, db.UpdateIssueProgressParams{
			ID: issue.ID, WorkspaceID: issue.WorkspaceID, Text: text, Source: "parking", Tone: progress.ToneStuck,
			AuthorType: "system", FallbackOnly: true,
		})
		return err == nil
	}
	if !parked("stalled") {
		t.Fatal("parking summary did not fill an empty line")
	}
	if !parked("stalled again") {
		t.Fatal("parking summary did not replace its own line")
	}
	for _, source := range []string{progress.SourceAgent, progress.SourceClose} {
		if _, err := testHandler.recordIssueProgress(ctx, issue, progressEntry{Text: source + " line", Source: source, AuthorType: "agent"}); err != nil {
			t.Fatal(err)
		}
		if parked("stalled") {
			t.Fatalf("parking summary replaced the %s line", source)
		}
		if got := loadProgressIssue(t, uuidToString(issue.ID)); got.ProgressText != source+" line" {
			t.Fatalf("progress = %q", got.ProgressText)
		}
	}
}

// Agent and close lines are both explicit: the latest one wins.
func TestIssueProgress_CloseAndAgentLatestWins(t *testing.T) {
	requireDB(t)
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	issue := loadProgressIssue(t, fx.Issue(t, "progress latest", testutil.Cols{"status": "blocked"}))
	if _, err := testHandler.recordIssueProgress(ctx, issue, progressEntry{Text: "closed it", Source: progress.SourceClose, AuthorType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if got := loadProgressIssue(t, uuidToString(issue.ID)); got.ProgressTone != progress.ToneStuck {
		t.Fatalf("derived tone for blocked = %q", got.ProgressTone)
	}
	if _, err := testHandler.recordIssueProgress(ctx, issue, progressEntry{Text: "picked it up again", Source: progress.SourceAgent, AuthorType: "agent"}); err != nil {
		t.Fatal(err)
	}
	if got := loadProgressIssue(t, uuidToString(issue.ID)); got.ProgressText != "picked it up again" {
		t.Fatalf("progress = %q", got.ProgressText)
	}
}
