package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/receipt"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A chat aligns, an issue executes (DENE-1665). Every issue a chat opens
// records the chat on issue.origin_chat_session_id, so both sides can see the
// other: the chat lists its tickets (GET /api/chat/sessions/{id}/tickets,
// `multica chat tickets`), the issue names its source chat (source_chat on
// GET /api/issues/{id}, `multica issue get`).
//
// A chat also follows issues it did not open (DENE-1719): the ones its run
// acted on — status, assignee, a comment, a handoff — and the ones pinned by
// hand. Those live in chat_followed_issue; the birthplace stays the one
// origin_chat_session_id, which the project monitor's chat flow still counts.

// Where a ticket came from, as the chat sees it.
const (
	chatTicketSourceCreated = "created" // the chat opened it
	chatTicketSourceAuto    = "auto"    // the chat's run acted on it
	chatTicketSourceManual  = "manual"  // pinned by hand
)

// ChatTicket is one issue a chat opened or follows. Goal is the first line of
// the description's 目标 section: the "why" shown on the chat's ticket card.
type ChatTicket struct {
	ID string `json:"id"`
	// Source is created, auto or manual (chatTicketSource*).
	Source       string  `json:"source"`
	Identifier   string  `json:"identifier"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	Priority     string  `json:"priority"`
	AssigneeType *string `json:"assignee_type"`
	AssigneeID   *string `json:"assignee_id"`
	AssigneeName string  `json:"assignee_name,omitempty"`
	Goal         string  `json:"goal,omitempty"`
	// Summary, PullRequests and Knowledge are the ticket's receipt
	// (DENE-1672): what its latest close reported, once it is done, blocked,
	// cancelled or waiting for review.
	Summary      string       `json:"summary,omitempty"`
	PullRequests []receipt.PR `json:"pull_requests,omitempty"`
	Knowledge    string       `json:"knowledge,omitempty"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
	// LinkedAt is when the issue joined this chat: created_at for one it
	// opened, the follow or pin time otherwise.
	LinkedAt string `json:"linked_at"`
	// The chat's progress bar (DENE-1667): the latest status move (FromStatus
	// empty and ChangedAt the creation time when it never moved) and the
	// caller's bucket, as the project report computes it.
	FromStatus string `json:"from_status,omitempty"`
	ChangedAt  string `json:"changed_at"`
	Phase      string `json:"phase"`
	NeedsYou   bool   `json:"needs_you"`
}

type ChatTicketsResponse struct {
	ChatSessionID string       `json:"chat_session_id"`
	Tickets       []ChatTicket `json:"tickets"`
}

// IssueSourceChat names the chat an issue was opened from. Title is empty and
// Accessible false when the viewer cannot see that chat: the issue still says
// it came from a chat, without leaking what the chat is called.
type IssueSourceChat struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Accessible bool   `json:"accessible"`
}

var (
	chatTicketGoalHeading       = regexp.MustCompile(`(?im)^[\s#>*_-]*(?:目标|goal)(?:\s*[(（][^)）\n]*[)）])?[\s*_]*(?:[:：][\s*_]*(.*))?$`)
	chatTicketAcceptanceHeading = regexp.MustCompile(`(?im)^[\s#>*_-]*(?:验收|acceptance)`)
)

// chatTicketDescriptionProblem enforces the chat → issue boundary: what the
// chat aligned on travels with the issue, so a chat-opened issue must say its
// goal and how it is accepted. Returns the refusal sentence, empty when fine.
func chatTicketDescriptionProblem(description string) string {
	var missing []string
	if !chatTicketGoalHeading.MatchString(description) {
		missing = append(missing, "目标 (goal)")
	}
	if !chatTicketAcceptanceHeading.MatchString(description) {
		missing = append(missing, "验收 (acceptance)")
	}
	if len(missing) == 0 {
		return ""
	}
	return "an issue opened from a chat must carry what the chat aligned on: the description is missing a line or heading starting with " +
		strings.Join(missing, " and ") + ` — e.g. "## 目标" and "## 验收"`
}

