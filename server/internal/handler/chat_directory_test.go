package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// chatDirectoryFixture is the DENE-1088 visibility matrix. The runtime owner
// (the fixture owner) runs a task that member A started. A sees its own
// private chats, workspace chats, chats of projects A belongs to and chats
// shared with A by name; it never sees B's or the runtime owner's private
// chats, nor a project chat of a project A is not in.
type chatDirectoryFixture struct {
	a, b, task, inProject, otherProject                          string
	ownPrivate, current, otherPrivate, ownerPrivate, projectChat string
	workspaceChat, sharedChat, unsharedChat                      string
}

func newChatDirectoryFixture(t *testing.T) chatDirectoryFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("test database not available")
	}
	slug := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	var f chatDirectoryFixture
	f.a = dbfx.User(t, "Directory A", slug+"-a@example.test")
	dbfx.Member(t, testWorkspaceID, f.a, "member")
	f.b = dbfx.User(t, "Directory B", slug+"-b@example.test")
	dbfx.Member(t, testWorkspaceID, f.b, "member")
	runtime := dbfx.Runtime(t, "directory-runtime", testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID})
	agent := dbfx.Agent(t, "directory-agent", runtime, testutil.Cols{"workspace_id": testWorkspaceID, "owner_id": testUserID})
	f.inProject = dbfx.Project(t, "Directory project", testutil.Cols{"workspace_id": testWorkspaceID})
	f.otherProject = dbfx.Project(t, "Directory other project", testutil.Cols{"workspace_id": testWorkspaceID})
	dbfx.Exec(t, `INSERT INTO project_member (workspace_id, project_id, member_id) VALUES ($1,$2,$3)`, testWorkspaceID, f.inProject, f.a)
	dbfx.Exec(t, `INSERT INTO project_member (workspace_id, project_id, member_id) VALUES ($1,$2,$3)`, testWorkspaceID, f.inProject, f.b)

	chat := func(title, creator, visibility, project string) string {
		return dbfx.ChatSession(t, agent, testutil.Cols{
			"workspace_id": testWorkspaceID, "creator_id": creator, "title": title,
			"visibility": visibility, "project_id": project, "explicitly_created_at": testutil.Raw("now()"),
		})
	}
	f.ownPrivate = chat("A private", f.a, "private", f.inProject)
	f.current = chat("A current", f.a, "private", f.inProject)
	f.otherPrivate = chat("B private", f.b, "private", f.inProject)
	f.ownerPrivate = chat("Owner private", testUserID, "private", f.inProject)
	f.projectChat = chat("B project", f.b, "project", f.inProject)
	f.workspaceChat = chat("B workspace", f.b, "workspace", f.inProject)
	f.sharedChat = chat("B shared", f.b, "project", f.otherProject)
	f.unsharedChat = chat("B unshared", f.b, "project", f.otherProject)
	dbfx.Exec(t, `INSERT INTO resource_share (workspace_id, resource_type, resource_id, member_id, added_by, access) VALUES ($1,'chat_session',$2,$3,$4,'view')`,
		testWorkspaceID, f.sharedChat, f.a, f.b)

	f.task = dbfx.Task(t, agent, testutil.Cols{
		"runtime_id": runtime, "status": "running", "chat_session_id": f.current,
		"originator_user_id": f.a, "accountable_user_id": f.a, "originator_source": "direct_human",
	})
	return f
}

// taskRequest is a request as the agent's task token: X-User-ID is the
// runtime owner, the visibility principal must come from the task.
func (f chatDirectoryFixture) taskRequest(t *testing.T, path string) *http.Request {
	t.Helper()
	r := withChatTestWorkspaceCtx(t, newRequest(http.MethodGet, path, nil))
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-Task-ID", f.task)
	return r
}

func (f chatDirectoryFixture) list(t *testing.T, query string) map[string]bool {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.ListChatDirectory(w, f.taskRequest(t, "/api/chat/directory?"+query))
	if w.Code != http.StatusOK {
		t.Fatalf("directory %q: %d %s", query, w.Code, w.Body.String())
	}
	var items []ChatDirectoryItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode directory: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.ID] = true
	}
	return seen
}

func (f chatDirectoryFixture) history(t *testing.T, sessionID string) int {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.GetChatSessionHandoff(w, withURLParam(f.taskRequest(t, "/api/chat/sessions/"+sessionID+"/handoff"), "sessionId", sessionID))
	return w.Code
}

