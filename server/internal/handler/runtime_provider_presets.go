package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Provider preset request store
// ---------------------------------------------------------------------------
//
// "Edit this machine's provider presets" has the same shape as every other
// runtime-scoped action the server cannot perform itself: the daemon sits
// behind the user's NAT and only polls, so the request is parked in a store,
// claimed on the next heartbeat, executed on the host, and reported back.
//
// The store therefore has to be coherent across API replicas — the POST, the
// heartbeat and the poll can each land on a different node — which is why the
// multi-node deployment uses the Redis implementation.
//
// CREDENTIAL HANDLING. An upsert payload carries the API key the user typed.
// It has exactly one destination, the daemon's credentials file, and this file
// is where that promise is kept:
//
//   - the request record never holds it once claimed. PopPending hands the
//     key-bearing copy to the heartbeat frame and persists the record with the
//     key removed, so delivery and "do not keep it" are both true.
//   - the poll response never holds it. Get returns a copy with the key
//     removed, which also means a poll cannot blank the record a later
//     heartbeat still has to deliver.
//   - a payload the server cannot rewrite is dropped rather than persisted.
//     Redaction has to be able to find the field; a body it cannot parse is
//     not one to guess about.
//
// The record is transient by construction (2 minute retention, in-memory or
// Redis TTL) — there is no table, so a key never reaches durable storage.

// ProviderPresetStatus represents the lifecycle of a provider preset request.
type ProviderPresetStatus string

const (
	ProviderPresetPending   ProviderPresetStatus = "pending"
	ProviderPresetRunning   ProviderPresetStatus = "running"
	ProviderPresetCompleted ProviderPresetStatus = "completed"
	ProviderPresetFailed    ProviderPresetStatus = "failed"
	ProviderPresetTimeout   ProviderPresetStatus = "timeout"
)

// Provider preset actions. Mirrors the daemon's own action names.
const (
	ProviderPresetActionList     = "list"
	ProviderPresetActionModels   = "models"
	ProviderPresetActionUpsert   = "upsert"
	ProviderPresetActionDelete   = "delete"
	ProviderPresetActionActivate = "activate"
	// ProviderPresetActionReplay rewrites the presets this machine already
	// saved back into a DSH installation whose configuration was reset or
	// upgraded away. It carries no payload and no credential.
	ProviderPresetActionReplay = "replay"
)

// ProviderPresetEntry mirrors the daemon's wire shape for one provider preset.
// It has no field for the key: `key_mask` is generated on the machine holding
// the credentials file and is all the server ever sees.
type ProviderPresetEntry struct {
	ID        string                `json:"id"`
	API       string                `json:"api,omitempty"`
	BaseURL   string                `json:"base_url,omitempty"`
	APIKeyEnv string                `json:"api_key_env,omitempty"`
	KeyMask   string                `json:"key_mask,omitempty"`
	HasKey    bool                  `json:"has_key"`
	Active    bool                  `json:"active,omitempty"`
	Models    []ProviderPresetModel `json:"models"`
}

