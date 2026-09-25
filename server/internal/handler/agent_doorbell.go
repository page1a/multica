package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Agent doorbell + timed access passes (DENE-808).
//
// A member who may not invoke an owner's agent used to be refused outright.
// With the agent's doorbell on, the refusal becomes a pending access request:
// the owner gets an action_required inbox item, and approving it replays the
// original trigger (the @mention or the assignment) as if the owner had
// admitted the member. Approval may also hand out a timed pass, which
// invokeAgentDecision honours like a member target until it expires or is
// revoked. Everything here lives in fork-owned files so upstream syncs stay
// conflict-free; the two hooks into shared code are in comment.go
// (ringDoorbellTargets) and issue.go (ringDoorbellForAssign).

const (
	accessRequestDefaultTTL = 24 * time.Hour
	accessPassMaxDuration   = 30 * 24 * time.Hour
	accessRequestSummaryMax = 240

	inboxTypeAgentAccessRequest  = "agent_access_request"
	inboxTypeAgentAccessApproved = "agent_access_approved"
	inboxTypeAgentAccessDeclined = "agent_access_declined"
)

// inboxItemIsPersonalNotice reports whether an inbox item is addressed to the
// recipient personally rather than being a projection of an issue they follow.
// Doorbell rings and receipts are about the recipient's own agent (or their
// own request), so the issue-visibility filter that scopes ordinary issue
// notifications (DENE-698) must not swallow them: the owner has to hear the
// bell even when the ticket it rang on is not shared with them. The item only
// carries the saved summary and metadata, never the issue body. Keep in step
// with the type list in CountUnreadInbox / CountUnreadInboxByWorkspace.
func inboxItemIsPersonalNotice(itemType string) bool {
	switch itemType {
	case inboxTypeAgentAccessRequest, inboxTypeAgentAccessApproved, inboxTypeAgentAccessDeclined:
		return true
	}
	return false
}

// AgentAccessRequestResponse is the wire shape of one doorbell ring.
type AgentAccessRequestResponse struct {
	ID                 string  `json:"id"`
	WorkspaceID        string  `json:"workspace_id"`
	AgentID            string  `json:"agent_id"`
	AgentName          string  `json:"agent_name"`
	RequesterID        string  `json:"requester_id"`
	RequesterName      string  `json:"requester_name"`
	RequesterEmail     string  `json:"requester_email"`
	RequesterAvatarURL *string `json:"requester_avatar_url"`
	IssueID            *string `json:"issue_id"`
	IssueNumber        *int32  `json:"issue_number"`
	IssueTitle         *string `json:"issue_title"`
	CommentID          *string `json:"comment_id"`
	TriggerKind        string  `json:"trigger_kind"`
	Summary            string  `json:"summary"`
	Status             string  `json:"status"`
	ResolvedBy         *string `json:"resolved_by"`
	ResolvedAt         *string `json:"resolved_at"`
	ExpiresAt          string  `json:"expires_at"`
	CreatedAt          string  `json:"created_at"`
}

// AgentAccessPassResponse is the wire shape of one timed pass.
type AgentAccessPassResponse struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	AgentID       string  `json:"agent_id"`
	UserID        string  `json:"user_id"`
	UserName      string  `json:"user_name"`
	UserEmail     string  `json:"user_email"`
	UserAvatarURL *string `json:"user_avatar_url"`
	GrantedBy     string  `json:"granted_by"`
	ExpiresAt     string  `json:"expires_at"`
	RevokedAt     *string `json:"revoked_at"`
	RequestID     *string `json:"request_id"`
	CreatedAt     string  `json:"created_at"`
	Active        bool    `json:"active"`
}