func chatReadCursorCount(t *testing.T, users ...string) int {
	t.Helper()
	var n int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM chat_session_read WHERE user_id = ANY($1::uuid[])`, users).Scan(&n); err != nil {
		t.Fatalf("count read cursors: %v", err)
	}
	return n
}

func TestChatDirectory_TaskVisibilityFollowsOriginator(t *testing.T) {
	f := newChatDirectoryFixture(t)
	cursorsBefore := chatReadCursorCount(t, f.a, f.b, testUserID)

	cases := []struct {
		name    string
		id      string
		visible bool
	}{
		{"originator's own private chat", f.ownPrivate, true},
		{"another member's private chat", f.otherPrivate, false},
		{"runtime owner's private chat", f.ownerPrivate, false},
		{"project chat of a project the originator is in", f.projectChat, true},
		{"workspace chat", f.workspaceChat, true},
		{"chat shared with the originator by name", f.sharedChat, true},
		{"project chat of a project the originator is not in", f.unsharedChat, false},
		{"the task's own chat", f.current, false},
	}
	all := f.list(t, "all_projects=true")
	for _, tc := range cases {
		if all[tc.id] != tc.visible {
			t.Errorf("all projects: %s visible = %v, want %v", tc.name, all[tc.id], tc.visible)
		}
	}

	// With no filter a task defaults to its own project; another project only
	// shows up when asked for.
	current := f.list(t, "")
	if !current[f.ownPrivate] || !current[f.projectChat] || current[f.sharedChat] {
		t.Errorf("default project scope = %v, want only the task project's chats", current)
	}
	if other := f.list(t, "project="+f.otherProject); !other[f.sharedChat] || other[f.ownPrivate] {
		t.Errorf("explicit other project = %v, want the shared chat only", other)
	}

	// Reading follows the same principal: what was listed opens, the runtime
	// owner's private chat does not open just because it runs the task.
	for _, tc := range cases {
		if tc.id == f.current {
			continue
		}
		code := f.history(t, tc.id)
		if tc.visible && code != http.StatusOK {
			t.Errorf("history %s = %d, want 200", tc.name, code)
		}
		if !tc.visible && code == http.StatusOK {
			t.Errorf("history %s = 200, want refused", tc.name)
		}
	}

	if after := chatReadCursorCount(t, f.a, f.b, testUserID); after != cursorsBefore {
		t.Fatalf("list/history changed read cursors from %d to %d", cursorsBefore, after)
	}
}

func TestChatDirectory_TaskWithoutOriginatorFailsClosed(t *testing.T) {
	f := newChatDirectoryFixture(t)
	dbfx.Exec(t, `UPDATE agent_task_queue SET originator_user_id = NULL WHERE id = $1`, f.task)
	if seen := f.list(t, "all_projects=true"); len(seen) != 0 {
		t.Fatalf("directory without originator = %v, want empty", seen)
	}
	if code := f.history(t, f.ownerPrivate); code != http.StatusNotFound {
		t.Fatalf("history without originator = %d, want 404", code)
	}
}

// TestApplyClaimChatCounts pins the brief's hint to the directory: same
// viewer, own chat excluded, and issue-style tasks (no chat) count too.
func TestApplyClaimChatCounts(t *testing.T) {
	f := newChatDirectoryFixture(t)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.task))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	listed := len(f.list(t, "project="+f.inProject))

	out := claimProjectContext{Projects: []claimProject{{ID: f.inProject}}}
	testHandler.applyClaimChatCounts(context.Background(), &out, &task, parseUUID(testWorkspaceID))
	if out.Projects[0].ChatCount != listed || listed != 3 {
		t.Fatalf("chat task count = %d, directory lists %d, want both 3", out.Projects[0].ChatCount, listed)
	}

	issueTask := task
	issueTask.ChatSessionID = db.AgentTaskQueue{}.ChatSessionID
	out = claimProjectContext{Projects: []claimProject{{ID: f.inProject}}}
	testHandler.applyClaimChatCounts(context.Background(), &out, &issueTask, parseUUID(testWorkspaceID))
	if out.Projects[0].ChatCount != 4 {
		t.Fatalf("issue task count = %d, want 4 (no current chat to exclude)", out.Projects[0].ChatCount)
	}
}
