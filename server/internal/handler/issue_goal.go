package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// GoalResponse is the stable API representation consumed by CLI and clients.
type GoalResponse struct {
	ID                     string              `json:"id"`
	IssueID                string              `json:"issue_id"`
	Status                 string              `json:"status"`
	Round                  int32               `json:"round"`
	Checks                 []GoalCheckResponse `json:"checks"`
	Budget                 GoalBudgetResponse  `json:"budget"`
	Usage                  GoalUsageResponse   `json:"usage"`
	Evidence               []any               `json:"evidence"`
	NoProgressRounds       int32               `json:"no_progress_rounds"`
	MaxNoProgressRounds    int32               `json:"max_no_progress_rounds"`
	BudgetWarningAt        *string             `json:"budget_warning_at,omitempty"`
	LastContinuationTaskID *string             `json:"last_continuation_task_id,omitempty"`
	NextAction             string              `json:"next_action"`
}
type GoalCheckResponse struct {
	ID          string `json:"id"`
	Position    int32  `json:"position"`
	Description string `json:"description"`
	Method      string `json:"method"`
	Status      string `json:"status"`
	Passed      bool   `json:"passed"`
	Evidence    []any  `json:"evidence"`
}
type GoalBudgetResponse struct {
	TokenLimit      int64 `json:"token_limit"`
	RunLimit        int32 `json:"run_limit"`
	DurationSeconds int64 `json:"duration_seconds"`
}
type GoalUsageResponse struct {
	TokensUsed          int64 `json:"tokens_used"`
	RunsUsed            int32 `json:"runs_used"`
	DurationSecondsUsed int64 `json:"duration_seconds_used"`
	// Short aliases keep the CLI table and older clients readable while the
	// *_used names make the wire contract explicit.
	Tokens          int64 `json:"tokens"`
	Runs            int32 `json:"runs"`
	DurationSeconds int64 `json:"duration_seconds"`
}

type goalCheckInput struct {
	Description string `json:"description"`
	Method      string `json:"method"`
}
type goalBudgetInput struct {
	TokenLimit      int64 `json:"token_limit"`
	RunLimit        int32 `json:"run_limit"`
	DurationSeconds int64 `json:"duration_seconds"`
}
type createGoalRequest struct {
	Checks []goalCheckInput `json:"checks"`
	Budget goalBudgetInput  `json:"budget"`
}
type finishGoalRequest struct {
	Status string `json:"status"`
}

func goalJSON(raw []byte) []any {
	if len(raw) == 0 {
		return []any{}
	}
	var out []any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []any{}
	}
	return out
}
func goalUUID(v pgtype.UUID) string { return uuidToString(v) }

func makeGoalResponse(goal db.IssueGoal, checks []db.IssueGoalCheck) GoalResponse {
	var warning *string
	if goal.BudgetWarningAt.Valid {
		v := goal.BudgetWarningAt.Time.UTC().Format(time.RFC3339)
		warning = &v
	}
	var continuation *string
	if goal.LastContinuationTaskID.Valid {
		v := goalUUID(goal.LastContinuationTaskID)
		continuation = &v
	}
	nextAction := "continue"
	if goal.Status == "draft" {
		nextAction = "confirm"
	} else if goal.Status == "achieved" {
		nextAction = "done"
	} else if goal.Status != "active" {
		nextAction = "blocked"
	} else if len(checks) > 0 {
		nextAction = "review"
		for _, check := range checks {
			if check.Status != "passed" {
				nextAction = "continue"
				break
			}
		}
	}
	out := GoalResponse{
		ID: goalUUID(goal.ID), IssueID: goalUUID(goal.IssueID), Status: goal.Status, Round: goal.Round,
		Budget: GoalBudgetResponse{TokenLimit: goal.TokenLimit, RunLimit: goal.RunLimit, DurationSeconds: goal.DurationSeconds},
		Usage: GoalUsageResponse{
			TokensUsed: goal.TokensUsed, RunsUsed: goal.RunsUsed, DurationSecondsUsed: goal.DurationSecondsUsed,
			Tokens: goal.TokensUsed, Runs: goal.RunsUsed, DurationSeconds: goal.DurationSecondsUsed,
		},
		Evidence: goalJSON(goal.Evidence), Checks: make([]GoalCheckResponse, 0, len(checks)),
		NoProgressRounds: goal.NoProgressRounds, MaxNoProgressRounds: goal.MaxNoProgressRounds, BudgetWarningAt: warning,
		LastContinuationTaskID: continuation, NextAction: nextAction,
	}
	for _, c := range checks {
		out.Checks = append(out.Checks, GoalCheckResponse{ID: goalUUID(c.ID), Position: c.Position, Description: c.Description, Method: c.Method, Status: c.Status, Passed: c.Status == "passed", Evidence: goalJSON(c.Evidence)})
	}
	return out
}

