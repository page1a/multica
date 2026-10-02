package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Recommended bool   `json:"recommended,omitempty"`
}
type AskQuestion struct {
	Text    string      `json:"text"`
	Options []AskOption `json:"options"`
}
type AskResponse struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	IssueID     *string           `json:"issue_id,omitempty"`
	AskerType   string            `json:"asker_type"`
	AskerID     string            `json:"asker_id"`
	Title       string            `json:"title"`
	Questions   []AskQuestion     `json:"questions"`
	Answers     map[string]string `json:"answers,omitempty"`
	Mode        string            `json:"mode"`
	Status      string            `json:"status"`
	CreatedAt   time.Time         `json:"created_at"`
	AnsweredAt  *time.Time        `json:"answered_at,omitempty"`
}

type createAskRequest struct {
	Title     string        `json:"title"`
	Questions []AskQuestion `json:"questions"`
	IssueID   string        `json:"issue_id"`
	Mode      string        `json:"mode"`
}
type answerAskRequest struct {
	Answers map[string]string `json:"answers"`
}

func validateAskQuestions(qs []AskQuestion) error {
	if len(qs) < 1 || len(qs) > 4 {
		return fmt.Errorf("questions must contain 1-4 items")
	}
	for i, q := range qs {
		if strings.TrimSpace(q.Text) == "" {
			return fmt.Errorf("question %d text is required", i+1)
		}
		if len(q.Options) < 2 || len(q.Options) > 6 {
			return fmt.Errorf("question %d must contain 2-6 options", i+1)
		}
		recommended := 0
		seen := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.Label) == "" {
				return fmt.Errorf("question %d options need id and label", i+1)
			}
			if seen[o.ID] {
				return fmt.Errorf("question %d has duplicate option id %q", i+1, o.ID)
			}
			seen[o.ID] = true
			if o.Recommended {
				recommended++
			}
		}
		if recommended > 1 {
			return fmt.Errorf("question %d has multiple recommended options", i+1)
		}
	}
	return nil
}

func (h *Handler) CreateAsk(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	var req createAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, 400, "title is required")
		return
	}
	if err := validateAskQuestions(req.Questions); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "needs_you"
	}
	if req.Mode != "needs_you" && req.Mode != "side_question" {
		writeError(w, 400, "mode must be needs_you or side_question")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	askerID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, 400, "invalid asker")
		return
	}
	var issueID any
	if strings.TrimSpace(req.IssueID) != "" {
		id, e := util.ParseUUID(req.IssueID)
		if e != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = id
	} else if pathIssue := chi.URLParam(r, "id"); pathIssue != "" {
		id, parseErr := util.ParseUUID(pathIssue)
		if parseErr != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = id
		req.IssueID = pathIssue
	}
	questions, _ := json.Marshal(req.Questions)
	var out AskResponse
	var id, storedIssue, asker pgtype.UUID
	var created time.Time
	err = h.DB.QueryRow(r.Context(), `INSERT INTO agent_ask(workspace_id,issue_id,asker_type,asker_id,title,questions,mode) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id,issue_id,asker_id,created_at`, ws, issueID, actorType, askerID, strings.TrimSpace(req.Title), questions, req.Mode).Scan(&id, &storedIssue, &asker, &created)
	if err != nil {
		writeError(w, 500, "failed to create ask")
		return
	}
	out = AskResponse{ID: id.String(), WorkspaceID: workspaceID, AskerType: actorType, AskerID: asker.String(), Title: strings.TrimSpace(req.Title), Questions: req.Questions, Mode: req.Mode, Status: "open", CreatedAt: created}
	if storedIssue.Valid {
		s := storedIssue.String()
		out.IssueID = &s
	}
	// A needs_you ask is actionable for the member who initiated the run.
	// Keep the ask itself as the source of truth and store only its id in the
	// inbox row so all surfaces render the same card through GET /api/asks.
	if req.Mode == "needs_you" {
		recipientID, recipientErr := util.ParseUUID(userID)
		if recipientErr != nil {
			writeError(w, 400, "invalid recipient")
			return
		}
		var inboxIssue pgtype.UUID
		inboxIssue = storedIssue
		details, _ := json.Marshal(map[string]string{"ask_id": id.String()})
		item, inboxErr := h.Queries.CreateInboxItem(r.Context(), db.CreateInboxItemParams{
			ID: dbid.NewV7(), WorkspaceID: ws, RecipientType: "member", RecipientID: recipientID,
			Type: service.InboxTypeNeedsYou, Severity: "action_required", IssueID: inboxIssue,
			Title: strings.TrimSpace(req.Title), Body: pgtype.Text{String: "需要回答提问", Valid: true},
			ActorType: pgtype.Text{String: actorType, Valid: true}, ActorID: askerID, Details: details,
		})
		if inboxErr == nil {
			h.publish(protocol.EventInboxNew, workspaceID, actorType, actorID, map[string]any{"item": inboxToResponse(item)})
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) GetAsk(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid ask id")
		return
	}
	var out AskResponse
	var qraw, araw []byte
	var issueID, askerID pgtype.UUID
	var answered *time.Time
	err = h.DB.QueryRow(r.Context(), `SELECT id,issue_id,asker_type,asker_id,title,questions,answers,mode,status,created_at,answered_at FROM agent_ask WHERE id=$1 AND workspace_id=$2`, id, ws).Scan(&id, &issueID, &out.AskerType, &askerID, &out.Title, &qraw, &araw, &out.Mode, &out.Status, &out.CreatedAt, &answered)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, "ask not found")
		} else {
			writeError(w, 500, "failed to read ask")
		}
		return
	}
	out.ID = id.String()
	out.WorkspaceID = workspaceID
	out.AskerID = askerID.String()
	_ = json.Unmarshal(qraw, &out.Questions)
	if len(araw) > 0 {
		_ = json.Unmarshal(araw, &out.Answers)
	}
	if issueID.Valid {
		s := issueID.String()
		out.IssueID = &s
	}
	out.AnsweredAt = answered
	writeJSON(w, 200, out)
}