// chatTicketGoal is the first line of the 目标 section: the rest of the
// heading line ("目标：…") or, for a bare heading, the next non-empty line.
func chatTicketGoal(description string) string {
	lines := strings.Split(description, "\n")
	for i, line := range lines {
		m := chatTicketGoalHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if rest := cleanChatTicketGoal(m[1]); rest != "" {
			return rest
		}
		for _, next := range lines[i+1:] {
			if strings.HasPrefix(strings.TrimSpace(next), "#") {
				break
			}
			if next = cleanChatTicketGoal(next); next != "" {
				return next
			}
		}
		return ""
	}
	return ""
}

func cleanChatTicketGoal(line string) string {
	line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*>0123456789.) "))
	line = strings.Trim(line, "*_ ")
	if strings.HasPrefix(line, "#") {
		return ""
	}
	if r := []rune(line); len(r) > 120 {
		line = string(r[:120]) + "…"
	}
	return line
}

// chatSessionForTask returns the chat a chat run belongs to, when the request
// is that run acting through its task token. Agent-created issues use it to
// stamp their source chat.
func (h *Handler) chatSessionForTask(ctx context.Context, r *http.Request, agentID string) pgtype.UUID {
	taskIDHeader := r.Header.Get("X-Task-ID")
	if taskIDHeader == "" {
		return pgtype.UUID{}
	}
	taskUUID, err := util.ParseUUID(taskIDHeader)
	if err != nil {
		return pgtype.UUID{}
	}
	task, err := h.Queries.GetAgentTask(ctx, taskUUID)
	if err != nil || uuidToString(task.AgentID) != agentID {
		return pgtype.UUID{}
	}
	return task.ChatSessionID
}

// ListChatSessionTickets serves `multica chat tickets`: the issues this chat
// opened or follows, in the order they joined it, filtered to what the caller
// may see. A chat run reads its own chat; anyone else goes through the
// ordinary chat gate.
func (h *Handler) ListChatSessionTickets(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionParam := chi.URLParam(r, "sessionId")
	var session db.ChatSession
	if own, found := h.ownChatForTaskToken(r, sessionParam, workspaceID); found {
		session = own
	} else {
		session, ok = h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionParam)
		if !ok {
			return
		}
	}
	rows, err := h.Queries.ListChatSessionTicketIssues(r.Context(), db.ListChatSessionTicketIssuesParams{
		WorkspaceID: session.WorkspaceID, ChatSessionID: session.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat tickets")
		return
	}
	issues := make([]db.Issue, 0, len(rows))
	links := make(map[string]db.ListChatSessionTicketIssuesRow, len(rows))
	for _, row := range rows {
		issues = append(issues, row.Issue)
		links[uuidToString(row.Issue.ID)] = row
	}
	viewer, err := h.visibilityViewerFor(r, session.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve visibility")
		return
	}
	progress, isTerminal, err := h.chatTicketProgress(r.Context(), session.WorkspaceID, userID, issues)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat tickets")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), session.WorkspaceID)
	names := map[string]string{}
	tickets := make([]ChatTicket, 0, len(issues))
	for _, issue := range issues {
		if !viewer.canSeeIssue(issue) {
			continue
		}
		resp := issueToResponse(issue, prefix)
		ticket := ChatTicket{
			ID: resp.ID, Source: links[resp.ID].Source, LinkedAt: timestampToString(links[resp.ID].LinkedAt), Identifier: resp.Identifier, Title: resp.Title,
			Status: resp.Status, Priority: resp.Priority,
			AssigneeType: resp.AssigneeType, AssigneeID: resp.AssigneeID,
			Goal:      chatTicketGoal(issue.Description.String),
			CreatedAt: resp.CreatedAt, UpdatedAt: resp.UpdatedAt,
		}
		if receipt.Reportable(issue.Status) {
			rc := h.issueReceipt(r.Context(), issue, prefix)
			ticket.Summary, ticket.PullRequests, ticket.Knowledge = rc.Summary, rc.PRs, rc.Knowledge
		}
		if issue.AssigneeType.Valid && issue.AssigneeID.Valid {
			ticket.AssigneeName = h.chatTicketAssigneeName(r.Context(), names, issue.AssigneeType.String, issue.AssigneeID)
		}
		ticket.ChangedAt = resp.CreatedAt
		p := progress[resp.ID]
		if p.ChangedAt.Valid {
			ticket.FromStatus = p.FromStatus
			ticket.ChangedAt = timestampToString(p.ChangedAt)
		}
		onPerson := p.HasOpenCall || (issue.AssigneeType.String == "member" && uuidToString(issue.AssigneeID) == userID)
		ticket.Phase, ticket.NeedsYou = projectReportPhaseOf(isTerminal[issue.Status], onPerson)
		tickets = append(tickets, ticket)
	}
	writeJSON(w, http.StatusOK, ChatTicketsResponse{ChatSessionID: uuidToString(session.ID), Tickets: tickets})
}

