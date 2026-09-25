package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The issue-draft protocol exists so that nothing is created until a human
// confirms. These tests are all database-backed on purpose: every guarantee the
// protocol makes — no issue and no queued task before confirm, one issue per
// draft under concurrency, workspace and ownership scoping, cleanup with no
// foreign keys — is a property of what the SQL actually did, and a mocked
// query layer would assert only that the handler called what the test told it
// to call.

// cleanupIssueDraftCarriers removes the hidden carriers (and, by cascade, their
// chat sessions) a test created, plus any issue its confirm produced.
func cleanupIssueDraftCarriers(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM issue
			WHERE workspace_id = $1 AND origin_type = 'issue_draft'
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM issue_draft WHERE workspace_id = $1
		`, testWorkspaceID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent
			WHERE workspace_id = $1 AND kind = 'system' AND system_key LIKE 'issue_draft:%'
		`, testWorkspaceID)
	})
}

func startIssueDraftSession(t *testing.T) CreateIssueDraftSessionResponse {
	t.Helper()
	var out CreateIssueDraftSessionResponse
	testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
	})).Want(http.StatusCreated).JSON(&out)
	if out.SessionID == "" || out.AgentID == "" {
		t.Fatalf("missing session identifiers: %+v", out)
	}
	return out
}

func saveIssueDraft(t *testing.T, sessionID string, revision int64, status string, draft map[string]any) issueDraftResponse {
	t.Helper()
	req := withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+sessionID, map[string]any{
		"draft":             draft,
		"status":            status,
		"expected_revision": revision,
	}), "sessionId", sessionID)
	var out issueDraftResponse
	testutil.Call(t, testHandler.UpdateIssueDraft, req).Want(http.StatusOK).JSON(&out)
	return out
}

func finalizeRequest(t *testing.T, sessionID string, revision int64) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPost, "/api/issue-drafts/"+sessionID+"/finalize", map[string]any{
		"expected_revision": revision,
	}), "sessionId", sessionID)
}

// The headline guarantee: aligning a request writes no issue and queues no
// agent work. Everything before finalize must be invisible to the board.
func TestIssueDraftCreatesNoIssueOrTaskBeforeConfirm(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "Align before creating",
		"description": "agreed in conversation",
	})

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("alignment created %d issues before confirm; it must create none", got)
	}
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM agent_task_queue WHERE agent_id = $1
	`, session.AgentID); got != 0 {
		t.Fatalf("alignment queued %d agent tasks before confirm; it must queue none", got)
	}

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_id = $2 AND origin_type = 'issue_draft'
	`, testWorkspaceID, session.SessionID); got != 1 {
		t.Fatalf("confirm produced %d issues, want exactly 1", got)
	}
	var title, originType string
	dbfx.QueryRow(t, `SELECT title, origin_type FROM issue WHERE id = $1`, finalized.IssueID).Scan(&title, &originType)
	if title != "Align before creating" {
		t.Fatalf("issue title came from somewhere other than the draft: %q", title)
	}
	if originType != "issue_draft" {
		t.Fatalf("issue is not traceable back to its alignment conversation: origin_type=%q", originType)
	}
	if finalized.Draft.Status != "completed" || finalized.Draft.IssueID == nil {
		t.Fatalf("confirmed draft must be completed and pinned to its issue: %+v", finalized.Draft)
	}
}

