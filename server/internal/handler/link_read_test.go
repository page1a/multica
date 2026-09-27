package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Tests for DENE-897: reading an issue in workspace A through its link from a
// run bound to workspace B, judged as the human who started that run.

// linkReadFixture is one linked issue in workspace A plus a run in the test
// workspace (B) acting for a person.
type linkReadFixture struct {
	workspaceAID string
	slugA        string
	issueAID     string
	issueAKey    string
	originatorID string
	agentID      string
	taskID       string
}

// newLinkReadFixture builds workspace A with one workspace-visible issue and
// a person who is a member of both A and B. The run in B is a live
// direct_human run for that person unless taskOver overrides it.
func newLinkReadFixture(t *testing.T, issueOver testutil.Cols, taskOver testutil.Cols) linkReadFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("test database not available")
	}
	slug := "dene897-a-" + strings.ToLower(strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(t.Name()))
	if len(slug) > 60 {
		slug = slug[:60]
	}
	wsA := dbfx.Workspace(t, "DENE-897 A", slug, testutil.Cols{"issue_prefix": "DA"})
	ownerA := dbfx.User(t, "DENE-897 A Owner", slug+"-owner@multica.ai")
	dbfx.Member(t, wsA, ownerA, "owner")

	originator := dbfx.User(t, "DENE-897 Originator", slug+"-orig@multica.ai")
	dbfx.Member(t, testWorkspaceID, originator, "member")

	issueCols := testutil.Cols{
		"workspace_id": wsA,
		"visibility":   "workspace",
		"creator_type": "member",
		"creator_id":   ownerA,
		"description":  "dene897 body from workspace A",
	}
	for k, v := range issueOver {
		issueCols[k] = v
	}
	issueA := dbfx.Issue(t, "dene897 linked issue", issueCols)
	var number int32
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, issueA).Scan(&number)
	dbfx.Comment(t, issueA, "dene897 comment in A", testutil.Cols{
		"workspace_id": wsA,
		"author_id":    ownerA,
	})

	agentID := createHandlerTestAgent(t, "dene897-reader", []byte("[]"))
	issueB := dbfx.Issue(t, "dene897 reader task issue")
	taskCols := testutil.Cols{
		"issue_id":            issueB,
		"status":              "running",
		"runtime_id":          handlerTestRuntimeID(t),
		"originator_user_id":  originator,
		"accountable_user_id": originator,
		"originator_source":   "direct_human",
		"started_at":          testutil.Raw("now()"),
	}
	for k, v := range taskOver {
		taskCols[k] = v
	}
	taskID := dbfx.Task(t, agentID, taskCols)

	return linkReadFixture{
		workspaceAID: wsA,
		slugA:        slug,
		issueAID:     issueA,
		issueAKey:    issueIdentifier("DA", number),
		originatorID: originator,
		agentID:      agentID,
		taskID:       taskID,
	}
}

// joinA makes the originator a member of workspace A.
func (f linkReadFixture) joinA(t *testing.T) {
	t.Helper()
	dbfx.Member(t, f.workspaceAID, f.originatorID, "member")
}

func (f linkReadFixture) link() string {
	return "https://app.example.test/" + f.slugA + "/issues/" + f.issueAKey
}

func newLinkReadRouter() http.Handler {
	r := chi.NewRouter()
	testHandler.MountLinkReadRoutes(r)
	return r
}

// taskRequest is what the auth middleware hands a handler for a mat_ token
// bound to the test workspace: the runtime owner as user, the agent, the
// task, the bound workspace, and the task_token source.
func (f linkReadFixture) taskRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Agent-ID", f.agentID)
	req.Header.Set("X-Task-ID", f.taskID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Actor-Source", "task_token")
	return req
}

func linkReadDo(req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	newLinkReadRouter().ServeHTTP(w, req)
	return w
}

func linkReadErrorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return body.Code
}

