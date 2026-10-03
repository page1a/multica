package handler

// The stall action protocol deliberately lives on issue metadata.  It keeps
// the feature backwards compatible with installed clients (unknown metadata
// keys are ignored), while comments and inbox rows remain the durable audit
// trail visible on every surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/stallaction"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Which stall.* transitions are legal, and the clocks that bound them, live
// in package stallaction. This file only gathers facts, writes the patch the
// state machine returns, and leaves the audit trail (comments, inbox, events).
const stallCandidateModelLimit = 20

type stallIssueRow struct {
	ID          pgtype.UUID
	WorkspaceID pgtype.UUID
	Number      int32
	Title       string
	Status      string
	Metadata    []byte
	LastActive  pgtype.Timestamptz
}

func allChildrenTerminal(children []db.Issue, terminal func(db.Issue) bool) bool {
	if len(children) == 0 {
		return false
	}
	for _, child := range children {
		if !terminal(child) {
			return false
		}
	}
	return true
}

func parentAutoCompletionEligible(status string, paused, activeRun, hasPullRequest bool, childCount int, childrenTerminal bool) bool {
	if status == "done" || status == "cancelled" || status == "backlog" || paused || activeRun || hasPullRequest {
		return false
	}
	return childCount > 0 && childrenTerminal
}

func stallMeta(issue db.Issue) map[string]any { return util.JSONObjectOrEmpty(issue.Metadata) }

func stallTicket(issue db.Issue) stallaction.Ticket {
	return stallaction.Ticket{Status: issue.Status, Meta: stallMeta(issue)}
}

// stallProbe answers the state machine's database questions for one issue.
type stallProbe struct {
	h     *Handler
	issue db.Issue
}

func (p stallProbe) HasLinkedPR(ctx context.Context) (bool, error) {
	prs, err := p.h.Queries.ListPullRequestsByIssue(ctx, p.issue.ID)
	return len(prs) > 0, err
}

func (p stallProbe) HasActiveRun(ctx context.Context) (bool, error) {
	return p.h.Queries.HasActiveTaskForIssue(ctx, p.issue.ID)
}