type updateGoalCheckRequest struct {
	Status   string `json:"status"`
	Evidence []any  `json:"evidence,omitempty"`
}

// UpdateIssueGoalCheck records execution evidence for one locked completion
// line item. It never changes the line itself; only the check result moves.
func (h *Handler) UpdateIssueGoalCheck(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, checks, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if goal.Status != "active" {
		writeError(w, http.StatusConflict, "goal is not active")
		return
	}
	target := chi.URLParam(r, "check")
	var check db.IssueGoalCheck
	for _, c := range checks {
		if goalUUID(c.ID) == target || fmt.Sprintf("%d", c.Position) == target || fmt.Sprintf("%d", c.Position+1) == target {
			check = c
			break
		}
	}
	if !check.ID.Valid {
		writeError(w, http.StatusNotFound, "goal check not found")
		return
	}
	var req updateGoalCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	if req.Status != "pending" && req.Status != "passed" && req.Status != "failed" {
		writeError(w, http.StatusBadRequest, "status must be pending, passed, or failed")
		return
	}
	evidence, _ := json.Marshal(req.Evidence)
	updated, err := h.Queries.UpdateIssueGoalCheck(r.Context(), db.UpdateIssueGoalCheckParams{ID: check.ID, GoalID: goal.ID, Status: req.Status, Column4: evidence})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update goal check")
		return
	}
	checksOut, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	_ = updated
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checksOut))
}

func (h *Handler) loadIssueGoal(r *http.Request, issue db.Issue) (db.IssueGoal, []db.IssueGoalCheck, error) {
	goal, err := h.Queries.GetIssueGoal(r.Context(), db.GetIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return db.IssueGoal{}, nil, err
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	return goal, checks, err
}

// GetIssueGoal returns the completion line attached to an issue.
func (h *Handler) GetIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, checks, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

func goalActorIsAgent(h *Handler, r *http.Request, issue db.Issue, userID string) bool {
	actorType, _ := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	return actorType == "agent"
}

// CreateIssueGoal attaches one draft completion line to an issue, or
// rewrites the checks of a draft that has not been locked yet.
func (h *Handler) CreateIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req createGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Checks) == 0 {
		writeError(w, http.StatusBadRequest, "checks must contain at least one item")
		return
	}
	if req.Budget.TokenLimit < 0 || req.Budget.RunLimit < 0 || req.Budget.DurationSeconds < 0 {
		writeError(w, http.StatusBadRequest, "budget values must be non-negative")
		return
	}
	for i := range req.Checks {
		req.Checks[i].Description = strings.TrimSpace(req.Checks[i].Description)
		req.Checks[i].Method = strings.ToLower(strings.TrimSpace(req.Checks[i].Method))
		if req.Checks[i].Description == "" {
			writeError(w, http.StatusBadRequest, "check description is required")
			return
		}
		if req.Checks[i].Method == "" {
			req.Checks[i].Method = "acceptance"
		}
		switch req.Checks[i].Method {
		case "command", "test", "screenshot", "acceptance":
		default:
			writeError(w, http.StatusBadRequest, "check method must be command, test, screenshot, or acceptance")
			return
		}
	}
	isAgent := goalActorIsAgent(h, r, issue, userID)
	// Entry points that create the issue (quick create, chat to goal) already
	// leave a drafted line behind. A human submitting the shared completion
	// panel rewrites that draft instead of colliding with it; a locked goal,
	// or an agent, still gets the conflict.
	existing, _, err := h.loadIssueGoal(r, issue)
	redraft := false
	if err == nil {
		if existing.Status != "draft" || isAgent {
			writeError(w, http.StatusConflict, "issue already has a goal")
			return
		}
		redraft = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check existing goal")
		return
	}
	actorType := "member"
	if isAgent {
		actorType = "agent"
	}
	creatorID, _ := util.ParseUUID(userID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start goal transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := db.New(tx)
	goal := existing
	if redraft {
		if err := qtx.DeleteDraftIssueGoalChecks(r.Context(), existing.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to replace goal checks")
			return
		}
	} else {
		goal, err = qtx.CreateIssueGoal(r.Context(), db.CreateIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, TokenLimit: req.Budget.TokenLimit, RunLimit: req.Budget.RunLimit, DurationSeconds: req.Budget.DurationSeconds, CreatedByType: actorType, CreatedByID: creatorID})
		if err != nil {
			writeError(w, http.StatusConflict, "issue already has a goal")
			return
		}
	}
	checks := make([]db.IssueGoalCheck, 0, len(req.Checks))
	for i, c := range req.Checks {
		row, e := qtx.CreateIssueGoalCheck(r.Context(), db.CreateIssueGoalCheckParams{GoalID: goal.ID, Position: int32(i), Description: c.Description, Method: c.Method})
		if e != nil {
			writeError(w, http.StatusInternalServerError, "failed to create goal check")
			return
		}
		checks = append(checks, row)
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save goal")
		return
	}
	writeJSON(w, http.StatusCreated, makeGoalResponse(goal, checks))
}

