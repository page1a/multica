package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/entitlement/entitlementtest"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// An alignment confirm produces a GROUP. These tests are database-backed
// because every guarantee at stake — one group per alignment under
// concurrency, children that roll back with their parent, identity derived
// from the conversation rather than from the client — is a statement about what
// the transaction and the partial unique index actually did.

// cleanupIssueDraftGroup is cleanupIssueDraftCarriers plus the agent tasks the
// group's nodes enqueued. agent_task_queue carries no foreign key (repo rule),
// so nothing prunes it when the issue goes away — and the tasks have to be
// registered AFTER the carriers cleanup so they run BEFORE it (t.Cleanup is
// LIFO) while the issues they point at still exist.
func cleanupIssueDraftGroup(t *testing.T) {
	t.Helper()
	cleanupIssueDraftCarriers(t)
	dbfx.Cleanup(t, `
		DELETE FROM agent_task_queue
		WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft')
	`, testWorkspaceID)
}

// draftChild builds one sub-issue the way the preview panel does: a stable key
// plus the fields the alignment settled on.
func draftChild(key, title, status string) map[string]any {
	return map[string]any{
		"key":         key,
		"title":       title,
		"description": "",
		"status":      status,
		"priority":    "medium",
	}
}

func assignTo(child map[string]any, agentID string) map[string]any {
	child["assignee_type"] = "agent"
	child["assignee_id"] = agentID
	return child
}

// draftGroupPayload is the flattened root fields plus the children array.
func draftGroupPayload(title string, children ...map[string]any) map[string]any {
	payload := map[string]any{
		"title":       title,
		"description": "agreed in conversation",
		"status":      "todo",
		"priority":    "medium",
	}
	if len(children) > 0 {
		payload["children"] = children
	}
	return payload
}

// issueDraftGroupIssueCount counts the issues an alignment produced. Every node
// of a group carries origin_type = 'issue_draft', so this is the group's size on
// the board.
func issueDraftGroupIssueCount(t *testing.T) int {
	t.Helper()
	return dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID)
}