func (p stallProbe) CommentedSince(ctx context.Context, at time.Time) (bool, error) {
	var resumed bool
	err := p.h.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM comment WHERE issue_id=$1 AND workspace_id=$2 AND author_type <> 'system' AND created_at >= $3)`, p.issue.ID, p.issue.WorkspaceID, at).Scan(&resumed)
	return resumed, err
}

func (h *Handler) setStallMetadata(ctx context.Context, issue db.Issue, values stallaction.Patch) error {
	for key, value := range values {
		raw, _ := json.Marshal(value)
		if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key, Value: raw}); err != nil {
			return err
		}
	}
	return nil
}

// markStallCandidateJudged records a model decision without making that
// bookkeeping count as new issue activity. A normal metadata write updates
// last_activity_at and would restart the 36-hour quiet clock.
func (h *Handler) markStallCandidateJudged(ctx context.Context, issue db.Issue, activityAt time.Time) error {
	raw, err := json.Marshal(activityAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	_, err = h.DB.Exec(ctx, `
		UPDATE issue
		SET metadata = jsonb_set(metadata, ARRAY[$1::text], $2::jsonb, true),
		    revision = revision + 1,
		    updated_at = now()
		WHERE id = $3 AND workspace_id = $4`,
		stallaction.KeyJudgedAt, raw, issue.ID, issue.WorkspaceID)
	return err
}

func (h *Handler) clearCloseMetadata(ctx context.Context, issue db.Issue) {
	for _, key := range closeprotocol.Keys {
		_, _ = h.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key})
	}
}

func (h *Handler) stallComment(ctx context.Context, issue db.Issue, body string) pgtype.UUID {
	commentID := dbid.NewV7()
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID: commentID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		AuthorType: "system", AuthorID: pgtype.UUID{Valid: true}, Content: body,
		Type: "system", ParentID: pgtype.UUID{Valid: false},
	})
	if err != nil {
		return pgtype.UUID{}
	}
	if h.Bus != nil {
		h.Bus.Publish(events.Event{Type: protocol.EventCommentCreated, WorkspaceID: util.UUIDToString(issue.WorkspaceID), ActorType: "system", Payload: map[string]any{
			"comment": commentToResponse(created.Comment(), nil, nil), "issue_title": issue.Title,
			"issue_status": issue.Status, "issue_revision": created.IssueRevision,
		}})
	}
	return commentID
}

func (h *Handler) notifyStallInbox(ctx context.Context, issue db.Issue, title, body string, actions ...string) {
	// The summary belongs to the workspace owner/admins even when the ticket is
	// assigned to an agent. Keep the assigned member as an additional recipient.
	recipients := make(map[string]pgtype.UUID)
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "member" && issue.AssigneeID.Valid {
		recipients[util.UUIDToString(issue.AssigneeID)] = issue.AssigneeID
	}
	rows, err := h.DB.Query(ctx, `SELECT user_id FROM member WHERE workspace_id=$1 AND role IN ('owner','admin')`, issue.WorkspaceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id pgtype.UUID
			if rows.Scan(&id) == nil && id.Valid {
				recipients[util.UUIDToString(id)] = id
			}
		}
	}
	entry := fmt.Sprintf("%s：票 #%d（%s）\n%s", title, issue.Number, issue.Title, body)
	summaryDetails, _ := json.Marshal(map[string]any{"kind": "stall_action_summary"})
	actionDetails, _ := json.Marshal(map[string]any{"kind": "stall_action", "issue_id": util.UUIDToString(issue.ID), "actions": actions})
	for _, recipient := range recipients {
		// Keep the daily digest issue-less and read-only. Actionable rows are
		// per issue so a later event cannot retarget an earlier ticket's button.
		var existingID pgtype.UUID
		var existingBody pgtype.Text
		err := h.DB.QueryRow(ctx, `SELECT id, body FROM inbox_item WHERE workspace_id=$1 AND recipient_type='member' AND recipient_id=$2 AND type='issue_stall_action' AND issue_id IS NULL AND created_at >= date_trunc('day', now()) ORDER BY created_at LIMIT 1`, issue.WorkspaceID, recipient).Scan(&existingID, &existingBody)
		if err == nil && existingID.Valid {
			_, _ = h.DB.Exec(ctx, `UPDATE inbox_item SET body=$2, details=$3 WHERE id=$1`, existingID, strings.TrimSpace(existingBody.String+"\n\n"+entry), summaryDetails)
		} else {
			_, _ = h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
				ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, RecipientType: "member", RecipientID: recipient,
				Type: "issue_stall_action", Severity: "attention", IssueID: pgtype.UUID{Valid: false}, Title: "停滞处理每日汇总",
				Body: pgtype.Text{String: entry, Valid: true}, ActorType: pgtype.Text{String: "system", Valid: true},
				ActorID: pgtype.UUID{Valid: true}, Details: summaryDetails,
			})
		}
		if len(actions) == 0 {
			continue
		}
		_, _ = h.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, RecipientType: "member", RecipientID: recipient,
			Type: "issue_stall_action", Severity: "attention", IssueID: issue.ID, Title: title,
			Body: pgtype.Text{String: body, Valid: true}, ActorType: pgtype.Text{String: "system", Valid: true},
			ActorID: pgtype.UUID{Valid: true}, Details: actionDetails,
		})
	}
}

func (h *Handler) stallArtifactCheck(ctx context.Context, issue db.Issue) string {
	prs, _ := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	attachments, _ := h.Queries.ListAttachmentsByIssue(ctx, db.ListAttachmentsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	return fmt.Sprintf("已核对关联 PR（%d 个）、附件（%d 个）与其他交付产物", len(prs), len(attachments))
}

// autoCompleteParent is called by the periodic stall patrol after the last
// child status has been quiet for long enough. The update is deterministic: no
// routing model, PR gate, or agent decision is involved. A close record /
// explicit pause keeps the parent contract visible, and the old status plus a
// seven-day expiry makes the action reversible from all clients.
func (h *Handler) autoCompleteParent(ctx context.Context, parent, child db.Issue) error {
	if parent.Status == "done" || parent.Status == "cancelled" {
		return nil
	}
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: parent.ID, Status: "done", WorkspaceID: parent.WorkspaceID})
	if err != nil {
		return err
	}
	body := fmt.Sprintf("所有子任务均已完成（最后完成：%s）。系统按停滞规则将父票从 `%s` 自动收口为 `done`。7 天内可用“撤销停滞处理”恢复。", child.Title, parent.Status)
	commentID := h.stallComment(ctx, updated, body)
	if !commentID.Valid {
		return fmt.Errorf("create auto-close evidence comment")
	}
	if err := h.setStallMetadata(ctx, updated, stallaction.CompleteParent(parent.Status, time.Now())); err != nil {
		return err
	}
	// Automatic closure still records the same close.* contract as a normal
	// delivered close, so the parent review barrier can distinguish an actual
	// close from a bare status mutation.
	rec := closeRecordAfterRelease(updated, stallMeta(updated), util.UUIDToString(commentID), "")
	if rec != nil && h.TxStarter != nil {
		if err := closeprotocol.Validate(rec, updated.Status, body); err == nil {
			if err := h.writeCloseKeysTx(ctx, updated, rec); err != nil {
				return err
			}
		}
	}
	h.notifyStallInbox(ctx, updated, "父票已自动收口", "所有子任务已完成，父票已标记 done；7 天内可撤销。", "undo")
	if fresh, err := h.Queries.GetIssue(ctx, updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "system", "", map[string]any{"issue": service.IssueToMapResolved(ctx, h.Queries, fresh, h.getIssuePrefix(ctx, fresh.WorkspaceID))})
	}
	return nil
}

// KeepStallAction prevents an announced candidate from being cancelled.
func (h *Handler) KeepStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	patch, err := stallaction.Keep(stallTicket(issue), time.Now())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.setStallMetadata(r.Context(), issue, patch); err != nil {
		writeError(w, 500, "failed to keep stall action")
		return
	}
	h.stallComment(r.Context(), issue, "公示期内收到“保留”操作，这张票继续保留，系统不会自动取消。")
	writeJSON(w, http.StatusOK, map[string]any{"action": stallaction.ActionKept, "issue_id": util.UUIDToString(issue.ID)})
}

// UndoStallAction restores the status captured by an automatic close/cancel.
func (h *Handler) UndoStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	reversal, err := stallaction.Undo(stallTicket(issue), time.Now())
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	previous := reversal.Restore
	updated, err := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{ID: issue.ID, Status: previous, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, 500, "failed to restore issue status")
		return
	}
	h.clearCloseMetadata(r.Context(), updated)
	_ = h.setStallMetadata(r.Context(), updated, reversal.Patch)
	h.stallComment(r.Context(), updated, fmt.Sprintf("已撤销系统自动停滞处理，票状态恢复为 `%s`。", previous))
	h.notifyStallInbox(r.Context(), updated, "停滞处理已撤销", "自动收口/取消已撤销，票状态已恢复。")
	if fresh, err := h.Queries.GetIssue(r.Context(), updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "member", "", map[string]any{"issue": service.IssueToMapResolved(r.Context(), h.Queries, fresh, h.getIssuePrefix(r.Context(), fresh.WorkspaceID))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": stallaction.ActionRevoked, "status": updated.Status, "issue_id": util.UUIDToString(updated.ID)})
}

// ReviewStallAction is the AI-to-server handoff. The model (or a trusted
// caller) supplies its reason; the server always appends an artifact check
// before announcing and never allows a reason that omits that check.
func (h *Handler) ReviewStallAction(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Reason) == "" {
		writeError(w, 400, "reason is required")
		return
	}
	announced, err := h.announceStall(r.Context(), issue, req.Reason)
	var rejected stallaction.Rejection
	switch {
	case errors.As(err, &rejected):
		writeError(w, http.StatusConflict, rejected.Error())
		return
	case errors.Is(err, stallaction.ErrLookup):
		writeError(w, http.StatusInternalServerError, "无法核对关联 PR")
		return
	case err != nil:
		writeError(w, 500, "failed to announce stall action")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": stallaction.ActionAnnounced, "review_until": announced.ReviewUntil, "reason": announced.Reason})
}

// ListStallActions is consumed by the CLI and the inbox/detail surfaces.
func (h *Handler) ListStallActions(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, 400, "workspace_id is required")
		return
	}
	rows, err := h.DB.Query(r.Context(), `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE workspace_id=$1 AND metadata ? 'stall.action' ORDER BY updated_at DESC LIMIT 500`, ws)
	if err != nil {
		writeError(w, 500, "failed to list stall actions")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var x stallIssueRow
		if err := rows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive); err != nil {
			continue
		}
		st := stallaction.Read(stallMeta(db.Issue{Metadata: x.Metadata}))
		items = append(items, map[string]any{"issue_id": util.UUIDToString(x.ID), "number": x.Number, "title": x.Title, "status": x.Status, "action": st.Action, "reason": st.Reason, "review_until": st.ReviewUntil, "revert_until": st.RevertUntil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// SweepStallActions is called by the DB scheduler every 30 minutes. Candidates
// are marked by the AI review handoff; this job only owns the clock and the
// deterministic expiry/cancel transition.
func (h *Handler) SweepStallActions(ctx context.Context) (int64, error) {
	var affected int64
	parentRows, err := h.DB.Query(ctx, `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE `+stallaction.PatrolStatusSQL()+` AND metadata->>'stall.action' IS DISTINCT FROM 'revoked' AND `+stallaction.QuietSQL()+` AND EXISTS (SELECT 1 FROM issue child WHERE child.parent_issue_id=issue.id) AND NOT EXISTS (SELECT 1 FROM issue child_open WHERE child_open.parent_issue_id=issue.id AND child_open.status NOT IN ('done','cancelled')) ORDER BY last_activity_at ASC LIMIT 500`)
	if err != nil {
		return 0, err
	}
	for parentRows.Next() {
		var x stallIssueRow
		if parentRows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive) != nil {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, x.ID)
		if err != nil {
			continue
		}
		if stallTicket(issue).Paused() {
			continue
		}
		if h.parentReadyForAutoCompletion(ctx, issue) {
			children, _ := h.Queries.ListChildIssues(ctx, issue.ID)
			if len(children) > 0 {
				if err := h.autoCompleteParent(ctx, issue, children[len(children)-1]); err == nil {
					affected++
				}
			}
		}
	}
	parentRows.Close()

	// Which tickets the patrol may look at is stallaction's Eligibility; the
	// query and the per-row re-check are both built from it.
	//
	// Quiet top-level tickets are sent through the configured routing model for
	// the duplicate/invalid decision. Self-hosted deployments without that
	// model can still honor an explicit candidate marker written by a trusted
	// agent, while never guessing from age alone.
	candidateQuery := `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE ` + stallaction.PatrolStatusSQL() + ` AND metadata->>'stall.action' IS NULL AND ` + stallaction.QuietSQL() + ` AND NOT EXISTS (SELECT 1 FROM issue child WHERE child.parent_issue_id=issue.id) AND (COALESCE(metadata->>'stall.judged_at','') = '' OR CASE WHEN metadata->>'stall.judged_at' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$' THEN last_activity_at > (metadata->>'stall.judged_at')::timestamptz ELSE TRUE END) AND ` + stallaction.NotWaitingSQL()
	if h.Routing == nil {
		candidateQuery += ` AND metadata->>'stall.candidate'='true'`
	}
	candidateQuery += ` ORDER BY last_activity_at ASC LIMIT 500`
	candidateRows, err := h.DB.Query(ctx, candidateQuery)
	if err != nil {
		return affected, err
	}
	modelCalls := 0
	for candidateRows.Next() {
		var x stallIssueRow
		if candidateRows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive) != nil {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, x.ID)
		if err != nil {
			continue
		}
		ticket := stallTicket(issue)
		st := stallaction.Read(ticket.Meta)
		if st.JudgedFor(x.LastActive.Time) {
			continue
		}
		// The same rule the query filtered on, re-read from the live row.
		if !ticket.PatrolEligible() {
			continue
		}
		if prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID); err != nil || len(prs) > 0 {
			continue
		}
		marked := st.Marked()
		reason := "AI 停滞巡检将这张票识别为重复或无效候选。"
		if h.Routing != nil && !marked {
			if modelCalls >= stallCandidateModelLimit {
				continue
			}
			modelCalls++
			decision, err := h.Routing.JudgeStallCandidate(ctx, util.UUIDToString(issue.WorkspaceID), util.UUIDToString(issue.ID), h.stallArtifactCheck(ctx, issue), int(time.Since(x.LastActive.Time).Hours()))
			if err != nil {
				continue
			}
			if err := h.markStallCandidateJudged(ctx, issue, x.LastActive.Time); err != nil {
				return affected, err
			}
			if !decision.Candidate || decision.Confidence < 0.5 {
				continue
			}
			reason = decision.Reason
		} else if h.Routing == nil && !marked {
			continue
		}
		if err := h.ReviewStallActionInternal(ctx, issue, reason); err == nil {
			affected++
		}
	}
	candidateRows.Close()

	// The announcement clock is independent from issue activity: posting the
	// public notice itself must not restart the 24-hour window.
	rows, err := h.DB.Query(ctx, `SELECT id, workspace_id, number, title, status, metadata, last_activity_at FROM issue WHERE status NOT IN ('done','cancelled') AND metadata->>'stall.action'='announced' ORDER BY (metadata->>'stall.review_until') ASC NULLS LAST, last_activity_at ASC LIMIT 500`)
	if err != nil {
		return affected, err
	}
	defer rows.Close()
	for rows.Next() {
		var x stallIssueRow
		if rows.Scan(&x.ID, &x.WorkspaceID, &x.Number, &x.Title, &x.Status, &x.Metadata, &x.LastActive) != nil {
			continue
		}
		issue, err := h.Queries.GetIssue(ctx, x.ID)
		if err != nil {
			continue
		}
		expiry := stallaction.Expire(ctx, stallTicket(issue), stallProbe{h: h, issue: issue}, time.Now())
		switch expiry.Verdict {
		case stallaction.ExpiryKeep:
			if err := h.keepInterceptedStall(ctx, issue, expiry); err == nil {
				affected++
			}
		case stallaction.ExpiryCancel:
			if err := h.cancelStallIssue(ctx, issue, expiry); err == nil {
				affected++
			}
		}
	}
	return affected, nil
}

