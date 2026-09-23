package main

// DENE-717 acceptance: realtime delivery must be filtered per recipient.
//
// Every test here drives the real HTTP write path, the real event bus, the
// real listener registration (including the production filter wiring) and a
// real WebSocket connection. Nothing in this file calls the filter directly —
// the point of the ticket is what one member's socket receives while another
// member writes, and only an actual socket can answer that.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---- websocket client -----------------------------------------------------

// visWS is a connected browser-shaped client: a background reader turns frames
// into channel receives so a test can both wait for a specific event and
// assert absence over a window. Reading inline would not work — a gorilla read
// that times out is permanent, so "nothing arrived" could not be followed by
// "now something arrived".
type visWS struct {
	t      *testing.T
	conn   *websocket.Conn
	frames chan map[string]any
	once   sync.Once
}

func visDial(t *testing.T, token string) *visWS {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws?workspace_id=" + testWorkspaceID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]any{
		"type":    "auth",
		"payload": map[string]string{"token": token},
	})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		conn.Close()
		t.Fatalf("send auth frame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, ack, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		t.Fatalf("read auth ack: %v", err)
	}
	if !strings.Contains(string(ack), "auth_ack") {
		conn.Close()
		t.Fatalf("expected auth_ack, got %s", ack)
	}
	_ = conn.SetReadDeadline(time.Time{})

	client := &visWS{t: t, conn: conn, frames: make(chan map[string]any, 512)}
	go client.read()
	t.Cleanup(client.close)
	// The hub registers the connection asynchronously; a write that raced the
	// register would look like a delivery bug.
	time.Sleep(150 * time.Millisecond)
	return client
}

func (c *visWS) read() {
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			close(c.frames)
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		select {
		case c.frames <- frame:
		default:
		}
	}
}

func (c *visWS) close() { c.once.Do(func() { _ = c.conn.Close() }) }

// waitForType returns the first frame of the wanted type, discarding unrelated
// ones. It fails the test on timeout so a missing delivery cannot be mistaken
// for a passing "no content" assertion.
func (c *visWS) waitForType(want string, timeout time.Duration) map[string]any {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				c.t.Fatalf("websocket closed while waiting for %q", want)
			}
			if frame["type"] == want {
				return frame
			}
		case <-deadline:
			c.t.Fatalf("timed out after %s waiting for %q", timeout, want)
		}
	}
}

// collectFor gathers every frame that arrives inside the window.
func (c *visWS) collectFor(window time.Duration) []map[string]any {
	c.t.Helper()
	var out []map[string]any
	deadline := time.After(window)
	for {
		select {
		case frame, ok := <-c.frames:
			if !ok {
				return out
			}
			out = append(out, frame)
		case <-deadline:
			return out
		}
	}
}

// assertNoLeak is the assertion the whole ticket turns on: over the window, no
// frame may carry this issue's content. Two frame types are allowed to name
// the issue by id — the deletion frame and the invalidation frame — because
// they carry nothing but ids; both are checked for content anyway.
func (c *visWS) assertNoLeak(issueID, title string, window time.Duration) {
	c.t.Helper()
	for _, frame := range c.collectFor(window) {
		if why := visFrameLeaks(frame, issueID, title); why != "" {
			raw, _ := json.Marshal(frame)
			c.t.Fatalf("recipient received %s: %s", why, raw)
		}
	}
}

func visFrameLeaks(frame map[string]any, issueID, title string) string {
	raw, err := json.Marshal(frame)
	if err != nil {
		return ""
	}
	text := string(raw)
	if title != "" && strings.Contains(text, title) {
		return "the issue title"
	}
	if issueID != "" && strings.Contains(text, issueID) {
		switch frame["type"] {
		case "issue:invalidated", "issue:deleted":
			return ""
		}
		return "an issue reference"
	}
	return ""
}

// ---- http helpers ---------------------------------------------------------

func visDo(t *testing.T, token, method, path string, body any) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, testServer.URL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, raw)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ---- fixtures -------------------------------------------------------------

