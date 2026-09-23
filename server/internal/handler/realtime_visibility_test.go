package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The delivery-time filter, exercised against real fixtures. The end-to-end
// proof lives in cmd/server (a real socket receives or does not receive a real
// broadcast); these pin the classification rules that are cheaper to state
// here than to reach through a socket: which events are judged, which are
// exempt, and what an unanswerable question answers.

func filterFrame(t *testing.T, frame map[string]any) realtime.BroadcastDecision {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return testHandler.FilterRealtimeBroadcast(context.Background(), realtime.ScopeWorkspace, testWorkspaceID, raw)
}

// filterRemoteIssueFixture is an issue created by a member, private by
// default, with both ids kept so a test can ask about the creator as well as
// about everyone else.
type filterIssueFixture struct {
	issueID string
	userID  string
}

func privateIssueFixture(t *testing.T, label string) filterIssueFixture {
	t.Helper()
	userID := dbfx.User(t, label, strings.ToLower(strings.ReplaceAll(label, " ", "-"))+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, userID, "member")
	var created struct {
		ID         string `json:"id"`
		Visibility string `json:"visibility"`
	}
	testutil.Call(t, testHandler.CreateIssue,
		newRequestAs(userID, "POST", "/api/issues", map[string]any{"title": label})).
		Want(201).JSON(&created)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})
	if created.Visibility != "private" {
		t.Fatalf("fixture issue is %q, want private", created.Visibility)
	}
	return filterIssueFixture{issueID: created.ID, userID: userID}
}

func filterRemoteMember(t *testing.T, label string) string {
	t.Helper()
	userID := dbfx.User(t, label, strings.ToLower(strings.ReplaceAll(label, " ", "-"))+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, userID, "member")
	return userID
}

// A private issue's satellite frames are judged by the issue's own scope, not
// by the shape of the event: an inbox item, a comment and a bare id all name
// the same resource.
func TestFilterRealtimeBroadcastJudgesEveryIssueScopedShape(t *testing.T) {
	requireDB(t)
	author := privateIssueFixture(t, "Filter Shapes")
	stranger := filterRemoteMember(t, "Filter Stranger")

	frames := map[string]map[string]any{
		"issue:created":   {"type": "issue:created", "payload": map[string]any{"issue": map[string]any{"id": author.issueID}}},
		"issue:updated":   {"type": "issue:updated", "payload": map[string]any{"issue": map[string]any{"id": author.issueID}}},
		"comment:created": {"type": "comment:created", "payload": map[string]any{"comment": map[string]any{"id": "c1", "issue_id": author.issueID, "content": "secret"}}},
		"comment:deleted": {"type": "comment:deleted", "payload": map[string]any{"comment_id": "c1", "issue_id": author.issueID}},
		"subscriber":      {"type": "subscriber:added", "payload": map[string]any{"issue_id": author.issueID, "user_type": "member"}},
		"activity":        {"type": "activity:created", "payload": map[string]any{"issue_id": author.issueID, "entry": map[string]any{"id": "a1"}}},
		"pin":             {"type": "pin:created", "payload": map[string]any{"pin": map[string]any{"item_type": "issue", "item_id": author.issueID}}},
		"pin_deleted":     {"type": "pin:deleted", "payload": map[string]any{"item_type": "issue", "item_id": author.issueID}},
	}

	for name, frame := range frames {
		decision := filterFrame(t, frame)
		if decision == nil {
			t.Fatalf("%s: no decision for a frame that names an issue", name)
		}
		if _, ok := decision(stranger); ok {
			t.Fatalf("%s: delivered to a member the issue was never shared with", name)
		}
		if _, ok := decision(author.userID); !ok {
			t.Fatalf("%s: suppressed for the issue's creator", name)
		}
	}
}

// The id-only frames are exempt on purpose: they carry nothing but ids, and
// the people who need them are exactly the people a content filter excludes.
func TestFilterRealtimeBroadcastLetsIdOnlyFramesThrough(t *testing.T) {
	requireDB(t)
	issue := privateIssueFixture(t, "Filter Id Only")
	stranger := filterRemoteMember(t, "Filter Bystander")

	for _, frame := range []map[string]any{
		{"type": "issue:invalidated", "payload": map[string]any{"issue_id": issue.issueID}},
		{"type": "issue:invalidated", "payload": map[string]any{"project_id": "00000000-0000-0000-0000-0000000000aa"}},
		{"type": "issue:deleted", "payload": map[string]any{"issue_id": issue.issueID}},
	} {
		decision := filterFrame(t, frame)
		if decision == nil {
			continue
		}
		if _, ok := decision(stranger); !ok {
			t.Fatalf("%v: id-only frame was suppressed for %s", frame["type"], stranger)
		}
	}
}

