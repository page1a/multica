package handler

import (
	"context"
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
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// CloseIssueRequest is the body of POST /api/issues/{id}/close — the one-shot
// close protocol behind `multica issue close` (DENE-859).
//
// Outcome is the status the caller claims the ticket is now in. Evidence is
// the comment body (PR link, test conclusion). The wait fields are the same
// four kinds `issue status blocked` takes (DENE-850). Verdict is the acceptance
// seat's pass line; the platform then runs the same merge chain a
// `comment add --verdict pass` runs.
type CloseIssueRequest struct {
	Outcome  string `json:"outcome"`
	Evidence string `json:"evidence"`
	Summary  string `json:"summary"`
	// ParentID is the comment to reply under. A comment-triggered task on
	// this issue that leaves it empty gets its trigger comment, the same
	// thread `comment add` would insist on.
	ParentID      *string `json:"parent_id,omitempty"`
	BlockedBy     *string `json:"blocked_by,omitempty"`
	WakeAt        *string `json:"wake_at,omitempty"`
	WaitCondition *string `json:"wait_condition,omitempty"`
	WaitProbe     *string `json:"wait_probe,omitempty"`
	WaitTimeout   *string `json:"wait_timeout,omitempty"`
	NeedsHuman    *string `json:"needs_human,omitempty"`
	// NoCodeReason is the declared exit from the DENE-869 review gate: an
	// agent's in_review without a linked open/draft/merged PR is refused
	// unless the ticket says why it carries no code (docs, research).
	NoCodeReason string `json:"no_code_reason,omitempty"`
	Verdict      string `json:"verdict,omitempty"`
}

// CloseIssueResponse reports what actually happened, not what was asked for:
// the status written, whether a PR merged, and who gets woken.
type CloseIssueResponse struct {
	Issue         IssueResponse           `json:"issue"`
	Comment       CommentResponse         `json:"comment"`
	Status        string                  `json:"status"`
	PrevStatus    string                  `json:"prev_status"`
	StatusChanged bool                    `json:"status_changed"`
	Close         map[string]string       `json:"close,omitempty"`
	Merged        bool                    `json:"merged"`
	PRURL         string                  `json:"pr_url,omitempty"`
	Woken         []string                `json:"woken"`
	Triggers      []CommentTriggerOutcome `json:"trigger_outcomes,omitempty"`
	Warnings      []string                `json:"warnings,omitempty"`
	// Summoned is true when --needs-human went through the summon entry.
	Summoned bool `json:"summoned,omitempty"`
}

var closeOutcomes = []string{issuestatus.Done, issuestatus.InReview, issuestatus.Blocked, issuestatus.Cancelled}

// closeRecord is the close.* metadata derived from the request plus the
// blockwait record. It is validated by closeprotocol.Validate before any
// write; the evidence comment id is filled in inside the transaction.
type closeRecord struct {
	meta  map[string]string
	block blockwait.Record
}

// CloseIssue is one transaction for the three writes an agent used to do by
// hand — evidence comment, status, close.* keys — with closeprotocol.Validate
// on the production path (it was written for DENE-231 and never called).
// Rejections name the missing item and how to supply it.
func (h *Handler) CloseIssue(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CloseIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))

	outcome := strings.ToLower(strings.TrimSpace(req.Outcome))
	if outcome == "" {
		writeError(w, http.StatusBadRequest, "缺 --outcome：done（做完）、in_review（等验收）、blocked（卡住）、cancelled（取消）四选一")
		return
	}
	if !closeOutcomeAllowed(outcome) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("--outcome %q 不是收口结论；只能是 %s", outcome, strings.Join(closeOutcomes, " / ")))
		return
	}
	evidence := strings.TrimSpace(sanitizeNullBytes(req.Evidence))
	if evidence == "" {
		writeError(w, http.StatusBadRequest, "缺 --evidence：收口必须留证据（PR 链接、测试结论、或说明为什么"+outcome+"），一句话也行")
		return
	}
	summary := strings.TrimSpace(sanitizeNullBytes(req.Summary))
	body := evidence
	if summary != "" {
		body = summary + "\n\n" + evidence
	}

	verdict := strings.ToLower(strings.TrimSpace(req.Verdict))
	if verdict != "" && verdict != "pass" {
		writeError(w, http.StatusBadRequest, "--verdict 只接受 pass。验收不通过不是收口：用 `multica issue comment add <id> --verdict hold --content-file <path>` 写明要改什么，票留在 in_review")
		return
	}

	// Reply placement mirrors CreateComment: a comment-triggered task on this
	// issue must stay in its trigger thread. Rather than 409 on an omitted
	// --parent, the close defaults to the trigger comment.
	parentID, parentComment, ok := h.resolveCloseParent(w, r, issue, req.ParentID, actorType)
	if !ok {
		return
	}

	if verdict == "pass" {
		h.closeIssueByVerdict(w, r, issue, outcome, body, parentID, parentComment, actorType, actorID)
		return
	}

	statusKey, _, ok := h.resolveIssueStatusKeyKind(w, r, issue.WorkspaceID, outcome)
	if !ok {
		return
	}
	if outcome == issuestatus.InReview && issue.ParentIssueID.Valid {
		writeError(w, http.StatusBadRequest, "子票不进 in_review：子票是执行票，由父票的验收席连同整棵子票树一起验收。做完就 --outcome done，卡住就 --outcome blocked")
		return
	}

	rec, rejection := h.deriveCloseRecord(r, issue, req, outcome, statusKey, body, actorType)
	if rejection != "" {
		writeError(w, http.StatusBadRequest, rejection)
		return
	}
	// Pre-flight: the record is checked against §6.1 with a placeholder
	// evidence id, so a bad close is refused before anything is written.
	if err := closeprotocol.Validate(closeProbe(rec.meta), statusKey, body); err != nil {
		writeError(w, http.StatusBadRequest, closeRejection(err))
		return
	}
	// The same gate `issue status` runs (DENE-857 / DENE-869): a done with
	// an open linked PR merges it first or is rewritten as a structured
	// block; an agent's in_review needs a linked PR or a --no-code reason,
	// and an empty reviewer slot gets a different-family acceptance seat or
	// is refused. The close reports whichever of those actually happened.
	var tr statusTransition
	if outcome == issuestatus.Done || outcome == issuestatus.InReview {
		tr = h.guardSilentStall(ctx, issue, statusKey, actorType, actorID, req.NoCodeReason, issue.AssigneeType, issue.AssigneeID, issue.ReviewerType, issue.ReviewerID, false)
		if tr.refuse != "" {
			writeError(w, http.StatusConflict, tr.refuse)
			return
		}
		if tr.status != "" && tr.status != statusKey {
			statusKey = tr.status
			outcome = tr.status
			rec = closeRecordFromGate(statusKey, tr)
			if err := closeprotocol.Validate(closeProbe(rec.meta), statusKey, body); err != nil {
				writeError(w, http.StatusBadRequest, closeRejection(err))
				return
			}
		}
		if tr.setReviewer {
			rec.meta[closeprotocol.KeyNextOwnerType] = tr.reviewerType.String
			rec.meta[closeprotocol.KeyNextOwnerID] = uuidToString(tr.reviewerID)
		}
	}

	prev := issue
	var created db.CreateCommentRow
	var updated db.Issue
	writeErr := func() error {
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		qtx := h.Queries.WithTx(tx)
		if err := assertIssueStatusStillActive(ctx, qtx, issue.WorkspaceID, statusKey); err != nil {
			return err
		}
		created, err = qtx.CreateComment(ctx, db.CreateCommentParams{
			ID:           dbid.NewV7(),
			IssueID:      issue.ID,
			WorkspaceID:  issue.WorkspaceID,
			AuthorType:   actorType,
			AuthorID:     parseUUID(actorID),
			Content:      body,
			Type:         "comment",
			ParentID:     parentID,
			SourceTaskID: closeSourceTask(r, actorType),
		})
		if err != nil {
			return err
		}
		updated = issue
		if tr.setReviewer {
			updated, err = qtx.SetIssueReviewerIfUnset(ctx, db.SetIssueReviewerIfUnsetParams{
				ReviewerType: tr.reviewerType.String,
				ReviewerID:   tr.reviewerID,
				ID:           issue.ID,
				WorkspaceID:  issue.WorkspaceID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				// Someone filled the slot between the gate and the write;
				// keep theirs, the record is corrected after reload.
				updated, err = qtx.GetIssue(ctx, issue.ID)
			}
			if err != nil {
				return err
			}
		}
		if statusKey != updated.Status {
			updated, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID:          issue.ID,
				Status:      statusKey,
				WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				return err
			}
		}
		if tr.setReviewer && updated.ReviewerType.Valid {
			rec.meta[closeprotocol.KeyNextOwnerType] = updated.ReviewerType.String
			rec.meta[closeprotocol.KeyNextOwnerID] = uuidToString(updated.ReviewerID)
		}
		rec.meta[closeprotocol.KeyEvidenceCommentID] = uuidToString(created.ID)
		rec.meta[closeprotocol.KeyAt] = time.Now().UTC().Format(time.RFC3339)
		if err := closeprotocol.Validate(rec.meta, updated.Status, body); err != nil {
			return err
		}
		for key, value := range rec.meta {
			if err := setIssueMetaStringTx(ctx, qtx, updated, key, value); err != nil {
				return err
			}
		}
		if outcome != issuestatus.Blocked {
			// A record left from an earlier blocked close must not survive a
			// delivered/awaiting one (§6.1: blocker fields empty outside blocked).
			for _, key := range []string{closeprotocol.KeyBlockKind, closeprotocol.KeyBlockAction} {
				if _, err := qtx.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: updated.ID, WorkspaceID: updated.WorkspaceID, Key: key}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
		}
		return tx.Commit(ctx)
	}()
	if writeErr != nil {
		if writeIssueStatusRaceError(w, writeErr) {
			return
		}
		var ce *closeprotocol.Error
		if errors.As(writeErr, &ce) {
			writeError(w, http.StatusBadRequest, closeRejection(ce))
			return
		}
		if errors.Is(writeErr, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		slog.Warn("close issue failed", append(logger.RequestAttrs(r), "error", writeErr, "issue_id", uuidToString(issue.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to close issue: "+writeErr.Error())
		return
	}

	comment := created.Comment()
	resp := CloseIssueResponse{
		Status:        updated.Status,
		PrevStatus:    prev.Status,
		StatusChanged: prev.Status != updated.Status,
		Close:         rec.meta,
	}
	resp.Comment = commentToResponse(comment, nil, nil)
	resp.Comment.IssueRevision = created.IssueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"comment":             resp.Comment,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        updated.Status,
		"issue_revision":      created.IssueRevision,
	})
	h.TaskService.AutoUnresolveThreadOnReply(ctx, parentComment, uuidToString(issue.WorkspaceID), actorType, actorID)

	// Post-commit hooks, in the order UpdateIssue and CreateComment run them.
	// Each is best-effort on its own; the close itself is already durable.
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	identifier := issueToResponse(updated, prefix).Identifier
	if resp.StatusChanged {
		h.publishCloseStatus(r, prev, updated, prefix, actorType, actorID)
		h.syncBlockWait(ctx, prev, updated)
	}
	if outcome == issuestatus.Blocked {
		h.persistBlockRecord(ctx, updated, rec.block)
	}
	// --needs-human names a person; the summon entry is what makes them hear
	// it (inbox, subscription, a visible @). Before the evidence comment's own
	// triggers run, so an @ of the same person there dedupes against this call.
	if updated.Status == issuestatus.Blocked || updated.Status == issuestatus.InReview {
		if rec.meta[closeprotocol.KeyNextOwnerType] == closeprotocol.OwnerMember && strings.TrimSpace(deref(req.NeedsHuman)) != "" {
			h.summonNeedsHuman(ctx, updated, rec.meta[closeprotocol.KeyNextOwnerID], actorType, actorID, closeSummonReason(summary, evidence))
			resp.Summoned = true
		}
	}
	updated = h.finishStatusTransition(ctx, updated, tr)
	resp.Merged = tr.merged
	resp.PRURL = tr.prURL
	originator := h.invokeOriginatorFromRequest(r, actorType, actorID)
	resp.Triggers = h.triggerTasksForComment(ctx, updated, comment, parentComment, actorType, actorID, originator, nil)
	if resp.StatusChanged {
		waiters := h.listBlockWaiters(ctx, updated, identifier)
		h.notifyParentOfChildDone(ctx, prev, updated)
		h.notifyWaitersOfIssueDone(ctx, prev, updated)
		h.RouteIssueAsync(r, uuidToString(issue.WorkspaceID), uuidToString(issue.ID))
		resp.Woken = describeCloseWake(updated, rec, prefix, len(waiters))
	} else {
		resp.Woken = []string{"状态没变（本来就是 " + updated.Status + "），只补了证据和收口记录；没有叫醒任何人"}
	}
	if tr.merged {
		resp.Woken = append([]string{"关联 PR 已先合并（" + tr.prURL + "），再写 done"}, resp.Woken...)
	}
	if tr.status != "" && tr.status != strings.ToLower(strings.TrimSpace(req.Outcome)) {
		resp.Warnings = append(resp.Warnings, "你要的是 "+strings.ToLower(strings.TrimSpace(req.Outcome))+"，平台实际落的是 "+updated.Status+"："+strings.TrimSpace(tr.note))
	}
	// The gate's note (seat filled, no-code reason recorded) is a system
	// comment already; the reply repeats it so the caller quotes what
	// actually happened. A rewritten status carries it in Warnings instead.
	if note := strings.TrimSpace(tr.note); note != "" && len(resp.Warnings) == 0 {
		resp.Woken = append(resp.Woken, note)
	}
	for _, t := range resp.Triggers {
		resp.Woken = append(resp.Woken, "证据里 @ 到的对象："+describeTrigger(t))
	}
	if resp.Summoned {
		resp.Woken = append(resp.Woken, "已替你叫 --needs-human 的人：收件箱、关注、票上 @ 都已送到；他回复后平台叫醒执行智能体")
	}

	reloaded, err := h.Queries.GetIssue(ctx, issue.ID)
	if err == nil {
		updated = reloaded
	}
	resp.Issue = issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, issue.WorkspaceID, &resp.Issue)
	slog.Info("issue closed", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "outcome", outcome, "status", updated.Status)...)
	writeJSON(w, http.StatusOK, resp)
}