func accessRequestFromOwnerRow(r db.ListAgentAccessRequestsForOwnerRow) AgentAccessRequestResponse {
	var issueNumber *int32
	if r.IssueNumber.Valid {
		n := r.IssueNumber.Int32
		issueNumber = &n
	}
	return AgentAccessRequestResponse{
		ID:                 uuidToString(r.ID),
		WorkspaceID:        uuidToString(r.WorkspaceID),
		AgentID:            uuidToString(r.AgentID),
		AgentName:          r.AgentName,
		RequesterID:        uuidToString(r.RequesterID),
		RequesterName:      r.RequesterName,
		RequesterEmail:     r.RequesterEmail,
		RequesterAvatarURL: textToPtr(r.RequesterAvatarUrl),
		IssueID:            uuidToPtr(r.IssueID),
		IssueNumber:        issueNumber,
		IssueTitle:         textToPtr(r.IssueTitle),
		CommentID:          uuidToPtr(r.CommentID),
		TriggerKind:        r.TriggerKind,
		Summary:            r.Summary,
		Status:             r.Status,
		ResolvedBy:         uuidToPtr(r.ResolvedBy),
		ResolvedAt:         timestampToPtr(r.ResolvedAt),
		ExpiresAt:          timestampToString(r.ExpiresAt),
		CreatedAt:          timestampToString(r.CreatedAt),
	}
}

func accessRequestFromRequesterRow(r db.ListAgentAccessRequestsForRequesterRow) AgentAccessRequestResponse {
	return accessRequestFromOwnerRow(db.ListAgentAccessRequestsForOwnerRow(r))
}

func accessRequestFromRow(r db.AgentAccessRequest, agentName, requesterName string) AgentAccessRequestResponse {
	return AgentAccessRequestResponse{
		ID:            uuidToString(r.ID),
		WorkspaceID:   uuidToString(r.WorkspaceID),
		AgentID:       uuidToString(r.AgentID),
		AgentName:     agentName,
		RequesterID:   uuidToString(r.RequesterID),
		RequesterName: requesterName,
		IssueID:       uuidToPtr(r.IssueID),
		CommentID:     uuidToPtr(r.CommentID),
		TriggerKind:   r.TriggerKind,
		Summary:       r.Summary,
		Status:        r.Status,
		ResolvedBy:    uuidToPtr(r.ResolvedBy),
		ResolvedAt:    timestampToPtr(r.ResolvedAt),
		ExpiresAt:     timestampToString(r.ExpiresAt),
		CreatedAt:     timestampToString(r.CreatedAt),
	}
}

func accessPassFromRow(p db.ListAgentAccessPassesRow, now time.Time) AgentAccessPassResponse {
	return AgentAccessPassResponse{
		ID:            uuidToString(p.ID),
		WorkspaceID:   uuidToString(p.WorkspaceID),
		AgentID:       uuidToString(p.AgentID),
		UserID:        uuidToString(p.UserID),
		UserName:      p.UserName,
		UserEmail:     p.UserEmail,
		UserAvatarURL: textToPtr(p.UserAvatarUrl),
		GrantedBy:     uuidToString(p.GrantedBy),
		ExpiresAt:     timestampToString(p.ExpiresAt),
		RevokedAt:     timestampToPtr(p.RevokedAt),
		RequestID:     uuidToPtr(p.RequestID),
		CreatedAt:     timestampToString(p.CreatedAt),
		Active:        !p.RevokedAt.Valid && p.ExpiresAt.Valid && p.ExpiresAt.Time.After(now),
	}
}

func accessPassFromPlainRow(p db.AgentAccessPass, userName, userEmail string, now time.Time) AgentAccessPassResponse {
	return accessPassFromRow(db.ListAgentAccessPassesRow{
		ID: p.ID, WorkspaceID: p.WorkspaceID, AgentID: p.AgentID, UserID: p.UserID,
		GrantedBy: p.GrantedBy, ExpiresAt: p.ExpiresAt, RevokedAt: p.RevokedAt,
		RequestID: p.RequestID, CreatedAt: p.CreatedAt, UserName: userName, UserEmail: userEmail,
	}, now)
}

// accessRequestSummary clips a trigger's text for the owner's inbox card.
func accessRequestSummary(text string) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if utf8.RuneCountInString(text) <= accessRequestSummaryMax {
		return text
	}
	runes := []rune(text)
	return string(runes[:accessRequestSummaryMax]) + "…"
}