// Confirm is retried by real clients: a double click, a lost response, a second
// tab. Every repeat must answer with the issue the first one made.
func TestFinalizeIssueDraftCreatesAtMostOneIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title": "Confirmed exactly once",
	})

	const attempts = 4
	results := make([]FinalizeIssueDraftResponse, attempts)
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision))
			codes[i] = res.Code
			if res.Code == http.StatusOK {
				res.JSON(&results[i])
			}
		}(i)
	}
	wg.Wait()

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_id = $2 AND origin_type = 'issue_draft'
	`, testWorkspaceID, session.SessionID); got != 1 {
		t.Fatalf("%d concurrent confirms produced %d issues, want exactly 1", attempts, got)
	}
	first := ""
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent confirm %d returned %d; every repeat of an accepted confirm must succeed", i, code)
		}
		if results[i].IssueID == "" {
			t.Fatalf("concurrent confirm %d returned no issue id", i)
		}
		if first == "" {
			first = results[i].IssueID
			continue
		}
		if results[i].IssueID != first {
			t.Fatalf("concurrent confirms disagreed on the issue: %s vs %s", first, results[i].IssueID)
		}
	}

	// And the serial retry, which is what a double click actually looks like.
	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)
	if again.IssueID != first {
		t.Fatalf("retried confirm returned a different issue: %s, want %s", again.IssueID, first)
	}
}

// A draft is a private conversation. Neither another workspace nor another
// member of the same workspace may read or write it.
func TestIssueDraftRejectsCrossWorkspaceAndNonOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	otherUser := dbfx.User(t, "Issue Draft Outsider", "issue-draft-outsider@multica.ai")
	otherWorkspace := dbfx.Workspace(t, "Issue Draft Other WS", "issue-draft-other-ws")
	dbfx.Member(t, otherWorkspace, otherUser, "owner")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")

	finalizeAs := func(userID, workspaceID string) *http.Request {
		req := finalizeRequest(t, session.SessionID, 0)
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", workspaceID)
		return req
	}

	// Addressed from another workspace the draft does not exist at all — the
	// session lookup is workspace-scoped, so this must not leak its existence.
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeAs(otherUser, otherWorkspace)).Want(http.StatusNotFound)
	// A fellow workspace member cannot see the conversation, so both writes
	// answer 404 — the same as a missing session, not a 403 that admits it exists.
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeAs(otherUser, testWorkspaceID)).Want(http.StatusNotFound)
	testutil.Call(t, testHandler.AbandonIssueDraft, withURLParam(
		testutil.WithHeaders(newRequest(http.MethodPost, "/api/issue-drafts/"+session.SessionID+"/abandon", nil),
			"X-User-ID", otherUser, "X-Workspace-ID", testWorkspaceID),
		"sessionId", session.SessionID,
	)).Want(http.StatusNotFound)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("a rejected confirm still created %d issues", got)
	}
}

// An ordinary chat session must not be usable as a draft carrier, and the
// alignment conversation must not show up as ordinary chat.
func TestIssueDraftSessionIsNotAnOrdinaryChat(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	plainAgent := createHandlerTestAgent(t, "IssueDraftPlainAgent", []byte("[]"))
	plainSession := createHandlerTestChatSession(t, plainAgent)

	// The alignment conversation is invisible to the chat list, which is what
	// makes the hidden carrier a carrier rather than a second inbox.
	req := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, "/api/chat/sessions?status=all", nil))
	var listed []ChatSessionResponse
	testutil.Call(t, testHandler.ListChatSessions, req).Want(http.StatusOK).JSON(&listed)
	for _, s := range listed {
		if s.ID == session.SessionID {
			t.Fatal("alignment conversation appeared in the ordinary chat list")
		}
	}

	// And an ordinary chat cannot be driven through the draft endpoints.
	testutil.Call(t, testHandler.FinalizeIssueDraft, withURLParam(
		newRequest(http.MethodPost, "/api/issue-drafts/"+plainSession+"/finalize", map[string]any{"expected_revision": 0}),
		"sessionId", plainSession,
	)).Want(http.StatusNotFound)
}

// Saving and confirming both refuse a client that was looking at an older
// draft — the lost-update this protocol exists to prevent.
func TestIssueDraftRefusesStaleRevision(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saveIssueDraft(t, session.SessionID, 0, "draft", map[string]any{"title": "first"})
	current := saveIssueDraft(t, session.SessionID, 1, "ready", map[string]any{"title": "second"})

	stale := withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+session.SessionID, map[string]any{
		"draft":             map[string]any{"title": "built on a view that moved"},
		"expected_revision": 0,
	}), "sessionId", session.SessionID)
	testutil.Call(t, testHandler.UpdateIssueDraft, stale).Want(http.StatusConflict)

	var storedTitle string
	dbfx.QueryRow(t, `SELECT draft->>'title' FROM issue_draft WHERE chat_session_id = $1`, session.SessionID).Scan(&storedTitle)
	if storedTitle != "second" {
		t.Fatalf("a rejected save still overwrote the draft: title is %q", storedTitle)
	}

	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, current.Revision-1)).
		Want(http.StatusConflict)
	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("a refused confirm still created %d issues", got)
	}
}

// A draft that is still being discussed, or has been thrown away, is not
// confirmable.
func TestFinalizeIssueDraftRequiresReadyDraft(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	stillDrafting := startIssueDraftSession(t)
	saveIssueDraft(t, stillDrafting.SessionID, 0, "draft", map[string]any{"title": "not agreed yet"})
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, stillDrafting.SessionID, 1)).
		Want(http.StatusConflict)

	abandoned := startIssueDraftSession(t)
	saveIssueDraft(t, abandoned.SessionID, 0, "ready", map[string]any{"title": "changed my mind"})
	testutil.Call(t, testHandler.AbandonIssueDraft, withURLParam(
		newRequest(http.MethodPost, "/api/issue-drafts/"+abandoned.SessionID+"/abandon", nil),
		"sessionId", abandoned.SessionID,
	)).Want(http.StatusOK)
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, abandoned.SessionID, 1)).
		Want(http.StatusConflict)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("confirming an unready draft created %d issues", got)
	}
}

// A confirm must fail whole. A draft whose structured half says nothing about
// what to create leaves no issue and no completed draft behind.
func TestFinalizeIssueDraftWithoutTitleLeavesNothingBehind(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{"description": "no title agreed"})

	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, 1)).
		Want(http.StatusBadRequest)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("a refused confirm created %d issues", got)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue_draft WHERE chat_session_id = $1`, session.SessionID).Scan(&status)
	if status != "ready" {
		t.Fatalf("a refused confirm moved the draft to %q; it must stay ready so the user can fix it", status)
	}
}