// closeIssueByVerdict is the acceptance seat's close: the pass line is posted
// as a comment and the DENE-850 release chain decides whether the ticket
// ends as done (merged or nothing to merge) or blocked (merge failed). The
// close.* record is written from the state the chain actually produced.
func (h *Handler) closeIssueByVerdict(w http.ResponseWriter, r *http.Request, issue db.Issue, outcome, body string, parentID pgtype.UUID, parentComment *db.Comment, actorType, actorID string) {
	ctx := r.Context()
	if outcome != issuestatus.Done {
		writeError(w, http.StatusBadRequest, "--verdict pass 的收口结论只能是 --outcome done：验收通过就由平台合并并关票")
		return
	}
	if issue.Status != issuestatus.InReview {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("票现在是 %s，不在 in_review，没有可验收的交付；--verdict pass 只在票等验收时有效", issue.Status))
		return
	}
	probe := db.Comment{AuthorType: actorType, AuthorID: parseUUID(actorID)}
	if !h.authorIsReviewer(issue, probe) {
		writeError(w, http.StatusForbidden, "--verdict pass 只有这张票的验收席能给；你不是它的 reviewer。执行人交付用 --outcome in_review，不带 --verdict")
		return
	}
	body, err := blockwait.AppendVerdict(body, "pass")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   actorType,
		AuthorID:     parseUUID(actorID),
		Content:      body,
		Type:         "comment",
		ParentID:     parentID,
		SourceTaskID: closeSourceTask(r, actorType),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if err != nil {
		slog.Warn("close issue verdict comment failed", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issue.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to post verdict: "+err.Error())
		return
	}
	comment := created.Comment()
	resp := CloseIssueResponse{PrevStatus: issue.Status}
	resp.Comment = commentToResponse(comment, nil, nil)
	resp.Comment.IssueRevision = created.IssueRevision
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"comment":             resp.Comment,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	h.TaskService.AutoUnresolveThreadOnReply(ctx, parentComment, uuidToString(issue.WorkspaceID), actorType, actorID)
	originator := h.invokeOriginatorFromRequest(r, actorType, actorID)
	resp.Triggers = h.triggerTasksForComment(ctx, issue, comment, parentComment, actorType, actorID, originator, nil)

	// Same chain as `comment add --verdict pass`: once per in_review stay.
	out := h.releaseOnAcceptance(ctx, issue)
	resp.Merged = out.Merged
	resp.PRURL = out.PRURL

	updated, err := h.Queries.GetIssue(ctx, issue.ID)
	if err != nil {
		updated = issue
		updated.Status = out.Status
	}
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	resp.Status = updated.Status
	resp.StatusChanged = updated.Status != issue.Status
	if !out.Released {
		resp.Warnings = append(resp.Warnings, "这段 in_review 已经放行过一次（block.released=pass），本次 pass 只留了言，没有再合并或改状态")
	}
	rec := closeRecordAfterRelease(updated, parseIssueMetadata(updated.Metadata), uuidToString(comment.ID))
	if rec != nil {
		if err := closeprotocol.Validate(rec, updated.Status, body); err != nil {
			resp.Warnings = append(resp.Warnings, "收口记录没写："+closeRejection(err))
		} else {
			for key, value := range rec {
				h.setIssueMetaString(ctx, updated, key, value)
			}
			if updated.Status != issuestatus.Blocked {
				h.deleteIssueMeta(ctx, updated, closeprotocol.KeyBlockKind)
				h.deleteIssueMeta(ctx, updated, closeprotocol.KeyBlockAction)
			}
			resp.Close = rec
		}
	}
	switch {
	case out.Merged:
		resp.Woken = []string{fmt.Sprintf("PR 已合并（%s），票已置 done", out.PRURL)}
		if out.Baseline != "" {
			resp.Woken = append(resp.Woken, out.Baseline)
		}
	case updated.Status == issuestatus.Done:
		resp.Woken = []string{"没有待合并的 PR，票已置 done"}
	case updated.Status == issuestatus.Blocked:
		resp.Woken = []string{"验收通过但合并没成功，票改成 blocked 并写了等待条件：" + strings.TrimSpace(out.Note)}
	default:
		resp.Woken = []string{"票仍是 " + updated.Status}
	}
	if updated.Status == issuestatus.Done {
		if updated.ParentIssueID.Valid {
			resp.Woken = append(resp.Woken, "父票已收到子票完成通知；本 stage 全部终态时下一 stage 会被叫醒")
		}
		resp.Woken = append(resp.Woken, "等这张票的（close.waiting_on / block.blocked_by）已被叫醒")
	}
	for _, t := range resp.Triggers {
		resp.Woken = append(resp.Woken, "证据里 @ 到的对象："+describeTrigger(t))
	}
	if reloaded, err := h.Queries.GetIssue(ctx, issue.ID); err == nil {
		updated = reloaded
	}
	resp.Issue = issueToResponse(updated, prefix)
	h.fillStatusCategory(ctx, issue.WorkspaceID, &resp.Issue)
	slog.Info("issue closed by verdict", append(logger.RequestAttrs(r), "issue_id", uuidToString(issue.ID), "status", updated.Status, "merged", out.Merged)...)
	writeJSON(w, http.StatusOK, resp)
}

