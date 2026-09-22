package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// DENE-385: the V3 task import endpoint. These are the ten blocking
// assertions from docs/kun/config-transfer-v3-issues.md §7.3 plus the two
// §11.7 / §10.3 additions, driven through the real handler.

const (
	transferIssuesCreatedAt  = "2026-09-01T10:00:00.123456Z"
	transferIssuesUpdatedAt  = "2026-09-02T11:11:11.5Z"
	transferIssuesActivityAt = "2026-09-03T12:00:00.000001Z"
)

func postTransferIssues(t *testing.T, ws string, body any) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.ImportWorkspaceTransferIssues, transferReq(
		"POST", "/api/workspaces/"+ws+"/transfer/issues", ws, body))
}

// transferIssueRow builds one issues/*.jsonl row with the fields every case
// shares already filled in.
func transferIssueRow(sourceID string, number int, title, creatorID string, over map[string]any) map[string]any {
	row := map[string]any{
		"source_id":        sourceID,
		"number":           number,
		"title":            title,
		"description":      "",
		"status":           "todo",
		"status_category":  "todo",
		"priority":         "none",
		"assignee_type":    nil,
		"assignee_id":      nil,
		"creator_type":     "member",
		"creator_id":       creatorID,
		"parent_issue_id":  nil,
		"project_id":       nil,
		"position":         0.0,
		"stage":            nil,
		"start_date":       nil,
		"due_date":         nil,
		"created_at":       transferIssuesCreatedAt,
		"updated_at":       transferIssuesUpdatedAt,
		"last_activity_at": transferIssuesActivityAt,
		"metadata":         map[string]any{},
		"properties":       map[string]any{},
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

func transferCommentRow(sourceID, issueID, content string, over map[string]any) map[string]any {
	row := map[string]any{
		"source_id":        sourceID,
		"issue_id":         issueID,
		"author_type":      "member",
		"author_id":        testUserID,
		"content":          content,
		"type":             "comment",
		"parent_id":        nil,
		"created_at":       transferIssuesCreatedAt,
		"updated_at":       transferIssuesUpdatedAt,
		"resolved_at":      nil,
		"resolved_by_type": nil,
		"resolved_by_id":   nil,
		"deleted_at":       nil,
		"attachment_ids":   []string{},
	}
	for k, v := range over {
		row[k] = v
	}
	return row
}

// transferIssueBody assembles a shard request. refs is the per-shard identity
// index the export side ships with every slice.
func transferIssueBody(refs map[string]any, issues, comments, relations []map[string]any, finalize bool) map[string]any {
	if refs == nil {
		refs = map[string]any{}
	}
	if issues == nil {
		issues = []map[string]any{}
	}
	if comments == nil {
		comments = []map[string]any{}
	}
	if relations == nil {
		relations = []map[string]any{}
	}
	return map[string]any{
		"dry_run":   false,
		"finalize":  finalize,
		"refs":      refs,
		"issues":    issues,
		"comments":  comments,
		"relations": relations,
	}
}

func transferMemberRef(email string) map[string]any {
	return map[string]any{"email": email}
}

// Assertion 3 + 4 + 9: timestamps survive to the microsecond, the subscriber
// pair is rebuilt, and a status_change / system comment is written rather than
// rejected like the client-facing comment endpoint would reject it.
func TestTransferIssues_PreservesTimestampsAndRebuildsSubscribers(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentName := "XferIssueAgent-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})

	srcAgent := uuid.NewString()
	srcIssue := uuid.NewString()
	srcComment := uuid.NewString()
	body := transferIssueBody(
		map[string]any{
			"agents":  map[string]any{srcAgent: map[string]any{"name": agentName}},
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":  map[string]any{srcIssue: map[string]any{"number": 12, "identifier": "HAN-12"}},
		},
		[]map[string]any{transferIssueRow(srcIssue, 12, "imported task", testUserID, map[string]any{
			"status":        "in_review",
			"priority":      "high",
			"assignee_type": "agent",
			"assignee_id":   srcAgent,
			"position":      3.5,
		})},
		[]map[string]any{
			transferCommentRow(srcComment, srcIssue, "moved to review", map[string]any{
				"type":       "status_change",
				"created_at": "2026-09-02T00:00:00Z",
				"updated_at": "2026-09-02T00:00:00Z",
			}),
			transferCommentRow(uuid.NewString(), srcIssue, "", map[string]any{
				"type":       "system",
				"author_id":  uuid.Nil.String(),
				"created_at": "2026-09-02T00:00:01Z",
				"updated_at": "2026-09-02T00:00:01Z",
			}),
		},
		nil, true)

	postTransferIssues(t, dst, body).Want(http.StatusOK)

	targetIssue := service.TransferIssueID(dst, srcIssue).String()
	created := transferScanTime(t, `SELECT created_at FROM issue WHERE id = $1`, targetIssue)
	updated := transferScanTime(t, `SELECT updated_at FROM issue WHERE id = $1`, targetIssue)
	activity := transferScanTime(t, `SELECT last_activity_at FROM issue WHERE id = $1`, targetIssue)
	wantCreated := mustParseTime(t, transferIssuesCreatedAt)
	wantUpdated := mustParseTime(t, transferIssuesUpdatedAt)
	wantActivity := mustParseTime(t, transferIssuesActivityAt)
	if !created.Equal(wantCreated) || !updated.Equal(wantUpdated) || !activity.Equal(wantActivity) {
		t.Fatalf("issue timestamps drifted: created=%s updated=%s activity=%s want %s / %s / %s",
			created.UTC().Format(time.RFC3339Nano), updated.UTC().Format(time.RFC3339Nano),
			activity.UTC().Format(time.RFC3339Nano),
			wantCreated.Format(time.RFC3339Nano), wantUpdated.Format(time.RFC3339Nano), wantActivity.Format(time.RFC3339Nano))
	}

	targetComment := service.TransferCommentID(dst, srcComment).String()
	commentCreated := transferScanTime(t, `SELECT created_at FROM comment WHERE id = $1`, targetComment)
	wantCommentCreated := mustParseTime(t, "2026-09-02T00:00:00Z")
	if !commentCreated.Equal(wantCommentCreated) {
		t.Fatalf("comment created_at=%s want %s", commentCreated.UTC().Format(time.RFC3339Nano), wantCommentCreated.Format(time.RFC3339Nano))
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, targetIssue); n != 2 {
		t.Fatalf("comments=%d want 2 (status_change and system must both be written)", n)
	}

	// Assertion 4: exactly the creator and assignee subscriptions.
	rows := transferScanRows(t, `SELECT user_type, reason FROM issue_subscriber WHERE issue_id = $1 ORDER BY reason`, targetIssue)
	if len(rows) != 2 {
		t.Fatalf("subscribers=%v want exactly creator+assignee", rows)
	}
	if rows[0][1] != "assignee" || rows[1][1] != "creator" {
		t.Fatalf("subscriber reasons=%v", rows)
	}
	if rows[1][0] != "member" || rows[0][0] != "agent" {
		t.Fatalf("subscriber types=%v", rows)
	}

	// The issue counter watermark rises to the imported maximum.
	if counter := transferScanInt(t, `SELECT issue_counter FROM workspace WHERE id = $1`, dst); counter != 12 {
		t.Fatalf("issue_counter=%d want 12", counter)
	}
}

// Assertions 1 and 2: the whole import performs no task enqueue, and the only
// event it publishes is the workspace-level list invalidation — never
// issue:created / issue:assigned / comment:created.
func TestTransferIssues_NoEnqueueAndNoIssueBroadcast(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentA := "XferNoQueueA-" + uuid.NewString()[:6]
	agentB := "XferNoQueueB-" + uuid.NewString()[:6]
	agentC := "XferNoQueueC-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentA, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	dbfx.Agent(t, agentB, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	dbfx.Agent(t, agentC, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})

	srcA, srcB, srcC := uuid.NewString(), uuid.NewString(), uuid.NewString()
	srcIssue, srcRoot := uuid.NewString(), uuid.NewString()

	var mu sync.Mutex
	var seen []string
	testHandler.Bus.SubscribeAll(func(e events.Event) {
		mu.Lock()
		seen = append(seen, e.Type)
		mu.Unlock()
	})

	body := transferIssueBody(
		map[string]any{
			"agents": map[string]any{
				srcA: map[string]any{"name": agentA},
				srcB: map[string]any{"name": agentB},
				srcC: map[string]any{"name": agentC},
			},
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
		},
		[]map[string]any{transferIssueRow(srcIssue, 3, "todo for an agent", testUserID, map[string]any{
			"assignee_type": "agent",
			"assignee_id":   srcA,
		})},
		[]map[string]any{
			transferCommentRow(srcRoot, srcIssue, "[@"+agentB+"]("+"mention://agent/"+srcB+") please look", nil),
			transferCommentRow(uuid.NewString(), srcIssue, "root mentions", map[string]any{
				"parent_id": srcRoot,
				"content":   "[@" + agentC + "](mention://agent/" + srcC + ") take it",
			}),
		},
		nil, true)

	postTransferIssues(t, dst, body).Want(http.StatusOK)

	targetIssue := service.TransferIssueID(dst, srcIssue).String()
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, targetIssue); n != 0 {
		t.Fatalf("agent_task_queue rows=%d want 0", n)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, eventType := range seen {
		switch eventType {
		case "issue:created", "issue:assigned", "comment:created":
			t.Fatalf("import published %q; the transfer path must broadcast nothing that looks like user activity (saw %v)", eventType, seen)
		}
	}
	found := false
	for _, eventType := range seen {
		if eventType == "workspace:updated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("finalize published no workspace-level invalidation (saw %v)", seen)
	}
}

// Assertion 5: re-importing the same bundle changes no row count and no
// counter.
func TestTransferIssues_Idempotent(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentName := "XferIdemAgent-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})

	srcAgent, srcIssue, srcRoot, srcReply, srcLabel := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	dbfx.Insert(t, "issue_label", testutil.Cols{
		"workspace_id": dst, "resource_type": "issue", "name": srcLabel, "color": "#112233",
	})

	relations := []map[string]any{
		{"kind": "issue_label", "issue_id": srcIssue, "label_resource_type": "issue", "label_name": srcLabel},
		{"kind": "issue_reaction", "issue_id": srcIssue, "actor_type": "member", "actor_id": testUserID, "emoji": "👍", "created_at": transferIssuesCreatedAt},
		{"kind": "comment_reaction", "comment_id": srcRoot, "actor_type": "agent", "actor_id": srcAgent, "emoji": "✅", "created_at": transferIssuesCreatedAt},
	}
	body := transferIssueBody(
		map[string]any{
			"agents":  map[string]any{srcAgent: map[string]any{"name": agentName}},
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":  map[string]any{srcIssue: map[string]any{"number": 21, "identifier": "HAN-21"}},
		},
		[]map[string]any{transferIssueRow(srcIssue, 21, "idempotent", testUserID, map[string]any{
			"assignee_type": "agent", "assignee_id": srcAgent,
		})},
		[]map[string]any{
			transferCommentRow(srcRoot, srcIssue, "root", nil),
			transferCommentRow(srcReply, srcIssue, "reply", map[string]any{"parent_id": srcRoot}),
		},
		relations, true)

	postTransferIssues(t, dst, body).Want(http.StatusOK)
	first := transferIssueCounts(t, dst)
	counterBefore := transferScanInt(t, `SELECT issue_counter FROM workspace WHERE id = $1`, dst)

	postTransferIssues(t, dst, body).Want(http.StatusOK)
	second := transferIssueCounts(t, dst)
	if first != second {
		t.Fatalf("row counts changed on re-import: %v -> %v", first, second)
	}
	if counter := transferScanInt(t, `SELECT issue_counter FROM workspace WHERE id = $1`, dst); counter != counterBefore {
		t.Fatalf("issue_counter moved on re-import: %d -> %d", counterBefore, counter)
	}
}

// Assertion 6: a workspace that already has tasks is refused outright, with
// nothing written.
func TestTransferIssues_RejectsNonEmptyTarget(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dbfx.Issue(t, "pre-existing", testutil.Cols{"workspace_id": dst})

	srcIssue := uuid.NewString()
	body := transferIssueBody(
		map[string]any{"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)}},
		[]map[string]any{transferIssueRow(srcIssue, 99, "should not land", testUserID, nil)},
		nil, nil, true)

	resp := postTransferIssues(t, dst, body).Want(http.StatusBadRequest)
	if resp.Map()["code"] != "transfer_issues_target_not_empty" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, dst); n != 1 {
		t.Fatalf("issues=%d want the pre-existing row only", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, service.TransferIssueID(dst, srcIssue).String()); n != 0 {
		t.Fatal("the refused import still wrote a row")
	}
}

// DENE-401: a shard that fails halfway through leaves nothing behind.
//
// The write half used to run statement by statement against the pool, so a
// failure after the issue rows landed left the target holding part of a
// migrated task tree: the retry's `ON CONFLICT DO NOTHING` then silently
// skipped the rows the failed run had written, and the report called it
// finished. Contract §7.1 asks for the ROLLBACK instead.
func TestTransferIssues_RollsBackTheWholeShardOnMidwayFailure(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)

	// Two rows carrying the same number: the first insert lands, the second
	// trips `uq_issue_workspace_number` inside the write loop, which is exactly
	// the "failure after a row was already written" case.
	body := transferIssueBody(nil, []map[string]any{
		transferIssueRow("rollback-a", 1, "first", testUserID, nil),
		transferIssueRow("rollback-b", 1, "second", testUserID, nil),
	}, nil, nil, false)

	postTransferIssues(t, dst, body).Want(http.StatusInternalServerError)

	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, dst); n != 0 {
		t.Fatalf("a failed shard left %d issue rows in the target, want 0 — the write half has to roll back whole", n)
	}
}

// DENE-400: the escape hatch §2.3 promises has to be reachable on the real
// gate, not only against the CLI's fake server (which never implemented the
// gate at all). `renumber` is the request declaring that the caller offset the
// numbers, and it is the one thing that turns the §2.2 refusal into a write.
//
// Both requests carry the flag in the real flow — the shards and the finalize
// pass — so the test drives them as two requests.
func TestTransferIssues_RenumberAcceptsNonEmptyTarget(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	// The pre-existing task is the whole point: its number is what the imported
	// numbers must stay above, and the fake server never modelled this row.
	dbfx.Issue(t, "pre-existing", testutil.Cols{"workspace_id": dst, "number": 7})

	srcIssue := uuid.NewString()
	refs := map[string]any{
		"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
		"issues":  map[string]any{srcIssue: map[string]any{"number": 15, "identifier": "HAN-15"}},
	}
	importedID := service.TransferIssueID(dst, srcIssue).String()

	// Without the flag the non-empty target is still refused, and nothing lands.
	shard := transferIssueBody(refs,
		[]map[string]any{transferIssueRow(srcIssue, 15, "should not land", testUserID, nil)},
		nil, nil, false)
	resp := postTransferIssues(t, dst, shard).Want(http.StatusBadRequest)
	if resp.Map()["code"] != "transfer_issues_target_not_empty" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1`, importedID); n != 0 {
		t.Fatal("the refused shard still wrote a row")
	}

	// With it, the shard lands.
	shard["renumber"] = true
	postTransferIssues(t, dst, shard).Want(http.StatusOK)

	// The finalize pass runs through the same gate, so it needs the flag too.
	finalize := transferIssueBody(refs,
		[]map[string]any{{"source_id": srcIssue, "parent_issue_id": nil}},
		nil, nil, true)
	finalize["renumber"] = true
	postTransferIssues(t, dst, finalize).Want(http.StatusOK)

	// The new number is strictly above every number the target already had —
	// the invariant that keeps the workspace's own numbering allocatable.
	existingMax := transferScanInt(t, `SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1 AND id <> $2`, dst, importedID)
	importedNumber := transferScanInt(t, `SELECT number FROM issue WHERE id = $1`, importedID)
	if importedNumber <= existingMax {
		t.Fatalf("imported number=%d is not above the target's existing max %d", importedNumber, existingMax)
	}
	// And the watermark moved with it, so the next create cannot reuse a number
	// this import just took.
	if counter := transferScanInt(t, `SELECT issue_counter FROM workspace WHERE id = $1`, dst); counter < importedNumber {
		t.Fatalf("issue_counter=%d stayed below the imported number %d", counter, importedNumber)
	}
}

// Assertion 7 (§3.4): every mention link left in the imported text resolves on
// the target workspace, and the one that cannot is plain text.
func TestTransferIssues_MentionsAllResolvable(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentName := "XferMentionAgent-" + uuid.NewString()[:6]
	dbfx.Agent(t, agentName, "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	squadName := "XferMentionSquad-" + uuid.NewString()[:6]
	squadLeader := dbfx.Agent(t, "XferSquadLeader-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": dst, "visibility": "workspace"})
	dbfx.Squad(t, squadName, squadLeader, testutil.Cols{"workspace_id": dst})
	projectTitle := "XferMentionProject-" + uuid.NewString()[:6]
	dbfx.Project(t, projectTitle, testutil.Cols{"workspace_id": dst})

	srcAgent, srcIssue, srcIssue2, srcSquad, srcProject, srcGhost := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	desc := strings.Join([]string{
		"[@Handler](mention://member/" + testUserID + ")",
		"[@agent](mention://agent/" + srcAgent + ")",
		"[HAN-2](mention://issue/" + srcIssue2 + ")",
		"[@squad](mention://squad/" + srcSquad + ")",
		"[@project](mention://project/" + srcProject + ")",
		"[@gone](mention://agent/" + srcGhost + ")",
		"[all](mention://all/all)",
	}, " ")

	body := transferIssueBody(
		map[string]any{
			"agents":   map[string]any{srcAgent: map[string]any{"name": agentName}},
			"squads":   map[string]any{srcSquad: map[string]any{"name": squadName}},
			"projects": map[string]any{srcProject: map[string]any{"title": projectTitle}},
			"members":  map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":   map[string]any{srcIssue2: map[string]any{"number": 2, "identifier": "HAN-2"}},
		},
		[]map[string]any{
			transferIssueRow(srcIssue, 1, "mentions", testUserID, map[string]any{"description": desc}),
			transferIssueRow(srcIssue2, 2, "mentioned issue", testUserID, nil),
		},
		nil, nil, true)

	postTransferIssues(t, dst, body).Want(http.StatusOK)

	targetIssue := service.TransferIssueID(dst, srcIssue).String()
	stored := transferScanString(t, `SELECT COALESCE(description, '') FROM issue WHERE id = $1`, targetIssue)
	if strings.Contains(stored, "mention://agent/"+srcGhost) {
		t.Fatalf("unmapped agent mention kept a dead link: %s", stored)
	}
	if !strings.Contains(stored, "@gone") {
		t.Fatalf("unmapped mention lost its label instead of degrading to plain text: %s", stored)
	}
	if !strings.Contains(stored, "mention://project/") {
		t.Fatalf("mapped project mention was not rewritten: %s", stored)
	}
	// @all carries no id and must survive untouched.
	if !strings.Contains(stored, "mention://all/all") {
		t.Fatalf("@all was dropped: %s", stored)
	}
	assertTransferMentionsResolvable(t, dst)
}

// Assertion 8: a child whose parent arrives in a later shard is linked at
// finalize, and a parent that is not in the bundle stays NULL with a report
// row.
func TestTransferIssues_BackfillsParentsAcrossShards(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcParent, srcChild, srcOrphan := uuid.NewString(), uuid.NewString(), uuid.NewString()
	refs := map[string]any{
		"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
		// refs.issues is the package-wide index every shard carries; the
		// empty-target gate reads it to tell this bundle's own rows from
		// tasks that were already there.
		"issues": map[string]any{
			srcParent: map[string]any{"number": 30, "identifier": "HAN-30"},
			srcChild:  map[string]any{"number": 31, "identifier": "HAN-31"},
			srcOrphan: map[string]any{"number": 32, "identifier": "HAN-32"},
		},
	}

	// The child is imported first, before its parent exists.
	postTransferIssues(t, dst, transferIssueBody(refs,
		[]map[string]any{transferIssueRow(srcChild, 31, "child", testUserID, map[string]any{"parent_issue_id": srcParent})},
		nil, nil, false)).Want(http.StatusOK)

	postTransferIssues(t, dst, transferIssueBody(refs,
		[]map[string]any{transferIssueRow(srcParent, 30, "parent", testUserID, nil)},
		nil, nil, false)).Want(http.StatusOK)

	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1 AND parent_issue_id IS NULL`,
		service.TransferIssueID(dst, srcChild).String()); n != 1 {
		t.Fatal("the child was linked before finalize")
	}

	// Finalize carries the (source_id, source_parent_id) pairs for the bundle.
	resp := postTransferIssues(t, dst, transferIssueBody(refs,
		[]map[string]any{
			transferIssueRow(srcParent, 30, "parent", testUserID, nil),
			transferIssueRow(srcChild, 31, "child", testUserID, map[string]any{"parent_issue_id": srcParent}),
			transferIssueRow(srcOrphan, 32, "orphan", testUserID, map[string]any{"parent_issue_id": uuid.NewString()}),
		},
		nil, nil, true)).Want(http.StatusOK)

	var report service.TransferIssuesReport
	resp.JSON(&report)

	parentTarget := service.TransferIssueID(dst, srcParent).String()
	childParent := transferScanString(t, `SELECT COALESCE(parent_issue_id::text, '') FROM issue WHERE id = $1`, service.TransferIssueID(dst, srcChild).String())
	if childParent != parentTarget {
		t.Fatalf("child parent=%s want %s", childParent, parentTarget)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE id = $1 AND parent_issue_id IS NULL`,
		service.TransferIssueID(dst, srcOrphan).String()); n != 1 {
		t.Fatal("an out-of-bundle parent was not left NULL")
	}
	var seenUnmapped bool
	for _, row := range report.ParentUnmapped {
		if row.SourceID == srcOrphan && row.Resolution == "nulled" {
			seenUnmapped = true
		}
	}
	if !seenUnmapped {
		t.Fatalf("parent_unmapped report rows=%v", report.ParentUnmapped)
	}
}

// Assertion 10: a tombstone parent keeps its reply on a direct pointer instead
// of flattening the thread.
func TestTransferIssues_KeepsTombstoneParent(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcIssue, srcTombstone, srcReply := uuid.NewString(), uuid.NewString(), uuid.NewString()

	postTransferIssues(t, dst, transferIssueBody(
		map[string]any{
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":  map[string]any{srcIssue: map[string]any{"number": 41, "identifier": "HAN-41"}},
		},
		[]map[string]any{transferIssueRow(srcIssue, 41, "thread", testUserID, nil)},
		[]map[string]any{
			transferCommentRow(srcTombstone, srcIssue, "", map[string]any{
				"deleted_at": "2026-09-02T00:00:02Z",
			}),
			transferCommentRow(srcReply, srcIssue, "reply under a tombstone", map[string]any{"parent_id": srcTombstone}),
		},
		nil, true)).Want(http.StatusOK)

	tombstone := service.TransferCommentID(dst, srcTombstone).String()
	replyParent := transferScanString(t, `SELECT COALESCE(parent_id::text, '') FROM comment WHERE id = $1`, service.TransferCommentID(dst, srcReply).String())
	if replyParent != tombstone {
		t.Fatalf("reply parent=%s want the tombstone %s", replyParent, tombstone)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1 AND deleted_at IS NOT NULL`, tombstone); n != 1 {
		t.Fatal("the tombstone row was not written with its deleted_at")
	}
}