// ChatTicketPinRequest pins an issue to a chat: an id or an identifier.
type ChatTicketPinRequest struct {
	Issue string `json:"issue"`
}

// AddChatSessionTicket pins an issue to the chat by hand (`multica chat
// tickets add`, the chat bar's 挂上). It also undoes a take-down.
func (h *Handler) AddChatSessionTicket(w http.ResponseWriter, r *http.Request) {
	var req ChatTicketPinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Issue) == "" {
		writeError(w, http.StatusBadRequest, "issue is required (an id or an identifier like DENE-12)")
		return
	}
	h.setChatSessionTicket(w, r, strings.TrimSpace(req.Issue), false)
}

// RemoveChatSessionTicket takes an issue off the chat (`multica chat tickets
// remove`, the ticket card's 取下). Auto-follow does not bring it back; the
// issue itself and its birthplace are untouched.
func (h *Handler) RemoveChatSessionTicket(w http.ResponseWriter, r *http.Request) {
	h.setChatSessionTicket(w, r, chi.URLParam(r, "issueId"), true)
}

func (h *Handler) setChatSessionTicket(w http.ResponseWriter, r *http.Request, issueRef string, hidden bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionParam := chi.URLParam(r, "sessionId")
	session, own := h.ownChatForTaskToken(r, sessionParam, workspaceID)
	if !own {
		session, ok = h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionParam)
		if !ok {
			return
		}
		if uuidToString(session.CreatorID) != userID {
			access, err := h.chatAccessFor(r.Context(), session, userID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check chat access")
				return
			}
			if !access.speak {
				writeError(w, http.StatusForbidden, "you can view this chat but not change its tickets")
				return
			}
		}
	}
	issue, ok := h.loadIssueForUser(w, r, issueRef)
	if !ok {
		return
	}
	if issue.WorkspaceID != session.WorkspaceID {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err := h.Queries.SetChatFollowedIssue(r.Context(), db.SetChatFollowedIssueParams{
		ChatSessionID: session.ID, IssueID: issue.ID, WorkspaceID: session.WorkspaceID, Hidden: hidden,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat tickets")
		return
	}
	h.publishChatTicketsChanged(session)
	w.WriteHeader(http.StatusNoContent)
}

// followIssueFromChatTask is auto-follow (DENE-1719): when a chat's run acts
// on an issue — status, assignee, a comment, a handoff — the issue joins that
// chat's ticket list. Which chat is decided by the run's task, so the agent
// does nothing extra; a reply that only names an issue never gets here.
// Best-effort: the action itself already succeeded.
func (h *Handler) followIssueFromChatTask(r *http.Request, actorType, actorID string, issue db.Issue) {
	if actorType != "agent" {
		return
	}
	ctx := r.Context()
	sessionID := h.chatSessionForTask(ctx, r, actorID)
	if !sessionID.Valid || issue.OriginChatSessionID == sessionID {
		return
	}
	session, err := h.Queries.GetChatSession(ctx, sessionID)
	if err != nil || session.WorkspaceID != issue.WorkspaceID {
		return
	}
	added, err := h.Queries.FollowIssueFromChat(ctx, db.FollowIssueFromChatParams{
		ChatSessionID: session.ID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("chat tickets: auto-follow failed", "chat_session_id", uuidToString(session.ID), "issue_id", uuidToString(issue.ID), "error", err)
		return
	}
	if added > 0 {
		h.publishChatTicketsChanged(session)
	}
}

// publishChatTicketsChanged tells the chat's viewers to refetch its tickets.
func (h *Handler) publishChatTicketsChanged(session db.ChatSession) {
	sessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatTicketsChanged, uuidToString(session.WorkspaceID), "system", "", sessionID, protocol.ChatTicketsChangedPayload{
		ChatSessionID: sessionID,
	})
}

// ownChatForTaskToken admits a chat run to its own chat without the member
// gate: the token→task binding already proves which chat it serves.
func (h *Handler) ownChatForTaskToken(r *http.Request, sessionParam, workspaceID string) (db.ChatSession, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		return db.ChatSession{}, false
	}
	taskUUID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		return db.ChatSession{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil || !task.ChatSessionID.Valid || uuidToString(task.ChatSessionID) != sessionParam {
		return db.ChatSession{}, false
	}
	session, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID)
	if err != nil || uuidToString(session.WorkspaceID) != workspaceID {
		return db.ChatSession{}, false
	}
	return session, true
}

