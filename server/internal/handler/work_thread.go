package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkThreadSnapshot is the bounded read model shared by Issue and Chat.
// Queue rows are intentionally projected as short inputs; callers never get
// the full task context or provider session payload from this endpoint.
type WorkThreadSnapshot struct {
	ThreadID       string            `json:"thread_id"`
	AgentID        string            `json:"agent_id"`
	IssueID        string            `json:"issue_id,omitempty"`
	ChatSessionID  string            `json:"chat_session_id,omitempty"`
	Continuous     bool              `json:"continuous"`
	CurrentTurn    *WorkThreadTurn   `json:"current_turn,omitempty"`
	LastTurn       *WorkThreadTurn   `json:"last_turn,omitempty"`
	State          string            `json:"state"`
	CanResume      bool              `json:"can_resume"`
	SessionID      string            `json:"session_id,omitempty"`
	QueuedInputs   []WorkThreadInput `json:"queued_inputs"`
	QueueTruncated bool              `json:"queue_truncated"`
	Context        WorkThreadContext `json:"context"`
	UpdatedAt      string            `json:"updated_at"`
}

type WorkThreadTurn struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

type WorkThreadInput struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Summary   string `json:"summary,omitempty"`
	CreatedAt string `json:"created_at"`
}

type WorkThreadContext struct {
	Generation       int32  `json:"generation"`
	MessageLimit     int32  `json:"message_limit"`
	TokenBudget      int32  `json:"token_budget"`
	SummaryAvailable bool   `json:"summary_available"`
	BreakReason      string `json:"break_reason,omitempty"`
}

const workThreadQueueLimit = 50

type WorkThreadActionRequest struct {
	Action  string `json:"action"`
	Summary string `json:"summary,omitempty"`
	TaskID  string `json:"task_id,omitempty"`
}