// visUser seeds a member of the shared integration workspace with the given
// tier and mints a JWT for them, which is what makes them a legitimate
// WebSocket recipient rather than a stand-in.
func visUser(t *testing.T, label, role string) (userID, token string) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("dene717-%s-%d@multica.test", label, time.Now().UnixNano())
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		label, email,
	).Scan(&userID); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
	t.Cleanup(func() {
		// Issues carry the creator id without a foreign key, so this cannot
		// fail on rows the test wrote.
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	if _, err := testPool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
		testWorkspaceID, userID, role,
	); err != nil {
		t.Fatalf("seed member %s: %v", label, err)
	}
	token, err := generateTestJWT(userID, email, label)
	if err != nil {
		t.Fatalf("jwt for %s: %v", label, err)
	}
	return userID, token
}

func visIssueTitle(label string) string {
	return fmt.Sprintf("dene717 %s %d", label, time.Now().UnixNano())
}

func visCreateIssue(t *testing.T, token, projectID, title string, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"title": title, "status": "todo"}
	if projectID != "" {
		body["project_id"] = projectID
	}
	for k, v := range extra {
		body[k] = v
	}
	issue := visDo(t, token, "POST", "/api/issues?workspace_id="+testWorkspaceID, body)
	if issue == nil || issue["id"] == nil {
		t.Fatalf("create issue %q returned no id", title)
	}
	return issue
}

func visSetIssueVisibility(t *testing.T, token, issueID, visibility string) {
	t.Helper()
	visDo(t, token, "PUT", "/api/issues/"+issueID+"/visibility", map[string]any{"visibility": visibility})
}

func visCreateProject(t *testing.T, token, title string) string {
	t.Helper()
	project := visDo(t, token, "POST", "/api/projects?workspace_id="+testWorkspaceID, map[string]any{"title": title})
	if project == nil || project["id"] == nil {
		t.Fatalf("create project %q returned no id", title)
	}
	return project["id"].(string)
}

func visReposFromFrame(t *testing.T, frame map[string]any) []string {
	t.Helper()
	payload, _ := frame["payload"].(map[string]any)
	workspace, _ := payload["workspace"].(map[string]any)
	list, _ := workspace["repos"].([]any)
	urls := make([]string, 0, len(list))
	for _, entry := range list {
		repo, _ := entry.(map[string]any)
		if url, _ := repo["url"].(string); url != "" {
			urls = append(urls, url)
		}
	}
	return urls
}

// ---- the acceptance criteria ---------------------------------------------

// A private issue is its creator's alone: another member's socket must never
// receive its title, body or comments — while the creator's socket keeps
// working exactly as before.
func TestRealtimePrivateIssueNeverReachesAnotherMember(t *testing.T) {
	_, authorToken := visUser(t, "private-author", "member")
	_, watcherToken := visUser(t, "private-watcher", "member")
	author := visDial(t, authorToken)
	watcher := visDial(t, watcherToken)

	title := visIssueTitle("private-title")
	issue := visCreateIssue(t, authorToken, "", title, nil)
	issueID := issue["id"].(string)
	// The author is the only viewer, so the author's socket is the control:
	// a filter that dropped everything would pass the watcher assertion below
	// and fail here.
	created := author.waitForType("issue:created", 5*time.Second)
	if !strings.Contains(mustJSON(t, created), title) {
		t.Fatalf("author's issue:created frame lost the title: %s", mustJSON(t, created))
	}
	watcher.assertNoLeak(issueID, title, 800*time.Millisecond)

	// Edit.
	visDo(t, authorToken, "PUT", "/api/issues/"+issueID, map[string]any{"title": title + " edited"})
	author.waitForType("issue:updated", 5*time.Second)
	watcher.assertNoLeak(issueID, title, 800*time.Millisecond)

	// Comment.
	body := "private comment body " + title
	visDo(t, authorToken, "POST", "/api/issues/"+issueID+"/comments", map[string]any{"content": body})
	comment := author.waitForType("comment:created", 5*time.Second)
	if !strings.Contains(mustJSON(t, comment), body) {
		t.Fatalf("author's comment:created frame lost the body: %s", mustJSON(t, comment))
	}
	watcher.assertNoLeak(issueID, body, 800*time.Millisecond)
}