// doorbellApplies reports whether a refused member trigger should ring the
// bell instead: the agent is live, has its doorbell on, and the author is a
// human member (agent/system chains never ring — they have no one to ask).
func doorbellApplies(agent db.Agent, authorType string) bool {
	return authorType == "member" && agent.DoorbellEnabled && !agent.ArchivedAt.Valid
}

// ringDoorbell records one pending request for (agent, requester, issue) —
// re-ringing on the same issue refreshes the summary instead of stacking
// requests — and, for a NEW request, drops an action_required inbox item on
// the agent owner. Returns the request and whether it was newly created.
func (h *Handler) ringDoorbell(ctx context.Context, agent db.Agent, requesterID string, issue db.Issue, commentID pgtype.UUID, triggerKind, summary string) (db.AgentAccessRequest, bool, error) {
	requesterUUID, err := util.ParseUUID(requesterID)
	if err != nil {
		return db.AgentAccessRequest{}, false, err
	}
	row, err := h.Queries.CreateAgentAccessRequest(ctx, db.CreateAgentAccessRequestParams{
		WorkspaceID: issue.WorkspaceID,
		AgentID:     agent.ID,
		RequesterID: requesterUUID,
		IssueID:     issue.ID,
		CommentID:   commentID,
		TriggerKind: triggerKind,
		Summary:     accessRequestSummary(summary),
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(accessRequestDefaultTTL), Valid: true},
	})
	if err != nil {
		return db.AgentAccessRequest{}, false, err
	}
	req := db.AgentAccessRequest{
		ID: row.ID, WorkspaceID: row.WorkspaceID, AgentID: row.AgentID, RequesterID: row.RequesterID,
		IssueID: row.IssueID, CommentID: row.CommentID, TriggerKind: row.TriggerKind, Summary: row.Summary,
		Status: row.Status, ResolvedBy: row.ResolvedBy, ResolvedAt: row.ResolvedAt, ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	}
	if !row.Inserted {
		return req, false, nil
	}
	requesterName := requesterID
	if u, err := h.Queries.GetUser(ctx, requesterUUID); err == nil {
		requesterName = u.Name
	}
	details, _ := json.Marshal(map[string]any{
		"request_id":     uuidToString(req.ID),
		"agent_id":       uuidToString(agent.ID),
		"agent_name":     agent.Name,
		"requester_id":   requesterID,
		"requester_name": requesterName,
		"trigger_kind":   triggerKind,
		"comment_id":     uuidToString(commentID),
		"summary":        req.Summary,
		"expires_at":     timestampToString(req.ExpiresAt),
		"status":         "pending",
	})
	h.createAccessInboxItem(ctx, issue, agent.OwnerID, inboxTypeAgentAccessRequest, "action_required",
		requesterName+" wants to use "+agent.Name, req.Summary, requesterUUID, details)
	return req, true, nil
}

// createAccessInboxItem writes one member inbox item and broadcasts it.
func (h *Handler) createAccessInboxItem(ctx context.Context, issue db.Issue, recipient pgtype.UUID, itemType, severity, title, body string, actorID pgtype.UUID, details []byte) {
	if !recipient.Valid {
		return
	}
	item, err := h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   recipient,
		Type:          itemType,
		Severity:      severity,
		IssueID:       issue.ID,
		Title:         title,
		Body:          pgtype.Text{String: body, Valid: body != ""},
		ActorType:     pgtype.Text{String: "member", Valid: true},
		ActorID:       actorID,
		Details:       details,
	})
	if err != nil {
		slog.WarnContext(ctx, "agent doorbell: inbox write failed", "type", itemType, "error", err)
		return
	}
	resp := inboxToResponse(item)
	status := issue.Status
	resp.IssueStatus = &status
	h.publish(protocol.EventInboxNew, uuidToString(issue.WorkspaceID), "member", uuidToString(actorID), map[string]any{"item": resp})
}