// WorkThreadAction mutates one Issue work thread while preserving its
// continuity key. Continue clones the latest resumable turn inside the same
// transaction; interrupt cancels active turns; queue appends a new input.
// The database unique pending-task fence is the final concurrency guard.
//
// Every action runs behind the same gates kun already applies to the
// equivalent direct action, so the panel is never a back door:
//   - continue / queue enqueue a run for the thread's (or assignee) agent, so
//     they re-validate canInvokeAgent (MUL-4525) and refuse a derived run while
//     the issue is in Triage, exactly like RerunIssue;
//   - interrupt is a user cancellation, so it goes through CancelTaskByUser
//     with the operator recorded, after the private-agent access check the
//     id-only cancel endpoint applies.
func (h *Handler) WorkThreadAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	originatorUserID := h.invokeOriginatorFromRequest(r, actorType, actorID)
	canInvoke := func(agent db.Agent) bool {
		return h.canInvokeAgent(r.Context(), agent, actorType, actorID, originatorUserID, workspaceID)
	}
	var req WorkThreadActionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid work thread action")
			return
		}
	}
	switch req.Action {
	case "interrupt":
		threadID, agentID, err := h.latestIssueWorkThread(r, issue.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to interrupt work thread")
			return
		}
		if err == nil {
			agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: issue.WorkspaceID})
			if err != nil {
				writeError(w, http.StatusNotFound, "work thread agent not found")
				return
			}
			if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
				writeError(w, http.StatusForbidden, "you do not have access to this agent")
				return
			}
			if err := h.cancelIssueWorkThread(r, threadID, h.taskCancellationActor(r.Context(), actorType, actorID)); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to interrupt work thread")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "interrupted"})
		return
	case "queue":
		// A queued input is a derived run of the issue's assignee: gate the
		// resolved target before anything is written, as RerunIssue does.
		targetAgent, err := h.issueRunTargetAgent(r, issue)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if !canInvoke(targetAgent) {
			h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
			return
		}
		task, err := h.TaskService.EnqueueTaskForIssueByActor(r.Context(), issue, memberActorUserID(actorType, actorID))
		if errors.Is(err, service.ErrIssueInTriage) {
			h.writeDispatchBlocked(w, http.StatusForbidden, ReasonIssueInTriage)
			return
		}
		coalesced := errors.Is(err, service.ErrDuplicatePendingTask)
		if coalesced {
			task, err = h.appendQueuedWorkThreadInput(r, issue.ID, targetAgent.ID, req.Summary)
		}
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if req.Summary != "" && !coalesced {
			if _, err := h.DB.Exec(r.Context(), `UPDATE agent_task_queue SET trigger_summary = $2 WHERE id = $1`, task.ID, req.Summary); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to record queued input")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID)})
		return
	case "prioritize":
		taskID, ok := parseUUIDOrBadRequest(w, req.TaskID, "task id")
		if !ok {
			return
		}
		prioritized, activeID, err := h.prioritizeIssueWorkThreadInput(r, issue.ID, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "task is no longer queued or there is no active turn to interrupt")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to prioritize work thread input")
			return
		}
		h.TaskService.BroadcastTaskQueued(r.Context(), prioritized)
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "prioritized", "task_id": uuidToString(prioritized.ID), "active_task_id": uuidToString(activeID)})
		return
	case "continue":
		// Resuming the thread is a derived run of its own agent (nobody named
		// it), so it follows RerunIssue's no-source rule: refused in Triage,
		// and the historical thread agent must be invocable by the operator.
		if issue.TriageState.Valid {
			h.writeDispatchBlocked(w, http.StatusForbidden, ReasonIssueInTriage)
			return
		}
		threadID, agentID, err := h.latestIssueWorkThread(r, issue.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "work thread has no resumable turn or already has a pending turn")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to continue work thread")
			return
		}
		targetAgent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "work thread agent not found")
			return
		}
		if !canInvoke(targetAgent) {
			h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
			return
		}
		task, err := h.continueWorkThread(r, threadID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "work thread has no resumable turn or already has a pending turn")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to continue work thread")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID), "session_id": task.SessionID.String})
		return
	default:
		writeError(w, http.StatusBadRequest, "action must be continue, interrupt, or queue")
	}
}

// issueRunTargetAgent resolves the agent a derived issue run would target —
// the assignee, or the leader of the assigned squad — the same way
// RerunIssue does when no source task is named.
func (h *Handler) issueRunTargetAgent(r *http.Request, issue db.Issue) (db.Agent, error) {
	var agentID pgtype.UUID
	switch {
	case issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid:
		agentID = issue.AssigneeID
	case issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid:
		squad, err := h.Queries.GetSquad(r.Context(), issue.AssigneeID)
		if err != nil {
			return db.Agent{}, errors.New("issue is assigned to a squad but squad not found")
		}
		agentID = squad.LeaderID
	default:
		return db.Agent{}, errors.New("issue is not assigned to an agent or squad")
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return db.Agent{}, errors.New("target agent not found")
	}
	return agent, nil
}

// latestIssueWorkThread returns the issue's primary thread and its agent. An
// issue can retain older threads after an agent/model boundary change;
// actions always address the newest one so stopping it never touches those
// unrelated runs.
func (h *Handler) latestIssueWorkThread(r *http.Request, issueID pgtype.UUID) (threadID, agentID pgtype.UUID, err error) {
	err = h.DB.QueryRow(r.Context(), `
		SELECT id, agent_id FROM work_thread
		WHERE issue_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT 1`, issueID).Scan(&threadID, &agentID)
	return threadID, agentID, err
}