func TestLinkRead_DirectHumanMemberReadsIssueCommentsTimeline(t *testing.T) {
	f := newLinkReadFixture(t, nil, nil)
	f.joinA(t)

	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link()))
	if w.Code != http.StatusOK {
		t.Fatalf("issue: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp linkIssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Issue.ID != f.issueAID || resp.Issue.Identifier != f.issueAKey {
		t.Fatalf("issue = %s/%s, want %s/%s", resp.Issue.ID, resp.Issue.Identifier, f.issueAID, f.issueAKey)
	}
	if resp.Issue.Description == nil || *resp.Issue.Description != "dene897 body from workspace A" {
		t.Fatalf("description not carried: %v", resp.Issue.Description)
	}
	if resp.Issue.SourceContext != nil {
		t.Fatalf("source_context must not cross the workspace boundary")
	}
	p := resp.Provenance
	if !p.CrossWorkspace || p.WorkspaceID != f.workspaceAID || p.WorkspaceSlug != f.slugA ||
		p.IssueIdentifier != f.issueAKey || p.ReadAs != f.originatorID || p.Notice == "" {
		t.Fatalf("provenance = %+v", p)
	}

	w = linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue/comments?url="+f.link()))
	if w.Code != http.StatusOK {
		t.Fatalf("comments: status = %d; body=%s", w.Code, w.Body.String())
	}
	var comments []CommentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &comments); err != nil {
		t.Fatalf("decode comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Content != "dene897 comment in A" {
		t.Fatalf("comments = %+v", comments)
	}
	if h := w.Header().Get(HeaderLinkProvenance); !strings.Contains(h, `"cross_workspace":true`) {
		t.Fatalf("comments provenance header = %q", h)
	}

	w = linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue/timeline?url="+f.link()))
	if w.Code != http.StatusOK {
		t.Fatalf("timeline: status = %d; body=%s", w.Code, w.Body.String())
	}
	var entries []TimelineEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("timeline empty")
	}
}

func TestLinkRead_AuditRowAppearsInWorkspaceATimeline(t *testing.T) {
	f := newLinkReadFixture(t, nil, nil)
	f.joinA(t)

	if w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link())); w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		dbfx.Exec(t, `DELETE FROM activity_log WHERE issue_id = $1`, f.issueAID)
	})

	n := dbfx.Count(t, `SELECT count(*) FROM activity_log
		WHERE workspace_id = $1 AND issue_id = $2 AND action = $3 AND actor_type = 'member' AND actor_id = $4`,
		f.workspaceAID, f.issueAID, linkReadAction, f.originatorID)
	if n != 1 {
		t.Fatalf("audit rows in A = %d, want 1", n)
	}
	var details map[string]any
	dbfx.QueryRow(t, `SELECT details FROM activity_log WHERE issue_id = $1 AND action = $2`, f.issueAID, linkReadAction).Scan(&details)
	if details["reader_workspace_id"] != testWorkspaceID || details["reader_agent_id"] != f.agentID ||
		details["reader_task_id"] != f.taskID || details["surface"] != "issue" {
		t.Fatalf("details = %v", details)
	}

	// The row shows on A's own issue timeline, read as an A member.
	ownerA := dbfx.QueryRow(t, `SELECT user_id FROM member WHERE workspace_id = $1 AND role = 'owner'`, f.workspaceAID)
	var ownerAID string
	ownerA.Scan(&ownerAID)
	req := httptest.NewRequest(http.MethodGet, "/api/issues/"+f.issueAID+"/timeline", nil)
	req.Header.Set("X-User-ID", ownerAID)
	req.Header.Set("X-Workspace-ID", f.workspaceAID)
	req = withURLParam(req, "id", f.issueAID)
	w := httptest.NewRecorder()
	testHandler.ListTimeline(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("A timeline: status = %d; body=%s", w.Code, w.Body.String())
	}
	var entries []TimelineEntry
	json.Unmarshal(w.Body.Bytes(), &entries)
	found := false
	for _, e := range entries {
		if e.Type == "activity" && e.Action != nil && *e.Action == linkReadAction && e.ActorID == f.originatorID {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross_workspace_read not on A's timeline: %+v", entries)
	}
}