// ringDoorbellTargets is the trigger-path side effect for mention targets the
// resolver marked as doorbell rings. Like noteBlockedRuntimeTargets it must
// never run from the composer preview.
func (h *Handler) ringDoorbellTargets(ctx context.Context, issue db.Issue, comment db.Comment, actorType, actorID string, targets []commentMentionTarget) {
	if actorType != "member" {
		return
	}
	rung := map[string]struct{}{}
	for _, t := range targets {
		if t.doorbell == nil {
			continue
		}
		id := uuidToString(t.doorbell.ID)
		if _, done := rung[id]; done {
			continue
		}
		rung[id] = struct{}{}
		if _, _, err := h.ringDoorbell(ctx, *t.doorbell, actorID, issue, comment.ID, "mention", comment.Content); err != nil {
			slog.WarnContext(ctx, "agent doorbell: mention ring failed", "agent_id", id, "issue_id", uuidToString(issue.ID), "error", err)
		}
	}
}

// ringDoorbellForAssign is the UpdateIssue hook: called once the ordinary
// assignee gate has refused a member-authored assignment to an agent. Returns
// true when a request was recorded (the caller then answers access_requested
// instead of the plain permission error).
func (h *Handler) ringDoorbellForAssign(ctx context.Context, r *http.Request, issue db.Issue, assigneeType string, assigneeID pgtype.UUID) bool {
	if assigneeType != "agent" || !assigneeID.Valid {
		return false
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType != "member" {
		return false
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: assigneeID, WorkspaceID: issue.WorkspaceID})
	if err != nil || !doorbellApplies(agent, actorType) {
		return false
	}
	if _, _, err := h.ringDoorbell(ctx, agent, actorID, issue, pgtype.UUID{}, "assign", issue.Title); err != nil {
		slog.WarnContext(ctx, "agent doorbell: assign ring failed", "agent_id", uuidToString(agent.ID), "issue_id", uuidToString(issue.ID), "error", err)
		return false
	}
	return true
}

// expireAccessRequests is the lazy sweep: pending rings past their deadline
// flip to expired the next time anyone looks.
func (h *Handler) expireAccessRequests(ctx context.Context) {
	if _, err := h.Queries.ExpireAgentAccessRequests(ctx); err != nil {
		slog.WarnContext(ctx, "agent doorbell: expiry sweep failed", "error", err)
	}
}