func (h *Handler) requireHumanGoalActor(w http.ResponseWriter, r *http.Request, issue db.Issue, goal db.IssueGoal) bool {
	userID, ok := requireUserID(w, r)
	if !ok {
		return false
	}
	if goalActorIsAgent(h, r, issue, userID) {
		writeError(w, http.StatusForbidden, "only a human can confirm or modify this goal")
		return false
	}
	return true
}

// ConfirmIssueGoal locks a draft and starts its execution state.
func (h *Handler) ConfirmIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	goal, err = h.Queries.ConfirmIssueGoal(r.Context(), db.ConfirmIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal is already locked or finished")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to confirm goal")
		return
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	// Goal confirmation is the single transition that releases the execution
	// gate. Creation and assignment paths deliberately skip draft goals, so a
	// pre-assigned agent gets its first run only after the human locks the line.
	if h.TaskService != nil && issue.AssigneeType.Valid && issue.AssigneeID.Valid {
		switch issue.AssigneeType.String {
		case "agent":
			if _, enqueueErr := h.TaskService.EnqueueTaskForIssue(r.Context(), issue); enqueueErr != nil && !errors.Is(enqueueErr, service.ErrDuplicatePendingTask) {
				slog.Warn("confirmed goal could not enqueue assigned agent", "issue_id", uuidToString(issue.ID), "error", enqueueErr)
			}
		case "squad":
			// Squad assignment resolves to its leader. Reuse the ordinary
			// assignment path so the same access, readiness, and pending-task
			// guards apply after the human locks the completion line.
			userID, _ := requireUserID(w, r)
			if !h.enqueueSquadLeaderTask(r.Context(), issue, pgtype.UUID{}, "member", userID, "") {
				slog.Warn("confirmed goal could not enqueue assigned squad leader", "issue_id", uuidToString(issue.ID), "squad_id", uuidToString(issue.AssigneeID))
			}
		}
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

// AppendIssueGoalBudget adds to the limits. Once locked, only a human may do this.
func (h *Handler) AppendIssueGoalBudget(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	wasStopped := goal.Status == "stopped"
	if goal.Status != "draft" && !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	var req goalBudgetInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TokenLimit < 0 || req.RunLimit < 0 || req.DurationSeconds < 0 {
		writeError(w, http.StatusBadRequest, "budget values must be non-negative")
		return
	}
	goal, err = h.Queries.AppendIssueGoalBudget(r.Context(), db.AppendIssueGoalBudgetParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, TokenLimit: req.TokenLimit, RunLimit: req.RunLimit, DurationSeconds: req.DurationSeconds})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal cannot accept budget in its current state")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to append budget")
		return
	}
	if wasStopped {
		updated, updateErr := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{ID: issue.ID, Status: issuestatus.InProgress, WorkspaceID: issue.WorkspaceID})
		if updateErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to resume goal issue")
			return
		}
		h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", RoutingIssueUpdatedPayload(issue, updated))
		if h.TaskService != nil {
			if _, enqueueErr := h.TaskService.EnqueueTaskForIssueWithHandoff(r.Context(), issue, "已追加目标预算，自动继续处理尚未通过的完成线检查项。", pgtype.UUID{}); enqueueErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to resume goal")
				return
			}
		}
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}

// FinishIssueGoal stops or marks a goal achieved.
func (h *Handler) FinishIssueGoal(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	goal, _, err := h.loadIssueGoal(r, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal")
		return
	}
	if !h.requireHumanGoalActor(w, r, issue, goal) {
		return
	}
	var req finishGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Status = strings.ToLower(strings.TrimSpace(req.Status))
	if req.Status != "stopped" && req.Status != "achieved" {
		writeError(w, http.StatusBadRequest, "status must be stopped or achieved")
		return
	}
	goal, err = h.Queries.FinishIssueGoal(r.Context(), db.FinishIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: req.Status})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "goal cannot be finished in its current state")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to finish goal")
		return
	}
	checks, err := h.Queries.ListIssueGoalChecks(r.Context(), goal.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load goal checks")
		return
	}
	writeJSON(w, http.StatusOK, makeGoalResponse(goal, checks))
}
