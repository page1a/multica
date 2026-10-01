package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// originIssue inserts a ticket on the workspace's own counter, so tickets the
// handler creates afterwards do not collide with its number.
func originIssue(t *testing.T, title string) string {
	t.Helper()
	var number int
	dbfx.QueryRow(t, `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter`, testWorkspaceID).Scan(&number)
	return dbfx.Issue(t, title, testutil.Cols{"number": number})
}

// originFixture is one agent mid-run for a person: the seat that "names" an
// executor, the seat it names, and the run's trigger comment.
type originFixture struct {
	caller string // agent doing the naming
	target string // agent being named
	name   string // target's name, what a real quote has to contain
	task   string
}

// originRun seeds a caller agent running a task that a person started.
// trigger is the message that started it, written by author (a member id, or
// "" for an agent-authored message).
func originRun(t *testing.T, suffix, trigger, authorType, authorID, originator string) originFixture {
	t.Helper()
	f := originFixture{
		name: "Origin Target " + suffix,
	}
	f.caller = createHandlerTestAgent(t, "Origin Caller "+suffix, []byte("[]"))
	f.target = createHandlerTestAgent(t, f.name, []byte("[]"))
	home := originIssue(t, "origin home "+suffix)
	comment := dbfx.Comment(t, home, trigger, testutil.Cols{"author_type": authorType, "author_id": authorID})
	f.task = dbfx.Task(t, f.caller, testutil.Cols{
		"runtime_id":          handlerTestRuntimeID(t),
		"status":              "running",
		"issue_id":            home,
		"started_at":          testutil.Raw("now()"),
		"trigger_comment_id":  comment,
		"originator_user_id":  originator,
		"accountable_user_id": originator,
		"originator_source":   "direct_human",
	})
	return f
}

// agentCreate has the caller agent create a todo ticket naming the target,
// with routing's async pass kept off so the write itself is what is asserted.
func agentCreate(t *testing.T, f originFixture, title, quote string) map[string]any {
	t.Helper()
	body := map[string]any{
		"title":         title,
		"status":        "todo",
		"assignee_type": "agent",
		"assignee_id":   f.target,
	}
	if quote != "" {
		body["assignee_quote"] = quote
	}
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id, _ := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
	return resp
}