// ListAgentAccessRequests returns rings on agents the caller owns plus rings
// the caller made, for the current workspace.
func (h *Handler) ListAgentAccessRequests(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	h.expireAccessRequests(r.Context())
	userUUID := parseUUID(userID)
	owned, err := h.Queries.ListAgentAccessRequestsForOwner(r.Context(), db.ListAgentAccessRequestsForOwnerParams{WorkspaceID: wsUUID, OwnerID: userUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access requests")
		return
	}
	mine, err := h.Queries.ListAgentAccessRequestsForRequester(r.Context(), db.ListAgentAccessRequestsForRequesterParams{WorkspaceID: wsUUID, RequesterID: userUUID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access requests")
		return
	}
	incoming := make([]AgentAccessRequestResponse, 0, len(owned))
	for _, row := range owned {
		incoming = append(incoming, accessRequestFromOwnerRow(row))
	}
	outgoing := make([]AgentAccessRequestResponse, 0, len(mine))
	for _, row := range mine {
		outgoing = append(outgoing, accessRequestFromRequesterRow(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"incoming": incoming, "outgoing": outgoing})
}

// loadOwnedAccessRequest resolves {id} and checks the caller owns the agent.
func (h *Handler) loadOwnedAccessRequest(w http.ResponseWriter, r *http.Request) (db.AgentAccessRequest, db.Agent, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.AgentAccessRequest{}, db.Agent{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "request id")
	if !ok {
		return db.AgentAccessRequest{}, db.Agent{}, false
	}
	req, err := h.Queries.GetAgentAccessRequest(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "access request not found")
		return db.AgentAccessRequest{}, db.Agent{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), req.AgentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "access request not found")
		return db.AgentAccessRequest{}, db.Agent{}, false
	}
	if uuidToString(agent.OwnerID) != userID {
		writeError(w, http.StatusForbidden, "only the agent owner can resolve this request")
		return db.AgentAccessRequest{}, db.Agent{}, false
	}
	return req, agent, true
}

// approveAccessRequestBody is the optional pass grant attached to an approval.
// Exactly one of pass_expires_at / pass_duration_minutes may be set; both
// absent means a one-off approval.
type approveAccessRequestBody struct {
	PassExpiresAt       *string `json:"pass_expires_at"`
	PassDurationMinutes *int    `json:"pass_duration_minutes"`
}

// resolvePassExpiry turns the two accepted expiry spellings into a deadline.
// Returns zero time when no pass was requested.
func resolvePassExpiry(expiresAt *string, durationMinutes *int, now time.Time) (time.Time, string) {
	switch {
	case expiresAt != nil && *expiresAt != "":
		t, err := time.Parse(time.RFC3339, *expiresAt)
		if err != nil {
			return time.Time{}, "pass_expires_at must be an RFC3339 timestamp"
		}
		if !t.After(now) {
			return time.Time{}, "pass_expires_at must be in the future"
		}
		if t.Sub(now) > accessPassMaxDuration {
			return time.Time{}, "a pass may last at most 30 days"
		}
		return t, ""
	case durationMinutes != nil:
		if *durationMinutes <= 0 {
			return time.Time{}, "pass_duration_minutes must be positive"
		}
		d := time.Duration(*durationMinutes) * time.Minute
		if d > accessPassMaxDuration {
			return time.Time{}, "a pass may last at most 30 days"
		}
		return now.Add(d), ""
	}
	return time.Time{}, ""
}

// ApproveAgentAccessRequest admits one ring: marks it approved, optionally
// issues a pass, replays the original trigger, and receipts the requester.
func (h *Handler) ApproveAgentAccessRequest(w http.ResponseWriter, r *http.Request) {
	req, agent, ok := h.loadOwnedAccessRequest(w, r)
	if !ok {
		return
	}
	var body approveAccessRequestBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	now := time.Now()
	passExpiry, msg := resolvePassExpiry(body.PassExpiresAt, body.PassDurationMinutes, now)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	ownerUUID := agent.OwnerID
	resolved, err := h.Queries.ResolveAgentAccessRequest(r.Context(), db.ResolveAgentAccessRequestParams{ID: req.ID, Status: "approved", ResolvedBy: ownerUUID})
	if errors.Is(err, pgx.ErrNoRows) {
		h.expireAccessRequests(r.Context())
		writeErrorCode(w, http.StatusConflict, "request_not_pending", "this request is no longer pending")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to approve access request")
		return
	}

	var pass *AgentAccessPassResponse
	if !passExpiry.IsZero() {
		created, err := h.Queries.CreateAgentAccessPass(r.Context(), db.CreateAgentAccessPassParams{
			WorkspaceID: resolved.WorkspaceID, AgentID: resolved.AgentID, UserID: resolved.RequesterID,
			GrantedBy: ownerUUID, ExpiresAt: pgtype.Timestamptz{Time: passExpiry, Valid: true}, RequestID: resolved.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "request approved but the pass could not be issued")
			return
		}
		p := accessPassFromPlainRow(created, "", "", now)
		pass = &p
	}

	replay := h.replayAccessRequest(r.Context(), resolved, agent)
	h.receiptRequester(r.Context(), resolved, agent, inboxTypeAgentAccessApproved, pass)

	writeJSON(w, http.StatusOK, map[string]any{
		"request": accessRequestFromRow(resolved, agent.Name, ""),
		"pass":    pass,
		"replay":  replay,
	})
}

// DeclineAgentAccessRequest refuses one ring and tells the requester.
func (h *Handler) DeclineAgentAccessRequest(w http.ResponseWriter, r *http.Request) {
	req, agent, ok := h.loadOwnedAccessRequest(w, r)
	if !ok {
		return
	}
	resolved, err := h.Queries.ResolveAgentAccessRequest(r.Context(), db.ResolveAgentAccessRequestParams{ID: req.ID, Status: "declined", ResolvedBy: agent.OwnerID})
	if errors.Is(err, pgx.ErrNoRows) {
		h.expireAccessRequests(r.Context())
		writeErrorCode(w, http.StatusConflict, "request_not_pending", "this request is no longer pending")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decline access request")
		return
	}
	h.receiptRequester(r.Context(), resolved, agent, inboxTypeAgentAccessDeclined, nil)
	writeJSON(w, http.StatusOK, map[string]any{"request": accessRequestFromRow(resolved, agent.Name, "")})
}

// replayAccessRequest re-runs the trigger the ring stood in for. The run is
// attributed to the owner who approved it: they are the accountable human.
func (h *Handler) replayAccessRequest(ctx context.Context, req db.AgentAccessRequest, agent db.Agent) DispatchOutcome {
	blocked := func(code DispatchReasonCode) DispatchOutcome {
		return DispatchOutcome{Status: DispatchBlocked, ReasonCode: code, Target: &DispatchTarget{Type: "agent", ID: uuidToString(agent.ID), Name: agent.Name}}
	}
	if !req.IssueID.Valid {
		return blocked(ReasonTargetUnavailable)
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: req.IssueID, WorkspaceID: req.WorkspaceID})
	if err != nil {
		return blocked(ReasonTargetUnavailable)
	}
	if agent.ArchivedAt.Valid {
		return blocked(ReasonTargetUnavailable)
	}
	var task db.AgentTaskQueue
	switch req.TriggerKind {
	case "mention":
		if !req.CommentID.Valid {
			return blocked(ReasonTargetUnavailable)
		}
		task, err = h.TaskService.EnqueueTaskForMention(ctx, issue, agent.ID, req.CommentID, service.OriginNamed)
	case "assign":
		prev := issue
		if uuidToString(issue.AssigneeID) != uuidToString(agent.ID) || issue.AssigneeType.String != "agent" {
			issue, err = h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
				ID:            issue.ID,
				AssigneeType:  pgtype.Text{String: "agent", Valid: true},
				AssigneeID:    agent.ID,
				ReviewerType:  issue.ReviewerType,
				ReviewerID:    issue.ReviewerID,
				StartDate:     issue.StartDate,
				DueDate:       issue.DueDate,
				ParentIssueID: issue.ParentIssueID,
				ProjectID:     issue.ProjectID,
				Stage:         issue.Stage,
			})
			if err != nil {
				return blocked(ReasonInternalError)
			}
			h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), "member", uuidToString(agent.OwnerID), RoutingIssueUpdatedPayload(prev, issue))
		}
		task, err = h.TaskService.EnqueueTaskForIssueByActor(ctx, issue, agent.OwnerID)
	default:
		return blocked(ReasonInternalError)
	}
	if err != nil {
		slog.WarnContext(ctx, "agent doorbell: replay failed", "request_id", uuidToString(req.ID), "kind", req.TriggerKind, "error", err)
		return blocked(ReasonInternalError)
	}
	if !task.ID.Valid {
		return DispatchOutcome{Status: DispatchCoalesced, ReasonCode: ReasonCoalesced, Target: &DispatchTarget{Type: "agent", ID: uuidToString(agent.ID), Name: agent.Name}}
	}
	taskID := uuidToString(task.ID)
	return DispatchOutcome{Status: DispatchQueued, ReasonCode: ReasonQueued, Target: &DispatchTarget{Type: "agent", ID: uuidToString(agent.ID), Name: agent.Name}, TaskID: &taskID}
}

