package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const routingAnalysisTimeout = 20 * time.Second

type routingAnalysisRequest struct {
	id, runtimeID, model, thinking, prompt string
	workspaceID, issueID, contentHash      string
	status                                 string
	result                                 string
	err                                    string
	created                                time.Time
}

// RoutingAnalysisStore is deliberately separate from task queues: analysis is
// a short, no-tools probe and must never consume a runtime work slot.
type RoutingAnalysisStore struct {
	mu    sync.Mutex
	items map[string]*routingAnalysisRequest
}

func NewRoutingAnalysisStore() *RoutingAnalysisStore {
	return &RoutingAnalysisStore{items: make(map[string]*routingAnalysisRequest)}
}

func (s *RoutingAnalysisStore) Create(_ context.Context, target routing.Target, prompt string) *routingAnalysisRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := randomID()
	r := &routingAnalysisRequest{id: id, runtimeID: target.RuntimeID, model: target.Model, thinking: target.ThinkingLevel, prompt: prompt, status: "pending", created: time.Now()}
	s.items[id] = r
	return r
}

func (s *RoutingAnalysisStore) PopPending(_ context.Context, runtimeID string) *protocol.DaemonHeartbeatPendingRoutingAnalysis {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.items {
		if r.runtimeID == runtimeID && r.status == "pending" {
			r.status = "running"
			return &protocol.DaemonHeartbeatPendingRoutingAnalysis{ID: r.id, Model: r.model, ThinkingLevel: r.thinking, Prompt: r.prompt}
		}
	}
	return nil
}

func (s *RoutingAnalysisStore) Complete(id, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.items[id]; r != nil {
		r.status = "completed"
		r.result = result
	}
}

func (s *RoutingAnalysisStore) Fail(id, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.items[id]; r != nil {
		r.status = "failed"
		r.err = message
	}
}

func (s *RoutingAnalysisStore) run(ctx context.Context, h *Handler, target routing.Target, prompt string) (string, error) {
	r := s.Create(ctx, target, prompt)
	if wsID, issueID, hash, ok := routing.AnalysisRequestFromContext(ctx); ok {
		s.mu.Lock()
		r.workspaceID, r.issueID, r.contentHash = wsID, issueID, hash
		s.mu.Unlock()
	}
	h.requestDaemonPendingWork(target.RuntimeID, protocol.PendingWorkKindRoutingAnalysis)
	deadline := time.NewTimer(routingAnalysisTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		status, result, errMsg := r.status, r.result, r.err
		s.mu.Unlock()
		if status == "completed" {
			return result, nil
		}
		if status == "failed" {
			return "", errors.New(errMsg)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", context.DeadlineExceeded
		case <-tick.C:
		}
	}
}

// runtimeRoutingPromptRunner adapts the handler queue to routing.Analyst.
type runtimeRoutingPromptRunner struct{ h *Handler }

func (r runtimeRoutingPromptRunner) Run(ctx context.Context, target routing.Target, prompt string) (string, error) {
	if r.h.RoutingAnalysisStore == nil {
		return "", errors.New("runtime analysis unavailable")
	}
	return r.h.RoutingAnalysisStore.run(ctx, r.h, target, prompt)
}

// ReportRoutingAnalysisResult completes the daemon-carried request. Results
// are raw JSON because routing owns the schema and validates it centrally.
func (h *Handler) ReportRoutingAnalysisResult(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}
	id := chi.URLParam(r, "requestId")
	var body struct {
		Status string `json:"status"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.RoutingAnalysisStore.mu.Lock()
	req := h.RoutingAnalysisStore.items[id]
	if req == nil || req.runtimeID != runtimeID {
		h.RoutingAnalysisStore.mu.Unlock()
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if body.Status == "completed" {
		req.status, req.result = "completed", body.Result
	} else {
		req.status, req.err = "failed", body.Error
	}
	h.RoutingAnalysisStore.mu.Unlock()
	if body.Status == "completed" {
		// Persist even when the original 20s waiter has already returned. The
		// next dispatch can then reuse the late result instead of re-running it.
		h.persistLateRoutingAnalysis(req, body.Result)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) persistLateRoutingAnalysis(req *routingAnalysisRequest, raw string) {
	if req.workspaceID == "" || req.issueID == "" || req.contentHash == "" || h.Routing == nil {
		return
	}
	rec, err := routing.ParseAnalysisResult(raw, req.model)
	if err != nil {
		return
	}
	issue, err := h.Routing.Store.Issue(context.Background(), req.workspaceID, req.issueID)
	if err != nil || issue.ContentHash != req.contentHash {
		return
	}
	rec.Hash = req.contentHash
	if cache, ok := h.Routing.Store.(routing.AnalysisCache); ok {
		_ = cache.SaveAnalysis(context.Background(), req.workspaceID, req.issueID, rec)
	}
}