// deriveCloseRecord turns the outcome and wait fields into the close.* keys
// (§6.1) and, for blocked, the DENE-850 wait record. A non-empty rejection
// is the 400 body, naming what is missing.
func (h *Handler) deriveCloseRecord(r *http.Request, issue db.Issue, req CloseIssueRequest, outcome, statusKey, body, actorType string) (closeRecord, string) {
	meta := map[string]string{
		closeprotocol.KeyStatus:        statusKey,
		closeprotocol.KeyNextOwnerType: closeprotocol.OwnerNone,
		closeprotocol.KeyNextOwnerID:   "",
		closeprotocol.KeyWaitingOn:     "",
		closeprotocol.KeyWakeAction:    closeprotocol.WakeNone,
	}
	rec := closeRecord{meta: meta}
	switch outcome {
	case issuestatus.Done, issuestatus.Cancelled:
		meta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionDelivered
		if issue.ParentIssueID.Valid {
			meta[closeprotocol.KeyWakeAction] = closeprotocol.WakeStageDone
		}
	case issuestatus.InReview:
		meta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionAwaitingReview
		meta[closeprotocol.KeyWakeAction] = closeprotocol.WakeRoute
		if issue.ReviewerType.Valid && issue.ReviewerID.Valid {
			meta[closeprotocol.KeyNextOwnerType] = issue.ReviewerType.String
			meta[closeprotocol.KeyNextOwnerID] = uuidToString(issue.ReviewerID)
			if issue.ReviewerType.String == closeprotocol.OwnerMember {
				meta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionAwaitingHuman
			}
		}
		if human := strings.TrimSpace(deref(req.NeedsHuman)); human != "" {
			meta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionAwaitingHuman
			meta[closeprotocol.KeyNextOwnerType] = closeprotocol.OwnerMember
			meta[closeprotocol.KeyNextOwnerID] = human
		}
	case issuestatus.Blocked:
		block, _, rejection := h.gateBlockedStatus(r, issue, UpdateIssueRequest{
			BlockedBy:     req.BlockedBy,
			WakeAt:        req.WakeAt,
			WaitCondition: req.WaitCondition,
			WaitProbe:     req.WaitProbe,
			WaitTimeout:   req.WaitTimeout,
			NeedsHuman:    req.NeedsHuman,
		}, actorType)
		if rejection != "" {
			return rec, rejection
		}
		if !block.Structured() {
			// A member is allowed to drag a card into blocked without a wait;
			// a close under the protocol is not that gesture.
			return rec, blockwait.Rejection(blockwait.SuggestFromComments(h.recentCommentBodies(r.Context(), issue)))
		}
		rec.block = block
		meta[closeprotocol.KeyConclusion] = closeprotocol.ConclusionBlocked
		kind, action := blockKindFor(block, strings.TrimSpace(req.Summary))
		meta[closeprotocol.KeyBlockKind] = kind
		meta[closeprotocol.KeyBlockAction] = action
		if len(block.BlockedBy) > 0 {
			meta[closeprotocol.KeyWaitingOn] = block.BlockedBy[0]
		}
		if human := strings.TrimSpace(block.NeedsHuman); human != "" {
			meta[closeprotocol.KeyNextOwnerType] = closeprotocol.OwnerMember
			meta[closeprotocol.KeyNextOwnerID] = human
		}
	}
	return rec, ""
}

