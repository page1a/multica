package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Issue drafts are the alignment step that has to happen BEFORE an issue
// exists. Creating an issue enqueues agent work, so a request that was never
// agreed on is already executing by the time anyone reads it back. This
// protocol gives that agreement somewhere to live: a hidden conversation plus a
// server-owned structured draft, and exactly one endpoint — finalize — that
// turns the agreed draft into an issue.
//
// The carrier mechanism is the one Agent Builder already uses: a per-session
// `kind = 'system'` agent, invisible to every agent list, assignment surface
// and chat list, so an alignment conversation cannot be mistaken for ordinary
// chat and its runtime/model stay frozen per conversation.
//
// How that carrier behaves is the alignment policy — see
// issue_draft_policy.go. The draft records which policy key and prompt version
// it is running, so the prompt behind a finished alignment stays auditable.

// maxIssueDraftBytes bounds one stored draft. The honest fields are title and
// description, both far below this; the limit exists so a client bug cannot
// grow an unbounded row.
const maxIssueDraftBytes = 256 * 1024

// issueDraftResponse is the wire shape of one alignment draft. `draft` is
// echoed verbatim: the server reads the four issue fields out of it at finalize
// and leaves everything else the client keeps there untouched.
type issueDraftResponse struct {
	ChatSessionID string                   `json:"chat_session_id"`
	WorkspaceID   string                   `json:"workspace_id"`
	Status        string                   `json:"status"`
	Revision      int64                    `json:"revision"`
	Draft         json.RawMessage          `json:"draft"`
	IssueID       *string                  `json:"issue_id,omitempty"`
	Policy        issueDraftPolicyResponse `json:"policy"`
	// Capabilities is the other half of the audit record: the methods the
	// carrier's prompt was assembled from, and the version of their text. It is
	// also what the alignment page draws its capability control from — the set
	// the conversation is running, not the set this client would pick.
	Capabilities issueDraftCapabilityResponse `json:"capabilities"`
	// FinalizeRound counts the rounds this alignment has been confirmed in: 0
	// until it is reopened, then one more per reopen. It is what a client reads
	// to know the draft it is looking at is a continuation, and what the server
	// reads to know a confirm may add nodes instead of adopting the group.
	FinalizeRound int32 `json:"finalize_round"`
	// FinalizedRevision is the content revision the last confirmed round
	// settled on, recorded when the draft was reopened. Absent until then.
	FinalizedRevision *int64 `json:"finalized_revision,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func issueDraftToResponse(d db.IssueDraft) issueDraftResponse {
	out := issueDraftResponse{
		ChatSessionID: uuidToString(d.ChatSessionID),
		WorkspaceID:   uuidToString(d.WorkspaceID),
		Status:        d.Status,
		Revision:      d.Revision,
		Draft:         json.RawMessage(d.Draft),
		Policy:        issueDraftPolicyResponseFromRow(d.PolicyKey, d.PolicyVersion),
		Capabilities:  issueDraftCapabilityResponseFromRow(d.CapabilityKeys, d.CapabilityVersion),
		FinalizeRound: d.FinalizeRound,
		CreatedAt:     timestampToString(d.CreatedAt),
		UpdatedAt:     timestampToString(d.UpdatedAt),
	}
	if d.IssueID.Valid {
		id := uuidToString(d.IssueID)
		out.IssueID = &id
	}
	if d.FinalizedRevision.Valid {
		revision := d.FinalizedRevision.Int64
		out.FinalizedRevision = &revision
	}
	return out
}

// issueDraftPayload is the part of `draft` the server understands. Everything
// else in the object is the client's and is never read here — but these fields
// are a contract, not a convenience: finalize builds the issues straight out of
// them, so an unparseable draft must fail the confirm rather than create a
// half-meant issue.
//
// The eight flat fields describe the group's ROOT (its parent issue). Children,
// when the alignment settled on more than one issue, come in `Children`. An
// absent or empty `Children` is not a legacy shape to be tolerated: it is a
// group with a single node, which is exactly what a lone issue always was.
type issueDraftPayload struct {
	Title         string            `json:"title"`
	Description   string            `json:"description"`
	Status        string            `json:"status"`
	Priority      string            `json:"priority"`
	AssigneeType  *string           `json:"assignee_type"`
	AssigneeID    *string           `json:"assignee_id"`
	ProjectID     *string           `json:"project_id"`
	ParentIssueID *string           `json:"parent_issue_id"`
	Children      []issueDraftChild `json:"children"`
}

// issueDraftChild is one sub-issue of an alignment payload.
//
// `Key` identifies this node WITHIN this draft. The preview panel mints it once,
// when the row first enters the draft, and every later save carries it back
// unchanged — that is what lets the server derive the same origin_id on every
// confirm and refuse to build the group twice. It is not a UUID and never
// becomes one directly: the server hashes (chat_session_id, key) into the
// node's identity, so a client cannot name an id that points at another draft.
//
// There is deliberately no nested children and no assignee name: a sub-issue
// cannot have sub-issues (the stage barrier is a sibling-scoped judgement, and
// a second level would silently fall out of it), and the model that fills this
// block has no workspace roster to resolve a real assignee id against.
type issueDraftChild struct {
	Key          string  `json:"key"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	// Stage is the barrier slot, 1-based. Absent means "no stage", i.e. the
	// implicit single stage. Bounds are checked before anything is created.
	Stage *int32 `json:"stage"`
}

