package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/statecard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueContextResponse is GET /api/issues/{id}/context — `multica issue
// context`. Text is the same card rendered for a brief or a terminal.
type IssueContextResponse struct {
	statecard.Card
	Text string `json:"text"`
}

// GetIssueContext builds the state card for the caller (DENE-1328). The
// change list is the caller's own: an agent's is measured from its previous
// run on the issue, a person's from their last comment; ?since= overrides.
func (h *Handler) GetIssueContext(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var explicit *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC3339 timestamp")
			return
		}
		explicit = &t
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	var excludeTask pgtype.UUID
	if actorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok {
			excludeTask = task.ID
		}
	}
	card, err := h.buildStateCard(r.Context(), issue, statecard.Caller{Type: actorType, ID: actorID}, excludeTask, explicit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build state card: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, IssueContextResponse{Card: card, Text: statecard.Render(card)})
}

func (h *Handler) buildStateCard(ctx context.Context, issue db.Issue, caller statecard.Caller, excludeTask pgtype.UUID, explicit *time.Time) (statecard.Card, error) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	card := statecard.Card{
		IssueID:    uuidToString(issue.ID),
		Identifier: issueToResponse(issue, prefix).Identifier,
		Goal:       statecard.Goal{Title: issue.Title, FinishLine: []statecard.Check{}},
		Decisions:  []statecard.Decision{},
	}

	if goal, err := h.Queries.GetIssueGoal(ctx, db.GetIssueGoalParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID}); err == nil {
		card.Goal.GoalStatus = goal.Status
		checks, err := h.Queries.ListIssueGoalChecks(ctx, goal.ID)
		if err != nil {
			return card, err
		}
		for _, c := range checks {
			card.Goal.FinishLine = append(card.Goal.FinishLine, statecard.Check{Description: c.Description, Status: c.Status})
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return card, err
	}

	decisions, err := h.Queries.ListIssueDecisions(ctx, db.ListIssueDecisionsParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return card, err
	}
	for _, d := range decisions {
		card.Decisions = append(card.Decisions, decisionToCard(d))
	}

	meta := issueMetaStrings(issue.Metadata)
	card.Now = statecard.DeriveNow(meta, issue.Status)
	card.Baton = statecard.DeriveBaton(meta, h.closeNote(ctx, issue, meta))

	changes, err := h.stateCardChanges(ctx, issue, caller, excludeTask, explicit)
	if err != nil {
		return card, err
	}
	card.Changes = changes
	return card, nil
}

// closeNote is the latest close's summary: its progress line when the line
// belongs to this close, else the evidence comment's first paragraph.
func (h *Handler) closeNote(ctx context.Context, issue db.Issue, meta map[string]string) statecard.CloseNote {
	if !closeprotocol.Complete(meta) {
		return statecard.CloseNote{}
	}
	note := statecard.CloseNote{CommentID: strings.TrimSpace(meta[closeprotocol.KeyEvidenceCommentID])}
	row, err := h.Queries.GetLatestIssueProgressBySource(ctx, db.GetLatestIssueProgressBySourceParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Source: progress.SourceClose,
	})
	if err == nil && row.CreatedAt.Valid && statecard.SummaryBelongsToClose(row.CreatedAt.Time, meta[closeprotocol.KeyAt]) {
		note.Summary, note.ByType, note.ByID = row.Text, row.AuthorType, uuidToString(row.AuthorID)
		return note
	}
	if id, err := parseUUIDStrict(note.CommentID); err == nil {
		if c, err := h.Queries.GetComment(ctx, id); err == nil && c.IssueID == issue.ID && !c.DeletedAt.Valid {
			note.Summary = statecard.FirstParagraph(c.Content)
			note.ByType, note.ByID = c.AuthorType, uuidToString(c.AuthorID)
		}
	}
	return note
}