// closeProbe is the record as closeprotocol.Validate will see it, with the
// two fields only the transaction can fill stubbed in.
func closeProbe(meta map[string]string) map[string]string {
	probe := cloneStringMap(meta)
	probe[closeprotocol.KeyEvidenceCommentID] = "pending"
	probe[closeprotocol.KeyAt] = time.Now().UTC().Format(time.RFC3339)
	return probe
}

// closeRecordFromGate is the record for a close the DENE-857 gate rewrote:
// the caller asked for done, the linked PR did not merge, and the ticket is
// blocked on the gate's wait record instead.
func closeRecordFromGate(statusKey string, tr statusTransition) closeRecord {
	kind, action := blockKindFor(tr.block, "")
	meta := map[string]string{
		closeprotocol.KeyStatus:        statusKey,
		closeprotocol.KeyConclusion:    closeprotocol.ConclusionBlocked,
		closeprotocol.KeyNextOwnerType: closeprotocol.OwnerNone,
		closeprotocol.KeyNextOwnerID:   "",
		closeprotocol.KeyWaitingOn:     "",
		closeprotocol.KeyWakeAction:    closeprotocol.WakeNone,
		closeprotocol.KeyBlockKind:     kind,
		closeprotocol.KeyBlockAction:   action,
	}
	if len(tr.block.BlockedBy) > 0 {
		meta[closeprotocol.KeyWaitingOn] = tr.block.BlockedBy[0]
	}
	return closeRecord{meta: meta, block: tr.block}
}

