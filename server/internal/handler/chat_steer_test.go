package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type chatSteerFixture struct {
	agentID, sessionID, headID string
}

// newChatSteerFixture opens a chat whose first reply is running on a provider
// CLI. negotiated: the daemon advertised task supplements at start.
func newChatSteerFixture(t *testing.T, provider string, negotiated bool) chatSteerFixture {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "chat-steer-"+provider, testutil.Cols{"provider": provider})
	agentID := dbfx.Agent(t, "Chat steer "+provider, runtimeID)
	sessionID := createHandlerTestChatSession(t, agentID)
	dbfx.Cleanup(t, `DELETE FROM chat_task_supplement WHERE chat_session_id = $1`, sessionID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)

	head := sendChatWithMode(t, sessionID, "original ask", "").Want(http.StatusCreated)
	var sent SendChatMessageResponse
	head.JSON(&sent)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`, sent.TaskID)
	var capabilities []string
	if negotiated {
		capabilities = []string{protocol.DaemonCapabilityTaskSupplementV1}
		if agent.SteersByRestart(provider) {
			capabilities = append(capabilities, protocol.DaemonCapabilitySteerRestartV1)
		} else if agent.SteersByHandoff(provider) {
			capabilities = append(capabilities, protocol.DaemonCapabilitySteerHandoffV1)
		}
	}
	if _, err := testHandler.TaskService.StartTask(t.Context(), parseUUID(sent.TaskID), capabilities...); err != nil {
		t.Fatalf("start head: %v", err)
	}
	dbfx.Cleanup(t, `DELETE FROM task_supplement_capability WHERE task_id = $1`, sent.TaskID)
	return chatSteerFixture{agentID: agentID, sessionID: sessionID, headID: sent.TaskID}
}

func sendChatWithMode(t *testing.T, sessionID, content, mode string) *testutil.Response {
	t.Helper()
	body := map[string]any{"content": content}
	if mode != "" {
		body["mode"] = mode
	}
	req := newRequest(http.MethodPost, "/api/chat/sessions/"+sessionID+"/messages", body)
	req = withChatTestWorkspaceCtx(t, withURLParam(req, "sessionId", sessionID))
	return testutil.Call(t, testHandler.SendChatMessage, req)
}

func chatPending(t *testing.T, sessionID string) PendingChatTaskResponse {
	t.Helper()
	req := newRequest(http.MethodGet, "/api/chat/sessions/"+sessionID+"/pending-task", nil)
	req = withChatTestWorkspaceCtx(t, withURLParam(req, "sessionId", sessionID))
	var resp PendingChatTaskResponse
	testutil.Call(t, testHandler.GetPendingChatTask, req).Want(http.StatusOK).JSON(&resp)
	return resp
}

func daemonClaimSupplement(t *testing.T, taskID string) map[string]any {
	t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/supplements/claim", nil, testWorkspaceID, "legit-daemon")
	var out map[string]any
	testutil.Call(t, testHandler.ClaimTaskSupplement, withURLParam(req, "taskId", taskID)).Want(http.StatusOK).JSON(&out)
	return out
}

func daemonAckSupplement(t *testing.T, taskID, messageID string, delivered bool) *testutil.Response {
	t.Helper()
	body := map[string]any{"delivered": delivered}
	if !delivered {
		body["error"] = protocol.TaskSupplementFailureTurnEnded
	}
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/supplements/"+messageID+"/ack", body, testWorkspaceID, "legit-daemon")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("taskId", taskID)
	rctx.URLParams.Add("commentId", messageID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return testutil.Call(t, testHandler.AckTaskSupplement, req)
}

func TestChatStartNegotiatesSupplementWithoutIssue(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var issueNull bool
	dbfx.QueryRow(t, `SELECT issue_id IS NULL FROM task_supplement_capability WHERE task_id = $1`, f.headID).Scan(&issueNull)
	if !issueNull {
		t.Fatal("chat capability row should carry no issue")
	}
	if p := chatPending(t, f.sessionID); !p.SteerSupported || p.SteerProvider != "claude" || p.SteerMode != protocol.SteerModeSame {
		t.Fatalf("pending = %+v, want steer supported on claude", p)
	}
}

// A one-shot CLI steers by restarting on the same session (DENE-1349); the
// composer needs to know so it can say the CLI will restart.
func TestChatSteerOnOneShotCLIRestartsTheSession(t *testing.T) {
	f := newChatSteerFixture(t, "cursor", true)
	if p := chatPending(t, f.sessionID); !p.SteerSupported || p.SteerMode != protocol.SteerModeRestart {
		t.Fatalf("pending = %+v, want restart steer on cursor", p)
	}
	var mode string
	dbfx.QueryRow(t, `SELECT steer_mode FROM task_supplement_capability WHERE task_id = $1`, f.headID).Scan(&mode)
	if mode != protocol.SteerModeRestart {
		t.Fatalf("steer_mode = %q, want restart", mode)
	}
}

// An ACP CLI steers by stopping the current step and prompting the same
// session again (DENE-1347).
func TestChatSteerOnACPCLIHandsOffTheStep(t *testing.T) {
	f := newChatSteerFixture(t, "kimi", true)
	if p := chatPending(t, f.sessionID); !p.SteerSupported || p.SteerMode != protocol.SteerModeHandoff {
		t.Fatalf("pending = %+v, want handoff steer on kimi", p)
	}
}

func TestChatSteerDeliversIntoTheRunningReply(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "grok", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			f := newChatSteerFixture(t, provider, true)
			var sent SendChatMessageResponse
			sendChatWithMode(t, f.sessionID, "only fix web", sendModeSteer).Want(http.StatusCreated).JSON(&sent)
			if sent.Mode != sendModeSteer || !sent.Queued {
				t.Fatalf("send = %+v, want a steered queued follow-up", sent)
			}
			pending := chatPending(t, f.sessionID)
			if len(pending.QueuedTasks) != 1 || !pending.QueuedTasks[0].Steering {
				t.Fatalf("queue = %+v, want the follow-up marked steering", pending.QueuedTasks)
			}

			claimed := daemonClaimSupplement(t, f.headID)
			if claimed["comment_id"] != sent.MessageID || claimed["content"] != "only fix web" {
				t.Fatalf("claim = %+v, want the chat message", claimed)
			}
			daemonAckSupplement(t, f.headID, sent.MessageID, true).Want(http.StatusOK)

			var followupStatus, messageTask string
			dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, sent.TaskID).Scan(&followupStatus)
			if followupStatus != "cancelled" {
				t.Fatalf("follow-up status = %s, want cancelled after delivery", followupStatus)
			}
			dbfx.QueryRow(t, `SELECT task_id::text FROM chat_message WHERE id = $1`, sent.MessageID).Scan(&messageTask)
			if messageTask != f.headID {
				t.Fatalf("message task = %s, want it moved to the running reply %s", messageTask, f.headID)
			}
			if p := chatPending(t, f.sessionID); len(p.QueuedTasks) != 0 {
				t.Fatalf("queue = %+v, want empty after delivery", p.QueuedTasks)
			}
		})
	}
}

func TestChatSteerFailureLeavesTheFollowupQueued(t *testing.T) {
	f := newChatSteerFixture(t, "codex", true)
	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "also check mobile", sendModeSteer).Want(http.StatusCreated).JSON(&sent)
	daemonClaimSupplement(t, f.headID)
	daemonAckSupplement(t, f.headID, sent.MessageID, false).Want(http.StatusOK)

	pending := chatPending(t, f.sessionID)
	if len(pending.QueuedTasks) != 1 || pending.QueuedTasks[0].TaskID != sent.TaskID || pending.QueuedTasks[0].Steering {
		t.Fatalf("queue = %+v, want the follow-up back as an ordinary queued turn", pending.QueuedTasks)
	}
}

func TestChatSteerAfterTheReplyEndedIsNotDelivered(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "late note", sendModeSteer).Want(http.StatusCreated).JSON(&sent)
	completeTaskViaDaemon(t, f.headID)

	var status string
	dbfx.QueryRow(t, `SELECT status FROM chat_task_supplement WHERE chat_message_id = $1`, sent.MessageID).Scan(&status)
	if status != "failed" {
		t.Fatalf("receipt status = %s, want failed once the reply ended", status)
	}
	if out := daemonClaimSupplement(t, f.headID); len(out) != 0 {
		t.Fatalf("claim after end = %+v, want nothing", out)
	}
	var followupStatus string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, sent.TaskID).Scan(&followupStatus)
	if followupStatus == "cancelled" {
		t.Fatal("the follow-up must answer a message the reply never read")
	}
}

func TestChatSteerRefusedWhenTheCLICannotTakeIt(t *testing.T) {
	for _, tc := range []struct{ provider string }{{"kimi"}, {"claude"}} {
		t.Run(tc.provider, func(t *testing.T) {
			// claude without negotiation stands for a CLI too old to steer.
			f := newChatSteerFixture(t, tc.provider, false)
			before := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, f.sessionID)
			var refusal struct {
				Code           string   `json:"code"`
				Error          string   `json:"error"`
				AvailableModes []string `json:"available_modes"`
			}
			sendChatWithMode(t, f.sessionID, "steer me", sendModeSteer).Want(http.StatusConflict).JSON(&refusal)
			if refusal.Code != errCodeSteerUnsupported || refusal.Error == "" ||
				len(refusal.AvailableModes) != 2 || refusal.AvailableModes[0] != sendModeQueue || refusal.AvailableModes[1] != sendModeRestart {
				t.Fatalf("refusal = %+v", refusal)
			}
			if after := dbfx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, f.sessionID); after != before {
				t.Fatalf("a refused steer stored %d messages", after-before)
			}
			if p := chatPending(t, f.sessionID); p.SteerSupported {
				t.Fatal("pending must not offer steer for this CLI")
			}
		})
	}
}

func TestChatSteerWithNothingReplyingJustSends(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	runtimeID := dbfx.Runtime(t, "chat-steer-idle", testutil.Cols{"provider": "claude"})
	agentID := dbfx.Agent(t, "Chat steer idle", runtimeID)
	sessionID := createHandlerTestChatSession(t, agentID)
	dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, sessionID)
	var sent SendChatMessageResponse
	sendChatWithMode(t, sessionID, "hello", sendModeSteer).Want(http.StatusCreated).JSON(&sent)
	if sent.Mode != "start" || sent.Queued {
		t.Fatalf("send = %+v, want an ordinary start", sent)
	}
}

func TestChatRestartStopsTheReplyAndRunsTheMessageNext(t *testing.T) {
	f := newChatSteerFixture(t, "kimi", false)
	var sent SendChatMessageResponse
	sendChatWithMode(t, f.sessionID, "start over with this", sendModeRestart).Want(http.StatusCreated).JSON(&sent)
	if sent.Mode != sendModeRestart {
		t.Fatalf("send mode = %s, want restart", sent.Mode)
	}
	var headStatus string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, f.headID).Scan(&headStatus)
	if headStatus != "cancelled" {
		t.Fatalf("head status = %s, want cancelled", headStatus)
	}
	pending := chatPending(t, f.sessionID)
	if pending.TaskID != sent.TaskID {
		t.Fatalf("pending head = %s, want the restart message %s", pending.TaskID, sent.TaskID)
	}
}

func TestChatSendRejectsUnknownMode(t *testing.T) {
	f := newChatSteerFixture(t, "claude", true)
	sendChatWithMode(t, f.sessionID, "x", "later").Want(http.StatusBadRequest)
}

func TestCreateCommentModeSteer(t *testing.T) {
	post := func(t *testing.T, f supplementFixture, mode string) *testutil.Response {
		body := map[string]any{"content": agentMention("Supplement", f.agentID) + " only fix web", "mode": mode}
		return testutil.Call(t, testHandler.CreateComment, withURLParam(
			newRequest(http.MethodPost, "/api/issues/"+f.issueID+"/comments", body), "id", f.issueID))
	}
	t.Run("steerable turn takes it", func(t *testing.T) {
		f := newSupplementFixture(t, "grok", "running", true)
		dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
		var created CommentResponse
		post(t, f, sendModeSteer).Want(http.StatusCreated).JSON(&created)
		if len(created.Supplements) != 1 || created.Supplements[0].TaskID != f.taskID {
			t.Fatalf("receipts = %+v, want one on the running turn", created.Supplements)
		}
	})
	t.Run("turn without the capability is refused", func(t *testing.T) {
		f := newSupplementFixture(t, "codex", "running", false)
		dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
		before := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.issueID)
		var refusal struct {
			Code string `json:"code"`
		}
		post(t, f, sendModeSteer).Want(http.StatusConflict).JSON(&refusal)
		if refusal.Code != errCodeSteerUnsupported {
			t.Fatalf("code = %s", refusal.Code)
		}
		if after := dbfx.Count(t, `SELECT count(*) FROM comment WHERE issue_id = $1`, f.issueID); after != before {
			t.Fatal("a refused steer posted the comment")
		}
	})
	t.Run("restart stops the running turn", func(t *testing.T) {
		f := newSupplementFixture(t, "codex", "running", false)
		dbfx.Cleanup(t, `DELETE FROM agent_task_queue WHERE issue_id = $1`, f.issueID)
		post(t, f, sendModeRestart).Want(http.StatusCreated)
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, f.taskID).Scan(&status)
		if status != "cancelled" {
			t.Fatalf("running turn status = %s, want cancelled", status)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND status = 'queued'`, f.issueID); n != 1 {
			t.Fatalf("queued follow-ups = %d, want one started by the comment", n)
		}
	})
}
