package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/statecard"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Consult (DENE-1721): `multica issue consult`. A run working a ticket asks
// a strong-tier seat one question; the server picks the seat, counts the ask
// against the ticket's limit and queues a short read-only run whose final
// output is the advice. Every refusal is immediate, so the asker never waits
// in a queue for nothing.

const (
	ConsultRefusedNotARun     = "consult_not_a_run"
	ConsultRefusedDisabled    = "consult_disabled"
	ConsultRefusedLimit       = "consult_limit_reached"
	ConsultRefusedNoAdvisor   = "consult_no_advisor"
	ConsultRefusedUnavailable = "consult_advisors_unavailable"
)

type ConsultResponse struct {
	ID              string  `json:"id"`
	IssueID         string  `json:"issue_id"`
	Status          string  `json:"status"`
	Question        string  `json:"question"`
	Answer          string  `json:"answer,omitempty"`
	FailureReason   string  `json:"failure_reason,omitempty"`
	AskerAgentID    string  `json:"asker_agent_id"`
	AdvisorAgentID  string  `json:"advisor_agent_id"`
	AdvisorName     string  `json:"advisor_name"`
	AdvisorTaskID   string  `json:"advisor_task_id,omitempty"`
	TokensUsed      int64   `json:"tokens_used"`
	DurationSeconds int64   `json:"duration_seconds"`
	CreatedAt       string  `json:"created_at"`
	FinishedAt      *string `json:"finished_at"`
}

type consultRefusal struct {
	status  int
	code    string
	message string
	limit   int
}

func writeConsultRefusal(w http.ResponseWriter, f consultRefusal) {
	body := map[string]any{"error": f.message, "code": f.code, "reason_code": f.code}
	if f.limit > 0 {
		body["limit"] = f.limit
	}
	writeJSON(w, f.status, body)
}

func (h *Handler) consultToResponse(ctx context.Context, c db.IssueConsult) ConsultResponse {
	resp := ConsultResponse{
		ID: uuidToString(c.ID), IssueID: uuidToString(c.IssueID), Status: c.Status, Question: c.Question,
		Answer: c.Answer.String, FailureReason: c.FailureReason.String,
		AskerAgentID: uuidToString(c.AskerAgentID), AdvisorAgentID: uuidToString(c.AdvisorAgentID),
		AdvisorTaskID: uuidToString(c.AdvisorTaskID), TokensUsed: c.TokensUsed, DurationSeconds: c.DurationSeconds,
		CreatedAt: timestampToString(c.CreatedAt), FinishedAt: timestampToPtr(c.FinishedAt),
	}
	if agent, err := h.Queries.GetAgent(ctx, c.AdvisorAgentID); err == nil {
		resp.AdvisorName = agent.Name
	}
	return resp
}

type createConsultRequest struct {
	Question string `json:"question"`
}

// CreateIssueConsult — POST /api/issues/{id}/consults. Only a run working
// this ticket may ask; the answer arrives on GET .../consults/{consultId}.
func (h *Handler) CreateIssueConsult(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	task, isRun := h.agentSpawnTask(r, actorType, actorID)
	if !isRun || task.IssueID != issue.ID || (task.Status != "running" && task.Status != "dispatched") {
		writeConsultRefusal(w, consultRefusal{status: http.StatusForbidden, code: ConsultRefusedNotARun,
			message: "only an agent run working this ticket can consult; people can comment and @mention a seat instead"})
		return
	}
	var req createConsultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	if n := utf8.RuneCountInString(question); n > service.MaxConsultQuestionRunes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("question is %d characters; keep it under %d — paste the code that matters, not whole files", n, service.MaxConsultQuestionRunes))
		return
	}

	policy, err := h.agentSpawnPolicy(r.Context(), h.Queries, issue.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read agent permissions")
		return
	}
	if !policy.Consult.Enabled {
		writeConsultRefusal(w, consultRefusal{status: http.StatusForbidden, code: ConsultRefusedDisabled,
			message: "this workspace does not allow consults (Settings → Agent permissions); keep working on your own judgment"})
		return
	}
	limit := policy.Consult.PerIssue
	if limit <= 0 {
		limit = maxAgentSpawnLimit
	}

	if h.Routing == nil {
		writeConsultRefusal(w, consultRefusal{status: http.StatusConflict, code: ConsultRefusedNoAdvisor,
			message: "no strong seat is set up in this workspace; keep working on your own judgment"})
		return
	}
	seats, refusal, err := h.Routing.AdvisorCandidates(r.Context(), uuidToString(issue.WorkspaceID), uuidToString(issue.ID), uuidToString(task.AgentID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to find an advisor: "+err.Error())
		return
	}
	switch refusal {
	case routing.AdvisorNoStrongSeat:
		writeConsultRefusal(w, consultRefusal{status: http.StatusConflict, code: ConsultRefusedNoAdvisor,
			message: "no other seat sits on the strongest tier for this ticket; keep working on your own judgment"})
		return
	case routing.AdvisorAllUnavailable:
		writeConsultRefusal(w, consultRefusal{status: http.StatusConflict, code: ConsultRefusedUnavailable,
			message: "every strong seat is out of quota or offline right now; keep working on your own judgment"})
		return
	}
	candidates := make([]pgtype.UUID, 0, len(seats))
	for _, s := range seats {
		if id, err := util.ParseUUID(s.ID); err == nil {
			candidates = append(candidates, id)
		}
	}
	consult, _, err := h.TaskService.RequestConsult(r.Context(), service.ConsultRequest{
		Issue: issue, AskerTask: task, Question: question, Limit: limit, Candidates: candidates,
		Ready: h.consultRuntimeReady,
	})
	switch {
	case errors.Is(err, service.ErrConsultLimitReached):
		writeConsultRefusal(w, consultRefusal{status: http.StatusConflict, code: ConsultRefusedLimit, limit: limit,
			message: fmt.Sprintf("this ticket already used its %d consults; keep working on your own judgment", limit)})
		return
	case errors.Is(err, service.ErrConsultAdvisorsBusy):
		writeConsultRefusal(w, consultRefusal{status: http.StatusConflict, code: ConsultRefusedUnavailable,
			message: "every strong seat is busy or its computer is offline right now; keep working, or try once more later"})
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to start the consult: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, h.consultToResponse(r.Context(), consult))
}