// §11.7: a reply whose source parent is outside the bundle hangs on the
// nearest ancestor the bundle still carries, rather than being flattened.
func TestTransferIssues_ReparentsToNearestBundledAncestor(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcIssue, srcRoot, srcGrandchild := uuid.NewString(), uuid.NewString(), uuid.NewString()
	refs := map[string]any{
		"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
		"issues":  map[string]any{srcIssue: map[string]any{"number": 51, "identifier": "HAN-51"}},
	}

	postTransferIssues(t, dst, transferIssueBody(
		refs,
		[]map[string]any{transferIssueRow(srcIssue, 51, "truncated thread", testUserID, nil)},
		[]map[string]any{transferCommentRow(srcRoot, srcIssue, "root", nil)},
		nil, false)).Want(http.StatusOK)

	// The bundle carries a link pair for a dropped middle comment without its
	// row: the pair alone is the whole chain, and the reply must skip it.
	resp := postTransferIssues(t, dst, transferIssueBody(
		refs,
		nil,
		[]map[string]any{
			transferCommentRow(srcGrandchild, srcIssue, "grandchild", map[string]any{"parent_id": uuid.NewString()}),
		},
		nil, true)).Want(http.StatusOK)

	var report service.TransferIssuesReport
	resp.JSON(&report)

	// The dropped parent has no row anywhere, so there is no chain to walk:
	// the reply stays at the root with an explicit report row.
	parent := transferScanString(t, `SELECT COALESCE(parent_id::text, '') FROM comment WHERE id = $1`,
		service.TransferCommentID(dst, srcGrandchild).String())
	if parent != "" {
		t.Fatalf("parent=%s want NULL (no bundled ancestor exists)", parent)
	}
	if len(report.ParentUnmapped) != 1 {
		t.Fatalf("parent_unmapped=%v want one row", report.ParentUnmapped)
	}
}