// cancelIssueWorkThread cancels every live turn of one thread as an explicit
// user cancellation, so the run history records who stopped it and the
// delegated-failure sweeper does not rebuild the turn the operator just ended.
func (h *Handler) cancelIssueWorkThread(r *http.Request, threadID pgtype.UUID, actor service.TaskCancellationActor) error {
	rows, err := h.DB.Query(r.Context(), `
		SELECT id FROM agent_task_queue
		WHERE work_thread_id = $1
		  AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')`, threadID)
	if err != nil {
		return err
	}
	var taskIDs []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		taskIDs = append(taskIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, taskID := range taskIDs {
		if _, err := h.TaskService.CancelTaskByUser(r.Context(), taskID, actor); err != nil {
			return err
		}
	}
	return nil
}

// appendQueuedWorkThreadInput keeps the single pending-task fence intact while
// preserving a follow-up submitted during an already queued turn. The row is
// the one the fence just refused to duplicate — this issue, this agent, no
// comment thread — so several agents queued on one issue never share the
// update. The summary is bounded so repeated clicks cannot grow the task row
// without limit.
func (h *Handler) appendQueuedWorkThreadInput(r *http.Request, issueID, agentID pgtype.UUID, summary string) (db.AgentTaskQueue, error) {
	const maxSummary = 4000
	if len(summary) > maxSummary {
		summary = summary[:maxSummary]
	}
	var taskID pgtype.UUID
	err := h.DB.QueryRow(r.Context(), `
		UPDATE agent_task_queue
		SET trigger_summary = LEFT(CONCAT_WS(E'\n', NULLIF(trigger_summary, ''), NULLIF($2, '')), $3)
		WHERE id = (
			SELECT id FROM agent_task_queue
			WHERE issue_id = $1 AND agent_id = $4 AND comment_thread_id IS NULL AND status = 'queued'
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		)
		RETURNING id
	`, issueID, summary, maxSummary, agentID).Scan(&taskID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	return h.Queries.GetAgentTask(r.Context(), taskID)
}

func (h *Handler) prioritizeIssueWorkThreadInput(r *http.Request, issueID, taskID pgtype.UUID) (db.AgentTaskQueue, pgtype.UUID, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, pgtype.UUID{}, err
	}
	defer tx.Rollback(r.Context())
	var activeID pgtype.UUID
	if err := tx.QueryRow(r.Context(), `SELECT id FROM agent_task_queue WHERE issue_id=$1 AND status IN ('dispatched','running','waiting_local_directory') ORDER BY created_at,id LIMIT 1 FOR UPDATE`, issueID).Scan(&activeID); err != nil {
		return db.AgentTaskQueue{}, pgtype.UUID{}, pgx.ErrNoRows
	}
	if _, err := tx.Exec(r.Context(), `UPDATE agent_task_queue SET priority = 3 WHERE issue_id=$1 AND status='queued' AND id<>$2 AND priority>=4`, issueID, taskID); err != nil {
		return db.AgentTaskQueue{}, pgtype.UUID{}, err
	}
	var prioritizedID pgtype.UUID
	if err := tx.QueryRow(r.Context(), `UPDATE agent_task_queue SET priority=4 WHERE id=$1 AND issue_id=$2 AND status='queued' RETURNING id`, taskID, issueID).Scan(&prioritizedID); err != nil {
		return db.AgentTaskQueue{}, pgtype.UUID{}, pgx.ErrNoRows
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.AgentTaskQueue{}, pgtype.UUID{}, err
	}
	task, err := h.Queries.GetAgentTask(r.Context(), prioritizedID)
	return task, activeID, err
}

// ChatWorkThreadAction mirrors the chat rules kun already enforces on the
// composer and the cancel endpoint (DENE-840): seeing a chat is not enough.
// Interrupt is the creator's alone, like CancelTaskByUser. Continue enqueues a
// run, so — like SendChatMessage — a non-creator needs speak access and the run
// is authorized as the creator, whose agent grant and quota it uses.
func (h *Handler) ChatWorkThreadAction(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	var req WorkThreadActionRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	switch req.Action {
	case "interrupt":
		// Chat privacy: only the member who opened the conversation may
		// cancel its task, even though the chat may be shared.
		if uuidToString(session.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "not your task")
			return
		}
		var ids []pgtype.UUID
		rows, err := h.DB.Query(r.Context(), `SELECT id FROM agent_task_queue WHERE chat_session_id = $1 AND status IN ('queued','dispatched','running','waiting_local_directory')`, session.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to inspect chat work thread")
			return
		}
		for rows.Next() {
			var id pgtype.UUID
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		actor := h.taskCancellationActor(r.Context(), actorType, actorID)
		for _, id := range ids {
			if _, err := h.TaskService.CancelTaskByUser(r.Context(), id, actor); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to interrupt work thread")
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "interrupted"})
	case "continue":
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: session.AgentID, WorkspaceID: session.WorkspaceID})
		if err != nil {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		invokeActorType, invokeActorID := actorType, actorID
		invokeOriginator := h.invokeOriginatorFromRequest(r, actorType, actorID)
		if actorType != "agent" && uuidToString(session.CreatorID) != userID {
			access, err := h.chatAccessFor(r.Context(), session, userID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check chat access")
				return
			}
			if !access.speak {
				writeError(w, http.StatusForbidden, "you can view this chat but not send messages")
				return
			}
			invokeActorType = "member"
			invokeActorID = uuidToString(session.CreatorID)
			invokeOriginator = invokeActorID
		}
		if !h.canInvokeAgent(r.Context(), agent, invokeActorType, invokeActorID, invokeOriginator, workspaceID) {
			h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
			return
		}
		task, err := h.continueChatWorkThread(r, session.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "work thread has no resumable turn or already has a pending turn")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to continue work thread")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"action": req.Action, "state": "queued", "task_id": uuidToString(task.ID), "thread_id": uuidToString(task.WorkThreadID), "session_id": task.SessionID.String})
	case "queue":
		writeError(w, http.StatusConflict, "chat queue inputs must be sent through the composer")
	default:
		writeError(w, http.StatusBadRequest, "action must be continue, interrupt, or queue")
	}
}

