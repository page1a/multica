package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Agent spawn permissions (DENE-1271). workspace.settings.agent_spawn decides
// what an agent run may create: an issue or a chat, from a chat run or an
// issue run. People are never governed by it. Every agent create-issue and
// create-chat entry goes through checkAgentSpawn, so the table has one reader.

const (
	SpawnRefusedDisabled       = "agent_spawn_disabled"
	SpawnRefusedBudget         = "agent_spawn_budget_exceeded"
	SpawnRefusedDepth          = "chat_spawn_depth_exceeded"
	SpawnRefusedTaskMode       = "chat_spawn_task_mode"
	agentSpawnSettingsKey      = "agent_spawn"
	agentSpawnKindChat         = "chat"
	agentSpawnKindIssue        = "issue"
	defaultChatChatPerChat     = 5
	defaultChatChatPerRun      = 3
	maxAgentSpawnLimit         = 1000
	chatSpawnClientKeyMaxBytes = 200
)

// AgentSpawnCell is one row of the table. Zero limits mean unlimited.
type AgentSpawnCell struct {
	Enabled bool `json:"enabled"`
	PerChat int  `json:"per_chat,omitempty"`
	PerRun  int  `json:"per_run"`
}

// AgentSpawnPolicy is workspace.settings.agent_spawn. Chat-from-issue has no
// cell: a discussion inside an issue belongs in comments and parallel work in
// sub-issues, so that path is always refused with chat_spawn_task_mode.
type AgentSpawnPolicy struct {
	ChatIssue  AgentSpawnCell `json:"chat_issue"`
	IssueIssue AgentSpawnCell `json:"issue_issue"`
	ChatChat   AgentSpawnCell `json:"chat_chat"`
}

// defaultAgentSpawnPolicy keeps today's behavior: agents may create issues
// from anywhere without a cap. Only the new chat-from-chat path is bounded.
func defaultAgentSpawnPolicy() AgentSpawnPolicy {
	return AgentSpawnPolicy{
		ChatIssue:  AgentSpawnCell{Enabled: true},
		IssueIssue: AgentSpawnCell{Enabled: true},
		ChatChat:   AgentSpawnCell{Enabled: true, PerChat: defaultChatChatPerChat, PerRun: defaultChatChatPerRun},
	}
}

type agentSpawnCellPatch struct {
	Enabled *bool `json:"enabled"`
	PerChat *int  `json:"per_chat"`
	PerRun  *int  `json:"per_run"`
}

type agentSpawnPolicyPatch struct {
	ChatIssue  *agentSpawnCellPatch `json:"chat_issue"`
	IssueIssue *agentSpawnCellPatch `json:"issue_issue"`
	ChatChat   *agentSpawnCellPatch `json:"chat_chat"`
}

func (c *AgentSpawnCell) apply(p *agentSpawnCellPatch, perChat bool) error {
	if p == nil {
		return nil
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.PerRun != nil {
		if *p.PerRun < 0 || *p.PerRun > maxAgentSpawnLimit {
			return fmt.Errorf("per_run must be between 0 and %d", maxAgentSpawnLimit)
		}
		c.PerRun = *p.PerRun
	}
	if p.PerChat != nil {
		if !perChat {
			return fmt.Errorf("per_chat only applies to chat_chat")
		}
		if *p.PerChat < 0 || *p.PerChat > maxAgentSpawnLimit {
			return fmt.Errorf("per_chat must be between 0 and %d", maxAgentSpawnLimit)
		}
		c.PerChat = *p.PerChat
	}
	return nil
}

func (p *AgentSpawnPolicy) apply(patch agentSpawnPolicyPatch) error {
	if err := p.ChatIssue.apply(patch.ChatIssue, false); err != nil {
		return fmt.Errorf("chat_issue: %w", err)
	}
	if err := p.IssueIssue.apply(patch.IssueIssue, false); err != nil {
		return fmt.Errorf("issue_issue: %w", err)
	}
	if err := p.ChatChat.apply(patch.ChatChat, true); err != nil {
		return fmt.Errorf("chat_chat: %w", err)
	}
	return nil
}

// parseAgentSpawnPolicy reads the stored table over the defaults. A missing
// or malformed value falls back to the defaults cell by cell, so a partial
// write never turns a cell off by accident.
func parseAgentSpawnPolicy(settings []byte) AgentSpawnPolicy {
	policy := defaultAgentSpawnPolicy()
	var root map[string]json.RawMessage
	if err := json.Unmarshal(settings, &root); err != nil {
		return policy
	}
	raw, ok := root[agentSpawnSettingsKey]
	if !ok {
		return policy
	}
	var patch agentSpawnPolicyPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return policy
	}
	next := policy
	if err := next.apply(patch); err != nil {
		return policy
	}
	return next
}