func originOf(t *testing.T, issueID string) (assignee, source, user, quote *string) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT assignee_id::text, assignee_source, assignee_source_user_id::text, assignee_quote
		FROM issue WHERE id = $1`, issueID).Scan(&assignee, &source, &user, &quote)
	return
}

func TestAgentPickWithoutAQuoteIsDroppedWhenRoutingIsOn(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "noquote", "请帮我看看这个", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "agent picks alone", "")
	if resp["assignee_id"] != nil {
		t.Fatalf("assignee_id = %v, the agent's own pick must not be written", resp["assignee_id"])
	}
	if resp["assignee_ignored"] != true {
		t.Fatalf("assignee_ignored = %v, the caller has to be told", resp["assignee_ignored"])
	}
	_, source, _, _ := originOf(t, resp["id"].(string))
	if source == nil || *source != "agent" {
		t.Fatalf("assignee_source = %v, want agent", source)
	}
}

func TestQuotedPickFromTheInitiatorIsKeptAndCredited(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	name := "Origin Target quoted"
	f := originRun(t, "quoted", "这张交给 "+name+" 做，别的不用管", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "user said it", "交给 "+name+" 做")
	assignee, source, user, quote := originOf(t, resp["id"].(string))
	if assignee == nil || *assignee != f.target {
		t.Fatalf("assignee = %v, want %s", assignee, f.target)
	}
	if source == nil || *source != "quote" || user == nil || *user != testUserID {
		t.Fatalf("source = %v user = %v, want quote by the initiator", source, user)
	}
	if quote == nil || *quote != "交给 "+name+" 做" {
		t.Fatalf("assignee_quote = %v", quote)
	}
	if resp["assignee_ignored"] == true {
		t.Fatal("a kept pick must not report assignee_ignored")
	}
}

func TestQuotedPickWorksForAnyMemberWhoStartedTheRun(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	other := dbfx.User(t, "Origin Other Member", "origin-other@example.com")
	dbfx.Member(t, testWorkspaceID, other, "member")
	name := "Origin Target other"
	f := originRun(t, "other", "让 "+name+" 来", "member", other, other)

	resp := agentCreate(t, f, "another person said it", name)
	_, source, user, _ := originOf(t, resp["id"].(string))
	if source == nil || *source != "quote" || user == nil || *user != other {
		t.Fatalf("source = %v user = %v, want quote by the other member", source, user)
	}
}

func TestQuoteFromSomeoneElseOrAnAgentDoesNotCount(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	bystander := dbfx.User(t, "Origin Bystander", "origin-bystander@example.com")
	dbfx.Member(t, testWorkspaceID, bystander, "member")

	cases := []struct {
		name, authorType, author string
	}{
		{"third party wrote the trigger", "member", bystander},
		{"another agent wrote the trigger", "agent", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			author := tc.author
			if tc.authorType == "agent" {
				author = createHandlerTestAgent(t, "Origin Relay "+tc.name, []byte("[]"))
			}
			target := "Origin Target " + tc.name
			f := originRun(t, tc.name, "交给 "+target, tc.authorType, author, testUserID)
			resp := agentCreate(t, f, "relayed "+tc.name, target)
			if resp["assignee_id"] != nil || resp["assignee_ignored"] != true {
				t.Fatalf("assignee_id = %v ignored = %v, a relayed quote must be dropped", resp["assignee_id"], resp["assignee_ignored"])
			}
		})
	}
}

func TestFabricatedQuoteIsDowngradedToTheAgentsOwnPick(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "fabricated", "只是随口一说，没点谁的名", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "made up quote", "用户说过：交给 "+f.name)
	if resp["assignee_id"] != nil || resp["assignee_ignored"] != true {
		t.Fatalf("assignee_id = %v ignored = %v, an invented quote must not stick", resp["assignee_id"], resp["assignee_ignored"])
	}
	_, source, _, quote := originOf(t, resp["id"].(string))
	if source == nil || *source != "agent" || quote != nil {
		t.Fatalf("source = %v quote = %v, want agent with no quote", source, quote)
	}
}

func TestQuoteThatDoesNotNameTheAssigneeIsRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "medium", confidence: 0.95})
	f := originRun(t, "unnamed", "用最强的那个做", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "quote names nobody", "用最强的那个做")
	if resp["assignee_id"] != nil || resp["assignee_ignored"] != true {
		t.Fatalf("assignee_id = %v ignored = %v", resp["assignee_id"], resp["assignee_ignored"])
	}
}

func TestAgentPickStaysWhenRoutingIsOff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := originRun(t, "off", "随便", "member", testUserID, testUserID)

	resp := agentCreate(t, f, "no routing here", "")
	if resp["assignee_id"] != f.target {
		t.Fatalf("assignee_id = %v, with routing off nothing else would fill the slot", resp["assignee_id"])
	}
	if resp["assignee_ignored"] == true {
		t.Fatal("nothing was ignored")
	}
}

func TestMemberPickIsHumanAndUntouchedByRouting(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	enableDraftSuggestRouting(t, tierJudge{tier: "weak", confidence: 0.95})
	target := createHandlerTestAgent(t, "Origin Human Target", []byte("[]"))

	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         "picked on the web",
		"status":        "todo",
		"assignee_type": "agent",
		"assignee_id":   target,
	})
	req = req.WithContext(withSkipIssueRouting(req.Context()))
	resp := testutil.Call(t, testHandler.CreateIssue, req).Want(http.StatusCreated).Map()
	id := resp["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, id) })

	assignee, source, user, _ := originOf(t, id)
	if assignee == nil || *assignee != target || source == nil || *source != "human" || user == nil || *user != testUserID {
		t.Fatalf("assignee = %v source = %v user = %v, want a human pick by the caller", assignee, source, user)
	}
}

func TestAgentAttachedLabelIsRecordedAsAgentAttached(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	f := originRun(t, "label", "随便", "member", testUserID, testUserID)
	issue := originIssue(t, "label carrier")
	label := dbfx.Insert(t, "issue_label", testutil.Cols{"workspace_id": testWorkspaceID, "name": "origin-tier-label", "color": "#000000"})

	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issue+"/labels", map[string]any{"label_id": label}), "id", issue)
	req.Header.Set("X-Agent-ID", f.caller)
	req.Header.Set("X-Task-ID", f.task)
	testutil.Call(t, testHandler.AttachLabel, req).Want(http.StatusOK)

	var by string
	dbfx.QueryRow(t, `SELECT attached_by_type FROM issue_to_label WHERE issue_id = $1 AND label_id = $2`, issue, label).Scan(&by)
	if by != "agent" {
		t.Fatalf("attached_by_type = %q, want agent", by)
	}
}

func TestEscalateTakesAReasonAndNothingElse(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	issue := originIssue(t, "escalate shape")
	for name, body := range map[string]map[string]any{
		"no reason":         {},
		"blank reason":      {"reason": "   "},
		"names an assignee": {"reason": "太难了", "assignee_id": "x"},
		"names a tier":      {"reason": "太难了", "tier": "strongest"},
	} {
		t.Run(name, func(t *testing.T) {
			req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issue+"/escalate", body), "id", issue)
			testutil.Call(t, testHandler.EscalateIssue, req).Want(http.StatusBadRequest)
		})
	}
}