func TestLinkRead_NonMemberOfADenied(t *testing.T) {
	f := newLinkReadFixture(t, nil, nil)
	// originator is NOT joined to A.
	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link()))
	if w.Code != http.StatusForbidden || linkReadErrorCode(t, w) != linkErrNotMember {
		t.Fatalf("status = %d code=%s; body=%s", w.Code, linkReadErrorCode(t, w), w.Body.String())
	}
	if strings.Contains(w.Body.String(), "dene897 body") {
		t.Fatalf("leaked issue body")
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1`, f.issueAID); n != 0 {
		t.Fatalf("refused read wrote %d audit rows", n)
	}
}

func TestLinkRead_TriggerOwnerRunDenied(t *testing.T) {
	f := newLinkReadFixture(t, nil, testutil.Cols{"originator_source": "trigger_owner"})
	f.joinA(t)
	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link()))
	if w.Code != http.StatusForbidden || linkReadErrorCode(t, w) != linkErrNoOriginator {
		t.Fatalf("status = %d code=%s; body=%s", w.Code, linkReadErrorCode(t, w), w.Body.String())
	}
}

func TestLinkRead_TerminalTaskDenied(t *testing.T) {
	f := newLinkReadFixture(t, nil, testutil.Cols{
		"status":       "completed",
		"completed_at": testutil.Raw("now()"),
	})
	f.joinA(t)
	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link()))
	if w.Code != http.StatusForbidden || linkReadErrorCode(t, w) != linkErrNoOriginator {
		t.Fatalf("status = %d code=%s; body=%s", w.Code, linkReadErrorCode(t, w), w.Body.String())
	}
}

func TestLinkRead_PrivateIssueReadsAsNotFound(t *testing.T) {
	f := newLinkReadFixture(t, testutil.Cols{"visibility": "private"}, nil)
	f.joinA(t)
	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+f.link()))
	if w.Code != http.StatusNotFound {
		t.Fatalf("private: status = %d; body=%s", w.Code, w.Body.String())
	}
	privateBody := w.Body.String()

	missing := "https://app.example.test/" + f.slugA + "/issues/DA-999999"
	w = linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+missing))
	if w.Code != http.StatusNotFound || w.Body.String() != privateBody {
		t.Fatalf("missing issue must read like a private one: %d %q vs %q", w.Code, w.Body.String(), privateBody)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1`, f.issueAID); n != 0 {
		t.Fatalf("hidden read wrote %d audit rows", n)
	}
}

func TestLinkRead_WriteMethodsRefused(t *testing.T) {
	f := newLinkReadFixture(t, nil, nil)
	f.joinA(t)
	for _, path := range []string{"/api/links/issue", "/api/links/issue/comments", "/api/links/issue/timeline"} {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			w := linkReadDo(f.taskRequest(m, path+"?url="+f.link()))
			if w.Code != http.StatusMethodNotAllowed || linkReadErrorCode(t, w) != linkErrWriteForbidden {
				t.Fatalf("%s %s: status = %d code=%s", m, path, w.Code, linkReadErrorCode(t, w))
			}
			if w.Header().Get("Allow") != http.MethodGet {
				t.Fatalf("%s %s: Allow = %q", m, path, w.Header().Get("Allow"))
			}
		}
	}
}

func TestLinkRead_SameWorkspaceLinkIsOrdinaryRead(t *testing.T) {
	f := newLinkReadFixture(t, nil, nil)
	issueB := dbfx.Issue(t, "dene897 own issue")
	var number int32
	dbfx.QueryRow(t, `SELECT number FROM issue WHERE id = $1`, issueB).Scan(&number)
	link := "https://app.example.test/" + handlerTestWorkspaceSlug + "/issues/" + issueB
	w := linkReadDo(f.taskRequest(http.MethodGet, "/api/links/issue?url="+link))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp linkIssueResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Provenance.CrossWorkspace || resp.Issue.ID != issueB {
		t.Fatalf("resp = %+v", resp.Provenance)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM activity_log WHERE issue_id = $1 AND action = $2`, issueB, linkReadAction); n != 0 {
		t.Fatalf("same-workspace read must not audit, got %d rows", n)
	}
}

func TestLinkRead_ParseIssueLink(t *testing.T) {
	cases := map[string]issueLink{
		"https://app.multica.ai/dene/issues/DENE-897":         {Slug: "dene", Ref: "DENE-897"},
		"https://ai.ferryway.cc/dene/issues/DENE-897?tab=x#c": {Slug: "dene", Ref: "DENE-897"},
		"/dene/issues/01a0dced-d2f0-7769-8b2f-5b3524b469ad":   {Slug: "dene", Ref: "01a0dced-d2f0-7769-8b2f-5b3524b469ad"},
		"dene/issues/DENE-1":                                  {Slug: "dene", Ref: "DENE-1"},
		"https://app.multica.ai/zh/dene/issues/DENE-897":      {Slug: "dene", Ref: "DENE-897"},
	}
	for raw, want := range cases {
		got, err := parseIssueLink(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %+v err=%v, want %+v", raw, got, err, want)
		}
	}
	for _, bad := range []string{"", "DENE-897", "https://app.multica.ai/dene", "https://app.multica.ai/issues/DENE-1"} {
		if _, err := parseIssueLink(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