// issue_draft carries no foreign key (repo rule), so deleting the conversation
// has to prune it explicitly or the row outlives everything that could reach it.
func TestDeleteChatSessionPrunesIssueDraft(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saveIssueDraft(t, session.SessionID, 0, "draft", map[string]any{"title": "will be discarded"})

	req := withChatTestWorkspaceCtx(t, withURLParam(
		newRequest(http.MethodDelete, "/api/chat/sessions/"+session.SessionID, nil),
		"sessionId", session.SessionID,
	))
	testutil.Call(t, testHandler.DeleteChatSession, req).WantOneOf(http.StatusOK, http.StatusNoContent)

	if got := dbfx.Count(t, `SELECT COUNT(*) FROM issue_draft WHERE chat_session_id = $1`, session.SessionID); got != 0 {
		t.Fatalf("deleting the conversation left %d orphan draft rows", got)
	}
	if got := dbfx.Count(t, `SELECT COUNT(*) FROM agent WHERE id = $1`, session.AgentID); got != 0 {
		t.Fatalf("deleting the conversation left its hidden carrier behind")
	}
}

// Unfinished drafts are listable — the only route back into an alignment
// conversation — and confirmed or discarded ones drop out of that list.
func TestListIssueDraftsReturnsOnlyUnfinishedOwnDrafts(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	open := startIssueDraftSession(t)
	saveIssueDraft(t, open.SessionID, 0, "draft", map[string]any{"title": "still aligning"})
	confirmed := startIssueDraftSession(t)
	saved := saveIssueDraft(t, confirmed.SessionID, 0, "ready", map[string]any{"title": "already created"})
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, confirmed.SessionID, saved.Revision)).
		Want(http.StatusOK)

	var listed ListIssueDraftsResponse
	testutil.Call(t, testHandler.ListIssueDrafts, newRequest(http.MethodGet, "/api/issue-drafts", nil)).
		Want(http.StatusOK).JSON(&listed)

	found := map[string]bool{}
	for _, d := range listed.Drafts {
		found[d.ChatSessionID] = true
	}
	if !found[open.SessionID] {
		t.Fatal("an unfinished alignment conversation was not listed; there is no other way back to it")
	}
	if found[confirmed.SessionID] {
		t.Fatal("a confirmed draft is still offered as unfinished work")
	}
}

// `?status=all` is the record half of the same endpoint (DENE-371): an
// alignment that produced an issue, or was given up on, has to stay reachable
// or the only copy of what was agreed is gone. The default list must keep
// meaning "still actionable", so the two readings are pinned side by side.
func TestListIssueDraftsStatusAllReturnsTerminalRecords(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	open := startIssueDraftSession(t)
	saveIssueDraft(t, open.SessionID, 0, "draft", map[string]any{"title": "still aligning"})

	confirmed := startIssueDraftSession(t)
	saved := saveIssueDraft(t, confirmed.SessionID, 0, "ready", map[string]any{"title": "became an issue"})
	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, confirmed.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	discarded := startIssueDraftSession(t)
	saveIssueDraft(t, discarded.SessionID, 0, "draft", map[string]any{"title": "gave up on it"})
	testutil.Call(t, testHandler.AbandonIssueDraft, withURLParam(
		newRequest(http.MethodPost, "/api/issue-drafts/"+discarded.SessionID+"/abandon", nil),
		"sessionId", discarded.SessionID,
	)).Want(http.StatusOK)

	var listed ListIssueDraftsResponse
	testutil.Call(t, testHandler.ListIssueDrafts, newRequest(http.MethodGet, "/api/issue-drafts?status=all", nil)).
		Want(http.StatusOK).JSON(&listed)

	byID := map[string]IssueDraftSummary{}
	for _, d := range listed.Drafts {
		byID[d.ChatSessionID] = d
	}
	for _, want := range []string{open.SessionID, confirmed.SessionID, discarded.SessionID} {
		if _, ok := byID[want]; !ok {
			t.Fatalf("alignment %s is missing from ?status=all; there is no other way back to it", want)
		}
	}
	// The record carries what a reader needs to name it and to jump to its
	// issue, which is the whole point of listing it.
	if got := byID[confirmed.SessionID].Status; got != "completed" {
		t.Fatalf("confirmed draft status = %q, want completed", got)
	}
	if got := byID[confirmed.SessionID].IssueID; got == nil || *got != finalized.IssueID {
		t.Fatalf("confirmed draft issue_id = %v, want %q", got, finalized.IssueID)
	}
	if got := byID[discarded.SessionID].Status; got != "abandoned" {
		t.Fatalf("discarded draft status = %q, want abandoned", got)
	}
	if got := byID[open.SessionID].Status; got != "draft" {
		t.Fatalf("live draft status = %q, want draft", got)
	}
}