// isIssueDraftCarrier reports whether an agent is a hidden alignment carrier.
// Mirrors the kind/system_key guard the SQL statements carry, so the handler
// rejects a non-alignment session before reaching the database rather than
// relying on an UPDATE matching zero rows.
func isIssueDraftCarrier(agent db.Agent) bool {
	return agent.Kind == "system" &&
		agent.SystemKey.Valid &&
		strings.HasPrefix(agent.SystemKey.String, "issue_draft:")
}

// loadIssueDraftSession resolves a path sessionId to a chat session the caller
// owns AND that is an alignment carrier. Both gates are needed: without the
// carrier check this would be a second, weaker way to hang state off — or read
// state out of — any chat session the caller happens to own.
func (h *Handler) loadIssueDraftSession(w http.ResponseWriter, r *http.Request, userID, workspaceID string) (db.ChatSession, bool) {
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, chi.URLParam(r, "sessionId"))
	if !ok {
		return db.ChatSession{}, false
	}
	if !denyUnlessChatCreator(w, session, userID) {
		return db.ChatSession{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return db.ChatSession{}, false
	}
	if !isIssueDraftCarrier(agent) {
		writeError(w, http.StatusNotFound, "issue draft session not found")
		return db.ChatSession{}, false
	}
	return session, true
}

type CreateIssueDraftSessionRequest struct {
	RuntimeID string `json:"runtime_id"`
	Model     string `json:"model,omitempty"`
	// ThinkingLevel is the reasoning effort the carrier runs at, empty meaning
	// "whatever the local CLI is configured with". Validated against the target
	// runtime exactly as agent create validates it, so a level the runtime
	// cannot take is refused here rather than persisted and dropped by the
	// daemon (DENE-514).
	ThinkingLevel string `json:"thinking_level,omitempty"`
	// Draft seeds the conversation with what the user already typed in the
	// create entry point, so the first turn can answer it instead of asking
	// for it again. Optional; omitted means an empty draft.
	Draft json.RawMessage `json:"draft,omitempty"`
	// Policy picks the alignment policy the conversation opens under. Optional:
	// omitted means the guided default, which is what the create entry points
	// offer. See issue_draft_policy.go for the registry.
	Policy string `json:"policy,omitempty"`
	// Capabilities picks the alignment methods the carrier is given, by key.
	// See issue_draft_capability.go for the registry.
	//
	// Absent and empty are different requests, and the difference is the one
	// the picker needs: absent means "this client has no capability control" and
	// gets the built-in default, while an empty array is a client saying "none
	// of them" — the state a picker with every box unchecked produces. Anything
	// else is resolved: unknown keys are refused, and a capability's own
	// requirements are added.
	Capabilities []string `json:"capabilities,omitempty"`
}

type CreateIssueDraftSessionResponse struct {
	SessionID string             `json:"session_id"`
	AgentID   string             `json:"agent_id"`
	RuntimeID string             `json:"runtime_id"`
	Draft     issueDraftResponse `json:"draft"`
}