func (h *Handler) stateCardChanges(ctx context.Context, issue db.Issue, caller statecard.Caller, excludeTask pgtype.UUID, explicit *time.Time) (statecard.Changes, error) {
	callerID, _ := parseUUIDStrict(caller.ID)
	var previousRun, lastComment *time.Time
	switch caller.Type {
	case "agent":
		if t, err := h.Queries.GetAgentPreviousRunStartOnIssue(ctx, db.GetAgentPreviousRunStartOnIssueParams{
			IssueID: issue.ID, AgentID: callerID, ExcludeTaskID: excludeTask,
		}); err == nil && t.Valid {
			previousRun = &t.Time
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return statecard.Changes{}, err
		}
	case "member":
		t, err := h.Queries.GetMemberLastCommentAtOnIssue(ctx, db.GetMemberLastCommentAtOnIssueParams{
			IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorID: callerID,
		})
		if err != nil {
			return statecard.Changes{}, err
		}
		if t.Valid {
			lastComment = &t.Time
		}
	}
	anchor, since := statecard.ChooseAnchor(caller, explicit, previousRun, lastComment)
	changes := statecard.Changes{Anchor: anchor, Threads: []statecard.Thread{}}
	var sinceTS pgtype.Timestamptz
	if since != nil {
		sinceTS = pgtype.Timestamptz{Time: *since, Valid: true}
		changes.Since = since.UTC().Format(time.RFC3339)
	}
	rows, err := h.Queries.ListIssueThreadsChangedSince(ctx, db.ListIssueThreadsChangedSinceParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Since: sinceTS,
		CallerType: caller.Type, CallerID: callerID, RowLimit: statecard.MaxThreads + 50,
	})
	if err != nil {
		return changes, err
	}
	for i, row := range rows {
		if i >= statecard.MaxThreads {
			changes.More = len(rows) - statecard.MaxThreads
			break
		}
		title := statecard.ThreadTitle(row.Content)
		if row.DeletedAt.Valid || title == "" {
			title = "（已删除的评论）"
		}
		changes.Threads = append(changes.Threads, statecard.Thread{
			ThreadID: uuidToString(row.ID), Title: title, AuthorType: row.AuthorType, AuthorID: uuidToString(row.AuthorID),
			NewCount: int(row.NewCount), LastAt: timestampToString(row.LastAt),
		})
	}
	return changes, nil
}

func decisionToCard(d db.IssueDecision) statecard.Decision {
	return statecard.Decision{
		ID: uuidToString(d.ID), Text: d.Text, Source: d.Source, AuthorType: d.AuthorType, AuthorID: uuidToString(d.AuthorID),
		CreatedAt: timestampToString(d.CreatedAt), UpdatedAt: timestampToString(d.UpdatedAt),
	}
}

// issueMetaStrings flattens issue metadata to strings, the shape
// closeprotocol and statecard read.
func issueMetaStrings(raw []byte) map[string]string {
	out := map[string]string{}
	for k, v := range parseIssueMetadata(raw) {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	return out
}

// decisionRejection words a statecard refusal for the caller.
func decisionRejection(err error) string {
	switch {
	case errors.Is(err, statecard.ErrDecisionEmpty):
		return "拍板不能为空"
	case errors.Is(err, statecard.ErrDecisionTooLong):
		return fmt.Sprintf("拍板每条最多 %d 字：只写定下来的那一句，理由放证据里", statecard.MaxDecisionLen)
	}
	return err.Error()
}

// prepareDecisions validates the decisions a close or handoff brings before
// anything is written.
func (h *Handler) prepareDecisions(ctx context.Context, issue db.Issue, texts []string) ([]string, string) {
	if len(texts) == 0 {
		return nil, ""
	}
	n, err := h.Queries.CountIssueDecisions(ctx, db.CountIssueDecisionsParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return nil, "failed to read decisions"
	}
	out, err := statecard.NormalizeDecisions(texts, int(n))
	if err != nil {
		return nil, decisionRejection(err)
	}
	return out, ""
}

func insertDecisions(ctx context.Context, q *db.Queries, issue db.Issue, texts []string, source, actorType, actorID string) error {
	for _, text := range texts {
		if _, err := q.CreateIssueDecision(ctx, db.CreateIssueDecisionParams{
			WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Text: text, Source: source,
			AuthorType: actorType, AuthorID: progressAuthorID(actorID),
		}); err != nil {
			return err
		}
	}
	return nil
}

// publishStateCardChanged tells open issue pages to refetch the card.
func (h *Handler) publishStateCardChanged(ctx context.Context, issue db.Issue, actorType, actorID string) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue":              service.IssueToMapResolved(ctx, h.Queries, issue, prefix),
		"status_changed":     false,
		"prev_status":        issue.Status,
		"state_card_changed": true,
	})
}