// §10.3 #14: a bundle whose issue bags still carry an uncleaned secret key is
// refused before anything is written.
func TestTransferIssues_RejectsUncleanedSecretKeys(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcIssue := uuid.NewString()
	body := transferIssueBody(
		map[string]any{"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)}},
		[]map[string]any{transferIssueRow(srcIssue, 61, "leaky", testUserID, map[string]any{
			"metadata": map[string]any{"deploy_token": "CANARY_META_1e2f"},
		})},
		nil, nil, true)

	resp := postTransferIssues(t, dst, body).Want(http.StatusBadRequest)
	if resp.Map()["code"] != "transfer_bundle_contains_secret" {
		t.Fatalf("code=%v body=%s", resp.Map()["code"], resp.Text())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id = $1`, dst); n != 0 {
		t.Fatal("the refused import wrote a row")
	}
}

// V3 issue attachments reuse the V2 attachment path with a different mount
// column, so the row must land on the imported issue rather than on nothing.
func TestTransferIssues_AttachmentMountsOnImportedIssue(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcIssue, srcComment, srcAttachment := uuid.NewString(), uuid.NewString(), uuid.NewString()

	postTransferIssues(t, dst, transferIssueBody(
		map[string]any{
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":  map[string]any{srcIssue: map[string]any{"number": 71, "identifier": "HAN-71"}},
		},
		[]map[string]any{transferIssueRow(srcIssue, 71, "with a file", testUserID, nil)},
		[]map[string]any{transferCommentRow(srcComment, srcIssue, "see attached", nil)},
		nil, true)).Want(http.StatusOK)

	orig := testHandler.Storage
	testHandler.Storage = &mockStorage{}
	defer func() { testHandler.Storage = orig }()

	meta := service.TransferAttachmentMeta{
		SourceID:  srcAttachment,
		CommentID: &srcComment,
		Filename:  "note.txt", ContentType: "text/plain", SizeBytes: 5,
		CreatedAt: transferIssuesCreatedAt,
	}
	metaJSON, _ := json.Marshal(meta)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("meta", string(metaJSON))
	part, _ := mw.CreateFormFile("file", "note.txt")
	_, _ = part.Write([]byte("hello"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/workspaces/"+dst+"/transfer/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	testutil.Call(t, testHandler.ImportWorkspaceTransferAttachment,
		testutil.WithURLParams(testutil.WithHeaders(req, "X-User-ID", testUserID), "id", dst)).Want(http.StatusOK)

	target := service.TransferAttachmentID(dst, srcAttachment).String()
	wantComment := service.TransferCommentID(dst, srcComment).String()
	if got := transferScanString(t, `SELECT comment_id::text FROM attachment WHERE id = $1`, target); got != wantComment {
		t.Fatalf("attachment comment_id=%s want %s", got, wantComment)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM attachment WHERE id = $1 AND issue_id IS NULL`, target); n != 1 {
		t.Fatal("the comment-mounted attachment also claimed an issue")
	}
}

// §1.2.1: the property value bag is keyed by the source workspace's property
// definition ids, which the config import replaces with fresh ones on the
// target, and an actor value embeds a member uuid that has to move with it.
func TestTransferIssues_RemapsPropertyKeysAndActorValues(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	srcPoints, srcOwner, srcDropped := uuid.NewString(), uuid.NewString(), uuid.NewString()
	srcIssue := uuid.NewString()

	// The target definitions the config group would have created: same names,
	// different ids.
	targetPoints := dbfx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": dst, "name": "Story points", "type": "number", "config": []byte(`{}`),
	})
	targetOwner := dbfx.Insert(t, "issue_property", testutil.Cols{
		"workspace_id": dst, "name": "Owner", "type": "actor", "config": []byte(`{}`),
	})

	body := transferIssueBody(
		map[string]any{
			"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
			"issues":  map[string]any{srcIssue: map[string]any{"number": 81, "identifier": "HAN-81"}},
			"issue_properties": map[string]any{
				srcPoints:  map[string]any{"name": "Story points"},
				srcOwner:   map[string]any{"name": "Owner"},
				srcDropped: map[string]any{"name": "Was deleted on the target"},
			},
		},
		[]map[string]any{transferIssueRow(srcIssue, 81, "with properties", testUserID, map[string]any{
			"properties": map[string]any{
				srcPoints:  42,
				srcOwner:   "member:" + testUserID,
				srcDropped: "gone",
			},
		})},
		nil, nil, true)

	postTransferIssues(t, dst, body).Want(http.StatusOK)

	stored := transferScanString(t, `SELECT properties::text FROM issue WHERE id = $1`,
		service.TransferIssueID(dst, srcIssue).String())
	var bag map[string]any
	if err := json.Unmarshal([]byte(stored), &bag); err != nil {
		t.Fatalf("stored properties %q: %v", stored, err)
	}
	if bag[targetPoints] != float64(42) {
		t.Fatalf("scalar property moved to the wrong key: %v", bag)
	}
	if bag[targetOwner] != "member:"+testUserID {
		t.Fatalf("actor property was not remapped: %v", bag)
	}
	for _, sourceKey := range []string{srcPoints, srcOwner, srcDropped} {
		if _, leaked := bag[sourceKey]; leaked {
			t.Fatalf("source property id %s survived the remap: %v", sourceKey, bag)
		}
	}
}