// CreateIssueDraftSession opens an alignment conversation and its draft in one
// transaction.
//
// The chat session is created here rather than accepted from the client on
// purpose: it is what makes "this draft belongs to this workspace and this
// user" true by construction. A create that took a caller-supplied
// chat_session_id would have to re-derive that on every later write, and would
// let a draft be attached to an ordinary conversation.
func (h *Handler) CreateIssueDraftSession(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreateIssueDraftSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	draft, ok := validIssueDraftBody(w, req.Draft)
	if !ok {
		return
	}

	// The policy decides the carrier's prompt, so an unknown key is rejected
	// before anything is created: a conversation running an empty prompt would
	// look like it worked and behave like nothing.
	policyKey := strings.TrimSpace(req.Policy)
	if policyKey == "" {
		policyKey = issueDraftPolicyQuestion
	}
	policy, ok := issueDraftPolicyByKey(policyKey)
	if !ok {
		writeError(w, http.StatusBadRequest, issueDraftPolicyUnknownMessage())
		return
	}

	// The capabilities are the other half of the carrier's prompt, so they are
	// resolved here too — before anything is created. An unknown key is refused
	// rather than dropped for the same reason an unknown policy is: a
	// conversation assembled without a method the user turned on looks like it
	// worked and behaves like the method was never there. The resolved set is
	// the union of what was asked for and what the policy requires, and it is
	// that union — never the raw request — which is both assembled and recorded.
	requested, err := resolveIssueDraftCapabilities(req.Capabilities)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	required, err := issueDraftPolicyRequiredCapabilities(policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve alignment capabilities")
		return
	}
	capabilities := issueDraftMergeCapabilities(requested, required)

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveSessionCarrierRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "an issue draft session", "start")
	if !ok {
		return
	}

	// The reasoning dial is part of what the user picked in the same panel as
	// the machine, so it is validated against THAT runtime here rather than
	// accepted and silently dropped: an alignment carrier that runs at the
	// default effort while its picker says "Extra high" is a setting the user
	// cannot trust. Same two checks as agent create (agent.go), shared so the
	// sentences cannot drift.
	thinkingLevel := strings.TrimSpace(req.ThinkingLevel)
	if !h.thinkingLevelAcceptedForRuntime(w, r, runtime, thinkingLevel, strings.TrimSpace(req.Model)) {
		return
	}

	flowID := uuid.NewString()
	ownerUUID := parseUUID(userID)
	model := strings.TrimSpace(req.Model)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start issue draft session")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// FOR KEY SHARE on the workspace row before creating the carrier's
	// chat_session — the creator half of the #5219 delete/create protocol, so a
	// session cannot be created into a workspace mid-delete.
	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}

	carrier, err := qtx.CreateAgentBuilder(r.Context(), db.CreateAgentBuilderParams{
		WorkspaceID:  workspaceUUID,
		Name:         fmt.Sprintf(".multica-issue-draft-%s", flowID),
		RuntimeMode:  runtime.RuntimeMode,
		RuntimeID:    runtime.ID,
		OwnerID:      ownerUUID,
		Instructions: policy.Instructions(capabilities),
		Model:        pgtype.Text{String: model, Valid: model != ""},
		ThinkingLevel: pgtype.Text{
			String: thinkingLevel,
			Valid:  thinkingLevel != "",
		},
		SystemKey: pgtype.Text{
			String: fmt.Sprintf("issue_draft:%s", flowID),
			Valid:  true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare issue draft agent")
		return
	}

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: workspaceUUID,
		AgentID:     carrier.ID,
		CreatorID:   ownerUUID,
		Title:       "Align a new issue",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue draft session")
		return
	}
	session, err = qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark issue draft session explicit")
		return
	}

	created, err := qtx.CreateIssueDraft(r.Context(), db.CreateIssueDraftParams{
		ChatSessionID:     session.ID,
		WorkspaceID:       workspaceUUID,
		Draft:             draft,
		PolicyKey:         policy.Key,
		PolicyVersion:     policy.Version,
		CapabilityKeys:    issueDraftCapabilityKeys(capabilities),
		CapabilityVersion: issueDraftCapabilityVersion,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create issue draft")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue draft session")
		return
	}

	writeJSON(w, http.StatusCreated, CreateIssueDraftSessionResponse{
		SessionID: uuidToString(session.ID),
		AgentID:   uuidToString(carrier.ID),
		RuntimeID: uuidToString(carrier.RuntimeID),
		Draft:     issueDraftToResponse(created),
	})
}

// validIssueDraftBody normalises and bounds a client-supplied draft object.
// Empty means "no seed", which stores the table default rather than an
// unparseable body.
func validIssueDraftBody(w http.ResponseWriter, raw json.RawMessage) ([]byte, bool) {
	if len(raw) == 0 {
		return []byte("{}"), true
	}
	if len(raw) > maxIssueDraftBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "draft is too large")
		return nil, false
	}
	if !json.Valid(raw) {
		writeError(w, http.StatusBadRequest, "draft must be valid JSON")
		return nil, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		writeError(w, http.StatusBadRequest, "draft must be a JSON object")
		return nil, false
	}
	return raw, true
}

// IssueDraftSummary is one alignment conversation, as the list endpoint
// renders it. `status` says which kind of record it is: the two live states are
// resume candidates, the two terminal ones are records to read back (DENE-371).
type IssueDraftSummary struct {
	issueDraftResponse
	Title              string `json:"title"`
	RuntimeID          string `json:"runtime_id"`
	LastMessageContent string `json:"last_message_content"`
	LastMessageRole    string `json:"last_message_role"`
	LastMessageAt      string `json:"last_message_at"`
}

type ListIssueDraftsResponse struct {
	Drafts []IssueDraftSummary `json:"drafts"`
}