type issueDecisionRequest struct {
	Text string `json:"text"`
}

func decodeDecision(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req issueDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return "", false
	}
	text, err := statecard.NormalizeDecision(req.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, decisionRejection(err))
		return "", false
	}
	return text, true
}

// CreateIssueDecision is POST /api/issues/{id}/decisions — a decision added
// by hand on the issue page or `multica issue decision add`.
func (h *Handler) CreateIssueDecision(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	text, ok := decodeDecision(w, r)
	if !ok {
		return
	}
	texts, msg := h.prepareDecisions(r.Context(), issue, []string{text})
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	d, err := h.Queries.CreateIssueDecision(r.Context(), db.CreateIssueDecisionParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Text: texts[0], Source: statecard.SourceManual,
		AuthorType: actorType, AuthorID: progressAuthorID(actorID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add decision")
		return
	}
	h.publishStateCardChanged(r.Context(), issue, actorType, actorID)
	writeJSON(w, http.StatusCreated, decisionToCard(d))
}

// loadDecisionForEdit finds the decision and checks the caller may change
// it: a person may edit any decision; an agent only its own, so a run cannot
// rewrite what a person settled.
func (h *Handler) loadDecisionForEdit(w http.ResponseWriter, r *http.Request) (db.Issue, pgtype.UUID, string, string, bool) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return issue, pgtype.UUID{}, "", "", false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return issue, pgtype.UUID{}, "", "", false
	}
	id, err := parseUUIDStrict(chi.URLParam(r, "decisionId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid decision id")
		return issue, id, "", "", false
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	rows, err := h.Queries.ListIssueDecisions(r.Context(), db.ListIssueDecisionsParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read decisions")
		return issue, id, "", "", false
	}
	for _, d := range rows {
		if d.ID != id {
			continue
		}
		if actorType == "agent" && !(d.AuthorType == "agent" && uuidToString(d.AuthorID) == actorID) {
			writeError(w, http.StatusForbidden, "智能体只能改自己写的拍板；别人写的由人在票详情页改")
			return issue, id, "", "", false
		}
		return issue, id, actorType, actorID, true
	}
	writeError(w, http.StatusNotFound, "decision not found")
	return issue, id, "", "", false
}

// UpdateIssueDecision is PATCH /api/issues/{id}/decisions/{decisionId}.
func (h *Handler) UpdateIssueDecision(w http.ResponseWriter, r *http.Request) {
	issue, id, actorType, actorID, ok := h.loadDecisionForEdit(w, r)
	if !ok {
		return
	}
	text, ok := decodeDecision(w, r)
	if !ok {
		return
	}
	d, err := h.Queries.UpdateIssueDecisionText(r.Context(), db.UpdateIssueDecisionTextParams{
		ID: id, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Text: text,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "decision not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update decision")
		return
	}
	h.publishStateCardChanged(r.Context(), issue, actorType, actorID)
	writeJSON(w, http.StatusOK, decisionToCard(d))
}

// DeleteIssueDecision is DELETE /api/issues/{id}/decisions/{decisionId}.
func (h *Handler) DeleteIssueDecision(w http.ResponseWriter, r *http.Request) {
	issue, id, actorType, actorID, ok := h.loadDecisionForEdit(w, r)
	if !ok {
		return
	}
	if _, err := h.Queries.DeleteIssueDecision(r.Context(), db.DeleteIssueDecisionParams{
		ID: id, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to delete decision")
		return
	}
	h.publishStateCardChanged(r.Context(), issue, actorType, actorID)
	w.WriteHeader(http.StatusNoContent)
}