// A guest is a member for reachability and not a member for sharing: project
// scope reaches them once they join the project, workspace scope never does.
func TestRealtimeGuestSeesProjectScopeOnlyAfterJoining(t *testing.T) {
	guestID, guestToken := visUser(t, "scope-guest", "guest")
	guest := visDial(t, guestToken)

	projectID := visCreateProject(t, testToken, "dene717 guest project "+visIssueTitle("p"))

	// A workspace-scope issue is off limits to a guest, no matter the project.
	wsTitle := visIssueTitle("workspace-scope")
	wsIssue := visCreateIssue(t, testToken, "", wsTitle, nil)
	visSetIssueVisibility(t, testToken, wsIssue["id"].(string), "workspace")
	guest.waitForType("issue:invalidated", 5*time.Second)
	guest.assertNoLeak(wsIssue["id"].(string), wsTitle, 800*time.Millisecond)

	// A project-scope issue is not visible until the guest is in the project.
	projectTitle := visIssueTitle("project-scope")
	projectIssue := visCreateIssue(t, testToken, projectID, projectTitle, nil)
	visSetIssueVisibility(t, testToken, projectIssue["id"].(string), "project")
	guest.assertNoLeak(projectIssue["id"].(string), projectTitle, 800*time.Millisecond)

	visDo(t, testToken, "POST", "/api/projects/"+projectID+"/members", map[string]any{"member_id": guestID})
	invalidation := guest.waitForType("issue:invalidated", 5*time.Second)
	payload, _ := invalidation["payload"].(map[string]any)
	if payload["project_id"] != projectID {
		t.Fatalf("membership invalidation names %v, want project %s", payload, projectID)
	}

	// From now on the project's issues are live for the guest.
	liveTitle := visIssueTitle("project-live")
	live := visCreateIssue(t, testToken, projectID, liveTitle, nil)
	visSetIssueVisibility(t, testToken, live["id"].(string), "project")
	delivered := guest.waitForType("issue:updated", 5*time.Second)
	if !strings.Contains(mustJSON(t, delivered), liveTitle) {
		t.Fatalf("guest's project-scope update lost the title: %s", mustJSON(t, delivered))
	}
}