func (h *Handler) continueChatWorkThread(r *http.Request, sessionID pgtype.UUID) (db.AgentTaskQueue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(r.Context())
	var taskID pgtype.UUID
	var status string
	var sid pgtype.Text
	err = tx.QueryRow(r.Context(), `SELECT latest.id, latest.status, latest.session_id FROM work_thread wt JOIN LATERAL (SELECT id,status,session_id FROM agent_task_queue WHERE work_thread_id=wt.id ORDER BY created_at DESC,id DESC LIMIT 1) latest ON true WHERE wt.chat_session_id=$1 AND NOT EXISTS (SELECT 1 FROM agent_task_queue WHERE work_thread_id=wt.id AND status IN ('queued','dispatched','running','waiting_local_directory')) FOR UPDATE OF wt`, sessionID).Scan(&taskID, &status, &sid)
	if err != nil || (status != "cancelled" && status != "failed") || !sid.Valid || sid.String == "" {
		return db.AgentTaskQueue{}, pgx.ErrNoRows
	}
	task, err := h.Queries.WithTx(tx).CreateRetryTask(r.Context(), db.CreateRetryTaskParams{ID: taskID})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}

// continueWorkThread clones the latest resumable turn of one already-gated
// thread. The thread id is the one the caller authorized, so the resumed run
// can never belong to a different agent than the one that passed the gate.
func (h *Handler) continueWorkThread(r *http.Request, threadID pgtype.UUID) (db.AgentTaskQueue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(r.Context())
	var taskID pgtype.UUID
	var status string
	var sessionID pgtype.Text
	err = tx.QueryRow(r.Context(), `
		SELECT latest.id, latest.status, latest.session_id
		FROM work_thread wt
		JOIN LATERAL (
			SELECT id, status, session_id FROM agent_task_queue
			WHERE work_thread_id = wt.id ORDER BY created_at DESC, id DESC LIMIT 1
		) latest ON true
		WHERE wt.id = $1
		  AND NOT EXISTS (SELECT 1 FROM agent_task_queue WHERE work_thread_id = wt.id AND status IN ('queued','dispatched','running','waiting_local_directory'))
		FOR UPDATE OF wt`, threadID).Scan(&taskID, &status, &sessionID)
	if err != nil || (status != "cancelled" && status != "failed") || !sessionID.Valid || sessionID.String == "" {
		return db.AgentTaskQueue{}, pgx.ErrNoRows
	}
	qtx := h.Queries.WithTx(tx)
	task, err := qtx.CreateRetryTask(r.Context(), db.CreateRetryTaskParams{ID: taskID})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return task, nil
}