// reopenIssueDraft puts a completed draft back in the state a crashed or racing
// confirm leaves behind: still `ready`, no issue recorded, and — when the
// caller passes replacement children — a payload that no longer matches what
// the first confirm created.
//
// It is deliberately NOT the /reopen endpoint. That endpoint starts a new round
// (finalize_round+1) on a group that exists, and the confirm that follows is
// allowed to append; this helper reproduces the two states a first round has to
// survive, where the group exists but no round was ever opened on it: the
// process that died between the commit and the record, and the save that
// re-keyed every child while a confirm was in flight. Both must adopt the group
// whole, so the round counter must stay 0 here — that is the point of the
// fixture.
func reopenIssueDraft(t *testing.T, sessionID string, draft map[string]any) int64 {
	t.Helper()
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatalf("encode replacement draft: %v", err)
	}
	dbfx.Exec(t, `
		UPDATE issue_draft
		SET status = 'ready', issue_id = NULL, revision = revision + 1, draft = $2::jsonb
		WHERE chat_session_id = $1
	`, sessionID, string(raw))
	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM issue_draft WHERE chat_session_id = $1`, sessionID).Scan(&revision)
	return revision
}

// The node id is the whole identity model: it has to be reproducible from the
// conversation alone, and the root has to keep being the chat session id, or
// every existing draft row loses the one lookup that finds it.
func TestIssueDraftNodeIDIsDerivedFromSessionAndKey(t *testing.T) {
	session := util.MustParseUUID("11111111-1111-7111-8111-111111111111")
	other := util.MustParseUUID("22222222-2222-7222-8222-222222222222")

	// The root IS the session id. Migration 486, GetIssueByOrigin and
	// issue_draft.issue_id all rest on this.
	if got := issueDraftNodeID(session, ""); got != session {
		t.Fatalf("root node id = %s, want the chat session itself", uuidToString(got))
	}

	// Golden values: they pin the namespace and the UUIDv5 construction. The
	// namespace is half the input of every node id already minted, so changing
	// it would give every existing group a second identity.
	for key, want := range map[string]string{
		"c1": "c6b0d5f3-6ca6-5dcb-9a14-a49e0cd64e02",
		"c2": "b7d92042-0404-5871-be7a-b2c8f5335460",
		"c3": "f3dcd233-68c1-5533-8612-588b3ff578ba",
	} {
		if got := uuidToString(issueDraftNodeID(session, key)); got != want {
			t.Fatalf("node %q derived %s, want %s — the derivation or its namespace changed, "+
				"which would rebuild every existing group on the next confirm", key, got, want)
		}
	}

	// Reproducible: the same (session, key) always lands on the same node, which
	// is what makes a retried confirm adopt instead of duplicate.
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(session, "c1"); a != b {
		t.Fatalf("the same (session, key) derived two ids: %s vs %s", uuidToString(a), uuidToString(b))
	}
	// Session-scoped: a client cannot construct a node id that belongs to
	// someone else's alignment.
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(other, "c1"); a == b {
		t.Fatal("the chat session does not participate in derivation; a node id could point at another draft")
	}
	if a, b := issueDraftNodeID(session, "c1"), issueDraftNodeID(session, "c2"); a == b {
		t.Fatal("two different child keys derived the same node id")
	}
}

// A payload without children is a group with one node — not a legacy branch.
// Its result has to be field-for-field what a lone issue always was.
func TestFinalizeIssueDraftSingleNodeMatchesTheSingleIssueShape(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "No children is one node",
		"description": "the shape every draft had before groups existed",
		"status":      "todo",
		"priority":    "medium",
	})

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if len(finalized.Issues) != 1 {
		t.Fatalf("a payload without children produced %d issues, want exactly the single node", len(finalized.Issues))
	}
	only := finalized.Issues[0]
	if only.ID != finalized.IssueID {
		t.Fatalf("the group's only row is %s but issue_id says %s", only.ID, finalized.IssueID)
	}
	if only.ParentIssueID != nil {
		t.Fatalf("the single node has a parent: %v", *only.ParentIssueID)
	}
	if only.Identifier == "" {
		t.Fatal("the confirmation row has no identifier; people are shown numbers, not UUIDs")
	}
	if finalized.Draft.IssueID == nil || *finalized.Draft.IssueID != finalized.IssueID {
		t.Fatalf("draft issue_id = %v, want %s — this is the field a client navigates to "+
			"and a crashed confirm recovers from", finalized.Draft.IssueID, finalized.IssueID)
	}
	var originID string
	dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, finalized.IssueID).Scan(&originID)
	if originID != session.SessionID {
		t.Fatalf("single node origin_id = %s, want the chat session %s", originID, session.SessionID)
	}

	// The repeat confirm answers with the same one-node group.
	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)
	if len(again.Issues) != 1 || again.Issues[0].ID != only.ID {
		t.Fatalf("repeat confirm returned %d issues (%+v), want the same single node", len(again.Issues), again.Issues)
	}
}

// The whole point: one confirm produces a parent and its children, linked by
// parent_issue_id, each node with its own origin and its own stage.
func TestFinalizeIssueDraftCreatesRootAndChildrenAsOneGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("c1", "stage one backend", "todo"), agentID)
	first["stage"] = 1
	second := assignTo(draftChild("c2", "stage one frontend", "todo"), agentID)
	second["stage"] = 1
	third := assignTo(draftChild("c3", "stage two verification", "backlog"), agentID)
	third["stage"] = 2

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("group parent", first, second, third))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if got := issueDraftGroupIssueCount(t); got != 4 {
		t.Fatalf("the group has %d issues, want the root plus 3 children", got)
	}
	if len(finalized.Issues) != 4 {
		t.Fatalf("the response carries %d issues, want the whole group of 4", len(finalized.Issues))
	}
	root := finalized.Issues[0]
	if root.ID != finalized.IssueID {
		t.Fatalf("root row %s is not issue_id %s", root.ID, finalized.IssueID)
	}
	if root.ParentIssueID != nil {
		t.Fatalf("the root hangs off a parent: %v", *root.ParentIssueID)
	}
	// The payload's flat fields describe the root, and nothing assigns it: the
	// group's work is done by the children.
	if root.AssigneeID != nil || root.AssigneeType != nil {
		t.Fatalf("the root carries an assignee (%v/%v); confirming a group must not "+
			"start a run for the container", root.AssigneeType, root.AssigneeID)
	}

	var rootOrigin string
	dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, root.ID).Scan(&rootOrigin)
	if rootOrigin != session.SessionID {
		t.Fatalf("root origin_id = %s, want the chat session %s", rootOrigin, session.SessionID)
	}

	// Children: parented to the root, each with the origin its (session, key)
	// derives, and each carrying the stage the alignment settled on.
	wantStages := []int{1, 1, 2}
	wantTitles := []string{"stage one backend", "stage one frontend", "stage two verification"}
	wantKeys := []string{"c1", "c2", "c3"}
	seenOrigins := map[string]bool{}
	for i, child := range finalized.Issues[1:] {
		if child.ParentIssueID == nil || *child.ParentIssueID != root.ID {
			t.Fatalf("child %d is not parented to the root: %v", i, child.ParentIssueID)
		}
		if child.Stage == nil || int(*child.Stage) != wantStages[i] {
			t.Fatalf("child %d stage = %v, want %d", i, child.Stage, wantStages[i])
		}
		if child.Title != wantTitles[i] {
			t.Fatalf("child %d title = %q, want %q", i, child.Title, wantTitles[i])
		}
		var originID string
		dbfx.QueryRow(t, `SELECT origin_id FROM issue WHERE id = $1`, child.ID).Scan(&originID)
		want := uuidToString(issueDraftNodeID(util.MustParseUUID(session.SessionID), wantKeys[i]))
		if originID != want {
			t.Fatalf("child %d origin_id = %s, want the derived node id %s", i, originID, want)
		}
		if seenOrigins[originID] {
			t.Fatalf("child %d shares an origin_id; a group must never share one, "+
				"or GetIssueByOrigin (LIMIT 1) returns an arbitrary member", i)
		}
		seenOrigins[originID] = true
	}
}

// The confirmed group is the authority on what runs: stage 1 is started, later
// stages are parked in backlog with their assignee already bound. The server
// adds no branch for this — the payload's status IS the policy, and the
// existing backlog skip does the rest.
func TestFinalizeIssueDraftGroupStartsOnlyTheFirstStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("s1", "started now", "todo"), agentID)
	first["stage"] = 1
	parked := assignTo(draftChild("s2", "parked until promoted", "backlog"), agentID)
	parked["stage"] = 2

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("staged parent", first, parked))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	statusByTitle := map[string]string{}
	for _, issue := range finalized.Issues {
		var status string
		dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issue.ID).Scan(&status)
		statusByTitle[issue.Title] = status
	}
	if statusByTitle["started now"] != "todo" {
		t.Fatalf("stage 1 child landed in %q, want todo", statusByTitle["started now"])
	}
	if statusByTitle["parked until promoted"] != "backlog" {
		t.Fatalf("stage 2 child landed in %q, want backlog so it does not run", statusByTitle["parked until promoted"])
	}

	queued := func(title string) int {
		return dbfx.Count(t, `
			SELECT COUNT(*) FROM agent_task_queue
			WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title = $2)
		`, testWorkspaceID, title)
	}
	if got := queued("started now"); got != 1 {
		t.Fatalf("stage 1 child queued %d tasks, want 1 — the backlog comparison below is "+
			"only meaningful if the started child really enqueued", got)
	}
	if got := queued("parked until promoted"); got != 0 {
		t.Fatalf("stage 2 child queued %d tasks; a parked stage must not start work", got)
	}
}

// A confirmed alignment answers with the same group every time it is confirmed.
func TestFinalizeIssueDraftGroupIsIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("idempotent parent",
		draftChild("c1", "first child", "todo"),
		draftChild("c2", "second child", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)

	var again FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&again)

	if again.IssueID != first.IssueID {
		t.Fatalf("retried confirm returned a different root: %s vs %s", again.IssueID, first.IssueID)
	}
	if len(again.Issues) != len(first.Issues) {
		t.Fatalf("retried confirm returned %d issues, want the same %d", len(again.Issues), len(first.Issues))
	}
	for i := range first.Issues {
		if again.Issues[i].ID != first.Issues[i].ID {
			t.Fatalf("retried confirm disagrees at position %d: %s vs %s",
				i, again.Issues[i].ID, first.Issues[i].ID)
		}
	}
	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("two confirms produced %d issues, want 3", got)
	}
}

// The case revision cannot defend against: between being admitted and creating,
// a confirm's payload was replaced with a whole new set of child keys. Only the
// root's id — the chat session id — can stop the second confirm from building a
// second group.
func TestFinalizeIssueDraftGroupIsNotDuplicatedByReKeyedChildren(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("rekey parent",
		draftChild("c1", "the child that was agreed", "todo"),
		draftChild("c2", "the other agreed child", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)
	if len(first.Issues) != 3 {
		t.Fatalf("first confirm produced %d issues, want 3", len(first.Issues))
	}

	// A save that replaced every child key, at a revision the next confirm will
	// find perfectly acceptable. §3.3 timeline B.
	revision := reopenIssueDraft(t, session.SessionID, draftGroupPayload("rekey parent",
		draftChild("x1", "a child nobody agreed to", "todo"),
		draftChild("x2", "another child nobody agreed to", "todo"),
		draftChild("x3", "a third child nobody agreed to", "todo"),
	))

	var second FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, revision)).
		Want(http.StatusOK).JSON(&second)

	if second.IssueID != first.IssueID {
		t.Fatalf("the re-keyed confirm built a second group: root %s, want %s", second.IssueID, first.IssueID)
	}
	if len(second.Issues) != 3 {
		t.Fatalf("the re-keyed confirm answered with %d issues, want the first group's 3", len(second.Issues))
	}
	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("the board holds %d issues from this alignment, want 3 — re-keying must "+
			"adopt the group that exists, never add to it", got)
	}
	for _, title := range []string{"a child nobody agreed to", "another child nobody agreed to", "a third child nobody agreed to"} {
		if got := dbfx.Count(t, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title); got != 0 {
			t.Fatalf("a re-keyed child was created anyway: %q", title)
		}
	}
}

// A confirm whose process died after the commit but before it recorded the
// group must recover into that group, not build a second one.
func TestFinalizeIssueDraftGroupAdoptsTheGroupAfterACrashedConfirm(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("crash parent",
		draftChild("c1", "committed before the crash", "todo"),
	))

	var first FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&first)

	// Same payload, same revision, draft never completed: exactly the state a
	// process that died between step 2 and step 3 leaves behind.
	revision := reopenIssueDraft(t, session.SessionID,
		draftGroupPayload("crash parent", draftChild("c1", "committed before the crash", "todo")))

	var recovered FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, revision)).
		Want(http.StatusOK).JSON(&recovered)

	if recovered.IssueID != first.IssueID {
		t.Fatalf("recovery built a second group: root %s, want %s", recovered.IssueID, first.IssueID)
	}
	if len(recovered.Issues) != 2 {
		t.Fatalf("recovery answered with %d issues, want the group's 2", len(recovered.Issues))
	}
	if recovered.Draft.IssueID == nil || *recovered.Draft.IssueID != first.IssueID {
		t.Fatalf("recovery did not point the draft at the group: issue_id = %v", recovered.Draft.IssueID)
	}
	if got := issueDraftGroupIssueCount(t); got != 2 {
		t.Fatalf("recovery left %d issues, want 2", got)
	}
}

// Two tabs confirming the same revision at the same moment. Both must be told
// about the same group; the loser adopts, it does not create.
func TestFinalizeIssueDraftGroupConcurrentConfirmsCreateOneGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("concurrent parent",
		draftChild("c1", "concurrent child one", "todo"),
		draftChild("c2", "concurrent child two", "todo"),
	))

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

	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("%d concurrent confirms produced %d issues, want exactly one group of 3", attempts, got)
	}
	root := ""
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("concurrent confirm %d returned %d; every repeat of an accepted confirm must succeed", i, code)
		}
		if len(results[i].Issues) != 3 {
			t.Fatalf("concurrent confirm %d answered with %d issues, want the whole group of 3", i, len(results[i].Issues))
		}
		if root == "" {
			root = results[i].IssueID
			continue
		}
		if results[i].IssueID != root {
			t.Fatalf("concurrent confirms disagreed on the group: %s vs %s", root, results[i].IssueID)
		}
	}
}

// A payload is validated before anything is created, so a client mistake costs
// nothing. Each case here asserts both halves: the readable error, and an empty
// board.
func TestFinalizeIssueDraftGroupRejectsMalformedChildren(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	twentyOne := make([]map[string]any, 0, 21)
	for i := range 21 {
		twentyOne = append(twentyOne, draftChild(fmt.Sprintf("k%d", i), "child", "todo"))
	}

	cases := []struct {
		name    string
		payload map[string]any
		message string
	}{
		{
			name: "duplicate key",
			payload: draftGroupPayload("dup keys",
				draftChild("c1", "first", "todo"),
				draftChild("c1", "second", "todo")),
			message: "duplicate sub-issue key",
		},
		{
			// The keys are trimmed before anything else, so a key that only
			// differs by whitespace is the same node — and would derive the
			// same origin_id inside the group transaction.
			name: "keys that collide once trimmed",
			payload: draftGroupPayload("trimmed keys",
				draftChild("c1", "first", "todo"),
				draftChild("  c1  ", "second", "todo")),
			message: "duplicate sub-issue key",
		},
		{
			name: "missing key",
			payload: draftGroupPayload("no key",
				draftChild("  ", "unnamed", "todo")),
			message: "sub-issue key is required",
		},
		{
			name:    "too many children",
			payload: draftGroupPayload("too many", twentyOne...),
			message: "draft has too many sub-issues",
		},
		{
			name: "stage below one",
			payload: draftGroupPayload("stage zero",
				map[string]any{"key": "c1", "title": "zero stage", "status": "todo", "priority": "medium", "stage": 0}),
			message: "invalid sub-issue stage",
		},
		{
			name: "negative stage",
			payload: draftGroupPayload("stage negative",
				map[string]any{"key": "c1", "title": "negative stage", "status": "todo", "priority": "medium", "stage": -1}),
			message: "invalid sub-issue stage",
		},
		{
			name: "stage beyond the bound",
			payload: draftGroupPayload("stage high",
				map[string]any{"key": "c1", "title": "high stage", "status": "todo", "priority": "medium", "stage": 21}),
			message: "invalid sub-issue stage",
		},
		{
			name: "child without a title",
			payload: draftGroupPayload("child without title",
				draftChild("c1", "", "todo")),
			message: "draft title is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanupIssueDraftGroup(t)

			session := startIssueDraftSession(t)
			saved := saveIssueDraft(t, session.SessionID, 0, "ready", tc.payload)

			res := testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
				Want(http.StatusBadRequest)
			if !strings.Contains(res.Body.String(), tc.message) {
				t.Fatalf("error body %q does not name the problem (%q)", res.Body.String(), tc.message)
			}
			if got := issueDraftGroupIssueCount(t); got != 0 {
				t.Fatalf("a rejected payload still created %d issues; validation has to happen "+
					"before the first insert, not inside the group transaction", got)
			}
		})
	}
}

// A confirm is one human decision about a whole group, so a single node whose
// seat cannot be applied must not cost the user the other rows (DENE-694). The
// unauthorized pair is dropped — never written — the node is created unassigned,
// and the response names it. The security property is unchanged: the caller
// cannot dispatch an agent it cannot invoke, it just does not lose the ticket
// over one.
func TestFinalizeIssueDraftGroupDropsAChildAssigneeItCannotApply(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	privateAgentID, _, _ := privateAgentTestFixture(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("parent with one unappliable seat",
		assignTo(draftChild("c1", "allowed child", "todo"), agentID),
		assignTo(draftChild("c2", "child the caller cannot dispatch", "todo"), privateAgentID),
	))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("a group with one unappliable assignee created %d issues, want the root plus both "+
			"children — one bad seat must not cost the rest of the group", got)
	}
	if len(finalized.Issues) != 3 {
		t.Fatalf("the response carries %d issues, want the whole group of 3", len(finalized.Issues))
	}

	allowed, blocked := finalized.Issues[1], finalized.Issues[2]
	if allowed.Title != "allowed child" || blocked.Title != "child the caller cannot dispatch" {
		t.Fatalf("the group came back in the wrong order: %q then %q", allowed.Title, blocked.Title)
	}
	if allowed.AssigneeID == nil || *allowed.AssigneeID != agentID {
		t.Fatalf("the allowed child lost its assignee too (%v); only the unappliable node may be dropped",
			allowed.AssigneeID)
	}
	if blocked.AssigneeID != nil || blocked.AssigneeType != nil {
		t.Fatalf("the unappliable child was assigned anyway (%v/%v)", blocked.AssigneeType, blocked.AssigneeID)
	}
	var storedAssignee *string
	dbfx.QueryRow(t, `SELECT assignee_id::text FROM issue WHERE id = $1`, blocked.ID).Scan(&storedAssignee)
	if storedAssignee != nil {
		t.Fatalf("the dropped assignee was written to the row anyway: %v", *storedAssignee)
	}

	if len(finalized.AssignmentWarnings) != 1 {
		t.Fatalf("assignment warnings = %+v, want the one child that could not be dispatched",
			finalized.AssignmentWarnings)
	}
	warning := finalized.AssignmentWarnings[0]
	if warning.Key != "c2" {
		t.Fatalf("warning key = %q, want c2 — the panel maps the warning back to the row by key", warning.Key)
	}
	if warning.Title != "child the caller cannot dispatch" {
		t.Fatalf("warning title = %q, want the node's title, which is what a person is shown", warning.Title)
	}
	if warning.Reason == "" {
		t.Fatal("the warning carries no reason; the user cannot tell why the seat was dropped")
	}
}

// A payload that cannot even be parsed into an assignee — here a bogus id — is
// the same case as a forbidden one: the node is created unassigned and named,
// not refused. It is the ordinary create path's own refusal reused as a
// warning, so the two surfaces cannot drift apart.
func TestFinalizeIssueDraftGroupDropsAMalformedChildAssignee(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	broken := draftChild("c1", "child with a bogus seat", "todo")
	broken["assignee_type"] = "agent"
	broken["assignee_id"] = "not-a-uuid"

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("parent with a malformed seat", broken))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	if got := issueDraftGroupIssueCount(t); got != 2 {
		t.Fatalf("a group with a malformed assignee created %d issues, want the root and its child", got)
	}
	if len(finalized.AssignmentWarnings) != 1 || finalized.AssignmentWarnings[0].Key != "c1" {
		t.Fatalf("assignment warnings = %+v, want the one malformed child", finalized.AssignmentWarnings)
	}
	if len(finalized.Issues) != 2 || finalized.Issues[1].AssigneeID != nil {
		t.Fatalf("the malformed child was not created unassigned: %+v", finalized.Issues)
	}
}

// The quota is per node and the group is atomic, so a group that cannot fit
// creates nothing. It is asserted through the handler because the error has to
// reach the client as 402, not as a generic failure.
func TestFinalizeIssueDraftGroupCreatesNothingWhenTheQuotaCannotCoverIt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	// Room for two, asking for four.
	limit := dbfx.Count(t, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1`, testWorkspaceID) + 2
	stub := entitlementtest.New()
	stub.Set(uuid.MustParse(testWorkspaceID), entitlement.GateIssueCount, entitlement.Decision{
		Gate:           entitlement.Gate{Action: entitlement.ActionEnforce, Limit: &limit},
		PolicyRevision: 34,
	})
	priorProvider := testHandler.IssueService.Entitlements
	testHandler.IssueService.Entitlements = stub
	t.Cleanup(func() { testHandler.IssueService.Entitlements = priorProvider })

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("over quota parent",
		draftChild("c1", "quota child one", "todo"),
		draftChild("c2", "quota child two", "todo"),
		draftChild("c3", "quota child three", "todo"),
	))

	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusPaymentRequired)

	if got := issueDraftGroupIssueCount(t); got != 0 {
		t.Fatalf("a group that did not fit the quota created %d issues; a half-created group "+
			"is worse than none", got)
	}
}