// ProviderPresetModel is one selectable model of a preset.
type ProviderPresetModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// ProviderPresetActive is the pair currently written into the agent's default
// model setting.
type ProviderPresetActive struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ProviderPresetRequest is a parked or finished provider-preset action.
//
// Payload is opaque to the server — it is forwarded to the daemon unchanged —
// and it is redacted on every read path that outlives the claim (see the
// credential handling note above).
type ProviderPresetRequest struct {
	ID        string               `json:"id"`
	RuntimeID string               `json:"runtime_id"`
	Provider  string               `json:"provider"`
	Action    string               `json:"action"`
	Payload   json.RawMessage      `json:"payload,omitempty"`
	Status    ProviderPresetStatus `json:"status"`
	// Providers is the refreshed preset list every successful action answers
	// with, so the client redraws from this reply alone.
	Providers []ProviderPresetEntry `json:"providers,omitempty"`
	Active    *ProviderPresetActive `json:"active,omitempty"`
	// ClearedActive marks a delete that also emptied the active-model setting,
	// which the UI has to say out loud — the user just lost their default model.
	ClearedActive bool `json:"cleared_active,omitempty"`
	// Models is the endpoint's own catalog, filled only by the models action.
	Models []ProviderPresetModel `json:"models,omitempty"`
	Error  string                `json:"error,omitempty"`
	// ErrorKind is the machine-readable classification of Error. A surface
	// renders localized copy from the kind and its parameters; Error is the
	// fallback for one it does not recognize.
	ErrorKind   string            `json:"error_kind,omitempty"`
	ErrorParams map[string]string `json:"error_params,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	// RunStartedAt is server-side bookkeeping, kept out of the HTTP response
	// the same way ModelListRequest does.
	RunStartedAt *time.Time `json:"-"`
}

const (
	providerPresetPendingTimeout = 30 * time.Second
	providerPresetRunningTimeout = 60 * time.Second
	providerPresetStoreRetention = 2 * time.Minute
)

// providerPresetTerminal reports whether no further report can change the
// record.
func providerPresetTerminal(status ProviderPresetStatus) bool {
	return status == ProviderPresetCompleted || status == ProviderPresetFailed || status == ProviderPresetTimeout
}

// providerPresetActionWrites reports whether an action edits the host's own
// agent configuration rather than only reading it.
//
// The read gate is the right one for `list`: it reports env var names, key
// masks and model ids, which is the same capability discovery the local-skill
// and model-list inventories expose to any member of a public runtime.
//
// `models` is not a read despite writing nothing. It takes a baseURL from the
// caller and makes the owner's daemon send a request — with the owner's stored
// credential attached when the caller names a preset instead of typing a key —
// so a member who could call it could point the owner's key at a host of their
// choosing. Reading a key mask is discovery; spending an owner's key is not.
//
// The other three edit the owner's files outright. `upsert` writes a key into
// the owner's .credentials.yaml and can point an existing apiKeyEnv at a new
// baseURL — which sends the owner's real key to whatever endpoint the caller
// named — and `activate` decides which provider every agent run on that
// machine then uses.
// This repository already draws that line: local-skill *import* stays
// owner-only even for workspace owners and admins because it touches the
// owner's files, and a private machine has no admin override at all
// (canUseRuntimeForAgent, MUL-6126). Writing the owner's credentials is at
// least that sensitive, so it gets the same rule.
func providerPresetActionWrites(action string) bool {
	return action != ProviderPresetActionList
}

// ownsRuntime reports whether member is the runtime's owner. Deliberately not
// canEditRuntime: that one lets workspace owners and admins through, and an
// administrative role is not consent to write someone else's credentials file.
func ownsRuntime(member db.Member, rt db.AgentRuntime) bool {
	return rt.OwnerID.Valid && uuidToString(rt.OwnerID) == uuidToString(member.UserID)
}

func validProviderPresetAction(action string) bool {
	switch action {
	case ProviderPresetActionList, ProviderPresetActionModels,
		ProviderPresetActionUpsert, ProviderPresetActionDelete,
		ProviderPresetActionActivate, ProviderPresetActionReplay:
		return true
	default:
		return false
	}
}

// applyProviderPresetTimeout transitions a stuck request to timeout so the
// client's poll terminates. The running escape catches "the daemon claimed it
// but its report never landed", which is otherwise indistinguishable from a
// completed request that lost its result.
func applyProviderPresetTimeout(req *ProviderPresetRequest, now time.Time) bool {
	switch req.Status {
	case ProviderPresetPending:
		if now.Sub(req.CreatedAt) > providerPresetPendingTimeout {
			req.Status = ProviderPresetTimeout
			req.Error = "daemon did not respond within 30 seconds"
			req.UpdatedAt = now
			return true
		}
	case ProviderPresetRunning:
		if req.RunStartedAt != nil && now.Sub(*req.RunStartedAt) > providerPresetRunningTimeout {
			req.Status = ProviderPresetTimeout
			req.Error = "daemon did not finish within 60 seconds"
			req.UpdatedAt = now
			return true
		}
	}
	return false
}

// redactProviderPresetPayload removes api_key from an action body.
//
// A body that is not a JSON object cannot be rewritten field by field, so it is
// dropped outright: the caller is about to store something that may be a
// credential, and "we could not tell" is not a reason to keep it.
func redactProviderPresetPayload(payload json.RawMessage) json.RawMessage {
	if len(payload) == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil
	}
	if _, ok := body["api_key"]; !ok {
		return payload
	}
	delete(body, "api_key")
	redacted, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	return redacted
}

// redactedProviderPresetCopy returns a copy safe to hand back over HTTP. It is
// a copy on purpose: blanking the stored record here would strip the key from
// the heartbeat delivery that has not happened yet.
func redactedProviderPresetCopy(req *ProviderPresetRequest) *ProviderPresetRequest {
	clone := *req
	clone.Payload = redactProviderPresetPayload(req.Payload)
	return &clone
}

// ProviderPresetResult is what a successful action reports back.
type ProviderPresetResult struct {
	Providers     []ProviderPresetEntry `json:"providers,omitempty"`
	Active        *ProviderPresetActive `json:"active,omitempty"`
	ClearedActive bool                  `json:"cleared_active,omitempty"`
	Models        []ProviderPresetModel `json:"models,omitempty"`
}

// ProviderPresetFailure is what a failed action reports back: a stable kind
// the UI switches on, the parameters its copy interpolates, and a
// human-readable message for everything else.
type ProviderPresetFailure struct {
	Kind    string            `json:"kind,omitempty"`
	Message string            `json:"message,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

// ProviderPresetStore is the contract every backend must satisfy.
type ProviderPresetStore interface {
	Create(ctx context.Context, runtimeID, provider, action string, payload json.RawMessage) (*ProviderPresetRequest, error)
	// Get returns a redacted copy for the polling client.
	Get(ctx context.Context, id string) (*ProviderPresetRequest, error)
	HasPending(ctx context.Context, runtimeID string) (bool, error)
	// PopPending claims the oldest pending request and returns the record WITH
	// its payload, which is what the heartbeat delivers to the daemon. The
	// persisted copy is redacted.
	PopPending(ctx context.Context, runtimeID string) (*ProviderPresetRequest, error)
	Complete(ctx context.Context, id string, result ProviderPresetResult) error
	Fail(ctx context.Context, id string, failure ProviderPresetFailure) error
}

// InMemoryProviderPresetStore is the single-node implementation used by
// self-hosted deployments and the test suite.
type InMemoryProviderPresetStore struct {
	mu       sync.Mutex
	requests map[string]*ProviderPresetRequest
}

func NewInMemoryProviderPresetStore() *InMemoryProviderPresetStore {
	return &InMemoryProviderPresetStore{requests: make(map[string]*ProviderPresetRequest)}
}

func (s *InMemoryProviderPresetStore) Create(_ context.Context, runtimeID, provider, action string, payload json.RawMessage) (*ProviderPresetRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, req := range s.requests {
		if time.Since(req.CreatedAt) > providerPresetStoreRetention {
			delete(s.requests, id)
		}
	}
	now := time.Now()
	req := &ProviderPresetRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Provider:  provider,
		Action:    action,
		Payload:   payload,
		Status:    ProviderPresetPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.requests[req.ID] = req
	return req, nil
}

func (s *InMemoryProviderPresetStore) Get(_ context.Context, id string) (*ProviderPresetRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	req, ok := s.requests[id]
	if !ok {
		return nil, nil
	}
	applyProviderPresetTimeout(req, time.Now())
	return redactedProviderPresetCopy(req), nil
}

func (s *InMemoryProviderPresetStore) HasPending(_ context.Context, runtimeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, req := range s.requests {
		applyProviderPresetTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == ProviderPresetPending {
			return true, nil
		}
	}
	return false, nil
}

func (s *InMemoryProviderPresetStore) PopPending(_ context.Context, runtimeID string) (*ProviderPresetRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var oldest *ProviderPresetRequest
	now := time.Now()
	for _, req := range s.requests {
		applyProviderPresetTimeout(req, now)
		if req.RuntimeID == runtimeID && req.Status == ProviderPresetPending {
			if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
				oldest = req
			}
		}
	}
	if oldest == nil {
		return nil, nil
	}

	delivery := *oldest
	oldest.Status = ProviderPresetRunning
	startedAt := now
	oldest.RunStartedAt = &startedAt
	oldest.UpdatedAt = now
	// The key leaves with the delivery above; the record must not keep it.
	oldest.Payload = redactProviderPresetPayload(oldest.Payload)
	return &delivery, nil
}