// issueDraftStatusesForList maps the list's `status` filter onto the draft
// statuses it selects. The default is the two live states — "which alignments
// can I still act on", which is what every caller wanted before DENE-371 and
// what an absent parameter must keep meaning. `all` adds the two terminal ones,
// which is the record half: a conversation that produced an issue, or was
// abandoned, is still there to be read back.
//
// An unrecognised value falls back to the default rather than 400: the filter
// only ever widens or narrows a read of the caller's own rows, and a client
// asking with a future value should see the safe subset, not an error.
func issueDraftStatusesForList(qualifier string) []string {
	if qualifier == "all" {
		return []string{"draft", "ready", "completed", "abandoned"}
	}
	return []string{"draft", "ready"}
}

// ListIssueDrafts returns the caller's alignment conversations, narrowed by the
// optional `status` filter. This is the only way back into one: the carrier is
// `kind = 'system'`, so the conversation is invisible to every chat surface by
// construction.
func (h *Handler) ListIssueDrafts(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	rows, err := h.Queries.ListIssueDraftsByCreator(r.Context(), db.ListIssueDraftsByCreatorParams{
		WorkspaceID: workspaceUUID,
		CreatorID:   parseUUID(userID),
		Statuses:    issueDraftStatusesForList(r.URL.Query().Get("status")),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issue drafts")
		return
	}

	drafts := make([]IssueDraftSummary, 0, len(rows))
	for _, row := range rows {
		drafts = append(drafts, IssueDraftSummary{
			issueDraftResponse: issueDraftToResponse(db.IssueDraft{
				ChatSessionID:     row.ChatSessionID,
				WorkspaceID:       row.WorkspaceID,
				Status:            row.Status,
				Revision:          row.Revision,
				Draft:             row.Draft,
				IssueID:           row.IssueID,
				PolicyKey:         row.PolicyKey,
				PolicyVersion:     row.PolicyVersion,
				CapabilityKeys:    row.CapabilityKeys,
				CapabilityVersion: row.CapabilityVersion,
				FinalizeRound:     row.FinalizeRound,
				FinalizedRevision: row.FinalizedRevision,
				CreatedAt:         row.CreatedAt,
				UpdatedAt:         row.UpdatedAt,
			}),
			Title:              row.Title,
			RuntimeID:          uuidToString(row.RuntimeID),
			LastMessageContent: row.LastMessageContent,
			LastMessageRole:    row.LastMessageRole,
			LastMessageAt:      timestampToString(row.LastMessageAt),
		})
	}
	writeJSON(w, http.StatusOK, ListIssueDraftsResponse{Drafts: drafts})
}

type UpdateIssueDraftRequest struct {
	Draft json.RawMessage `json:"draft"`
	// Status moves the alignment lifecycle. Only the two live states are
	// writable here: 'completed' is finalize's to set and 'abandoned' is
	// abandon's, so neither can be reached by a plain save.
	Status string `json:"status,omitempty"`
	// ExpectedRevision is the revision the caller was looking at. Required —
	// a save with no opinion about what it is overwriting is exactly the
	// lost-update this protocol exists to prevent.
	ExpectedRevision *int64 `json:"expected_revision"`
}

// UpdateIssueDraft saves the state an alignment conversation has arrived at.
func (h *Handler) UpdateIssueDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateIssueDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	if len(req.Draft) == 0 {
		writeError(w, http.StatusBadRequest, "draft is required")
		return
	}
	draft, ok := validIssueDraftBody(w, req.Draft)
	if !ok {
		return
	}
	status := req.Status
	if status == "" {
		status = "draft"
	}
	if status != "draft" && status != "ready" {
		writeError(w, http.StatusBadRequest, "status must be draft or ready")
		return
	}

	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}

	updated, err := h.Queries.UpdateIssueDraft(r.Context(), db.UpdateIssueDraftParams{
		ChatSessionID:    session.ID,
		WorkspaceID:      session.WorkspaceID,
		Draft:            draft,
		Status:           status,
		ExpectedRevision: *req.ExpectedRevision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeIssueDraftWriteConflict(w, r, session, "save")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save issue draft")
		return
	}
	writeJSON(w, http.StatusOK, issueDraftToResponse(updated))
}

// AbandonIssueDraft discards an alignment conversation's draft. The
// conversation itself is left alone — deleting it is the ordinary chat delete,
// which prunes this row too.
func (h *Handler) AbandonIssueDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}

	updated, err := h.Queries.MarkIssueDraftAbandoned(r.Context(), db.MarkIssueDraftAbandonedParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeIssueDraftWriteConflict(w, r, session, "abandon")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to abandon issue draft")
		return
	}
	writeJSON(w, http.StatusOK, issueDraftToResponse(updated))
}