// blockKindFor maps the wait record to the §6.1 blocker kind and a short
// action line (≤80 runes). The summary wins when the caller wrote one.
func blockKindFor(block blockwait.Record, summary string) (kind, action string) {
	switch {
	case len(block.BlockedBy) > 0:
		kind = closeprotocol.BlockDependency
		action = "等 " + strings.Join(block.BlockedBy, "、") + " 终态"
	case strings.TrimSpace(block.NeedsHuman) != "":
		kind = closeprotocol.BlockDecision
		action = "等指定的人拍板"
	default:
		kind = closeprotocol.BlockExternal
		action = strings.TrimSpace(block.WaitCondition)
		if action == "" && block.HasWakeAt {
			action = "到点复查：" + block.WakeAt.UTC().Format(time.RFC3339)
		}
	}
	if summary != "" {
		action = summary
	}
	if action == "" {
		action = "等待外部条件"
	}
	return kind, truncateRunes(action, 80)
}

// closeRecordAfterRelease derives the close.* record from what the merge
// chain left behind. A ticket the chain could not move stays as it is; the
// reviewer's pass is then only a comment and no record is written.
func closeRecordAfterRelease(issue db.Issue, meta map[string]any, evidenceID string) map[string]string {
	rec := map[string]string{
		closeprotocol.KeyStatus:            issue.Status,
		closeprotocol.KeyEvidenceCommentID: evidenceID,
		closeprotocol.KeyNextOwnerType:     closeprotocol.OwnerNone,
		closeprotocol.KeyNextOwnerID:       "",
		closeprotocol.KeyWakeAction:        closeprotocol.WakeNone,
		closeprotocol.KeyWaitingOn:         "",
		closeprotocol.KeyAt:                time.Now().UTC().Format(time.RFC3339),
	}
	switch issue.Status {
	case issuestatus.Done:
		rec[closeprotocol.KeyConclusion] = closeprotocol.ConclusionDelivered
		if issue.ParentIssueID.Valid {
			rec[closeprotocol.KeyWakeAction] = closeprotocol.WakeStageDone
		}
	case issuestatus.Blocked:
		rec[closeprotocol.KeyConclusion] = closeprotocol.ConclusionBlocked
		block := blockwait.ParseMetadata(meta)
		kind, action := blockKindFor(block, "")
		rec[closeprotocol.KeyBlockKind] = kind
		rec[closeprotocol.KeyBlockAction] = action
		if len(block.BlockedBy) > 0 {
			rec[closeprotocol.KeyWaitingOn] = block.BlockedBy[0]
		}
	default:
		return nil
	}
	return rec
}