func (s *InMemoryProviderPresetStore) Complete(_ context.Context, id string, result ProviderPresetResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = ProviderPresetCompleted
		req.Providers = result.Providers
		req.Active = result.Active
		req.ClearedActive = result.ClearedActive
		req.Models = result.Models
		req.Payload = redactProviderPresetPayload(req.Payload)
		req.UpdatedAt = time.Now()
	}
	return nil
}

func (s *InMemoryProviderPresetStore) Fail(_ context.Context, id string, failure ProviderPresetFailure) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req, ok := s.requests[id]; ok {
		req.Status = ProviderPresetFailed
		req.Error = failure.Message
		req.ErrorKind = failure.Kind
		req.ErrorParams = failure.Params
		req.Payload = redactProviderPresetPayload(req.Payload)
		req.UpdatedAt = time.Now()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// InitiateProviderPresetAction parks one provider-preset action for the
// runtime's daemon.
//
// The response is only {id, status}: the caller's own body may contain a key,
// and echoing it back adds a copy of the secret to a response that nothing
// needs it for. The refreshed list arrives through the poll.
func (h *Handler) InitiateProviderPresetAction(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, member, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}
	if rt.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "runtime is offline")
		return
	}

	var body struct {
		Provider string          `json:"provider"`
		Action   string          `json:"action"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	provider := strings.TrimSpace(body.Provider)
	action := strings.TrimSpace(body.Action)
	if provider == "" || action == "" {
		writeError(w, http.StatusBadRequest, "provider and action are required")
		return
	}
	if !validProviderPresetAction(action) {
		writeError(w, http.StatusBadRequest, "unsupported action")
		return
	}
	if providerPresetActionWrites(action) && !ownsRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "only the runtime owner can change its provider presets")
		return
	}

	resolvedRuntimeID := uuidToString(rt.ID)
	req, err := h.ProviderPresetStore.Create(r.Context(), resolvedRuntimeID, provider, action, body.Payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue provider preset request: "+err.Error())
		return
	}
	h.requestDaemonPendingWork(resolvedRuntimeID, protocol.PendingWorkKindProviderConfig)
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID, "status": string(req.Status)})
}

// GetProviderPresetRequest returns the status of a provider preset request.
func (h *Handler) GetProviderPresetRequest(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, runtimeID)
	if !ok {
		return
	}

	req, err := h.ProviderPresetStore.Get(r.Context(), chi.URLParam(r, "requestId"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if req == nil || req.RuntimeID != uuidToString(rt.ID) {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// ReportProviderPresetResult receives the action result from the daemon.
func (h *Handler) ReportProviderPresetResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")

	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}
	requestID := chi.URLParam(r, "requestId")

	// Read first so a duplicate report for an already-terminal request (the
	// heartbeat frame was retried, the first report landed) is a no-op instead
	// of rewriting the outcome.
	existing, err := h.ProviderPresetStore.Get(r.Context(), requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load request: "+err.Error())
		return
	}
	if existing == nil || existing.RuntimeID != runtimeID {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if providerPresetTerminal(existing.Status) {
		slog.Debug("ignoring stale provider preset report",
			"runtime_id", runtimeID, "request_id", requestID, "status", existing.Status)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	var body struct {
		Status        string                `json:"status"`
		Providers     []ProviderPresetEntry `json:"providers"`
		Active        *ProviderPresetActive `json:"active"`
		ClearedActive bool                  `json:"cleared_active"`
		Models        []ProviderPresetModel `json:"models"`
		Error         string                `json:"error"`
		ErrorKind     string                `json:"error_kind"`
		ErrorParams   map[string]string     `json:"error_params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.Status == "completed" {
		result := ProviderPresetResult{
			Providers:     body.Providers,
			Active:        body.Active,
			ClearedActive: body.ClearedActive,
			Models:        body.Models,
		}
		if err := h.ProviderPresetStore.Complete(r.Context(), requestID, result); err != nil {
			// 5xx so the daemon retries; a swallowed store failure would leave
			// the request in "running" until the server-side timeout.
			slog.Error("ProviderPresetStore Complete failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist completion")
			return
		}
	} else {
		failure := ProviderPresetFailure{
			Kind:    body.ErrorKind,
			Message: body.Error,
			Params:  body.ErrorParams,
		}
		if err := h.ProviderPresetStore.Fail(r.Context(), requestID, failure); err != nil {
			slog.Error("ProviderPresetStore Fail failed", "error", err, "request_id", requestID)
			writeError(w, http.StatusInternalServerError, "failed to persist failure")
			return
		}
	}

	slog.Debug("provider preset report",
		"runtime_id", runtimeID, "request_id", requestID,
		"status", body.Status, "providers", len(body.Providers),
		"models", len(body.Models), "error_kind", body.ErrorKind)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