// ReopenIssueDraft starts another round on an alignment that already produced
// its group, so the same conversation can be continued and confirmed again.
//
// It is the same draft row and the same chat session, never a new one: the
// group's identity is derived from the session (issueDraftNodeID), so a second
// session would mean a second group rather than more work in this one. What
// moves is the lifecycle — back to 'ready' — plus finalize_round, which is what
// lets the next confirm tell "these payload nodes are an increment" apart from
// "this group is already committed, adopt it".
//
// Idempotent by construction. Only a 'completed' row reopens, so a second call
// (a retried request, a double click) matches nothing and answers with the row
// as it stands rather than counting the round twice. A draft that is still
// 'draft' or 'ready' has an open round already and is answered the same way;
// an abandoned one is refused, because a discarded alignment does not come
// back to life.
//
// The lock is the one every draft write uses, so a reopen cannot interleave
// with a confirm deciding on the same row: either the confirm completed first
// and this reopens that round, or this reopens first and the confirm creates
// from the reopened revision.
func (h *Handler) ReopenIssueDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reopen issue draft")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	locked, err := qtx.LockIssueDraftInWorkspace(r.Context(), db.LockIssueDraftInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue draft not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock issue draft")
		return
	}

	reopened := locked
	switch locked.Status {
	case "completed":
		reopened, err = qtx.ReopenIssueDraft(r.Context(), db.ReopenIssueDraftParams{
			ChatSessionID: session.ID,
			WorkspaceID:   session.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reopen issue draft")
			return
		}
	case "abandoned":
		writeError(w, http.StatusConflict, "this draft has been abandoned")
		return
	default:
		// 'draft' or 'ready': the round is already open. Decided on the locked
		// row, so this is a decision about the state the row is in now, not
		// about the state it was in when the request was sent.
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue draft reopen")
		return
	}
	writeJSON(w, http.StatusOK, issueDraftToResponse(reopened))
}

type SwitchIssueDraftPolicyRequest struct {
	// Policy is the key of the alignment policy to run from the next turn on.
	Policy string `json:"policy"`
}

// SwitchIssueDraftPolicy swaps the questioning behaviour of a live alignment
// conversation: guided interview, or plain dialogue.
//
// Two writes, one decision. The draft row records which policy and prompt
// version is in force — that is the audit trail — and the carrier agent's
// instructions are replaced with that policy's prompt, which is the only thing
// that actually changes the next reply (the daemon reads instructions off the
// claimed agent). Writing one without the other would either leave a session
// behaving like its old policy while claiming the new one, or run a prompt
// nothing points at.
//
// The pending-task gate mirrors the runtime switch: a reply already in flight
// was claimed with the previous instructions, so switching under it would make
// the next message look like the switch did not take. The client is expected to
// stop the reply first.
func (h *Handler) SwitchIssueDraftPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SwitchIssueDraftPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	policyKey := strings.TrimSpace(req.Policy)
	if policyKey == "" {
		writeError(w, http.StatusBadRequest, "policy is required")
		return
	}
	// An unknown key is refused rather than defaulted: this endpoint exists to
	// make the running prompt an explicit, auditable choice.
	policy, ok := issueDraftPolicyByKey(policyKey)
	if !ok {
		writeError(w, http.StatusBadRequest, issueDraftPolicyUnknownMessage())
		return
	}

	// Owner-scoped and carrier-checked, like every other write on a draft.
	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to switch issue draft policy")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Same lock as the runtime switch, and for the same reason: it serialises
	// this against a send that is stamping a task with the carrier it read.
	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	if _, err := qtx.GetPendingChatTask(r.Context(), session.ID); err == nil {
		writeError(w, http.StatusConflict, "stop the current reply before switching policy")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check pending draft task")
		return
	}

	// The switch rewrites the whole installed prompt, so the capabilities have
	// to be carried across it: the row's own recorded set plus whatever the new
	// policy requires. Never the new policy's requirements alone — turning the
	// guidance down is not a request to have the user's other methods dropped,
	// and never the request's, because this endpoint does not take one.
	//
	// A capability the running build has retired is dropped from the prompt
	// instead of failing the switch: this is a live conversation being changed,
	// not a prompt being reconstructed, and refusing to switch because a method
	// no longer exists would strand it.
	recorded, err := qtx.GetIssueDraftInWorkspace(r.Context(), db.GetIssueDraftInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeIssueDraftWriteConflict(w, r, session, "switch policy on")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read issue draft capabilities")
		return
	}
	required, err := issueDraftPolicyRequiredCapabilities(policy)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve alignment capabilities")
		return
	}
	capabilities := issueDraftMergeCapabilities(
		issueDraftCapabilitiesFromKeys(recorded.CapabilityKeys),
		required,
	)

	if _, err := qtx.UpdateIssueDraftCarrierInstructions(r.Context(), db.UpdateIssueDraftCarrierInstructionsParams{
		ID:           session.AgentID,
		Instructions: policy.Instructions(capabilities),
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue draft session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update issue draft carrier")
		return
	}

	updated, err := qtx.UpdateIssueDraftPolicy(r.Context(), db.UpdateIssueDraftPolicyParams{
		ChatSessionID:     session.ID,
		WorkspaceID:       session.WorkspaceID,
		PolicyKey:         policy.Key,
		PolicyVersion:     policy.Version,
		CapabilityKeys:    issueDraftCapabilityKeys(capabilities),
		CapabilityVersion: issueDraftCapabilityVersion,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeIssueDraftWriteConflict(w, r, session, "switch policy on")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record issue draft policy")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue draft policy switch")
		return
	}

	writeJSON(w, http.StatusOK, issueDraftToResponse(updated))
}