type workThreadRow struct {
	ThreadID, AgentID, IssueID, ChatSessionID pgtype.UUID
	Generation, MessageLimit, TokenBudget     int32
	LastTurnID                                pgtype.UUID
	LastSessionID                             pgtype.Text
	BreakReason                               pgtype.Text
	UpdatedAt                                 time.Time
	CurrentID                                 pgtype.UUID
	CurrentSessionID                          pgtype.Text
	CurrentStatus                             pgtype.Text
	CurrentStartedAt                          pgtype.Timestamptz
	LastID                                    pgtype.UUID
	LastTaskSessionID                         pgtype.Text
	LastStatus                                pgtype.Text
	LastCompletedAt                           pgtype.Timestamptz
}

const workThreadByIssueSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at,
       latest.id, latest.session_id, latest.status, latest.completed_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
LEFT JOIN LATERAL (
  SELECT id, session_id, status, completed_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest ON true
WHERE wt.issue_id = $1
ORDER BY wt.updated_at DESC, wt.id DESC
LIMIT 1`

const workThreadByChatSQL = `
SELECT wt.id, wt.agent_id, wt.issue_id, wt.chat_session_id,
       wt.context_generation, wt.context_message_limit, wt.context_token_budget,
       wt.last_session_id, wt.last_turn_id, wt.continuity_break_reason, wt.updated_at,
       active.id, active.session_id, active.status, active.started_at,
       latest.id, latest.session_id, latest.status, latest.completed_at
FROM work_thread wt
LEFT JOIN LATERAL (
  SELECT id, session_id, status, started_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
    AND status IN ('dispatched', 'running', 'waiting_local_directory')
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) active ON true
LEFT JOIN LATERAL (
  SELECT id, session_id, status, completed_at
  FROM agent_task_queue
  WHERE work_thread_id = wt.id
  ORDER BY created_at DESC, id DESC
  LIMIT 1
) latest ON true
WHERE wt.chat_session_id = $1
ORDER BY wt.updated_at DESC, wt.id DESC
LIMIT 1`

const workThreadInputsSQL = `
SELECT id, status, COALESCE(trigger_summary, ''), created_at
FROM agent_task_queue
WHERE work_thread_id = $1 AND status IN ('queued', 'deferred')
ORDER BY created_at ASC, id ASC
LIMIT $2`

func (h *Handler) GetIssueWorkThread(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var row workThreadRow
	err := h.DB.QueryRow(r.Context(), workThreadByIssueSQL, issue.ID).Scan(
		&row.ThreadID, &row.AgentID, &row.IssueID, &row.ChatSessionID,
		&row.Generation, &row.MessageLimit, &row.TokenBudget, &row.LastSessionID,
		&row.LastTurnID, &row.BreakReason, &row.UpdatedAt, &row.CurrentID,
		&row.CurrentSessionID, &row.CurrentStatus, &row.CurrentStartedAt,
		&row.LastID, &row.LastTaskSessionID, &row.LastStatus, &row.LastCompletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load work thread")
		return
	}
	h.writeWorkThreadSnapshot(w, r, row)
}

func (h *Handler) GetChatWorkThread(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, ctxWorkspaceID(r.Context()), chi.URLParam(r, "sessionId"))
	if !ok {
		return
	}
	var row workThreadRow
	err := h.DB.QueryRow(r.Context(), workThreadByChatSQL, session.ID).Scan(
		&row.ThreadID, &row.AgentID, &row.IssueID, &row.ChatSessionID,
		&row.Generation, &row.MessageLimit, &row.TokenBudget, &row.LastSessionID,
		&row.LastTurnID, &row.BreakReason, &row.UpdatedAt, &row.CurrentID,
		&row.CurrentSessionID, &row.CurrentStatus, &row.CurrentStartedAt,
		&row.LastID, &row.LastTaskSessionID, &row.LastStatus, &row.LastCompletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load work thread")
		return
	}
	h.writeWorkThreadSnapshot(w, r, row)
}

func (h *Handler) writeWorkThreadSnapshot(w http.ResponseWriter, r *http.Request, row workThreadRow) {
	snapshot := WorkThreadSnapshot{
		ThreadID: uuidToString(row.ThreadID), AgentID: uuidToString(row.AgentID),
		IssueID: uuidToString(row.IssueID), ChatSessionID: uuidToString(row.ChatSessionID),
		Continuous: row.Generation > 0 || row.LastSessionID.Valid || row.LastTurnID.Valid,
		SessionID:  row.CurrentSessionID.String, QueuedInputs: []WorkThreadInput{},
		Context: WorkThreadContext{Generation: row.Generation, MessageLimit: row.MessageLimit, TokenBudget: row.TokenBudget,
			SummaryAvailable: false, BreakReason: row.BreakReason.String},
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
		State:     "idle",
	}
	if !snapshot.CurrentTurnExists() && row.LastTurnID.Valid {
		snapshot.SessionID = row.LastSessionID.String
	}
	if row.CurrentID.Valid {
		snapshot.State = "active"
		snapshot.CurrentTurn = &WorkThreadTurn{ID: uuidToString(row.CurrentID), Status: row.CurrentStatus.String,
			SessionID: row.CurrentSessionID.String, StartedAt: timestampToString(row.CurrentStartedAt)}
	}
	if row.LastID.Valid {
		snapshot.LastTurn = &WorkThreadTurn{ID: uuidToString(row.LastID), Status: row.LastStatus.String,
			SessionID: row.LastTaskSessionID.String, StartedAt: timestampToString(row.LastCompletedAt)}
		if snapshot.State == "idle" {
			switch row.LastStatus.String {
			case "cancelled", "failed":
				if row.LastTaskSessionID.Valid && !row.BreakReason.Valid {
					snapshot.State = "resumable"
					snapshot.CanResume = true
				} else if row.BreakReason.Valid {
					snapshot.State = "rebuild_required"
				}
			case "completed":
				snapshot.State = "completed"
			}
		}
	}
	rows, err := h.DB.Query(r.Context(), workThreadInputsSQL, row.ThreadID, workThreadQueueLimit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
		return
	}
	defer rows.Close()
	for rows.Next() {
		if len(snapshot.QueuedInputs) >= workThreadQueueLimit {
			snapshot.QueueTruncated = true
			break
		}
		var in WorkThreadInput
		var id pgtype.UUID
		var status, summary string
		var createdAt time.Time
		if scanErr := rows.Scan(&id, &status, &summary, &createdAt); scanErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
			return
		}
		in.ID, in.Status, in.Summary, in.CreatedAt = uuidToString(id), status, summary, createdAt.UTC().Format(time.RFC3339Nano)
		snapshot.QueuedInputs = append(snapshot.QueuedInputs, in)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queued work thread inputs")
		return
	}
	if snapshot.State == "idle" && len(snapshot.QueuedInputs) > 0 {
		snapshot.State = "queued"
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s WorkThreadSnapshot) CurrentTurnExists() bool { return s.CurrentTurn != nil }