// receiptRequester tells the person who rang how it went.
func (h *Handler) receiptRequester(ctx context.Context, req db.AgentAccessRequest, agent db.Agent, itemType string, pass *AgentAccessPassResponse) {
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: req.IssueID, WorkspaceID: req.WorkspaceID})
	if err != nil {
		issue = db.Issue{WorkspaceID: req.WorkspaceID}
	}
	detailMap := map[string]any{
		"request_id":   uuidToString(req.ID),
		"agent_id":     uuidToString(agent.ID),
		"agent_name":   agent.Name,
		"trigger_kind": req.TriggerKind,
		"status":       req.Status,
	}
	title := agent.Name + " request declined"
	severity := "info"
	if itemType == inboxTypeAgentAccessApproved {
		title = agent.Name + " request approved"
		if pass != nil {
			detailMap["pass_id"] = pass.ID
			detailMap["pass_expires_at"] = pass.ExpiresAt
		}
	}
	details, _ := json.Marshal(detailMap)
	h.createAccessInboxItem(ctx, issue, req.RequesterID, itemType, severity, title, req.Summary, agent.OwnerID, details)
}

// loadOwnedAgentForPasses resolves /api/agents/{id} for pass management:
// owner only.
func (h *Handler) loadOwnedAgentForPasses(w http.ResponseWriter, r *http.Request) (db.Agent, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Agent{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "agent id")
	if !ok {
		return db.Agent{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, false
	}
	if uuidToString(agent.OwnerID) != userID {
		writeError(w, http.StatusForbidden, "only the agent owner can manage access passes")
		return db.Agent{}, false
	}
	return agent, true
}

// ListAgentAccessPasses returns an agent's active passes and recent history.
func (h *Handler) ListAgentAccessPasses(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListAgentAccessPasses(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access passes")
		return
	}
	now := time.Now()
	out := make([]AgentAccessPassResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, accessPassFromRow(p, now))
	}
	writeJSON(w, http.StatusOK, out)
}