// parentReadyForAutoCompletion is the quiet-parent rule. Child completion
// notifications remain synchronous and only wake the parent; this check is
// intentionally owned by the periodic stall patrol so an active run or any
// linked delivery can finish the parent before the rule fires.
func (h *Handler) parentReadyForAutoCompletion(ctx context.Context, parent db.Issue) bool {
	paused := stallTicket(parent).Paused()
	active, err := h.Queries.HasActiveTaskForIssue(ctx, parent.ID)
	if err != nil {
		return false
	}
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, parent.ID)
	if err != nil {
		return false
	}
	children, err := h.Queries.ListChildIssues(ctx, parent.ID)
	if err != nil || len(children) == 0 {
		return false
	}
	effective := h.childStatusResolver(ctx)
	statuses, err := resolveChildStatuses(children, effective)
	if err != nil {
		return false
	}
	return parentAutoCompletionEligible(parent.Status, paused, active, len(prs) > 0, len(children), allChildrenTerminal(children, h.realChildTerminalPredicate(ctx, statuses)))
}

// ReviewStallActionInternal announces a patrol candidate. The sweep ignores
// refusals; the ticket simply stays where it is.
func (h *Handler) ReviewStallActionInternal(ctx context.Context, issue db.Issue, reason string) error {
	_, err := h.announceStall(ctx, issue, reason)
	return err
}