// writeIssueDraftWriteConflict turns a zero-row write into the reason it
// matched nothing. The guarded UPDATEs fold three different situations into one
// empty result — no draft, a terminal draft, a stale revision — and a client
// that is told only "conflict" cannot decide whether to reload, navigate to the
// created issue, or start over.
func (h *Handler) writeIssueDraftWriteConflict(w http.ResponseWriter, r *http.Request, session db.ChatSession, verb string) {
	current, err := h.Queries.GetIssueDraftInWorkspace(r.Context(), db.GetIssueDraftInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue draft not found")
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to %s issue draft", verb))
		return
	}
	switch current.Status {
	case "completed":
		writeError(w, http.StatusConflict, "this draft has already been created as an issue")
	case "abandoned":
		writeError(w, http.StatusConflict, "this draft has been abandoned")
	default:
		writeError(w, http.StatusConflict, "this draft changed since you loaded it; reload and try again")
	}
}

type FinalizeIssueDraftRequest struct {
	ExpectedRevision *int64 `json:"expected_revision"`
	// NewProject, when set, is created in the same transaction as the issues.
	// The carrier never sends this: the confirm panel does, after the person
	// has accepted a proposal. A directory the panel already created is named
	// by Directory; if this request fails before commit, that directory is the
	// caller's to remove.
	NewProject *FinalizeNewProjectRequest `json:"new_project,omitempty"`
}

// FinalizeNewProjectRequest is the project the confirm will create.
// Directory is a local_directory resource_ref; absent means no directory.
type FinalizeNewProjectRequest struct {
	Title       string          `json:"title"`
	Description *string         `json:"description"`
	Icon        *string         `json:"icon"`
	Directory   json.RawMessage `json:"directory,omitempty"`
}

type FinalizeIssueDraftResponse struct {
	Draft   issueDraftResponse `json:"draft"`
	IssueID string             `json:"issue_id"`
	// Issues is the whole group the confirm produced, root first. Its meaning
	// for a client that does not know the field — an older build — is simply
	// "the group is the one issue named by issue_id", which is exactly what a
	// group with no children is. See issueDraftCreatedIssues.
	Issues []IssueDraftCreatedIssue `json:"issues"`
	// AssignmentWarnings names the nodes that were created unassigned because
	// the assignee the draft carried could not be applied. Omitted when every
	// assignment landed, so a client that predates the field reads a plain
	// successful confirm — which is what it was (DENE-694).
	AssignmentWarnings []IssueDraftAssignmentWarning `json:"assignment_warnings,omitempty"`
}