// The project, the parent and the children are one confirm. Every issue in the
// group is filed under the project this request created, not under whatever
// the draft happened to be pointing at.
func TestFinalizeIssueDraftCreatesTheProjectWithTheGroup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("group with a new project",
		draftChild("c1", "first piece", "todo"),
		draftChild("c2", "second piece", "todo"),
	))
	title := "DENE-843 " + session.SessionID
	t.Cleanup(func() {
		dbfx.Cleanup(t, `DELETE FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	})

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, withURLParam(newRequest(http.MethodPost, "/api/issue-drafts/"+session.SessionID+"/finalize", map[string]any{
		"expected_revision": saved.Revision,
		"new_project": map[string]any{
			"title":       title,
			"icon":        "🛗",
			"description": "图像追溯的安全加固",
		},
	}), "sessionId", session.SessionID)).Want(http.StatusOK).JSON(&finalized)

	if len(finalized.Issues) != 3 {
		t.Fatalf("the response carries %d issues, want the root plus 2 children", len(finalized.Issues))
	}
	var projectID string
	dbfx.QueryRow(t, `SELECT id FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&projectID)
	for i, issue := range finalized.Issues {
		var got string
		dbfx.QueryRow(t, `SELECT project_id FROM issue WHERE id = $1`, issue.ID).Scan(&got)
		if got != projectID {
			t.Fatalf("issue %d project_id = %s, want the new project %s", i, got, projectID)
		}
	}
}

// A group that cannot be created must not leave the project behind either.
func TestFinalizeIssueDraftRollsBackTheNewProjectWhenTheGroupDoesNotFit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	limit := dbfx.Count(t, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1`, testWorkspaceID) + 1
	stub := entitlementtest.New()
	stub.Set(uuid.MustParse(testWorkspaceID), entitlement.GateIssueCount, entitlement.Decision{
		Gate:           entitlement.Gate{Action: entitlement.ActionEnforce, Limit: &limit},
		PolicyRevision: 34,
	})
	priorProvider := testHandler.IssueService.Entitlements
	testHandler.IssueService.Entitlements = stub
	t.Cleanup(func() { testHandler.IssueService.Entitlements = priorProvider })

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("over quota with a project",
		draftChild("c1", "quota child one", "todo"),
		draftChild("c2", "quota child two", "todo"),
		draftChild("c3", "quota child three", "todo"),
	))
	title := "DENE-843 rollback " + session.SessionID

	res := testutil.Call(t, testHandler.FinalizeIssueDraft, withURLParam(newRequest(http.MethodPost, "/api/issue-drafts/"+session.SessionID+"/finalize", map[string]any{
		"expected_revision": saved.Revision,
		"new_project":       map[string]any{"title": title},
	}), "sessionId", session.SessionID))
	res.Want(http.StatusPaymentRequired)

	if got := dbfx.Count(t, `SELECT COUNT(*) FROM project WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title); got != 0 {
		t.Fatalf("a group that did not fit left %d projects behind", got)
	}
	if got := issueDraftGroupIssueCount(t); got != 0 {
		t.Fatalf("a rolled-back confirm left %d issues", got)
	}
}