// announceStall starts the public notice: the state machine decides, then
// the notice is written, commented and sent to the owners' inboxes.
func (h *Handler) announceStall(ctx context.Context, issue db.Issue, reason string) (stallaction.Announcement, error) {
	announced, err := stallaction.Announce(ctx, stallTicket(issue), stallProbe{h: h, issue: issue}, reason, h.stallArtifactCheck(ctx, issue), time.Now())
	if err != nil {
		return announced, err
	}
	if err := h.setStallMetadata(ctx, issue, announced.Patch); err != nil {
		return announced, err
	}
	h.stallComment(ctx, issue, "AI 停滞巡检公示："+announced.Reason+"\n公示 24 小时内可选择“保留”；无人拦截将自动取消。")
	h.notifyStallInbox(ctx, issue, "停滞票进入 24 小时公示", announced.Reason, "keep")
	return announced, nil
}

func (h *Handler) keepInterceptedStall(ctx context.Context, issue db.Issue, expiry stallaction.Expiry) error {
	if err := h.setStallMetadata(ctx, issue, expiry.Patch); err != nil {
		return err
	}
	h.stallComment(ctx, issue, "停滞公示到期，但"+expiry.Why+"，这张票继续保留，系统不会自动取消。")
	return nil
}

func (h *Handler) cancelStallIssue(ctx context.Context, issue db.Issue, expiry stallaction.Expiry) error {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: "cancelled", WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return err
	}
	if err = h.setStallMetadata(ctx, updated, expiry.Patch); err != nil {
		return err
	}
	h.stallComment(ctx, updated, "AI 停滞巡检："+expiry.Why+" 7 天内可撤销。")
	h.notifyStallInbox(ctx, updated, "停滞票已自动取消", expiry.Why, "undo")
	if fresh, err := h.Queries.GetIssue(ctx, updated.ID); err == nil {
		h.publish(protocol.EventIssueUpdated, util.UUIDToString(fresh.WorkspaceID), "system", "", map[string]any{"issue": service.IssueToMapResolved(ctx, h.Queries, fresh, h.getIssuePrefix(ctx, fresh.WorkspaceID))})
	}
	return nil
}