func (h *Handler) agentSpawnPolicy(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) (AgentSpawnPolicy, error) {
	ws, err := q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return AgentSpawnPolicy{}, err
	}
	return parseAgentSpawnPolicy(ws.Settings), nil
}

// agentSpawnRefusal is a structured "no" the CLI and the chat card both read.
type agentSpawnRefusal struct {
	Code    string `json:"code"`
	Message string `json:"error"`
	Limit   int    `json:"limit,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

func (e *agentSpawnRefusal) Error() string { return e.Message }

func writeAgentSpawnRefusal(w http.ResponseWriter, refusal *agentSpawnRefusal) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error":       refusal.Message,
		"code":        refusal.Code,
		"reason_code": refusal.Code,
		"limit":       refusal.Limit,
		"scope":       refusal.Scope,
	})
}

// agentSpawnSource names which kind of run is asking. A run tied to neither a
// chat nor an issue (quick create, autopilot) is not governed by the table.
func agentSpawnSource(task db.AgentTaskQueue) string {
	switch {
	case task.ChatSessionID.Valid:
		return agentSpawnKindChat
	case task.IssueID.Valid:
		return agentSpawnKindIssue
	}
	return ""
}

// checkAgentSpawn is the one gate in front of every agent create. need is how
// many things this call will create (a plan applies several issues at once).
// The per-chat budget for chat-from-chat is counted by the caller under the
// parent session lock, so it is not checked here.
func (h *Handler) checkAgentSpawn(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, policy AgentSpawnPolicy, target string, need int) *agentSpawnRefusal {
	source := agentSpawnSource(task)
	if source == "" {
		return nil
	}
	var cell AgentSpawnCell
	var label string
	switch {
	case source == agentSpawnKindChat && target == agentSpawnKindIssue:
		cell, label = policy.ChatIssue, "creating issues from a chat"
	case source == agentSpawnKindIssue && target == agentSpawnKindIssue:
		cell, label = policy.IssueIssue, "creating issues from an issue"
	case source == agentSpawnKindChat && target == agentSpawnKindChat:
		cell, label = policy.ChatChat, "opening chats from a chat"
	default:
		return &agentSpawnRefusal{
			Code:    SpawnRefusedTaskMode,
			Message: "an issue run cannot open a chat: discuss in comments with `multica issue comment add`, or split parallel work with `multica issue create --parent`",
		}
	}
	if !cell.Enabled {
		return &agentSpawnRefusal{
			Code:    SpawnRefusedDisabled,
			Message: fmt.Sprintf("this workspace does not allow agents %s (Settings → Agent permissions)", label),
		}
	}
	if cell.PerRun > 0 {
		used, err := q.CountAgentSpawnRecords(ctx, db.CountAgentSpawnRecordsParams{TaskID: task.ID, TargetKind: target})
		if err != nil {
			return &agentSpawnRefusal{Code: SpawnRefusedBudget, Message: "could not count this run's creations; try again"}
		}
		if int(used)+need > cell.PerRun {
			return &agentSpawnRefusal{
				Code:    SpawnRefusedBudget,
				Message: fmt.Sprintf("this run already created %d of %d allowed (%s); finish with what you have or ask a person to continue", used, cell.PerRun, label),
				Limit:   cell.PerRun,
				Scope:   "run",
			}
		}
	}
	return nil
}

// recordAgentSpawn writes the ledger row the per-run budget counts. A run not
// governed by the table writes nothing.
func recordAgentSpawn(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, target string, workspaceID, targetID pgtype.UUID) error {
	source := agentSpawnSource(task)
	if source == "" {
		return nil
	}
	return q.InsertAgentSpawnRecord(ctx, db.InsertAgentSpawnRecordParams{
		WorkspaceID: workspaceID,
		TaskID:      task.ID,
		SourceKind:  source,
		TargetKind:  target,
		TargetID:    targetID,
	})
}

// agentSpawnTask returns the acting agent's run when the request comes from
// one (task token), else ok=false. People never hit the table.
func (h *Handler) agentSpawnTask(r *http.Request, actorType, actorID string) (db.AgentTaskQueue, bool) {
	if actorType != "agent" {
		return db.AgentTaskQueue{}, false
	}
	task, ok := h.taskFromRequestHeader(r)
	if !ok || uuidToString(task.AgentID) != actorID {
		return db.AgentTaskQueue{}, false
	}
	return task, true
}

// agentIssueSpawnReservation holds budget slots taken before an agent
// creates issues. The count and the reservation are one transaction under a
// per-run lock, so concurrent creates from the same run cannot both pass the
// check on a stale count (DENE-1271 review). A zero value is ungoverned and
// every method is a no-op on it.
type agentIssueSpawnReservation struct {
	h    *Handler
	ids  []pgtype.UUID
	used int
}

// fill binds the next reserved slots to the issues actually created.
func (res *agentIssueSpawnReservation) fill(ctx context.Context, issueIDs ...pgtype.UUID) {
	if res == nil {
		return
	}
	for _, id := range issueIDs {
		if res.used >= len(res.ids) {
			return
		}
		_ = res.h.Queries.SetAgentSpawnRecordTarget(ctx, db.SetAgentSpawnRecordTargetParams{ID: res.ids[res.used], TargetID: id})
		res.used++
	}
}

// release returns the slots no issue was created for. Deferred by every
// caller, so a failed create hands its budget back.
func (res *agentIssueSpawnReservation) release() {
	if res == nil || res.used >= len(res.ids) {
		return
	}
	_ = res.h.Queries.DeleteAgentSpawnRecords(context.Background(), res.ids[res.used:])
	res.used = len(res.ids)
}

// reserveAgentIssueSpawn checks the table and reserves need slots in one
// transaction. It returns a refusal when the run may not create them.
func (h *Handler) reserveAgentIssueSpawn(ctx context.Context, workspaceID pgtype.UUID, task db.AgentTaskQueue, need int) (*agentIssueSpawnReservation, *agentSpawnRefusal, error) {
	source := agentSpawnSource(task)
	if source == "" {
		return nil, nil, nil
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if err := qtx.LockAgentSpawnTask(ctx, uuidToString(task.ID)); err != nil {
		return nil, nil, err
	}
	policy, err := h.agentSpawnPolicy(ctx, qtx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if refusal := h.checkAgentSpawn(ctx, qtx, task, policy, agentSpawnKindIssue, need); refusal != nil {
		return nil, refusal, nil
	}
	res := &agentIssueSpawnReservation{h: h}
	for i := 0; i < need; i++ {
		id, err := qtx.ReserveAgentSpawnRecord(ctx, db.ReserveAgentSpawnRecordParams{
			ID:          dbid.NewV7(),
			WorkspaceID: workspaceID,
			TaskID:      task.ID,
			SourceKind:  source,
			TargetKind:  agentSpawnKindIssue,
		})
		if err != nil {
			return nil, nil, err
		}
		res.ids = append(res.ids, id)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return res, nil, nil
}

// gateAgentIssueSpawn is the create-issue entry check: it writes the refusal
// and returns ok=false when the acting run may not create need more issues.
// On ok it returns the reservation (nil when the caller is not governed); the
// caller fills it with the created issues and defers release.
func (h *Handler) gateAgentIssueSpawn(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, actorType, actorID string, need int) (*agentIssueSpawnReservation, bool) {
	task, governed := h.agentSpawnTask(r, actorType, actorID)
	if !governed {
		return nil, true
	}
	res, refusal, err := h.reserveAgentIssueSpawn(r.Context(), workspaceID, task, need)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check agent permissions")
		return nil, false
	}
	if refusal != nil {
		writeAgentSpawnRefusal(w, refusal)
		return nil, false
	}
	return res, true
}

// ---- settings endpoints ----

func (h *Handler) GetWorkspaceAgentSpawn(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	policy, err := h.agentSpawnPolicy(r.Context(), h.Queries, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

// UpdateWorkspaceAgentSpawn merges a partial table into the stored one. An
// agent run is refused even when its owner is an admin: an agent must not be
// able to raise its own limits.
func (h *Handler) UpdateWorkspaceAgentSpawn(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "agents cannot change agent permissions; ask a workspace admin")
		return
	}
	id, ok := parseUUIDOrBadRequest(w, workspaceIDFromURL(r, "id"), "workspace id")
	if !ok {
		return
	}
	var patch agentSpawnPolicyPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	policy := parseAgentSpawnPolicy(ws.Settings)
	if err := policy.apply(patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var settings map[string]any
	_ = json.Unmarshal(ws.Settings, &settings)
	if settings == nil {
		settings = map[string]any{}
	}
	settings[agentSpawnSettingsKey] = policy
	raw, err := json.Marshal(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode settings")
		return
	}
	updated, err := h.Queries.UpdateWorkspace(r.Context(), db.UpdateWorkspaceParams{ID: id, Settings: raw})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update agent permissions")
		return
	}
	h.publish(protocol.EventWorkspaceUpdated, uuidToString(updated.ID), "member", requestUserID(r), map[string]any{"workspace": h.workspaceToResponse(updated)})
	writeJSON(w, http.StatusOK, policy)
}

// keepStoredAgentSpawn replaces whatever agent_spawn an incoming settings
// write carries with the stored one (or removes it when none is stored).
func keepStoredAgentSpawn(incoming any, stored []byte) any {
	settings, ok := incoming.(map[string]any)
	if !ok {
		return incoming
	}
	var storedMap map[string]any
	_ = json.Unmarshal(stored, &storedMap)
	if value, ok := storedMap[agentSpawnSettingsKey]; ok {
		settings[agentSpawnSettingsKey] = value
	} else {
		delete(settings, agentSpawnSettingsKey)
	}
	return settings
}