// IssueDraftAssignmentWarning is one node of a confirmed group whose assignee
// was dropped. The issue itself was created; it is simply unassigned. Key is
// empty for the root, which is how a client maps the warning back onto the row
// it shows.
type IssueDraftAssignmentWarning struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// FinalizeIssueDraft is the single point where an alignment conversation
// becomes real work. It runs in three steps, each short-lived:
//
//  1. Under LockIssueDraftInWorkspace: decide. A completed draft answers with
//     the group it already made — that is what makes a double-clicked confirm,
//     a retried request and a second tab all safe. Anything not 'ready', or at
//     a revision the caller was not looking at, is refused here and nothing is
//     created.
//  2. Create the group, or adopt the one an earlier attempt already created.
//     "At most one issue per alignment NODE" is enforced by the database — the
//     partial unique index on issue (origin_id) WHERE origin_type =
//     'issue_draft' (migration 486) — not by how long a lock is held. Two
//     confirms that both get past step 1 cannot both create: the root node's
//     origin_id is the chat session id on both sides, so one of them gets a
//     unique violation and adopts the winner's whole group.
//  3. Under the lock again: point the draft at the group's root.
//
// The lock is deliberately NOT held across step 2. IssueService.CreateGroup
// opens its own transaction, so holding one here would make every confirm
// occupy two pool connections at once for the whole of issue creation and
// enqueue — enough concurrent confirms would deadlock on the pool rather than
// on each other. The constraint gets sharper as a group grows, not weaker:
// a group's transaction is longer than a single issue's. Correctness does not
// need the lock: the unique index is the authority, and step 3 re-decides under
// the lock on a re-read row.
//
// One window is accepted rather than closed: a save landing between steps 1
// and 2 would be created from the payload validated in step 1. An alignment
// conversation has a single editor on one screen (the same assumption
// agent_builder_draft documents), so that save and that confirm are the same
// person, and expected_revision already rejects a confirm from a client that
// was looking at an older draft. What that window can no longer do is produce a
// SECOND group: re-keying every child changes the children's ids, but not the
// root's, so the second confirm collides on the root and adopts the first
// group.
func (h *Handler) FinalizeIssueDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req FinalizeIssueDraftRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}

	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}

	ready, done, ok := h.admitIssueDraftForFinalize(w, r, session, *req.ExpectedRevision)
	if !ok {
		return
	}
	if done != nil {
		writeJSON(w, http.StatusOK, *done)
		return
	}

	state, ok := h.readIssueDraftGroupState(w, r, session)
	if !ok {
		return
	}
	group, warnings, ok := h.issueGroupParamsFromDraft(w, r, workspaceID, session, ready, state)
	if !ok {
		return
	}
	// A project the person accepted is created with the issues, and only on
	// the confirm that inserts the root. A retry that finds the group already
	// committed adopts it and must not create a second project.
	if req.NewProject != nil && !state.HasRoot && !group.RootIssueID.Valid {
		project, projectOK := h.issueGroupProjectFromRequest(w, r, session, userID, group, req.NewProject)
		if !projectOK {
			return
		}
		group.Project = project
	}
	issues, created, ok := h.createIssueGroupForDraft(w, r, session, state, group)
	if !ok {
		return
	}
	// The prototypes the conversation produced ride on the group's root, which
	// is issues[0] on every path this far: the created group, the one an
	// earlier confirm left behind, and the one a continuation round appended
	// to. Sub-issues reference them by markdown link instead (DENE-453).
	h.carryIssueDraftAttachments(r, session, issues[0])
	completed, ok := h.completeIssueDraft(w, r, session, issues)
	if !ok {
		return
	}
	// Only a confirm that wrote the rows may claim their assignees were dropped:
	// an adopted group belongs to the confirm that created it.
	if created {
		completed.AssignmentWarnings = warnings
		// The rows this confirm wrote never went through the create hook, so
		// routing seats them here: every node the preview left unheld gets an
		// executor, and the root its reviewer (DENE-812). Filling only empty
		// slots makes an adopted node in the same group a no-op.
		for _, issue := range issues {
			h.RouteGroupNodeAsync(r, uuidToString(session.WorkspaceID), uuidToString(issue.ID))
		}
	}
	writeJSON(w, http.StatusOK, *completed)
}

// admitIssueDraftForFinalize is step 1. It returns either the ready draft to
// create from, or the response for a draft that has already been confirmed.
func (h *Handler) admitIssueDraftForFinalize(w http.ResponseWriter, r *http.Request, session db.ChatSession, expectedRevision int64) (db.IssueDraft, *FinalizeIssueDraftResponse, bool) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize issue draft")
		return db.IssueDraft{}, nil, false
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	locked, err := qtx.LockIssueDraftInWorkspace(r.Context(), db.LockIssueDraftInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue draft not found")
			return db.IssueDraft{}, nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to lock issue draft")
		return db.IssueDraft{}, nil, false
	}

	// Decided on the locked row, never on anything read before the lock: a
	// confirm that blocked here resumes holding the pre-block values.
	switch locked.Status {
	case "completed":
		if !locked.IssueID.Valid {
			writeError(w, http.StatusInternalServerError, "completed draft has no issue")
			return db.IssueDraft{}, nil, false
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to finalize issue draft")
			return db.IssueDraft{}, nil, false
		}
		// A repeated confirm has to answer with the whole group, not just the
		// parent: the second click of a double click must show the user what
		// the first one showed, or the confirmation page quietly degrades from
		// "these five issues" to "this one issue".
		issues, ok := h.issueDraftGroupResponse(w, r, session)
		if !ok {
			return db.IssueDraft{}, nil, false
		}
		return db.IssueDraft{}, &FinalizeIssueDraftResponse{
			Draft:   issueDraftToResponse(locked),
			IssueID: uuidToString(locked.IssueID),
			Issues:  issues,
		}, true
	case "abandoned":
		writeError(w, http.StatusConflict, "this draft has been abandoned")
		return db.IssueDraft{}, nil, false
	case "ready":
		// The only state a confirm may act on.
	default:
		writeError(w, http.StatusConflict, "draft is not ready to be created")
		return db.IssueDraft{}, nil, false
	}
	if locked.Revision != expectedRevision {
		writeError(w, http.StatusConflict, "this draft changed since you loaded it; reload and try again")
		return db.IssueDraft{}, nil, false
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finalize issue draft")
		return db.IssueDraft{}, nil, false
	}
	return locked, nil, true
}