// An issue that cannot be loaded has no viewers: the same fail-closed answer
// the HTTP reads give (404 for everyone), rather than a frame delivered to a
// workspace because the lookup failed.
func TestFilterRealtimeBroadcastFailsClosedOnUnknownIssue(t *testing.T) {
	requireDB(t)
	member := filterRemoteMember(t, "Filter Missing")

	decision := filterFrame(t, map[string]any{
		"type":    "issue:updated",
		"payload": map[string]any{"issue": map[string]any{"id": "00000000-0000-0000-0000-0000000000ff"}},
	})
	if decision == nil {
		t.Fatal("unknown issue produced no decision")
	}
	if _, ok := decision(member); ok {
		t.Fatal("a frame naming an unreadable issue was delivered anyway")
	}
}

// A personal inbox item is judged by the issue it points at, and an item with
// no issue is not issue content.
func TestFilterRealtimeBroadcastJudgesInboxItemsByTheirIssue(t *testing.T) {
	requireDB(t)
	issue := privateIssueFixture(t, "Filter Inbox")
	stranger := filterRemoteMember(t, "Filter Inbox Stranger")

	decision := filterFrame(t, map[string]any{
		"type": "inbox:new",
		"payload": map[string]any{"item": map[string]any{
			"id": "i1", "issue_id": issue.issueID, "title": "you were mentioned",
		}},
	})
	if decision == nil {
		t.Fatal("inbox item naming an issue produced no decision")
	}
	if _, ok := decision(stranger); ok {
		t.Fatal("an inbox item for an unshared issue was delivered")
	}
	if _, ok := decision(issue.userID); !ok {
		t.Fatal("an inbox item for the creator's own issue was suppressed")
	}

	// No issue on the item (a quota notice, say) — nothing to judge.
	decision = filterFrame(t, map[string]any{
		"type":    "inbox:new",
		"payload": map[string]any{"item": map[string]any{"id": "i2", "title": "quota"}},
	})
	if decision == nil {
		return
	}
	if _, ok := decision(stranger); !ok {
		t.Fatal("an inbox item with no issue was suppressed")
	}
}

// Frames this server did not produce, and events with no issue scope, are not
// the filter's business: silence means "deliver unchanged".
func TestFilterRealtimeBroadcastIgnoresUnrelatedFrames(t *testing.T) {
	requireDB(t)
	for name, raw := range map[string][]byte{
		"empty":          nil,
		"not_json":       []byte("not a frame"),
		"no_type":        []byte(`{"payload":{"issue_id":"x"}}`),
		"member_added":   []byte(`{"type":"member:added","payload":{"member_id":"m1"}}`),
		"agent_status":   []byte(`{"type":"agent:status","payload":{"agent_id":"a1"}}`),
		"daemon_frame":   []byte(`{"type":"daemon:heartbeat","payload":{}}`),
		"issue_no_id":    []byte(`{"type":"issue:updated","payload":{"issue":{}}}`),
		"unparsable_id":  []byte(`{"type":"issue:updated","payload":{"issue_id":"not-a-uuid"}}`),
		"deleted_absent": []byte(`{"type":"issue:deleted","payload":{"issue_id":"not-a-uuid"}}`),
	} {
		if decision := testHandler.FilterRealtimeBroadcast(context.Background(), realtime.ScopeWorkspace, testWorkspaceID, raw); decision != nil {
			t.Fatalf("%s: expected no decision, got one", name)
		}
	}
	// The workspace snapshot is narrowed for the workspace room only.
	if decision := testHandler.FilterRealtimeBroadcast(context.Background(), realtime.ScopeUser, testUserID,
		[]byte(`{"type":"workspace:updated","payload":{"workspace":{"id":"00000000-0000-0000-0000-0000000000bb","repos":[{"url":"https://example.com/a.git","visibility":"workspace"}]}}}`)); decision != nil {
		t.Fatal("workspace snapshot was narrowed on the user scope")
	}
}
