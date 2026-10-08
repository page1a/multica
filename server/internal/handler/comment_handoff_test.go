package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/statecard"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// handoffFixture: agent A ("Supplement codex") is running on an issue and
// agent B, on the same runtime, is idle.
type handoffFixture struct {
	supplementFixture
	agentB, nameB string
}

func newHandoffFixture(t *testing.T) handoffFixture {
	t.Helper()
	f := newSupplementFixture(t, "codex", "running", false)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
	name := "Handoff B " + t.Name()
	return handoffFixture{supplementFixture: f, agentB: dbfx.Agent(t, name, f.runtimeID), nameB: name}
}

func postModeComment(t *testing.T, issueID, content, mode string) *testutil.Response {
	t.Helper()
	body := map[string]any{"content": content}
	if mode != "" {
		body["mode"] = mode
	}
	return testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", body), "id", issueID))
}

func TestCreateCommentModeHandoff(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	t.Run("stops the running agent, writes the card, starts the mentioned one fresh", func(t *testing.T) {
		f := newHandoffFixture(t)
		var created CommentResponse
		postModeComment(t, f.issueID, agentMention(f.nameB, f.agentB)+" 接着把前端做完", commentModeHandoff).
			Want(http.StatusCreated).JSON(&created)
		if len(created.HandoffStoppedTaskIDs) != 1 || created.HandoffStoppedTaskIDs[0] != f.taskID {
			t.Fatalf("stopped = %v, want A's run %s", created.HandoffStoppedTaskIDs, f.taskID)
		}
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, f.taskID).Scan(&status)
		if status != "cancelled" {
			t.Fatalf("A's run = %s, want cancelled", status)
		}
		var bTask string
		var fresh bool
		dbfx.QueryRow(t, `SELECT id, force_fresh_session FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, f.issueID, f.agentB).Scan(&bTask, &fresh)
		if !fresh {
			t.Fatal("B's run must open a new session")
		}

		issue, err := testHandler.Queries.GetIssue(t.Context(), parseUUID(f.issueID))
		if err != nil {
			t.Fatal(err)
		}
		meta := issueMetaStrings(issue.Metadata)
		if meta[statecard.KeyHandoffTo] != f.nameB || !strings.Contains(meta[statecard.KeyHandoffSummary], "接着把前端做完") {
			t.Fatalf("handoff note = %v", meta)
		}
		agentB, _ := testHandler.Queries.GetAgent(t.Context(), parseUUID(f.agentB))
		task, _ := testHandler.Queries.GetAgentTask(t.Context(), parseUUID(bTask))
		if card, reason := testHandler.stateCardForRun(t.Context(), issue, agentB, task, false, false); reason != stateCardReasonHandoff || !strings.Contains(card, "接着把前端做完") {
			t.Fatalf("B's first run gets no handoff card: %q (%s)", card, reason)
		}
		// A is not named by the handoff; it was stopped by it, so its next
		// run opens with the card as a baton, not as the one handed to.
		agentA, _ := testHandler.Queries.GetAgent(t.Context(), parseUUID(f.agentID))
		if _, reason := testHandler.stateCardForRun(t.Context(), issue, agentA, task, false, false); reason == stateCardReasonHandoff {
			t.Fatal("an agent the handoff does not name is not the one handed to")
		}
	})

	t.Run("refused without an @agent", func(t *testing.T) {
		f := newHandoffFixture(t)
		postModeComment(t, f.issueID, "no one to hand to", commentModeHandoff).Want(http.StatusBadRequest)
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, f.taskID).Scan(&status)
		if status != "running" {
			t.Fatalf("a refused handoff stopped A: %s", status)
		}
	})

	t.Run("parallel leaves the running agent alone", func(t *testing.T) {
		f := newHandoffFixture(t)
		postModeComment(t, f.issueID, agentMention(f.nameB, f.agentB)+" 一起看", commentModeParallel).Want(http.StatusCreated)
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, f.taskID).Scan(&status)
		if status != "running" {
			t.Fatalf("A's run = %s, want still running", status)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2 AND status = 'queued'`, f.issueID, f.agentB); n != 1 {
			t.Fatalf("B's runs = %d, want one", n)
		}
	})
}

func TestHandoffChatSessionOpensWithSummary(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "chat-handoff", testutil.Cols{"provider": "codex"})
	agentA := dbfx.Agent(t, "Chat handoff A", runtimeID)
	agentB := dbfx.Agent(t, "Chat handoff B", runtimeID)
	sessionID := createHandlerTestChatSession(t, agentA)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)
	sendChatWithMode(t, sessionID, "帮我改首页按钮", "").Want(http.StatusCreated)

	call := func(to string) *testutil.Response {
		req := newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/handoff", map[string]any{"to": to})
		return testutil.Call(t, testHandler.HandoffChatSession, withChatTestWorkspaceCtx(t, withURLParam(req, "sessionId", sessionID)))
	}
	call("Chat handoff A").Want(http.StatusBadRequest)
	call("nobody").Want(http.StatusBadRequest)

	var resp HandoffChatSessionResponse
	call("chat handoff b").Want(http.StatusCreated).JSON(&resp)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, resp.Session.ID)
	if resp.Session.AgentID != agentB || resp.FromSessionID != sessionID || resp.TaskID == "" {
		t.Fatalf("response = %+v", resp)
	}
	var content string
	dbfx.QueryRow(t, `SELECT content FROM chat_message WHERE id = $1`, resp.MessageID).Scan(&content)
	for _, want := range []string{"原来是 Chat handoff A 在聊", "multica chat history --session " + sessionID, "帮我改首页按钮"} {
		if !strings.Contains(content, want) {
			t.Fatalf("opening missing %q:\n%s", want, content)
		}
	}
}