// resolveCloseParent picks the thread the evidence lands in. It mirrors the
// CreateComment guard for comment-triggered tasks, but defaults to the
// trigger comment instead of refusing a top-level post.
func (h *Handler) resolveCloseParent(w http.ResponseWriter, r *http.Request, issue db.Issue, raw *string, actorType string) (pgtype.UUID, *db.Comment, bool) {
	var parentID pgtype.UUID
	if raw != nil && strings.TrimSpace(*raw) != "" {
		parsed, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(*raw), "parent_id")
		if !ok {
			return parentID, nil, false
		}
		parentID = parsed
	}
	if actorType == "agent" {
		if task, ok := h.taskFromRequestHeader(r); ok && task.IssueID.Valid && uuidToString(task.IssueID) == uuidToString(issue.ID) && task.TriggerCommentID.Valid {
			if !parentID.Valid {
				parentID = task.TriggerCommentID
			} else if !taskCoversReplyParent(task, parentID) {
				writeError(w, http.StatusConflict, "parent_id "+uuidToString(parentID)+" is not a comment this task may reply under; set --parent to "+uuidToString(task.TriggerCommentID)+" or leave it out")
				return parentID, nil, false
			}
		}
	}
	if !parentID.Valid {
		return parentID, nil, true
	}
	parent, err := h.Queries.GetComment(r.Context(), parentID)
	if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusBadRequest, "invalid parent comment")
		return parentID, nil, false
	}
	return parentID, &parent, true
}

