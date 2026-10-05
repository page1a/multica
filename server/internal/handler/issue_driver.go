package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// DriverResponse is who or what moves an issue right now (ADR-0006,
// DENE-1342). Kind "none" is the state the patrol and `issue dispose` exist
// to remove; Revives and Escalated say what the patrol already tried.
type DriverResponse struct {
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Revives   int    `json:"revives,omitempty"`
	Escalated bool   `json:"escalated,omitempty"`
}

// issueDrivers computes the driver of each issue with one facts query and one
// status catalog read. An issue that needs no driver (closed, backlog) is
// absent from the map. A failed read returns an empty map: the driver is
// display data and must not fail the response it rides on.
func (h *Handler) issueDrivers(ctx context.Context, workspaceID pgtype.UUID, issues []db.Issue) map[string]blockwait.Driver {
	out := map[string]blockwait.Driver{}
	if len(issues) == 0 {
		return out
	}
	ids := make([]pgtype.UUID, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}
	rows, err := h.Queries.ListIssueDriverFacts(ctx, db.ListIssueDriverFactsParams{WorkspaceID: workspaceID, IssueIds: ids})
	if err != nil {
		slog.Warn("driver: facts read failed", "error", err, "workspace_id", uuidToString(workspaceID))
		return out
	}
	facts := make(map[string]db.ListIssueDriverFactsRow, len(rows))
	for _, row := range rows {
		facts[uuidToString(row.IssueID)] = row
	}
	resolver := issuestatus.NewResolver(workspaceID)
	now := time.Now()
	for _, issue := range issues {
		row, ok := facts[uuidToString(issue.ID)]
		if !ok {
			continue
		}
		effective := resolver.Effective(ctx, h.issueStatusCatalog(), issue.Status)
		if driver, ok := blockwait.DriverOf(driverFacts(issue, effective, row, now)); ok {
			out[uuidToString(issue.ID)] = driver
		}
	}
	return out
}

// issueDriver is issueDrivers for one issue. ok is false when it needs none
// or the facts could not be read.
func (h *Handler) issueDriver(ctx context.Context, issue db.Issue) (blockwait.Driver, bool) {
	driver, ok := h.issueDrivers(ctx, issue.WorkspaceID, []db.Issue{issue})[uuidToString(issue.ID)]
	return driver, ok
}

func driverFacts(issue db.Issue, effective string, row db.ListIssueDriverFactsRow, now time.Time) blockwait.DriverFacts {
	meta := parseIssueMetadata(issue.Metadata)
	f := blockwait.DriverFacts{
		Status:       effective,
		Closed:       effective == "done" || effective == "cancelled",
		ActiveRun:    row.ActiveRun,
		Wakeup:       row.Wakeup,
		OpenChildren: row.OpenChildren,
		Record:       blockwait.ParseMetadata(meta),
		Watched:      blockwait.MetaString(meta, blockwait.KeyWatched) == blockwait.WatchedYes,
		Undriven:     blockwait.ParseUndriven(meta),
		Now:          now,
	}
	if issue.AssigneeType.Valid && issue.AssigneeID.Valid {
		f.AssigneeType = issue.AssigneeType.String
	}
	if issue.ReviewerType.Valid {
		f.ReviewerType = issue.ReviewerType.String
	}
	return f
}

func driverResponse(d blockwait.Driver) *DriverResponse {
	return &DriverResponse{Kind: d.Kind, Reason: d.Reason, Revives: d.Revives, Escalated: d.Escalated}
}

// undrivenView fills the patrol's view of one blocker: open and nobody
// driving it.
func (h *Handler) undrivenView(ctx context.Context, view *blockwait.BlockerView, blocker db.Issue) {
	driver, ok := h.issueDriver(ctx, blocker)
	if !ok || driver.Kind != blockwait.DriverNone {
		return
	}
	view.Undriven = true
	view.UndrivenWhy = driver.Reason
	view.State = blockwait.ParseUndriven(parseIssueMetadata(blocker.Metadata))
}

// reviveOrEscalate carries out the patrol's step on an undriven issue: the
// target is the waiter's blocker, or the waiter itself for an undriven todo.
func (h *Handler) reviveOrEscalate(ctx context.Context, waiter db.Issue, decision blockwait.Decision) {
	target := waiter
	if decision.Target != "" {
		other, ok := h.lookupBlocker(ctx, waiter.WorkspaceID, decision.Target)
		if !ok {
			return
		}
		target = other
	}
	st := blockwait.ParseUndriven(parseIssueMetadata(target.Metadata))
	for key, value := range decision.UndrivenFollowUp(st, time.Now()) {
		h.setIssueMetaString(ctx, target, key, value)
	}
	switch decision.Action {
	case blockwait.ActionRevive:
		h.wakeIssueOwner(ctx, target, decision.Reason, false)
	case blockwait.ActionEscalate:
		h.escalateUndriven(ctx, target, decision.Reason)
	}
}