type createAccessPassBody struct {
	UserID          string  `json:"user_id"`
	ExpiresAt       *string `json:"expires_at"`
	DurationMinutes *int    `json:"duration_minutes"`
}

// CreateAgentAccessPass issues a pass proactively (no ring needed).
func (h *Handler) CreateAgentAccessPass(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	var body createAccessPassBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, body.UserID, "user_id")
	if !ok {
		return
	}
	if uuidToString(agent.OwnerID) == body.UserID {
		writeError(w, http.StatusBadRequest, "the owner does not need a pass")
		return
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{UserID: userUUID, WorkspaceID: agent.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusBadRequest, "user_id does not refer to a member of this workspace")
		return
	}
	now := time.Now()
	expiry, msg := resolvePassExpiry(body.ExpiresAt, body.DurationMinutes, now)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if expiry.IsZero() {
		writeError(w, http.StatusBadRequest, "expires_at or duration_minutes is required")
		return
	}
	created, err := h.Queries.CreateAgentAccessPass(r.Context(), db.CreateAgentAccessPassParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, UserID: userUUID, GrantedBy: agent.OwnerID,
		ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to issue access pass")
		return
	}
	name, email := "", ""
	if u, err := h.Queries.GetUser(r.Context(), member.UserID); err == nil {
		name, email = u.Name, u.Email
	}
	writeJSON(w, http.StatusCreated, accessPassFromPlainRow(created, name, email, now))
}

// RevokeAgentAccessPass ends a pass immediately.
func (h *Handler) RevokeAgentAccessPass(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadOwnedAgentForPasses(w, r)
	if !ok {
		return
	}
	passID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "passId"), "pass id")
	if !ok {
		return
	}
	pass, err := h.Queries.GetAgentAccessPass(r.Context(), passID)
	if err != nil || uuidToString(pass.AgentID) != uuidToString(agent.ID) {
		writeError(w, http.StatusNotFound, "access pass not found")
		return
	}
	if pass.RevokedAt.Valid {
		writeJSON(w, http.StatusOK, accessPassFromPlainRow(pass, "", "", time.Now()))
		return
	}
	revoked, err := h.Queries.RevokeAgentAccessPass(r.Context(), passID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access pass")
		return
	}
	writeJSON(w, http.StatusOK, accessPassFromPlainRow(revoked, "", "", time.Now()))
}