func closeSourceTask(r *http.Request, actorType string) pgtype.UUID {
	if actorType != "agent" {
		return pgtype.UUID{}
	}
	if raw := r.Header.Get("X-Task-ID"); raw != "" {
		if id, err := parseUUIDStrict(raw); err == nil {
			return id
		}
	}
	return pgtype.UUID{}
}

func parseUUIDStrict(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

func setIssueMetaStringTx(ctx context.Context, q *db.Queries, issue db.Issue, key, value string) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       raw,
	})
	// No row means the key already holds this value (the query skips no-op
	// writes). A second close — blocked, then done — repeats keys such as
	// close.next_owner_type=none and must not fail as "issue not found".
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

func (h *Handler) publishCloseStatus(r *http.Request, prev, issue db.Issue, prefix, actorType, actorID string) {
	resp := issueToResponse(issue, prefix)
	h.fillStatusCategory(r.Context(), issue.WorkspaceID, &resp)
	h.publish(protocol.EventIssueUpdated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"issue":              resp,
		"assignee_changed":   false,
		"status_changed":     true,
		"prev_status":        prev.Status,
		"prev_assignee_type": textToPtr(prev.AssigneeType),
		"prev_assignee_id":   uuidToPtr(prev.AssigneeID),
		"creator_type":       prev.CreatorType,
		"creator_id":         uuidToString(prev.CreatorID),
	})
}