// escalateUndriven hands an issue its own seat could not move to whoever owns
// its parent, with the disposition command. The parent's agent is started;
// a person is summoned. With no parent the issue is blocked on a person.
func (h *Handler) escalateUndriven(ctx context.Context, issue db.Issue, reason string) {
	ref := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	menu := blockwait.DispositionMenu(ref)
	if issue.ParentIssueID.Valid {
		parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ParentIssueID, WorkspaceID: issue.WorkspaceID})
		if err == nil {
			note := strings.TrimSpace(reason) + " 子票 " + ref + " 交给父票负责人处置。" + menu
			if parent.AssigneeType.Valid && parent.AssigneeID.Valid &&
				(parent.AssigneeType.String == "agent" || parent.AssigneeType.String == "squad") {
				h.wakeExecutor(ctx, parent, note)
				return
			}
			recipient := pgtype.UUID{}
			if parent.AssigneeType.Valid && parent.AssigneeType.String == "member" {
				recipient = parent.AssigneeID
			} else {
				recipient = h.unseatedRecipient(ctx, parent)
			}
			mention := ""
			if recipient.Valid {
				mention = h.memberWakeMention(ctx, recipient)
			}
			comment := h.postBlockComment(ctx, parent, mention+note)
			h.summonPatrol(ctx, parent, recipient, note, comment.ID, !comment.ID.Valid)
			return
		}
	}
	recipient := h.unseatedRecipient(ctx, issue)
	rec := blockwait.Record{}
	mention := ""
	if recipient.Valid {
		rec.NeedsHuman = uuidToString(recipient)
		mention = h.memberWakeMention(ctx, recipient)
	} else {
		rec = blockwait.FailureWake(time.Now(), "等人处置", 1)
	}
	note := strings.TrimSpace(reason) + " 平台把它改成阻塞，等人处置。" + menu
	h.blockAcceptedIssue(ctx, issue, blockwait.Decision{Record: rec, Reason: mention + note})
	if recipient.Valid {
		h.summonPatrol(ctx, issue, recipient, note, pgtype.UUID{}, true)
	}
}

// DisposeIssueRequest is `multica issue dispose`: what to do with an issue
// nobody drives.
type DisposeIssueRequest struct {
	Action string   `json:"action"`
	Reason string   `json:"reason,omitempty"`
	Into   []string `json:"into,omitempty"`
}

type DisposeIssueResponse struct {
	Action string          `json:"action"`
	Status string          `json:"status"`
	Driver *DriverResponse `json:"driver,omitempty"`
	// Created lists the sub-issues a split made.
	Created []string `json:"created,omitempty"`
	Note    string   `json:"note,omitempty"`
}

const disposeSplitLimit = 10