// consultRuntimeReady: the advisor's daemon is online and knows how to run a
// consult claim.
func (h *Handler) consultRuntimeReady(ctx context.Context, agent db.Agent) bool {
	rt, err := h.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil || rt.Status != "online" {
		return false
	}
	return runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilityConsultV1)
}

// ListIssueConsults — GET /api/issues/{id}/consults.
func (h *Handler) ListIssueConsults(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueConsults(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list consults")
		return
	}
	policy, _ := h.agentSpawnPolicy(r.Context(), h.Queries, issue.WorkspaceID)
	out := make([]ConsultResponse, 0, len(rows))
	used := 0
	for _, c := range rows {
		c = h.TaskService.ReconcileConsult(r.Context(), c)
		if c.Status != "failed" {
			used++
		}
		out = append(out, h.consultToResponse(r.Context(), c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"consults": out, "used": used, "limit": policy.Consult.PerIssue, "enabled": policy.Consult.Enabled,
	})
}

// GetIssueConsult — GET /api/issues/{id}/consults/{consultId}; the CLI
// polls it until the status is answered or failed.
func (h *Handler) GetIssueConsult(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "consultId"), "consult id")
	if !ok {
		return
	}
	c, err := h.Queries.GetIssueConsult(r.Context(), db.GetIssueConsultParams{ID: id, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && c.IssueID != issue.ID) {
		writeError(w, http.StatusNotFound, "consult not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load consult")
		return
	}
	c = h.TaskService.ReconcileConsult(r.Context(), c)
	writeJSON(w, http.StatusOK, h.consultToResponse(r.Context(), c))
}

// renderConsultPrompt builds the advisor's prompt at claim time, so the
// ticket background is current when the advisor starts.
func (h *Handler) renderConsultPrompt(ctx context.Context, cc service.ConsultContext) (string, string, error) {
	consultID, err := util.ParseUUID(cc.ConsultID)
	if err != nil {
		return "", "", err
	}
	workspaceID, err := util.ParseUUID(cc.WorkspaceID)
	if err != nil {
		return "", "", err
	}
	consult, err := h.Queries.GetIssueConsult(ctx, db.GetIssueConsultParams{ID: consultID, WorkspaceID: workspaceID})
	if err != nil {
		return "", "", err
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: consult.IssueID, WorkspaceID: workspaceID})
	if err != nil {
		return "", "", err
	}
	identifier := issueToResponse(issue, h.getIssuePrefix(ctx, issue.WorkspaceID)).Identifier
	cardText := ""
	if card, err := h.buildStateCard(ctx, issue, statecard.Caller{Type: "agent", ID: uuidToString(consult.AdvisorAgentID)}, sourceViewer{}, pgtype.UUID{}, nil); err == nil {
		cardText = statecard.Render(card)
	}
	prompt := service.RenderConsultPrompt(identifier, issue.Title, issue.Description.String, cardText, consult.Question)
	return prompt, "Consult · " + identifier, nil
}