// describeCloseWake says who the platform wakes for this close, in words the
// caller can paste into its final report.
func describeCloseWake(issue db.Issue, rec closeRecord, prefix string, waiters int) []string {
	var out []string
	switch issue.Status {
	case issuestatus.Done, issuestatus.Cancelled:
		if issue.ParentIssueID.Valid {
			out = append(out, "父票已收到子票终态通知；本 stage 全部终态时下一 stage 会被叫醒（wake_action=stage_done）")
		} else {
			out = append(out, "顶层票，没有父票要通知（wake_action=none）")
		}
		if waiters > 0 {
			out = append(out, fmt.Sprintf("等这张票的 %d 张票已被叫醒", waiters))
		}
	case issuestatus.InReview:
		owner := rec.meta[closeprotocol.KeyNextOwnerType]
		switch owner {
		case closeprotocol.OwnerMember:
			out = append(out, "等人验收：路由只通知这位成员，不排 run（close.conclusion=awaiting_human）")
		case closeprotocol.OwnerAgent, closeprotocol.OwnerSquad:
			out = append(out, "路由会把票交给验收席 "+owner+"/"+rec.meta[closeprotocol.KeyNextOwnerID]+"，本命令不额外 @（wake_action=route）")
		default:
			out = append(out, "没有指定 reviewer：路由按验收席属性挑人；挑不到会写成 blocked 等人拍板（wake_action=route）")
		}
	case issuestatus.Blocked:
		block := rec.block
		if len(block.BlockedBy) > 0 {
			out = append(out, "等 "+strings.Join(block.BlockedBy, "、")+" 进入终态时叫醒你")
		}
		if block.HasWakeAt {
			out = append(out, "到 "+block.WakeAt.UTC().Format(time.RFC3339)+" 巡检叫醒你")
		}
		if strings.TrimSpace(block.WaitCondition) != "" {
			if block.HasWaitTimeout {
				out = append(out, "等待条件到期（"+block.WaitTimeout.UTC().Format(time.RFC3339)+"）巡检叫醒你")
			} else {
				out = append(out, "等待条件没有 --wait-timeout，只在人来解除时叫醒")
			}
		}
		if strings.TrimSpace(block.NeedsHuman) != "" {
			out = append(out, "等成员 "+strings.TrimSpace(block.NeedsHuman)+" 拍板：只留言不排 run")
		}
	}
	return out
}

func describeTrigger(t CommentTriggerOutcome) string {
	raw, err := json.Marshal(t)
	if err != nil {
		return fmt.Sprint(t)
	}
	return string(raw)
}

func closeRejection(err error) string {
	var ce *closeprotocol.Error
	if !errors.As(err, &ce) {
		return err.Error()
	}
	hint := ""
	switch ce.Rule {
	case "awaiting_human":
		hint = "；等人验收要么票的 reviewer 是成员，要么带 --needs-human <member-id>"
	case "block_kind", "block_action", "blocker":
		hint = "；卡住至少带一种：--blocked-by <票> / --wake-at <RFC3339> / --wait-condition 配 --wait-timeout / --needs-human <member-id>，动作说明可用 --summary（80 字内）"
	case "waiting_on":
		hint = "；done 不能同时还在等别的票，先解除再收口"
	}
	return "收口记录不合规（" + ce.Rule + "）：" + ce.Msg + hint
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func closeOutcomeAllowed(v string) bool {
	for _, item := range closeOutcomes {
		if item == v {
			return true
		}
	}
	return false
}

// closeSummonReason is the "why" a --needs-human close hands the person: the
// caller's summary when there is one, otherwise the head of the evidence.
func closeSummonReason(summary, evidence string) string {
	if s := strings.TrimSpace(summary); s != "" {
		return truncateRunes(s, 200)
	}
	return truncateRunes(strings.TrimSpace(evidence), 200)
}