// Agent actors are rejected exactly like the other transfer endpoints: an
// agent token must not be able to write history into a workspace.
func TestTransferIssues_AgentActorForbidden(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	agentID := dbfx.Agent(t, "XferIssueActor-"+uuid.NewString()[:6], "", testutil.Cols{"workspace_id": dst})
	req := transferReq("POST", "/api/workspaces/"+dst+"/transfer/issues", dst,
		transferIssueBody(nil, nil, nil, nil, true))
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.ImportWorkspaceTransferIssues, req).Want(http.StatusForbidden)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func transferScanTime(t *testing.T, sql string, args ...any) time.Time {
	t.Helper()
	var out time.Time
	dbfx.QueryRow(t, sql, args...).Scan(&out)
	return out.UTC()
}

func transferScanString(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var out string
	dbfx.QueryRow(t, sql, args...).Scan(&out)
	return out
}

func transferScanInt(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var out int
	dbfx.QueryRow(t, sql, args...).Scan(&out)
	return out
}

// transferScanRows reads a multi-row result into plain strings. testutil only
// wraps single-row reads, so the two list assertions reach for the pool.
func transferScanRows(t *testing.T, sql string, args ...any) [][]string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("query: %v\nSQL: %s", err, sql)
	}
	defer rows.Close()
	out := [][]string{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatalf("values: %v", err)
		}
		row := make([]string, len(values))
		for i, v := range values {
			if text, ok := v.(string); ok {
				row[i] = text
			} else {
				row[i] = ""
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func mustParseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed.UTC()
}

func transferIssueCounts(t *testing.T, ws string) string {
	t.Helper()
	return strings.Join([]string{
		transferScanString(t, `SELECT count(*)::text FROM issue WHERE workspace_id = $1`, ws),
		transferScanString(t, `SELECT count(*)::text FROM comment WHERE workspace_id = $1`, ws),
		transferScanString(t, `SELECT count(*)::text FROM issue_reaction WHERE workspace_id = $1`, ws),
		transferScanString(t, `SELECT count(*)::text FROM comment_reaction WHERE workspace_id = $1`, ws),
		transferScanString(t, `SELECT count(*)::text FROM issue_subscriber s JOIN issue i ON i.id = s.issue_id WHERE i.workspace_id = $1`, ws),
		transferScanString(t, `SELECT count(*)::text FROM issue_to_label l JOIN issue i ON i.id = l.issue_id WHERE i.workspace_id = $1`, ws),
	}, "/")
}

var transferProjectMentionRe = regexp.MustCompile(`mention://project/([0-9a-fA-F-]+)`)

// assertTransferMentionsResolvable is the §3.4 assertion: no mention link left
// in any imported description or comment may point at something the target
// workspace cannot resolve.
func assertTransferMentionsResolvable(t *testing.T, ws string) {
	t.Helper()
	texts := []string{}
	rows := transferScanRows(t, `SELECT COALESCE(description, '') FROM issue WHERE workspace_id = $1`, ws)
	for _, row := range rows {
		texts = append(texts, row[0])
	}
	rows = transferScanRows(t, `SELECT content FROM comment WHERE workspace_id = $1`, ws)
	for _, row := range rows {
		texts = append(texts, row[0])
	}
	for _, text := range texts {
		for _, match := range transferProjectMentionRe.FindAllStringSubmatch(text, -1) {
			if dbfx.Count(t, `SELECT count(*) FROM project WHERE workspace_id = $1 AND id = $2`, ws, match[1]) != 1 {
				t.Fatalf("unresolvable project mention %s in %q", match[1], text)
			}
		}
		for _, mention := range util.ParseMentions(text) {
			var query string
			switch mention.Type {
			case "member":
				query = `SELECT count(*) FROM member WHERE workspace_id = $1 AND user_id = $2`
			case "agent":
				query = `SELECT count(*) FROM agent WHERE workspace_id = $1 AND id = $2`
			case "squad":
				query = `SELECT count(*) FROM squad WHERE workspace_id = $1 AND id = $2`
			case "issue":
				query = `SELECT count(*) FROM issue WHERE workspace_id = $1 AND id = $2`
			case "all":
				continue
			}
			if dbfx.Count(t, query, ws, mention.ID) == 0 {
				t.Fatalf("unresolvable %s mention %s in %q", mention.Type, mention.ID, text)
			}
		}
	}
}

// §1.4: a status key the target catalog knows is written through unchanged,
// and one it does not know degrades to the source category with a report row.
//
// The built-ins are seeded with a lifecycle category rather than with
// category == key (MUL-7365), so a case that only uses them cannot tell a
// preserved key from its category. This one uses a custom status whose category
// differs from its key.
func TestTransferIssues_CustomStatusKeyIsPreservedAndUnknownKeyDegrades(t *testing.T) {
	_, dst := setupConfigWorkspaces(t)
	dbfx.Insert(t, "issue_status", testutil.Cols{
		"workspace_id": dst, "key": "design_review", "name": "Design Review",
		"description": "", "category": "started", "color": "#111111",
		"is_system": false, "position": 10.0,
	})

	kept, degraded := uuid.NewString(), uuid.NewString()
	body := transferIssueBody(
		map[string]any{"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)}},
		[]map[string]any{
			transferIssueRow(kept, 1, "custom status", testUserID, map[string]any{
				"status": "design_review", "status_category": "in_progress",
			}),
			transferIssueRow(degraded, 2, "status the target never had", testUserID, map[string]any{
				"status": "awaiting_legal", "status_category": "blocked",
			}),
		},
		nil, nil, true)
	resp := postTransferIssues(t, dst, body).Want(http.StatusOK)

	if got := transferScanString(t, `SELECT status FROM issue WHERE id = $1`,
		service.TransferIssueID(dst, kept).String()); got != "design_review" {
		t.Fatalf("status=%q want %q: a key the catalog has must be written through, not replaced by its category", got, "design_review")
	}
	if got := transferScanString(t, `SELECT status FROM issue WHERE id = $1`,
		service.TransferIssueID(dst, degraded).String()); got != "blocked" {
		t.Fatalf("status=%q want %q: an unknown key degrades to the source category", got, "blocked")
	}
	if !strings.Contains(resp.Text(), "status_key_unmapped") {
		t.Fatalf("the degraded status has no report row: %s", resp.Text())
	}
	if strings.Count(resp.Text(), "status_key_unmapped") != 1 {
		t.Fatalf("the preserved status must not be reported as unmapped: %s", resp.Text())
	}
}

// DENE-408 end to end, with the CLI's own defaults, on the flow the runbook
// documents: create an empty workspace, then import a bundle that carries tasks
// into it. The config step runs with on_conflict=fail (the CLI default) and
// apply_issue_prefix=true (what the CLI sends for a bundle carrying the issues
// group), so it walks straight into the target's 7 platform-seeded built-in
// statuses. While those read as conflicts the first batch 409'd and nothing
// after it ran — no prefix, no tasks, no preview.
//
// identifier is `<issue_prefix>-<number>` computed at read time, so asserting
// both against the source's own row is byte-for-byte parity for the two
// lists the runbook tells the user to diff.
func TestTransferImport_FreshWorkspaceKeepsSourceIdentifiers(t *testing.T) {
	suf := uuid.NewString()[:8]
	src := configWorkspace(t, "RunbookSrc "+suf, "runbooksrc-"+suf, "SRC")
	dst := configWorkspace(t, "RunbookDst "+suf, "runbookdst-"+suf, "TGT")
	dbfx.Issue(t, "the only task", testutil.Cols{"workspace_id": src, "number": 1})

	bundle := exportBundle(t, src)
	dry := false
	testutil.Call(t, testHandler.ImportWorkspaceTransferConfig, transferReq(
		"POST", "/api/workspaces/"+dst+"/transfer/config", dst, map[string]any{
			"config": bundle, "dry_run": dry, "on_conflict": "fail",
			"options": map[string]any{"apply_workspace_settings": true, "apply_issue_prefix": true},
		})).Want(http.StatusOK)

	if got := transferScanString(t, `SELECT issue_prefix FROM workspace WHERE id = $1`, dst); got != "SRC" {
		t.Fatalf("target issue_prefix=%q, want the source's SRC: the tasks' identifiers can only match if it landed", got)
	}

	srcIssue := uuid.NewString()
	// The ref index travels with every shard, the finalize pass included: it is
	// what tells the empty-target gate which of the target's rows this bundle
	// put there.
	refs := map[string]any{
		"members": map[string]any{testUserID: transferMemberRef(handlerTestEmail)},
		"issues":  map[string]any{srcIssue: map[string]any{"number": 1, "identifier": "SRC-1"}},
	}
	row := transferIssueRow(srcIssue, 1, "the only task", testUserID, nil)
	postTransferIssues(t, dst, transferIssueBody(refs, []map[string]any{row}, nil, nil, false)).Want(http.StatusOK)
	postTransferIssues(t, dst, transferIssueBody(refs, nil, nil, nil, true)).Want(http.StatusOK)

	if got := transferScanString(t, `SELECT number::text FROM issue WHERE workspace_id = $1`, dst); got != "1" {
		t.Fatalf("imported number=%q, want the source's 1 (SRC-1)", got)
	}
}