func (h *Handler) chatTicketAssigneeName(ctx context.Context, cache map[string]string, kind string, id pgtype.UUID) string {
	key := kind + ":" + uuidToString(id)
	if name, ok := cache[key]; ok {
		return name
	}
	var name string
	switch kind {
	case "agent":
		if a, err := h.Queries.GetAgent(ctx, id); err == nil {
			name = a.Name
		}
	case "member":
		if u, err := h.Queries.GetUser(ctx, id); err == nil {
			name = u.Name
		}
	case "squad":
		if s, err := h.Queries.GetSquad(ctx, id); err == nil {
			name = s.Name
		}
	}
	cache[key] = name
	return name
}

// issueSourceChat names the chat an issue was opened from for this viewer.
// A deleted chat reads as no source.
func (h *Handler) issueSourceChat(ctx context.Context, r *http.Request, issue db.Issue) *IssueSourceChat {
	if !issue.OriginChatSessionID.Valid {
		return nil
	}
	session, err := h.Queries.GetChatSession(ctx, issue.OriginChatSessionID)
	if err != nil || session.WorkspaceID != issue.WorkspaceID {
		return nil
	}
	out := &IssueSourceChat{ID: uuidToString(session.ID)}
	if _, own := h.ownChatForTaskToken(r, out.ID, uuidToString(issue.WorkspaceID)); own {
		out.Accessible = true
	} else if access, err := h.chatAccessFor(ctx, session, requestUserID(r)); err == nil && access.see {
		out.Accessible = true
	}
	if out.Accessible {
		out.Title = strings.TrimSpace(session.Title)
	}
	return out
}

// chatTicketProgress reads each ticket's latest status move and the caller's
// open calls, keyed by issue id, with the workspace's finished statuses.
func (h *Handler) chatTicketProgress(ctx context.Context, workspaceID pgtype.UUID, userID string, issues []db.Issue) (map[string]db.ListChatTicketProgressRow, map[string]bool, error) {
	out := map[string]db.ListChatTicketProgressRow{}
	isTerminal := map[string]bool{}
	if len(issues) == 0 {
		return out, isTerminal, nil
	}
	ids := make([]pgtype.UUID, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	rows, err := h.Queries.ListChatTicketProgress(ctx, db.ListChatTicketProgressParams{
		UserID: parseUUID(userID), WorkspaceID: workspaceID, IssueIds: ids,
	})
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		out[uuidToString(row.ID)] = row
	}
	terminal, err := h.terminalIssueStatusKeys(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	for _, key := range terminal {
		isTerminal[key] = true
	}
	return out, isTerminal, nil
}