// workspace:updated carries the repo registry, and the registry has to be
// narrowed to what each recipient may see — one payload, many answers.
func TestRealtimeWorkspaceReposAreNarrowedPerRecipient(t *testing.T) {
	ctx := context.Background()
	var stored []byte
	if err := testPool.QueryRow(ctx, `SELECT repos FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&stored); err != nil {
		t.Fatalf("read workspace repos: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(),
			`UPDATE workspace SET repos = $1::jsonb WHERE id = $2`, string(stored), testWorkspaceID)
	})

	publicURL := fmt.Sprintf("https://github.com/multica-test/public-%d.git", time.Now().UnixNano())
	secretURL := fmt.Sprintf("https://github.com/multica-test/secret-%d.git", time.Now().UnixNano())
	visDo(t, testToken, "PUT", "/api/workspaces/"+testWorkspaceID, map[string]any{
		"repos": []map[string]any{{"url": publicURL}, {"url": secretURL}},
	})

	_, memberToken := visUser(t, "repo-member", "member")
	member := visDial(t, memberToken)
	owner := visDial(t, testToken)

	// Re-scoping one repo publishes the workspace snapshot, which is the event
	// a client needs to see its repo list change.
	visDo(t, testToken, "PUT", "/api/repos/visibility", map[string]any{
		"url": publicURL, "visibility": "workspace",
	})

	memberRepos := visReposFromFrame(t, member.waitForType("workspace:updated", 5*time.Second))
	if !containsString(memberRepos, publicURL) {
		t.Fatalf("member repos = %v, want the workspace-scope repo %s", memberRepos, publicURL)
	}
	if containsString(memberRepos, secretURL) {
		t.Fatalf("member repos = %v leaked the private repo %s", memberRepos, secretURL)
	}
	ownerRepos := visReposFromFrame(t, owner.waitForType("workspace:updated", 5*time.Second))
	if !containsString(ownerRepos, publicURL) || !containsString(ownerRepos, secretURL) {
		t.Fatalf("owner repos = %v, want both repos (owner sees its own private repo)", ownerRepos)
	}
}

// Tightening an issue's scope has to reach the people who just lost access —
// with ids only, because that frame is the one the content filter excludes —
// and must not leave them holding a rendered copy.
func TestRealtimeScopeTighteningInvalidatesTheMemberWhoLostAccess(t *testing.T) {
	_, authorToken := visUser(t, "tighten-author", "member")
	_, memberToken := visUser(t, "tighten-member", "member")
	member := visDial(t, memberToken)
	author := visDial(t, authorToken)

	title := visIssueTitle("tighten")
	issue := visCreateIssue(t, authorToken, "", title, nil)
	issueID := issue["id"].(string)
	visSetIssueVisibility(t, authorToken, issueID, "workspace")
	// The member gained access, so the same write delivers content to them.
	if got := author.waitForType("issue:updated", 5*time.Second); got["type"] != "issue:updated" {
		t.Fatalf("author frame = %v", got)
	}
	delivered := member.waitForType("issue:updated", 5*time.Second)
	if !strings.Contains(mustJSON(t, delivered), title) {
		t.Fatalf("member's widened frame lost the title: %s", mustJSON(t, delivered))
	}

	visSetIssueVisibility(t, authorToken, issueID, "private")

	invalidation := member.waitForType("issue:invalidated", 5*time.Second)
	payload, _ := invalidation["payload"].(map[string]any)
	if payload["issue_id"] != issueID {
		t.Fatalf("invalidation payload = %v, want issue_id %s", payload, issueID)
	}
	if _, leaked := payload["issue"]; leaked {
		t.Fatalf("invalidation frame carried content: %v", payload)
	}
	// Nothing after the invalidation may carry the issue — the narrowed
	// update is filtered for this recipient.
	member.assertNoLeak(issueID, title, 800*time.Millisecond)
	// ...and the author, who never lost access, still gets the content.
	authorUpdate := author.waitForType("issue:updated", 5*time.Second)
	if !strings.Contains(mustJSON(t, authorUpdate), title) {
		t.Fatalf("author's update lost the title: %s", mustJSON(t, authorUpdate))
	}
}

// Joining and leaving a project is a sharing change for one person, so the
// invalidation is addressed to that person and not broadcast to the workspace.
func TestRealtimeProjectMembershipChangeInvalidatesOnlyTheMember(t *testing.T) {
	projectID := visCreateProject(t, testToken, "dene717 membership "+visIssueTitle("p"))
	title := visIssueTitle("membership-issue")
	issue := visCreateIssue(t, testToken, projectID, title, nil)
	issueID := issue["id"].(string)
	visSetIssueVisibility(t, testToken, issueID, "project")

	memberID, memberToken := visUser(t, "membership-member", "member")
	_, bystanderToken := visUser(t, "membership-bystander", "member")
	member := visDial(t, memberToken)
	bystander := visDial(t, bystanderToken)
	if visListHasIssue(t, memberToken, issueID) {
		t.Fatal("a non-member's list already held the project's issue")
	}

	visDo(t, testToken, "POST", "/api/projects/"+projectID+"/members", map[string]any{"member_id": memberID})
	joined := member.waitForType("issue:invalidated", 5*time.Second)
	if payload, _ := joined["payload"].(map[string]any); payload["project_id"] != projectID {
		t.Fatalf("join invalidation payload = %v, want project_id %s", payload, projectID)
	}
	// The frame is what tells the tab to refetch; the list is what it finds.
	if !visListHasIssue(t, memberToken, issueID) {
		t.Fatal("member's list omits the issue after joining the project")
	}
	for _, frame := range bystander.collectFor(600 * time.Millisecond) {
		if frame["type"] == "issue:invalidated" {
			t.Fatalf("membership invalidation reached a bystander: %s", mustJSON(t, frame))
		}
	}

	visDo(t, testToken, "DELETE", "/api/projects/"+projectID+"/members/"+memberID, nil)
	removed := member.waitForType("issue:invalidated", 5*time.Second)
	if payload, _ := removed["payload"].(map[string]any); payload["project_id"] != projectID {
		t.Fatalf("removal invalidation payload = %v, want project_id %s", payload, projectID)
	}
	if visListHasIssue(t, memberToken, issueID) {
		t.Fatal("member's list still holds the project's issue after being removed")
	}
	member.assertNoLeak(issueID, title, 800*time.Millisecond)
}

// A member moved off an issue loses the reason they could see it, and the
// content frame that records the change is filtered for exactly them — so the
// id-only frame is what tells their open tab to drop it.
func TestRealtimeUnassignInvalidatesFormerAssignee(t *testing.T) {
	_, authorToken := visUser(t, "unassign-author", "member")
	assigneeID, assigneeToken := visUser(t, "unassign-assignee", "member")
	author := visDial(t, authorToken)
	assignee := visDial(t, assigneeToken)

	title := visIssueTitle("unassign")
	issue := visCreateIssue(t, authorToken, "", title, map[string]any{
		"assignee_type": "member",
		"assignee_id":   assigneeID,
	})
	issueID := issue["id"].(string)
	if got := assignee.waitForType("issue:created", 5*time.Second); !strings.Contains(mustJSON(t, got), title) {
		t.Fatalf("assignee's create frame lost the title: %s", mustJSON(t, got))
	}
	author.waitForType("issue:created", 5*time.Second)

	visDo(t, authorToken, "PUT", "/api/issues/"+issueID, map[string]any{
		"assignee_type": nil,
		"assignee_id":   nil,
	})

	invalidation := assignee.waitForType("issue:invalidated", 5*time.Second)
	if payload, _ := invalidation["payload"].(map[string]any); payload["issue_id"] != issueID {
		t.Fatalf("unassign invalidation payload = %v, want issue_id %s", payload, issueID)
	}
	assignee.assertNoLeak(issueID, title, 800*time.Millisecond)
	// The author keeps seeing the issue they created.
	if got := author.waitForType("issue:updated", 5*time.Second); !strings.Contains(mustJSON(t, got), title) {
		t.Fatalf("author's update lost the title: %s", mustJSON(t, got))
	}
}

// A project-wide scope sweep re-scopes every issue the project holds, so the
// members who just lost access have to be told — once, by project, rather than
// once per issue.
func TestRealtimeProjectScopeSweepInvalidatesMembersWhoLostAccess(t *testing.T) {
	projectID := visCreateProject(t, testToken, "dene717 sweep "+visIssueTitle("p"))
	memberID, memberToken := visUser(t, "sweep-member", "member")
	visDo(t, testToken, "POST", "/api/projects/"+projectID+"/members", map[string]any{"member_id": memberID})

	title := visIssueTitle("sweep")
	issue := visCreateIssue(t, testToken, projectID, title, nil)
	issueID := issue["id"].(string)
	visSetIssueVisibility(t, testToken, issueID, "project")

	member := visDial(t, memberToken)
	// Widening the project imposes its scope on every issue it holds. The
	// frame is the id-only one — a sweep has no per-issue payload — so the
	// proof that the member can now see the issue is the list the client
	// would refetch, read through their own credentials.
	visDo(t, testToken, "PUT", "/api/projects/"+projectID+"/visibility", map[string]any{"visibility": "workspace"})
	widened := member.waitForType("issue:invalidated", 5*time.Second)
	if payload, _ := widened["payload"].(map[string]any); payload["project_id"] != projectID {
		t.Fatalf("widening invalidation payload = %v, want project_id %s", payload, projectID)
	}
	if !visListHasIssue(t, memberToken, issueID) {
		t.Fatal("member's list still omits the issue after the project was shared workspace-wide")
	}
	member.collectFor(600 * time.Millisecond)

	// Narrowing it back takes the issue away again, immediately.
	visDo(t, testToken, "PUT", "/api/projects/"+projectID+"/visibility", map[string]any{"visibility": "private"})
	invalidation := member.waitForType("issue:invalidated", 5*time.Second)
	if payload, _ := invalidation["payload"].(map[string]any); payload["project_id"] != projectID {
		t.Fatalf("sweep invalidation payload = %v, want project_id %s", payload, projectID)
	}
	if visListHasIssue(t, memberToken, issueID) {
		t.Fatal("member's list still holds an issue the project sweep took away")
	}
	member.assertNoLeak(issueID, title, 800*time.Millisecond)
}

// visListHasIssue asks the issue list the way the client does — through the
// viewer's own credentials, which is what makes the answer theirs.
func visListHasIssue(t *testing.T, token, issueID string) bool {
	t.Helper()
	page := visDo(t, token, "GET", "/api/issues?workspace_id="+testWorkspaceID, nil)
	issues, _ := page["issues"].([]any)
	for _, entry := range issues {
		issue, _ := entry.(map[string]any)
		if issue["id"] == issueID {
			return true
		}
	}
	return false
}

// ---- small helpers --------------------------------------------------------

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func containsString(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