// DisposeIssue is the one server-checked command behind an escalation
// (DENE-1342): rerun on the same seat, reroute to a fresh seat, split into
// sub-issues, or cancel. It refuses an issue somebody already drives, and an
// agent may only dispose a child of an issue it holds.
func (h *Handler) DisposeIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req DisposeIssueRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Action = strings.TrimSpace(req.Action)
	req.Reason = strings.TrimSpace(req.Reason)
	if !blockwait.ValidDisposition(req.Action) {
		writeError(w, http.StatusBadRequest, "--action must be rerun, reroute, split, or cancel")
		return
	}
	var into []string
	for _, title := range req.Into {
		if t := strings.TrimSpace(title); t != "" {
			into = append(into, t)
		}
	}
	switch {
	case req.Action == blockwait.DisposeSplit && len(into) == 0:
		writeError(w, http.StatusBadRequest, "split needs at least one --into title")
		return
	case req.Action == blockwait.DisposeSplit && len(into) > disposeSplitLimit:
		writeError(w, http.StatusBadRequest, "split takes at most 10 --into titles")
		return
	case req.Action != blockwait.DisposeSplit && len(into) > 0:
		writeError(w, http.StatusBadRequest, "--into only goes with --action split")
		return
	case req.Action == blockwait.DisposeCancel && req.Reason == "":
		writeError(w, http.StatusBadRequest, "cancel needs --reason")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	if actorType == "agent" && !h.agentOwnsParent(r.Context(), issue, actorID) {
		writeError(w, http.StatusForbidden, "only the parent issue's executor or a member can dispose this issue")
		return
	}
	driver, needs := h.issueDriver(r.Context(), issue)
	if !needs {
		writeError(w, http.StatusConflict, "issue is closed or parked; nothing to dispose")
		return
	}
	if driver.Kind != blockwait.DriverNone {
		writeError(w, http.StatusConflict, "issue already has a driver ("+driver.Kind+": "+driver.Reason+"); dispose is for an issue nobody drives")
		return
	}

	ctx := r.Context()
	ref := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	by := "处置"
	if req.Reason != "" {
		by = "处置（" + req.Reason + "）"
	}
	resp := DisposeIssueResponse{Action: req.Action}
	switch req.Action {
	case blockwait.DisposeRerun:
		h.dropUndrivenKeys(ctx, issue)
		h.wakeIssueOwner(ctx, issue, by+"：原席位再跑一次。", false)
		resp.Note = "rerun on the same seat"
	case blockwait.DisposeReroute:
		h.dropUndrivenKeys(ctx, issue)
		if issue.AssigneeID.Valid {
			cleared, err := h.Queries.ClearIssueExecutorIfCurrent(ctx, db.ClearIssueExecutorIfCurrentParams{
				ID: issue.ID, WorkspaceID: issue.WorkspaceID, CurrentAssigneeID: issue.AssigneeID,
			})
			if err != nil {
				writeError(w, http.StatusConflict, "executor changed meanwhile; read the issue again")
				return
			}
			h.publish(protocol.EventIssueUpdated, uuidToString(cleared.WorkspaceID), "system", "", RoutingIssueUpdatedPayload(issue, cleared))
			issue = cleared
		}
		// The wake seats through routing first and says so when it cannot.
		h.wakeIssueOwner(ctx, issue, by+"：交给路由重新选执行人。", false)
		resp.Note = "executor cleared; routing picks a new one"
	case blockwait.DisposeSplit:
		created, status, body := h.disposeSplit(r, issue, into)
		if status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		h.dropUndrivenKeys(ctx, issue)
		h.blockAcceptedIssue(ctx, issue, blockwait.Decision{
			Record: blockwait.Record{BlockedBy: created},
			Reason: by + "：拆成 " + strings.Join(created, "、") + "，" + ref + " 等它们做完再继续。",
		})
		resp.Created = created
	case blockwait.DisposeCancel:
		if status, body := h.disposeCancel(r); status != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		h.dropUndrivenKeys(ctx, issue)
		fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
		if err == nil {
			issue = fresh
		}
		h.postBlockComment(ctx, issue, "处置：取消。"+req.Reason)
	}
	fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err == nil {
		issue = fresh
	}
	resp.Status = issue.Status
	if d, ok := h.issueDriver(ctx, issue); ok {
		resp.Driver = driverResponse(d)
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentOwnsParent reports whether the calling agent holds the issue's parent,
// directly or as the leader of the squad that holds it.
func (h *Handler) agentOwnsParent(ctx context.Context, issue db.Issue, agentID string) bool {
	if !issue.ParentIssueID.Valid {
		return false
	}
	parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ParentIssueID, WorkspaceID: issue.WorkspaceID})
	if err != nil || !parent.AssigneeType.Valid || !parent.AssigneeID.Valid {
		return false
	}
	switch parent.AssigneeType.String {
	case "agent":
		return uuidToString(parent.AssigneeID) == agentID
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: parent.AssigneeID, WorkspaceID: parent.WorkspaceID})
		return err == nil && uuidToString(squad.LeaderID) == agentID
	}
	return false
}

func (h *Handler) dropUndrivenKeys(ctx context.Context, issue db.Issue) {
	for _, key := range blockwait.UndrivenKeys() {
		h.deleteIssueMeta(ctx, issue, key)
	}
}

// disposeSplit creates the sub-issues through the canonical create path, so
// routing, visibility and the caller's identity apply as for any create. A
// non-zero status is the failed create's response, passed back as is.
func (h *Handler) disposeSplit(r *http.Request, issue db.Issue, titles []string) ([]string, int, []byte) {
	var created []string
	for _, title := range titles {
		payload, _ := json.Marshal(map[string]any{
			"title":           title,
			"status":          "todo",
			"parent_issue_id": uuidToString(issue.ID),
		})
		req := r.Clone(r.Context())
		req.Method = http.MethodPost
		req.Body = io.NopCloser(bytes.NewReader(payload))
		req.ContentLength = int64(len(payload))
		rec := httptest.NewRecorder()
		h.CreateIssue(rec, req)
		if rec.Code < http.StatusOK || rec.Code >= http.StatusMultipleChoices {
			return created, rec.Code, rec.Body.Bytes()
		}
		var out struct {
			Identifier string `json:"identifier"`
			ID         string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		ref := out.Identifier
		if ref == "" {
			ref = out.ID
		}
		if ref != "" {
			created = append(created, ref)
		}
	}
	return created, 0, nil
}

// disposeCancel moves the issue to cancelled through UpdateIssue, so the
// status gates and the parent's child-done barrier run as for any cancel.
func (h *Handler) disposeCancel(r *http.Request) (int, []byte) {
	body := []byte(`{"status":"cancelled"}`)
	req := r.Clone(r.Context())
	req.Method = http.MethodPut
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	rec := httptest.NewRecorder()
	h.UpdateIssue(rec, req)
	if rec.Code >= http.StatusOK && rec.Code < http.StatusMultipleChoices {
		return 0, nil
	}
	return rec.Code, rec.Body.Bytes()
}