// completeIssueDraft is step 3: point the draft at the group that now exists
// for it. A draft completed by a racing confirm answers with its own group,
// which the unique index guarantees is the same one.
//
// `issues` is the committed group, root first. Its root is what
// issue_draft.issue_id records; the whole slice is what the response hands back.
func (h *Handler) completeIssueDraft(w http.ResponseWriter, r *http.Request, session db.ChatSession, issues []db.Issue) (*FinalizeIssueDraftResponse, bool) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, "committed", "failed to complete issue draft")
		return nil, false
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	locked, err := qtx.LockIssueDraftInWorkspace(r.Context(), db.LockIssueDraftInWorkspaceParams{
		ChatSessionID: session.ID,
		WorkspaceID:   session.WorkspaceID,
	})
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, "committed", "failed to lock issue draft")
		return nil, false
	}
	completed := locked
	if locked.Status != "completed" {
		completed, err = qtx.MarkIssueDraftCompleted(r.Context(), db.MarkIssueDraftCompletedParams{
			ChatSessionID: session.ID,
			WorkspaceID:   session.WorkspaceID,
			IssueID:       issues[0].ID,
		})
		if err != nil {
			// Never swallowed: returning 200 with a zero-valued draft here
			// would tell the client an issue was created and hand it an empty
			// id for the issue that actually exists.
			writeErrorCode(w, http.StatusInternalServerError, "committed", "failed to complete issue draft")
			return nil, false
		}
	} else if !completed.IssueID.Valid {
		writeErrorCode(w, http.StatusInternalServerError, "committed", "completed draft has no issue")
		return nil, false
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErrorCode(w, http.StatusInternalServerError, "committed", "failed to commit issue draft finalize")
		return nil, false
	}
	return &FinalizeIssueDraftResponse{
		Draft:   issueDraftToResponse(completed),
		IssueID: uuidToString(completed.IssueID),
		Issues:  issueDraftCreatedIssues(issues, h.getIssuePrefix(r.Context(), session.WorkspaceID)),
	}, true
}

// writeIssueDraftCreateError maps an IssueService.Create failure onto the same
// status codes the ordinary create endpoint returns, so a draft confirm that
// fails for an ordinary reason (archived status, project removed, issue limit)
// says so instead of reporting a generic server error. ErrActiveDuplicate is
// absent by construction: a draft is confirmed by a human who has just read it,
// so the confirm passes AllowDuplicate.
func writeIssueDraftCreateError(w http.ResponseWriter, r *http.Request, err error) {
	// rolled_back: the group transaction did not commit, so a directory the
	// client created for a new project is an orphan and should be removed.
	switch {
	case errors.Is(err, service.ErrParentIssueNotFound):
		writeErrorCode(w, http.StatusBadRequest, "rolled_back", "parent issue not found in this workspace")
	case errors.Is(err, service.ErrProjectNotFound):
		writeErrorCode(w, http.StatusBadRequest, "rolled_back", "project not found in this workspace")
	case errors.Is(err, service.ErrIssueStatusUnavailable):
		writeErrorCode(w, http.StatusConflict, "rolled_back",
			"the target status was archived while this request was in flight; reload the status list and retry")
	default:
		if writeIssueLimitReached(w, err) {
			return
		}
		slog.Warn("finalize issue draft failed", append(logger.RequestAttrs(r), "error", err)...)
		writeErrorCode(w, http.StatusInternalServerError, "rolled_back", "failed to create issue from draft")
	}
}

type SwitchIssueDraftRuntimeRequest struct {
	RuntimeID string `json:"runtime_id"`
}

type SwitchIssueDraftRuntimeResponse struct {
	RuntimeID string `json:"runtime_id"`
}

// SwitchIssueDraftRuntime re-points a live alignment conversation at another
// runtime. Same contract as SwitchAgentBuilderRuntime: the carrier is what
// stamps a chat task's runtime, so rebinding it under
// LockChatSessionForRuntimeBind is the only way "no reply is in flight" and
// "this conversation now runs on B" become one serialised decision.
func (h *Handler) SwitchIssueDraftRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req SwitchIssueDraftRuntimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}

	session, ok := h.loadIssueDraftSession(w, r, userID, workspaceID)
	if !ok {
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	runtime, ok := h.resolveSessionCarrierRuntime(w, r, workspaceID, workspaceUUID, runtimeID, "an issue draft session", "switch")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to switch issue draft runtime")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}
	if _, err := qtx.GetPendingChatTask(r.Context(), session.ID); err == nil {
		writeError(w, http.StatusConflict, "stop the current reply before switching runtime")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check pending issue draft task")
		return
	}

	updated, err := qtx.RebindIssueDraftRuntime(r.Context(), db.RebindIssueDraftRuntimeParams{
		ID:          session.AgentID,
		RuntimeID:   runtime.ID,
		RuntimeMode: runtime.RuntimeMode,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue draft session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to switch issue draft runtime")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue draft runtime switch")
		return
	}

	writeJSON(w, http.StatusOK, SwitchIssueDraftRuntimeResponse{RuntimeID: uuidToString(updated.RuntimeID)})
}