// issueDraftGroupTaskCount counts the tasks a single issue of a group enqueued.
// agent_task_queue carries no foreign key, so this is the only place a confirm's
// "did it actually start work" question can be answered.
func issueDraftGroupTaskCount(t *testing.T, issueID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT COUNT(*) FROM agent_task_queue WHERE issue_id = $1`, issueID)
}

// issueDraftGroupIssueByTitle finds one node of a just-confirmed group by the
// title the payload gave it.
func issueDraftGroupIssueByTitle(t *testing.T, issues []IssueDraftCreatedIssue, title string) IssueDraftCreatedIssue {
	t.Helper()
	for _, issue := range issues {
		if issue.Title == title {
			return issue
		}
	}
	t.Fatalf("the confirmed group has no issue titled %q", title)
	return IssueDraftCreatedIssue{}
}

// issueDraftGroupStoredStatus reads a node's status from the row itself, not
// from the response: the response is built from the same row, but a test that
// wants to prove what was WRITTEN should read what was written.
func issueDraftGroupStoredStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	return status
}

// A group's root coordinates. It keeps the assignee the alignment gave it —
// that is the seat the stage barrier wakes — and it is created in an ACTIVE
// status, because `notifyParentOfChildDone` skips a backlog parent silently and
// every stage after the first would then wait forever with nobody told to move
// it. What keeps it from doing implementation work is this confirm, not a
// parking status: its run is suppressed.
func TestFinalizeIssueDraftCoordinatorRootIsCreatedWithoutARun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("k1", "coordinated stage one", "todo"), agentID)
	first["stage"] = 1
	parked := assignTo(draftChild("k2", "coordinated stage two", "backlog"), agentID)
	parked["stage"] = 2

	session := startIssueDraftSession(t)
	payload := draftGroupPayload("coordinated parent", first, parked)
	payload["status"] = "todo"
	payload["assignee_type"] = "agent"
	payload["assignee_id"] = agentID
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", payload)

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	root := finalized.Issues[0]
	if root.Status != "in_progress" {
		t.Fatalf("coordinator root status = %q, want in_progress — a backlog parent is "+
			"never woken by the stage barrier", root.Status)
	}
	if root.AssigneeType == nil || *root.AssigneeType != "agent" ||
		root.AssigneeID == nil || *root.AssigneeID != agentID {
		t.Fatalf("coordinator root lost its assignee: %v/%v; the barrier would have "+
			"nobody to wake", root.AssigneeType, root.AssigneeID)
	}
	if got := issueDraftGroupTaskCount(t, root.ID); got != 0 {
		t.Fatalf("the coordinator root queued %d tasks; confirming a group must not give "+
			"the coordinator an implementation task of its own", got)
	}

	startedNow := issueDraftGroupIssueByTitle(t, finalized.Issues, "coordinated stage one")
	if startedNow.Status != "todo" {
		t.Fatalf("stage 1 child status = %q, want todo", startedNow.Status)
	}
	if got := issueDraftGroupTaskCount(t, startedNow.ID); got != 1 {
		t.Fatalf("stage 1 child queued %d tasks, want 1", got)
	}

	waiting := issueDraftGroupIssueByTitle(t, finalized.Issues, "coordinated stage two")
	if waiting.Status != "backlog" {
		t.Fatalf("stage 2 child status = %q, want backlog", waiting.Status)
	}
	if got := issueDraftGroupTaskCount(t, waiting.ID); got != 0 {
		t.Fatalf("stage 2 child queued %d tasks; a parked stage must not start work", got)
	}
}

// Closing stage 1 promotes a stage-2 sub-issue whose description states no
// extra dependency. The coordinator is not woken to do that promotion.
func TestFinalizeIssueDraftStageBarrierPromotesAClearNextStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("w1", "wake stage one", "todo"), agentID)
	first["stage"] = 1
	parked := assignTo(draftChild("w2", "wake stage two", "backlog"), agentID)
	parked["stage"] = 2

	session := startIssueDraftSession(t)
	payload := draftGroupPayload("wake parent", first, parked)
	payload["assignee_type"] = "agent"
	payload["assignee_id"] = agentID
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", payload)

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	root := finalized.Issues[0]
	stageOne := issueDraftGroupIssueByTitle(t, finalized.Issues, "wake stage one")
	stageTwo := issueDraftGroupIssueByTitle(t, finalized.Issues, "wake stage two")
	if got := issueDraftGroupTaskCount(t, root.ID); got != 0 {
		t.Fatalf("the coordinator was running before any stage closed (%d tasks)", got)
	}

	updateChildStatus(t, stageOne.ID, "done")

	if got := countSystemCommentsOn(t, root.ID); got != 1 {
		t.Fatalf("closing stage 1 produced %d comments, want 1", got)
	}
	content := parentSystemCommentContent(t, root.ID)
	if !strings.Contains(content, "提到待办") {
		t.Fatalf("stage comment does not say the next stage was promoted: %s", content)
	}
	if got := issueDraftGroupTaskCount(t, root.ID); got != 0 {
		t.Fatalf("closing stage 1 queued %d coordinator tasks, want 0 — a clear next stage does not wait for the parent", got)
	}
	if got := issueDraftGroupStoredStatus(t, stageTwo.ID); got != "todo" {
		t.Fatalf("stage 2 status after stage 1 closed = %q, want todo", got)
	}
	if got := issueDraftGroupTaskCount(t, stageTwo.ID); got != 1 {
		t.Fatalf("stage 2 queued %d tasks, want 1", got)
	}
}

// No assignee, no fake run. A sub-issue nobody matched is created as an
// UNASSIGNED todo — visible on the board as work with a hole in it — and the
// ordinary assign path is the way the hole gets filled. Parking it in Backlog
// instead would hide exactly the gap the confirm just warned about.
func TestFinalizeIssueDraftUnassignedChildWaitsForSomeoneToPickItUp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	unassigned := draftChild("n1", "nobody matched yet", "todo")
	unassigned["stage"] = 1
	parked := assignTo(draftChild("n2", "later stage", "backlog"), agentID)
	parked["stage"] = 2

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("unassigned parent", unassigned, parked))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	orphan := issueDraftGroupIssueByTitle(t, finalized.Issues, "nobody matched yet")
	if orphan.Status != "todo" {
		t.Fatalf("unassigned stage 1 child status = %q, want todo", orphan.Status)
	}
	if orphan.AssigneeID != nil || orphan.AssigneeType != nil {
		t.Fatalf("unassigned stage 1 child carries an assignee: %v/%v",
			orphan.AssigneeType, orphan.AssigneeID)
	}
	if got := issueDraftGroupTaskCount(t, orphan.ID); got != 0 {
		t.Fatalf("an unassigned sub-issue queued %d tasks; nobody was paged", got)
	}

	// The follow-up path: fill the seat with the ordinary assign write, and the
	// ordinary assignment trigger starts the work. No group-specific repair.
	rec := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+orphan.ID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	req = withURLParam(req, "id", orphan.ID)
	testHandler.UpdateIssue(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign the unassigned child: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := issueDraftGroupTaskCount(t, orphan.ID); got != 1 {
		t.Fatalf("assigning the unassigned child queued %d tasks, want 1", got)
	}
}

// Stage decides a sub-issue's status at the write boundary, not the payload.
// An older client — or a payload written straight into the draft — can say
// `todo` on stage 2, and the confirm must still park it: stage 1 runs, later
// stages wait, and that has to be a server guarantee rather than a promise the
// client keeps.
func TestFinalizeIssueDraftDerivesChildStatusFromStage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	late := assignTo(draftChild("z1", "raw late todo", "todo"), agentID)
	late["stage"] = 2
	early := assignTo(draftChild("z2", "raw early backlog", "backlog"), agentID)
	early["stage"] = 1

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready",
		draftGroupPayload("derived parent", late, early))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	stageTwo := issueDraftGroupIssueByTitle(t, finalized.Issues, "raw late todo")
	if got := issueDraftGroupStoredStatus(t, stageTwo.ID); got != "backlog" {
		t.Fatalf("stage 2 child stored %q, want backlog", got)
	}
	if got := issueDraftGroupTaskCount(t, stageTwo.ID); got != 0 {
		t.Fatalf("stage 2 child queued %d tasks", got)
	}

	stageOne := issueDraftGroupIssueByTitle(t, finalized.Issues, "raw early backlog")
	if got := issueDraftGroupStoredStatus(t, stageOne.ID); got != "todo" {
		t.Fatalf("stage 1 child stored %q, want todo", got)
	}
	if got := issueDraftGroupTaskCount(t, stageOne.ID); got != 1 {
		t.Fatalf("stage 1 child queued %d tasks, want 1", got)
	}
}

// Confirming twice — a double click, a lost response, a client that retries —
// adopts the group instead of building a second one, and the adoption must not
// re-run anything: not the suppressed coordinator, not the stage 1 children,
// not the parked ones.
func TestFinalizeIssueDraftCoordinatorGroupStaysIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	first := assignTo(draftChild("r1", "retry stage one", "todo"), agentID)
	first["stage"] = 1
	second := assignTo(draftChild("r2", "retry stage two", "backlog"), agentID)
	second["stage"] = 2

	session := startIssueDraftSession(t)
	payload := draftGroupPayload("retry parent", first, second)
	payload["assignee_type"] = "agent"
	payload["assignee_id"] = agentID
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", payload)

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)
	var repeated FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&repeated)

	if got := issueDraftGroupIssueCount(t); got != 3 {
		t.Fatalf("a repeated confirm left %d issues, want the original 3", got)
	}
	if len(repeated.Issues) != len(finalized.Issues) {
		t.Fatalf("repeat returned %d issues, first returned %d", len(repeated.Issues), len(finalized.Issues))
	}
	root := finalized.Issues[0]
	if got := issueDraftGroupTaskCount(t, root.ID); got != 0 {
		t.Fatalf("the repeat queued %d coordinator tasks, want 0", got)
	}
	started := issueDraftGroupIssueByTitle(t, finalized.Issues, "retry stage one")
	if got := issueDraftGroupTaskCount(t, started.ID); got != 1 {
		t.Fatalf("the repeat left %d stage-1 tasks, want the original 1", got)
	}
	waiting := issueDraftGroupIssueByTitle(t, finalized.Issues, "retry stage two")
	if got := issueDraftGroupTaskCount(t, waiting.ID); got != 0 {
		t.Fatalf("the repeat queued %d parked tasks, want 0", got)
	}
}

// A sub-issue's status is the stage's, decided here rather than trusted from
// the payload. This is the one-line rule that makes "stage 1 runs, later stages
// wait" true no matter which client wrote the draft, so it is worth pinning
// without a database.
func TestIssueDraftChildStatusForCreateIsDerivedFromStage(t *testing.T) {
	stage := func(n int32) *int32 { return &n }
	cases := []struct {
		name  string
		stage *int32
		want  string
	}{
		{"no stage is the implicit first stage", nil, "todo"},
		{"stage 1 starts", stage(1), "todo"},
		{"stage 2 parks", stage(2), "backlog"},
		{"the last accepted stage parks", stage(maxIssueDraftChildStage), "backlog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueDraftChildStatusForCreate(tc.stage); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// The coordinator status must be one the ordinary status catalog accepts, and
// it must not be backlog: a backlog parent is skipped by the child-done
// notifier, which is the whole reason the root is kept active.
func TestIssueDraftCoordinatorStatusIsActive(t *testing.T) {
	if issueDraftCoordinatorStatus == "backlog" {
		t.Fatal("a backlog coordinator is never woken when a stage closes")
	}
	if !issuestatus.IsBuiltIn(issueDraftCoordinatorStatus) {
		t.Fatalf("coordinator status %q is not a built-in status; a workspace whose "+
			"catalog is empty would refuse the create", issueDraftCoordinatorStatus)
	}
}

// DENE-812: a group confirmed with empty seats is seated by routing — every
// sub-issue gets an executor, the root gets its coordinator and reviewer — and
// only the stage-1 child starts running. The root coordinates and the stage-2
// child waits for its stage, seated or not.
func TestFinalizeIssueDraftGroupIsSeatedByRouting(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftGroup(t)

	agentID := handlerTestAgentID(t)
	var previousTier *string
	dbfx.QueryRow(t, `SELECT routing_tier FROM agent WHERE id = $1`, agentID).Scan(&previousTier)
	t.Cleanup(func() { dbfx.Exec(t, `UPDATE agent SET routing_tier = $2 WHERE id = $1`, agentID, previousTier) })
	dbfx.Exec(t, `UPDATE agent SET routing_tier = 'medium' WHERE id = $1`, agentID)
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})

	first := draftChild("r1", "routed stage one", "todo")
	first["stage"] = 1
	parked := draftChild("r2", "routed stage two", "backlog")
	parked["stage"] = 2
	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", draftGroupPayload("routed parent", first, parked))

	var finalized FinalizeIssueDraftResponse
	testutil.Call(t, testHandler.FinalizeIssueDraft, finalizeRequest(t, session.SessionID, saved.Revision)).
		Want(http.StatusOK).JSON(&finalized)

	seated := func(title string) bool {
		return dbfx.Count(t, `
			SELECT COUNT(*) FROM issue
			WHERE workspace_id = $1 AND title = $2 AND assignee_type = 'agent' AND assignee_id = $3
		`, testWorkspaceID, title, agentID) == 1
	}
	titles := []string{"routed parent", "routed stage one", "routed stage two"}
	deadline := time.Now().Add(15 * time.Second)
	for {
		all := true
		for _, title := range titles {
			all = all && seated(title)
		}
		if all {
			break
		}
		if time.Now().After(deadline) {
			for _, title := range titles {
				t.Logf("%s seated = %v", title, seated(title))
			}
			t.Fatal("routing did not seat every node of the confirmed group")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if dbfx.Count(t, `
		SELECT COUNT(*) FROM issue
		WHERE workspace_id = $1 AND title = 'routed parent' AND reviewer_type IS NOT NULL
	`, testWorkspaceID) != 1 {
		t.Fatal("the root's reviewer slot was not filled")
	}
	queued := func(title string) int {
		return dbfx.Count(t, `
			SELECT COUNT(*) FROM agent_task_queue
			WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1 AND title = $2)
		`, testWorkspaceID, title)
	}
	if got := queued("routed stage one"); got != 1 {
		t.Fatalf("stage 1 child queued %d tasks, want 1", got)
	}
	if got := queued("routed parent"); got != 0 {
		t.Fatalf("root queued %d tasks; a coordinator is seated without a run", got)
	}
	if got := queued("routed stage two"); got != 0 {
		t.Fatalf("stage 2 child queued %d tasks; a parked stage must not start work", got)
	}
}