func (h *Handler) ListAsks(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	issueFilter := r.URL.Query().Get("issue_id")
	var issueID any
	if issueFilter != "" {
		parsed, parseErr := util.ParseUUID(issueFilter)
		if parseErr != nil {
			writeError(w, 400, "invalid issue_id")
			return
		}
		issueID = parsed
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id,issue_id,asker_type,asker_id,title,questions,answers,mode,status,created_at,answered_at FROM agent_ask WHERE workspace_id=$1 AND ($2='' OR status=$2) AND ($3::uuid IS NULL OR issue_id=$3) ORDER BY created_at DESC LIMIT 100`, ws, status, issueID)
	if err != nil {
		writeError(w, 500, "failed to list asks")
		return
	}
	defer rows.Close()
	out := []AskResponse{}
	for rows.Next() {
		var a AskResponse
		var id, issueID, askerID pgtype.UUID
		var qraw, araw []byte
		var answered *time.Time
		if err := rows.Scan(&id, &issueID, &a.AskerType, &askerID, &a.Title, &qraw, &araw, &a.Mode, &a.Status, &a.CreatedAt, &answered); err != nil {
			writeError(w, 500, "failed to read asks")
			return
		}
		a.ID = id.String()
		a.WorkspaceID = workspaceID
		a.AskerID = askerID.String()
		_ = json.Unmarshal(qraw, &a.Questions)
		if len(araw) > 0 {
			_ = json.Unmarshal(araw, &a.Answers)
		}
		if issueID.Valid {
			s := issueID.String()
			a.IssueID = &s
		}
		a.AnsweredAt = answered
		out = append(out, a)
	}
	writeJSON(w, 200, map[string]any{"asks": out})
}

func (h *Handler) AnswerAsk(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	_ = userID
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "invalid workspace_id")
		return
	}
	id, err := util.ParseUUID(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, 400, "invalid ask id")
		return
	}
	var req answerAskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	// Read the question definition first so the server, CLI and every UI agree
	// on what a valid answer is. Missing questions use their recommendation (or
	// the first option), which is the contract for side questions and also makes
	// a one-click answer safe when a client only sends the currently visible tab.
	var qraw []byte
	var askerID pgtype.UUID
	var issueID pgtype.UUID
	var mode string
	if err = h.DB.QueryRow(r.Context(), `SELECT questions, asker_id, issue_id, mode FROM agent_ask WHERE id=$1 AND workspace_id=$2 AND status='open'`, id, ws).Scan(&qraw, &askerID, &issueID, &mode); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 409, "ask is already answered or not found")
		} else {
			writeError(w, 500, "failed to read ask")
		}
		return
	}
	var questions []AskQuestion
	if err := json.Unmarshal(qraw, &questions); err != nil {
		writeError(w, 500, "stored ask is invalid")
		return
	}
	for i, question := range questions {
		key := fmt.Sprintf("%d", i)
		value := strings.TrimSpace(req.Answers[key])
		if value == "" {
			for _, option := range question.Options {
				if option.Recommended {
					value = option.ID
					break
				}
			}
			if value == "" && len(question.Options) > 0 {
				value = question.Options[0].ID
			}
		}
		if value == "" {
			writeError(w, 400, fmt.Sprintf("answer for question %d is required", i+1))
			return
		}
		req.Answers[key] = value
	}
	raw, _ := json.Marshal(req.Answers)
	var status string
	err = h.DB.QueryRow(r.Context(), `UPDATE agent_ask SET answers=$1,status='answered',answered_at=now() WHERE id=$2 AND workspace_id=$3 AND status='open' RETURNING status`, raw, id, ws).Scan(&status)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 409, "ask is already answered or not found")
		} else {
			writeError(w, 500, "failed to answer ask")
		}
		return
	}
	// Inbox delivery is the durable wake signal consumed by the agent/desktop surfaces.
	if mode == "needs_you" {
		answererID, parseErr := util.ParseUUID(userID)
		if parseErr == nil {
			item, inboxErr := h.Queries.CreateInboxItem(r.Context(), db.CreateInboxItemParams{
				ID: dbid.NewV7(), WorkspaceID: ws, RecipientType: "agent", RecipientID: askerID,
				Type: "ask_answered", Severity: "action_required", IssueID: issueID,
				Title: "提问已回答", Body: pgtype.Text{String: string(raw), Valid: true},
				ActorType: pgtype.Text{String: "member", Valid: true}, ActorID: answererID, Details: raw,
			})
			if inboxErr == nil {
				h.publish(protocol.EventInboxNew, workspaceID, "member", userID, map[string]any{"item": inboxToResponse(item)})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"id": id.String(), "status": status, "answers": req.Answers})
}
