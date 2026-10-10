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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const blockPatrolLimit = 50

// gateBlockedStatus rejects an agent move into blocked that does not say what
// the issue is waiting on. Members can still drag a card; the patrol picks an
// unstructured one up. A non-empty rejection is a 400.
func (h *Handler) gateBlockedStatus(r *http.Request, issue db.Issue, req UpdateIssueRequest, actorType string) (blockwait.Record, bool, string) {
	rec, err := blockwait.Accept(parseIssueMetadata(issue.Metadata), blockwait.Input{
		BlockedBy:     deref(req.BlockedBy),
		WakeAt:        deref(req.WakeAt),
		WaitCondition: deref(req.WaitCondition),
		WaitProbe:     deref(req.WaitProbe),
		WaitTimeout:   deref(req.WaitTimeout),
		NeedsHuman:    deref(req.NeedsHuman),
	}, time.Now())
	if err != nil {
		return rec, false, err.Error()
	}
	persist := !blockInput(req).Empty()
	if rec.Structured() {
		return rec, persist, ""
	}
	if actorType != "agent" {
		return rec, false, ""
	}
	bodies := h.recentCommentBodies(r.Context(), issue)
	return rec, false, blockwait.Rejection(blockwait.SuggestFromComments(bodies))
}

func blockInput(req UpdateIssueRequest) blockwait.Input {
	return blockwait.Input{
		BlockedBy:     deref(req.BlockedBy),
		WakeAt:        deref(req.WakeAt),
		WaitCondition: deref(req.WaitCondition),
		WaitProbe:     deref(req.WaitProbe),
		WaitTimeout:   deref(req.WaitTimeout),
		NeedsHuman:    deref(req.NeedsHuman),
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func (h *Handler) recentCommentBodies(ctx context.Context, issue db.Issue) []string {
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       30,
	})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(comments))
	for _, c := range comments {
		out = append(out, c.Content)
	}
	return out
}

func (h *Handler) persistBlockRecord(ctx context.Context, issue db.Issue, rec blockwait.Record) {
	// A new wait record starts a fresh probe lifecycle on every block path
	// (close, status, batch, accept): a result from the previous wait must
	// not suppress this one's wakeup.
	for _, key := range []string{blockwait.KeyProbeStatus, blockwait.KeyProbeAt, blockwait.KeyProbeOutput, blockwait.KeyProbeNotified} {
		h.deleteIssueMeta(ctx, issue, key)
	}
	for key, value := range rec.Pairs() {
		h.setIssueMetaString(ctx, issue, key, value)
	}
}

func (h *Handler) deleteIssueMeta(ctx context.Context, issue db.Issue, key string) {
	if _, err := h.Queries.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("block wait: metadata delete failed", "error", err, "issue_id", uuidToString(issue.ID), "key", key)
	}
}

// syncBlockWait drops a wait that no longer belongs to the status the issue
// just entered, and marks a newly blocked or in-review issue as watched so
// the patrol does not walk tickets that were already sitting there.
func (h *Handler) syncBlockWait(ctx context.Context, prev, next db.Issue) {
	if prev.Status == next.Status {
		return
	}
	if prev.Status == "blocked" && next.Status != "blocked" {
		for _, key := range blockwait.WaitKeys() {
			h.deleteIssueMeta(ctx, next, key)
		}
		// block.watched is not a wait key, but a row that left blocked must
		// stop being a patrol candidate: DENE-1002 added in_progress to the
		// sweep, and a plain blocked -> in_progress move would otherwise stay
		// watched forever with nothing to wake for.
		h.deleteIssueMeta(ctx, next, blockwait.KeyWatched)
	}
	if prev.Status == "in_review" && next.Status != "in_review" {
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReviewNudged)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleaseHold)
	}
	if prev.Status == "done" && next.Status != "done" {
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
	}
	if next.Status == "in_review" && prev.Status != "in_review" {
		for _, key := range blockwait.ClockKeys() {
			h.deleteIssueMeta(ctx, next, key)
		}
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReviewNudged)
		h.setIssueMetaString(ctx, next, blockwait.KeyReviewRound, time.Now().UTC().Format(time.RFC3339))
		h.setIssueMetaString(ctx, next, blockwait.KeyWatched, blockwait.WatchedYes)
	}
	if next.Status == "blocked" && prev.Status != "blocked" {
		h.setIssueMetaString(ctx, next, blockwait.KeyWatched, blockwait.WatchedYes)
	}
}

func (h *Handler) setIssueMetaString(ctx context.Context, issue db.Issue, key, value string) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if _, err := h.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Key:         key,
		Value:       raw,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("block wait: metadata write failed", "error", err, "issue_id", uuidToString(issue.ID), "key", key)
	}
}

// listBlockWaiters adds block.blocked_by matches to the close.waiting_on set.
func (h *Handler) listBlockWaiters(ctx context.Context, issue db.Issue, identifier string) []db.Issue {
	seen := map[string]db.Issue{}
	add := func(rows []db.Issue) {
		for _, row := range rows {
			seen[uuidToString(row.ID)] = row
		}
	}
	primary, err := h.Queries.ListIssuesWaitingOn(ctx, db.ListIssuesWaitingOnParams{
		WorkspaceID:         issue.WorkspaceID,
		WaitingOnIdentifier: waitingOnFilter(identifier),
		WaitingOnID:         waitingOnFilter(uuidToString(issue.ID)),
	})
	if err != nil {
		slog.Warn("waiting_on: failed to list waiters", "error", err, "issue_id", uuidToString(issue.ID))
	} else {
		add(primary)
	}
	for _, token := range []string{identifier, uuidToString(issue.ID)} {
		if !blockwaitToken(token) {
			continue
		}
		extra, err := h.Queries.ListIssuesBlockedByToken(ctx, db.ListIssuesBlockedByTokenParams{
			WorkspaceID: issue.WorkspaceID,
			Token:       token,
		})
		if err != nil {
			slog.Warn("block wait: failed to list blocked_by waiters", "error", err, "token", token)
			continue
		}
		add(extra)
	}
	out := make([]db.Issue, 0, len(seen))
	for _, row := range seen {
		out = append(out, row)
	}
	return out
}

func blockwaitToken(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r == '%' || r == '_' || r == '\\' {
			return false
		}
	}
	return true
}

// noteBlockClearedOnSource leaves the human sentence on the issue that just
// unblocked someone. mention://issue does not start a run.
func (h *Handler) noteBlockClearedOnSource(ctx context.Context, completed, waiter db.Issue, waiterIdentifier string) {
	content := fmt.Sprintf(
		"挡路解除了，已经叫醒等待方 [%s](mention://issue/%s)。",
		waiterIdentifier, uuidToString(waiter.ID),
	)
	h.postBlockComment(ctx, completed, content)
}

func (h *Handler) postBlockComment(ctx context.Context, issue db.Issue, content string) db.Comment {
	created, err := h.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("block wait: create comment failed", "error", err, "issue_id", uuidToString(issue.ID))
		return db.Comment{}
	}
	comment := created.Comment()
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
	return comment
}

// maybeReleaseOnAcceptance runs when a reviewer says the work passed and the
// issue is still in review. DENE-810 only told the reviewer to merge; this
// performs the close, or turns a real merge block into a structured wait.
func (h *Handler) maybeReleaseOnAcceptance(ctx context.Context, issue db.Issue, comment db.Comment) {
	if comment.AuthorType == "system" || comment.Type == "system" {
		return
	}
	if issue.Status != "in_review" {
		return
	}
	if !h.authorIsReviewer(issue, comment) {
		return
	}
	if !blockwait.IsAcceptancePass(comment.Content) {
		if blockwait.LooksLikePassHint(comment.Content) {
			h.postBlockComment(ctx, issue, "这句看起来像验收通过。平台只认单独一行的 verdict: pass，或者评论时带上 --verdict pass。写在句子里的「通过」不会合并，也不会关票。")
		}
		return
	}
	h.releaseOnAcceptance(ctx, issue)
}

// releaseOutcome is what the merge chain actually did, so a caller that has
// to report honestly (issue close --verdict pass, DENE-859) can say whether
// the PR merged and where the ticket ended up instead of guessing.
type releaseOutcome struct {
	// Released is false when the pass was ignored: already released once.
	Released bool
	// Status the issue holds after the chain: done, blocked, or the status it
	// already had when nothing could be written.
	Status string
	// Merged is true when an open PR was merged by this call.
	Merged bool
	// PRURL is the open PR the chain looked at, if any.
	PRURL string
	// Note is the sentence the chain left on the issue.
	Note string
	// Baseline is the "因主线原有失败放行" sentence when the merge let red
	// checks through because the base branch already had them (DENE-892).
	Baseline string
	// Hold is the hold kind a pass that could not merge was kept in review
	// for (DENE-1219); HoldNext is what clears it.
	Hold     string
	HoldNext string
}

// releaseOnAcceptance is the once-per-stay half of maybeReleaseOnAcceptance:
// the caller has already checked the author is the reviewer and the body
// carries the pass line.
func (h *Handler) releaseOnAcceptance(ctx context.Context, issue db.Issue) releaseOutcome {
	meta := parseIssueMetadata(issue.Metadata)
	if blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass {
		return releaseOutcome{Status: issue.Status}
	}
	h.setIssueMetaString(ctx, issue, blockwait.KeyReleased, blockwait.ReleasedPass)
	out := h.releaseAcceptedIssue(ctx, issue, blockwait.Decision{Reason: "验收已经通过。"})
	out.Released = true
	return out
}

func (h *Handler) authorIsReviewer(issue db.Issue, comment db.Comment) bool {
	if !issue.ReviewerType.Valid || !issue.ReviewerID.Valid || !comment.AuthorID.Valid {
		return false
	}
	return issue.ReviewerType.String == comment.AuthorType && issue.ReviewerID == comment.AuthorID
}

// releasePlan is what a pass would do right now, without doing it: the
// linked PRs, the release decision, and the delivery the merge is checked
// against. ok is false when the PRs could not be read.
func (h *Handler) releasePlan(ctx context.Context, issue db.Issue) (prs []db.ListPullRequestsByIssueRow, decision blockwait.Decision, delivery *service.IssueDelivery, ok bool) {
	view, ensureErr := h.ensureIssueDeliveries(ctx, issue)
	var err error
	if ensureErr != nil {
		slog.Warn("block wait: delivery lookup failed", "error", ensureErr, "issue_id", uuidToString(issue.ID))
		prs, err = h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	} else {
		prs = view.GateRows()
	}
	if err != nil {
		slog.Warn("block wait: list pull requests failed", "error", err, "issue_id", uuidToString(issue.ID))
		return nil, blockwait.Decision{}, nil, false
	}
	prs = h.dropLineChildPulls(ctx, issue, prs)
	decision = blockwait.DecideRelease(h.gatePRSnapshots(ctx, prs), time.Now())
	// The delivery aggregate (DENE-820) decides whether anything is still
	// unaccounted for before the pass is allowed to close or merge. An
	// unresolved rescue line or an unclassified second line holds the pass:
	// the reviewer said the work is good, but the platform cannot yet say
	// which branch that work is on, and the executor is the one who can sort
	// it out.
	if decision.Action == blockwait.ReleaseDone || decision.Action == blockwait.ReleaseMerge {
		var deliveryErr error
		delivery, deliveryErr = service.BuildIssueDelivery(ctx, h.Queries, issue)
		if deliveryErr != nil {
			slog.Warn("block wait: load delivery failed", "error", deliveryErr, "issue_id", uuidToString(issue.ID))
		}
		openHeads := []string{}
		for _, pr := range prs {
			if pr.State == "open" && pr.Branch.Valid {
				openHeads = append(openHeads, pr.Branch.String)
			}
		}
		if blocker := service.DeliveryMergeBlocker(delivery, openHeads); blocker != "" {
			decision = blockwait.Decision{
				Action: blockwait.ReleaseHold,
				Kind:   blockwait.HoldDelivery,
				Record: blockwait.Record{WaitCondition: "交付线还没对齐：" + blocker + "（`multica issue delivery <issue>` 看现场）"},
			}
		}
	}
	return prs, decision, delivery, true
}

func (h *Handler) releaseAcceptedIssue(ctx context.Context, issue db.Issue, seed blockwait.Decision) releaseOutcome {
	prs, decision, delivery, ok := h.releasePlan(ctx, issue)
	if !ok {
		return releaseOutcome{Status: issue.Status}
	}
	if seed.Reason != "" && decision.Reason != "" {
		decision.Reason = seed.Reason + decision.Reason
	}
	switch decision.Action {
	case blockwait.ReleaseDone:
		return h.finishAcceptedIssue(ctx, issue, decision.Reason)
	case blockwait.ReleaseHold:
		return h.holdAcceptedIssue(ctx, issue, decision.Kind, decision.Record.WaitCondition, seed.Reason+passLead)
	case blockwait.ReleaseMerge:
		h.trackBaselineFix(ctx, issue, &decision, issue.ReviewerType.String, issue.ReviewerID)
		out := h.mergeAcceptedIssue(ctx, issue, prs, delivery, decision)
		if out.Merged && len(decision.Inherited) > 0 {
			out.Baseline = strings.TrimPrefix(decision.Reason, seed.Reason)
		}
		return out
	}
	return releaseOutcome{Status: issue.Status, Note: decision.Reason}
}

func (h *Handler) finishAcceptedIssue(ctx context.Context, issue db.Issue, reason string) releaseOutcome {
	updated, err := h.Queries.CompleteIssueFromReview(ctx, db.CompleteIssueFromReviewParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Statuses:    []string{issue.Status},
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: close after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return releaseOutcome{Status: issue.Status, Note: reason}
	}
	h.syncBlockWait(ctx, issue, updated)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, reason)
	h.notifyParentOfChildDone(ctx, issue, updated)
	h.postSourceChatReceipt(ctx, issue, updated)
	h.notifyWaitersOfIssueDone(ctx, issue, updated)
	return releaseOutcome{Status: updated.Status, Note: reason}
}

func (h *Handler) blockAcceptedIssue(ctx context.Context, issue db.Issue, decision blockwait.Decision) releaseOutcome {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Status:      "blocked",
	})
	if err != nil {
		slog.Warn("block wait: block after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		return releaseOutcome{Status: issue.Status, Note: decision.Reason}
	}
	h.syncBlockWait(ctx, issue, updated)
	h.persistBlockRecord(ctx, updated, decision.Record)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, decision.Reason)
	return releaseOutcome{Status: updated.Status, Note: decision.Reason}
}

// passLead opens the timeline sentence of a pass the platform could not merge.
const passLead = "先不合并："

// holdAcceptedIssue is a pass the platform could not merge yet (DENE-1219).
// The ticket stays in review and keeps its pass; the next pass or patrol
// round tries again, so checks that turn green merge without anyone acting.
// A stop the executor has to clear wakes it once per distinct stop; checks
// still running wake nobody.
func (h *Handler) holdAcceptedIssue(ctx context.Context, issue db.Issue, kind, condition, lead string) releaseOutcome {
	h.deleteIssueMeta(ctx, issue, blockwait.KeyReleased)
	next := releaseHoldNext(kind)
	note := lead + condition + "。" + next
	out := releaseOutcome{Status: issue.Status, Note: note, Hold: kind, HoldNext: next}
	signature := kind + "|" + condition
	if blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyReleaseHold) == signature {
		return out
	}
	h.setIssueMetaString(ctx, issue, blockwait.KeyReleaseHold, signature)
	if kind == blockwait.HoldChecksPending {
		h.postBlockComment(ctx, issue, note)
		return out
	}
	h.wakeExecutor(ctx, issue, note)
	return out
}

// releaseHoldNext is the sentence that tells the executor how a held pass
// ends. The ticket stays in review either way.
func releaseHoldNext(kind string) string {
	switch kind {
	case blockwait.HoldChecksPending:
		return "票保持待验收，检查跑完变绿后平台巡检会自动合并关单，不用人管。"
	case blockwait.HoldChecksRed:
		return "票保持待验收。请执行人看红的检查：这次改动引起的就修好推上去；不是这次改动的问题就处理掉后用 `gh pr merge --squash <PR 链接>` 合入。PR 变绿或已合并后，巡检按已通过收口。"
	case blockwait.HoldConflict:
		return "票保持待验收。请执行人把 origin/kun 合进分支、解决冲突推上去；PR 能干净合并后，巡检按已通过收口。"
	case blockwait.HoldMergeFailed:
		return "票保持待验收。请执行人用 `gh pr merge --squash <PR 链接>` 在本机合入；巡检看到已合并就关单。"
	case blockwait.HoldDelivery:
		return "票保持待验收。请执行人用 `multica issue delivery` 归类分支或换 canonical；对齐后巡检按已通过收口。"
	default:
		return "票保持待验收，巡检下一轮再试。"
	}
}

// wakeExecutor posts note with the executor's mention and starts it. A
// ticket in review would otherwise wake its reviewer, who passed already and
// cannot fix the branch.
func (h *Handler) wakeExecutor(ctx context.Context, issue db.Issue, note string) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid || (issue.AssigneeType.String != "agent" && issue.AssigneeType.String != "squad") {
		h.postBlockComment(ctx, issue, note)
		return
	}
	mention := h.buildParentAssigneeMention(ctx, issue)
	comment := h.postBlockComment(ctx, issue, mention+note)
	if comment.ID.Valid {
		h.dispatchWaitingOnAssigneeTrigger(ctx, issue, comment.ID)
	}
}

func (h *Handler) mergeAcceptedIssue(ctx context.Context, issue db.Issue, prs []db.ListPullRequestsByIssueRow, delivery *service.IssueDelivery, decision blockwait.Decision) releaseOutcome {
	// The PR on the canonical branch is the delivery; only when no PR sits on
	// it does the first open PR stand in, as before DENE-820.
	var open *db.ListPullRequestsByIssueRow
	for i := range prs {
		if prs[i].State != "open" {
			continue
		}
		if open == nil {
			open = &prs[i]
		}
		if delivery != nil && delivery.Canonical != nil && prs[i].Branch.Valid && prs[i].Branch.String == delivery.Canonical.Branch {
			open = &prs[i]
		}
	}
	if open == nil {
		return h.finishAcceptedIssue(ctx, issue, decision.Reason)
	}
	err := h.mergeOpenPulls(ctx, issue.WorkspaceID, []db.ListPullRequestsByIssueRow{*open})
	if err == nil {
		out := h.finishAcceptedIssue(ctx, issue, decision.Reason+" PR 已合并。")
		out.Merged = true
		out.PRURL = open.HtmlUrl
		return out
	}
	condition := "合并 " + open.HtmlUrl + " 重试一次后还是没成功"
	if errors.Is(err, errPullMergeUnavailable) {
		condition = "这台服务没有合并权限，合不了 " + open.HtmlUrl
	} else if errors.Is(err, errPullNotMergeable) {
		condition = open.HtmlUrl + " 现在合不进去"
	}
	out := h.holdAcceptedIssue(ctx, issue, blockwait.HoldMergeFailed, condition, "验收已经通过，平台合并没成功：")
	out.PRURL = open.HtmlUrl
	return out
}

var (
	errPullMergeUnavailable = errors.New("pull merge unavailable")
	errPullNotMergeable     = errors.New("pull request is not mergeable")
)

// canMergePulls reports whether mergePullRequest has any way to succeed.
func (h *Handler) canMergePulls() bool {
	return h.PRMerger != nil || (h.PRRefresh != nil && h.PRRefresh.Enabled())
}

func (h *Handler) mergePullRequest(ctx context.Context, installationID int64, owner, repo string, number int) error {
	if h.PRMerger != nil {
		return h.PRMerger.MergePullRequest(ctx, installationID, owner, repo, number)
	}
	if h.PRRefresh == nil || !h.PRRefresh.Enabled() {
		return errPullMergeUnavailable
	}
	err := h.PRRefresh.MergePullRequest(ctx, installationID, owner, repo, number)
	if err == nil {
		return nil
	}
	if errors.Is(err, ghsnapshot.ErrNotMergeable) {
		return errPullNotMergeable
	}
	return err
}

func (h *Handler) publishBlockStatus(prev, issue db.Issue) {
	if h.Bus == nil {
		return
	}
	h.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     RoutingIssueUpdatedPayload(prev, issue),
	})
}

// SweepBlockWaits is the periodic backstop. It wakes a blocked or in-review
// issue whose wait is missing or already due and that has no run in flight.
func (h *Handler) SweepBlockWaits(ctx context.Context) (int, error) {
	if h == nil || h.Queries == nil {
		return 0, nil
	}
	rows, err := h.Queries.ListBlockPatrolCandidates(ctx, db.ListBlockPatrolCandidatesParams{
		QuietBefore: pgtype.Timestamptz{Time: time.Now().Add(-blockwait.QuietAfter), Valid: true},
		TodoSince:   pgtype.Timestamptz{Time: time.Now().Add(-undrivenTodoHorizon), Valid: true},
		RowLimit:    blockPatrolLimit,
	})
	if err != nil {
		return 0, err
	}
	acted := 0
	for _, issue := range rows {
		if ctx.Err() != nil {
			return acted, ctx.Err()
		}
		if h.patrolOne(ctx, issue) {
			acted++
		}
	}
	return acted, nil
}

// undrivenTodoHorizon bounds the undriven-todo patrol (DENE-1342) to tickets
// that moved in the last week, so a deploy does not rerun every forgotten one.
const undrivenTodoHorizon = 7 * 24 * time.Hour

func (h *Handler) patrolOne(ctx context.Context, issue db.Issue) bool {
	meta := parseIssueMetadata(issue.Metadata)
	watched := blockwait.MetaString(meta, blockwait.KeyWatched) == blockwait.WatchedYes
	if !watched && issue.Status != "todo" {
		return false
	}
	var self blockwait.Driver
	if issue.Status == "todo" {
		driver, ok := h.issueDriver(ctx, issue)
		if !ok {
			return false
		}
		self = driver
	}
	rec := blockwait.ParseMetadata(meta)
	blockers := h.blockerViews(ctx, issue, rec)
	quiet := time.Since(activityTime(issue))
	var last time.Time
	hasLast := false
	if raw := blockwait.MetaString(meta, blockwait.KeyPatrolAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			last = t
			hasLast = true
		}
	}
	round, hasRound := parseMetaTime(blockwait.MetaString(meta, blockwait.KeyReviewRound))
	reviewFailed, reviewFailures, reviewAsked := h.reviewRoundFailure(ctx, issue, round, hasRound)
	var reviewSkip *service.ReviewSkip
	if reviewFailed {
		if skip, ok := h.findReviewSkip(ctx, issue, ""); ok {
			reviewSkip = &skip
		}
	}
	decision := blockwait.DecidePatrol(blockwait.PatrolInput{
		Status:          issue.Status,
		Quiet:           quiet,
		Record:          rec,
		Blockers:        blockers,
		Now:             time.Now(),
		LastPatrol:      last,
		HasLastPatrol:   hasLast,
		HasPassComment:  h.passInThisRound(ctx, issue, round, hasRound),
		ReleasedPass:    hasRound && blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass,
		SegmentNudged:   blockwait.MetaString(meta, blockwait.KeySegmentNudged) == "1",
		ReviewNudged:    blockwait.MetaString(meta, blockwait.KeyReviewNudged) == "1",
		ReviewerHuman:   issue.ReviewerType.Valid && issue.ReviewerType.String == "member",
		ReviewerEmpty:   reviewerSlotEmpty(issue),
		ReviewRunFailed: reviewFailed,
		ReviewFailures:  reviewFailures,
		ReviewAsked:     reviewAsked,
		ReviewSkip:      reviewSkipReason(reviewSkip),
		Watched:         watched,
		Undriven:        self.Kind == blockwait.DriverNone,
		UndrivenWhy:     self.Reason,
		UndrivenState:   blockwait.ParseUndriven(meta),
	})
	switch decision.Action {
	case blockwait.ActionRelease, blockwait.ActionWake, blockwait.ActionSeat, blockwait.ActionRevive, blockwait.ActionEscalate, blockwait.ActionCoverReview:
	default:
		return false
	}
	h.applyPatrolFollowUp(ctx, issue, meta, decision)
	switch decision.Action {
	case blockwait.ActionRevive, blockwait.ActionEscalate:
		h.reviveOrEscalate(ctx, issue, decision)
	case blockwait.ActionRelease:
		out := h.releaseAcceptedIssue(ctx, issue, decision)
		if reviewSkip != nil && out.Status == issuestatus.Done {
			if err := service.RecordReviewSkip(ctx, h.Queries, issue, *reviewSkip); err != nil {
				slog.Warn("review skip: record failed", "issue_id", uuidToString(issue.ID), "error", err)
			}
		}
	case blockwait.ActionWake:
		h.wakeIssueOwner(ctx, issue, decision.Reason, decision.CommentOnly)
	case blockwait.ActionSeat:
		h.seatQuietReview(ctx, issue, decision.Reason)
	case blockwait.ActionCoverReview:
		h.coverFailedReview(ctx, issue, decision.Force)
	}
	return true
}

func reviewSkipReason(skip *service.ReviewSkip) string {
	if skip == nil {
		return ""
	}
	return skip.Reason()
}

// reviewerSlotEmpty reports an acceptance slot nobody has answered. "none" is
// an answer ("this issue needs no acceptance pass"), so it is not empty.
func reviewerSlotEmpty(issue db.Issue) bool {
	if !issue.ReviewerType.Valid || strings.TrimSpace(issue.ReviewerType.String) == "" {
		return true
	}
	if issue.ReviewerType.String == "none" {
		return false
	}
	return !issue.ReviewerID.Valid
}

// seatQuietReview is the patrol's answer to an in_review issue whose
// acceptance seat is empty (DENE-869). It fills the seat the same way the
// status write does and starts it; when no seat can be picked the wait
// becomes a structured block pointing at a workspace manager, so a person
// sees it instead of two agents waiting on each other.
func (h *Handler) seatQuietReview(ctx context.Context, issue db.Issue, reason string) {
	var tr statusTransition
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid {
		tr = h.pickAcceptanceSeat(ctx, issue, issue.AssigneeID)
	} else {
		tr.refuse = "执行席不是 Agent，平台选不出与之不同的验收席"
	}
	if tr.refuse == "" && tr.setReviewer {
		updated, err := h.Queries.SetIssueReviewerIfUnset(ctx, db.SetIssueReviewerIfUnsetParams{
			ReviewerType: tr.reviewerType.String,
			ReviewerID:   tr.reviewerID,
			ID:           issue.ID,
			WorkspaceID:  issue.WorkspaceID,
		})
		if err != nil {
			slog.Warn("block wait: seat quiet review failed", "error", err, "issue_id", uuidToString(issue.ID))
			tr.refuse = "验收席没能写进这张票"
		} else {
			h.publishBlockStatus(issue, updated)
			tr.note = reason + tr.note
			h.finishStatusTransition(ctx, updated, tr)
			return
		}
	}
	h.blockReviewNeedingHuman(ctx, issue, reason+tr.refuse+"。")
}

// blockReviewNeedingHuman turns a seatless review into a structured block
// that names a workspace manager: the platform could not find a reviewer, so
// a person has to. The block is what keeps the patrol from asking again.
func (h *Handler) blockReviewNeedingHuman(ctx context.Context, issue db.Issue, why string) {
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("block wait: list managers failed", "error", err, "issue_id", uuidToString(issue.ID))
	}
	rec := blockwait.Record{}
	mention := ""
	if len(managers) > 0 && managers[0].Valid {
		rec.NeedsHuman = uuidToString(managers[0])
		mention = h.memberWakeMention(ctx, managers[0])
	} else {
		rec = blockwait.FailureWake(time.Now(), "验收席由人来定", 1)
	}
	reason := why + "平台补不上验收席，这张票改成阻塞，等人指定验收席后再送审（`multica issue update <issue> --reviewer <name>`）。"
	h.blockAcceptedIssue(ctx, issue, blockwait.Decision{Record: rec, Reason: mention + reason})
	if rec.NeedsHuman != "" {
		// The block comment carries the @, but a system comment's @ reaches
		// nobody's inbox; the summon entry writes the inbox row.
		h.summonPatrol(ctx, issue, managers[0], reason, pgtype.UUID{}, true)
	}
}

// summonPatrol is the patrol calling a person through the summon entry
// (DENE-880). commentID is a block comment already carrying the @; unset
// posts the entry's own. An unanswered call to the same person dedupes.
func (h *Handler) summonPatrol(ctx context.Context, issue db.Issue, recipient pgtype.UUID, reason string, commentID pgtype.UUID, noComment bool) {
	if !recipient.Valid {
		return
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      issue,
		Recipient:  recipient,
		CallerType: "system",
		Source:     service.SummonSourcePatrol,
		Reason:     reason,
		CommentID:  commentID,
		NoComment:  noComment,
	})
}

func parseMetaTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func (h *Handler) applyPatrolFollowUp(ctx context.Context, issue db.Issue, meta map[string]any, decision blockwait.Decision) {
	set, drop := decision.FollowUp(
		time.Now(),
		blockwait.MetaString(meta, blockwait.KeyBlockedBy),
		blockwait.MetaString(meta, "close.waiting_on"),
		blockwait.MetaString(meta, blockwait.KeyWokenBy),
	)
	for _, key := range drop {
		h.deleteIssueMeta(ctx, issue, key)
	}
	for key, value := range set {
		h.setIssueMetaString(ctx, issue, key, value)
	}
}

func (h *Handler) passInThisRound(ctx context.Context, issue db.Issue, round time.Time, hasRound bool) bool {
	if !hasRound || !issue.ReviewerID.Valid {
		return false
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 30,
	})
	if err != nil {
		return false
	}
	for _, c := range comments {
		if c.AuthorID != issue.ReviewerID || !c.CreatedAt.Valid {
			continue
		}
		if issue.ReviewerType.Valid && c.AuthorType != issue.ReviewerType.String {
			continue
		}
		if blockwait.PassInRound(c.Content, c.CreatedAt.Time, round) {
			return true
		}
	}
	return false
}

func (h *Handler) blockerViews(ctx context.Context, issue db.Issue, rec blockwait.Record) []blockwait.BlockerView {
	var out []blockwait.BlockerView
	for _, ref := range rec.BlockedBy {
		view := blockwait.BlockerView{Ref: ref, Status: "unknown"}
		other, ok := h.lookupBlocker(ctx, issue.WorkspaceID, ref)
		if ok {
			view.Status = other.Status
			view.Accepted = blockwait.MetaString(parseIssueMetadata(other.Metadata), blockwait.KeyReleased) == blockwait.ReleasedPass &&
				(other.Status == "done" || other.Status == "in_review")
			h.undrivenView(ctx, &view, other)
		}
		out = append(out, view)
	}
	return out
}

func (h *Handler) lookupBlocker(ctx context.Context, workspaceID pgtype.UUID, ref string) (db.Issue, bool) {
	if id, err := util.ParseUUID(ref); err == nil {
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		return issue, err == nil
	}
	number := identifierNumber(ref)
	if number <= 0 {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: workspaceID, Number: number})
	return issue, err == nil
}

func identifierNumber(ref string) int32 {
	dash := -1
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '-' {
			dash = i
			break
		}
	}
	if dash < 0 || dash == len(ref)-1 {
		return 0
	}
	var n int32
	for _, r := range ref[dash+1:] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int32(r-'0')
	}
	return n
}

func activityTime(issue db.Issue) time.Time {
	if issue.LastActivityAt.Valid {
		return issue.LastActivityAt.Time
	}
	if issue.UpdatedAt.Valid {
		return issue.UpdatedAt.Time
	}
	return time.Now()
}

// wakeIssueOwner starts the assignee (or the reviewer, while in review) and
// leaves a sentence. A disabled seat is named and not replaced here — that
// handoff belongs to the disabled-seat path. A ticket nobody holds is seated
// by routing first; when routing cannot, a person is told exactly that.
func (h *Handler) wakeIssueOwner(ctx context.Context, issue db.Issue, reason string, commentOnly bool) {
	if !commentOnly && issue.Status != "in_review" && !issue.AssigneeID.Valid &&
		blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman) == "" {
		seated, why := h.seatBeforeWake(ctx, issue)
		if !seated.AssigneeID.Valid {
			h.reportUnseatedWake(ctx, issue, reason, why)
			return
		}
		issue = seated
	}
	targetType, targetID := issue.AssigneeType, issue.AssigneeID
	if issue.Status == "in_review" && issue.ReviewerType.Valid && issue.ReviewerID.Valid && issue.ReviewerType.String != "none" {
		targetType, targetID = issue.ReviewerType, issue.ReviewerID
	}
	if issue.Status == "in_review" && reviewerSlotEmpty(issue) {
		// The executor already said it is done; asking it to review its own
		// work only produces "请验收". Leave the sentence, start nobody.
		commentOnly = true
	}
	if blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman) != "" {
		commentOnly = true
	}
	if !commentOnly && targetType.Valid && targetID.Valid && targetType.String == "agent" {
		var covered bool
		issue, targetID, reason, covered = h.coverDisabledWakeTarget(ctx, issue, targetID, reason)
		if !covered {
			commentOnly = true
		}
	}
	mention := ""
	if targetType.Valid && targetID.Valid && (targetType.String == "agent" || targetType.String == "squad") && !commentOnly {
		mention = h.buildParentAssigneeMention(ctx, db.Issue{AssigneeType: targetType, AssigneeID: targetID, WorkspaceID: issue.WorkspaceID})
	}
	if targetType.Valid && targetID.Valid && targetType.String == "member" {
		commentOnly = true
		mention = h.memberWakeMention(ctx, targetID)
	}
	comment := h.postBlockComment(ctx, issue, mention+reason)
	if targetType.Valid && targetID.Valid && targetType.String == "member" {
		h.summonPatrol(ctx, issue, targetID, reason, comment.ID, !comment.ID.Valid)
	} else if human := blockwait.MetaString(parseIssueMetadata(issue.Metadata), blockwait.KeyNeedsHuman); human != "" {
		h.summonPatrol(ctx, issue, parseUUID(human), reason, pgtype.UUID{}, false)
	}
	if commentOnly || !targetType.Valid || !targetID.Valid || !comment.ID.Valid {
		return
	}
	waker := issue
	waker.AssigneeType = targetType
	waker.AssigneeID = targetID
	h.dispatchWaitingOnAssigneeTrigger(ctx, waker, comment.ID)
}

// seatBeforeWake asks routing to fill the empty executor slot of a ticket
// whose wait just ended (DENE-1255). The seat is written parked; the wake
// that follows starts it. The ticket is read again either way, so a slot a
// person or another pass filled meanwhile counts too. why says, in words a
// person can act on, why the slot is still empty.
func (h *Handler) seatBeforeWake(ctx context.Context, issue db.Issue) (db.Issue, string) {
	why := "这个工作区没有开自动派单"
	if h.Routing != nil {
		rctx, cancel := context.WithTimeout(ctx, routeTimeout)
		out, err := h.Routing.SeatExecutor(rctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID))
		cancel()
		switch {
		case err != nil:
			why = "路由这次出错了（" + err.Error() + "）"
		case out.Action == routing.ActionSkipped && out.Reason == "routing not enabled":
		case strings.TrimSpace(out.Reason) != "":
			why = "路由没补上（" + out.Reason + "）"
		default:
			why = "路由没补上"
		}
	}
	fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		return issue, why
	}
	return fresh, why
}

// reportUnseatedWake is the wake with nobody to start: the comment says
// what ended, that the ticket has no executor, why routing left it empty,
// and the one action that resumes it; the summon puts that same sentence on
// a person's inbox card.
func (h *Handler) reportUnseatedWake(ctx context.Context, issue db.Issue, reason, why string) {
	recipient := h.unseatedRecipient(ctx, issue)
	mention := ""
	if recipient.Valid {
		mention = h.memberWakeMention(ctx, recipient)
	}
	body := strings.TrimSpace(reason) + " 但这张票没有执行人，" + why + "，所以没有叫醒任何人。" + unseatedReason + "选好后平台会接着叫醒它。"
	comment := h.postBlockComment(ctx, issue, mention+body)
	if !recipient.Valid {
		return
	}
	_, _ = h.summonPerson(ctx, service.SummonInput{
		Issue:      issue,
		Recipient:  recipient,
		CallerType: "system",
		Source:     service.SummonSourceUnseated,
		Reason:     unseatedReason,
		CommentID:  comment.ID,
		NoComment:  !comment.ID.Valid,
	})
}

// unseatedRecipient is who decides who holds a ticket nobody holds: the
// person who created it, else a workspace manager — routing's notify rule.
func (h *Handler) unseatedRecipient(ctx context.Context, issue db.Issue) pgtype.UUID {
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		return issue.CreatorID
	}
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("block wait: list managers failed", "error", err, "issue_id", uuidToString(issue.ID))
	}
	for _, m := range managers {
		if m.Valid {
			return m
		}
	}
	return pgtype.UUID{}
}

// coverDisabledWakeTarget keeps the patrol from waking a seat that is not
// taking work (DENE-870). Before this, a seat turned off after its account
// ran out of money was @-mentioned every time the clock came due, failed
// again, and was blocked again with a fresh clock. An executor seat that is
// off hands the ticket to another house's seat, never the ticket's own
// reviewer; the new seat carries on from the ticket's comments and branch.
// With nobody to take it, or when the off seat is the reviewer, the wake
// becomes a plain comment and nobody is dispatched. covered is false when
// the wake must not dispatch.
func (h *Handler) coverDisabledWakeTarget(ctx context.Context, issue db.Issue, targetID pgtype.UUID, reason string) (db.Issue, pgtype.UUID, string, bool) {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: targetID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || agent.ArchivedAt.Valid || agent.WorkEnabled {
		return issue, targetID, reason, true
	}
	isAssignee := issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && issue.AssigneeID == targetID
	if !isAssignee {
		return issue, targetID, reason + fmt.Sprintf(" 验收人 %s 已停用，平台没有叫醒它，请重新启用或换一位验收人。", agent.Name), false
	}
	var avoid []string
	if issue.ReviewerType.Valid && issue.ReviewerType.String == "agent" && issue.ReviewerID.Valid {
		avoid = append(avoid, uuidToString(issue.ReviewerID))
	}
	replacement, _, ok := h.substituteAgent(ctx, issue.WorkspaceID, agent, avoid, issue)
	if !ok {
		return issue, targetID, reason + fmt.Sprintf(" 执行人 %s 已停用，暂时没有能接手的席位，平台没有叫醒它。重新启用或改派后再继续。", agent.Name), false
	}
	updated, err := h.Queries.ReassignIssueToAgentIfCurrent(ctx, db.ReassignIssueToAgentIfCurrentParams{
		AssigneeID:        replacement.ID,
		ID:                issue.ID,
		WorkspaceID:       issue.WorkspaceID,
		CurrentAssigneeID: agent.ID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: reassign off seat failed", "issue_id", uuidToString(issue.ID), "error", err)
		}
		return issue, targetID, reason + fmt.Sprintf(" 执行人 %s 已停用，平台没有叫醒它。", agent.Name), false
	}
	if _, err := h.Queries.CancelPendingTasksByIssueAndAgent(ctx, db.CancelPendingTasksByIssueAndAgentParams{
		IssueID: issue.ID, AgentID: agent.ID,
	}); err != nil {
		slog.Warn("block wait: cancel off seat tasks failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
	h.publish(protocol.EventIssueUpdated, uuidToString(updated.WorkspaceID), "system", "", RoutingIssueUpdatedPayload(issue, updated))
	note := fmt.Sprintf(" 原执行人 %s 已停用，这张票改由 %s 接手：先读评论和原分支上的提交，接着做，别从头来。", agent.Name, replacement.Name)
	return updated, replacement.ID, reason + note, true
}

func (h *Handler) memberWakeMention(ctx context.Context, userID pgtype.UUID) string {
	user, err := h.Queries.GetUser(ctx, userID)
	if err != nil {
		return ""
	}
	name := sanitizeMentionLabel(user.Name)
	if name == "" {
		name = "member"
	}
	return fmt.Sprintf("[@%s](mention://member/%s) ", name, uuidToString(userID))
}

// blockAttributionRejection is the 400 an agent gets for moving a ticket to
// blocked without saying what kind of stop it is and what happens next. The
// sub-issue blocker card reads exactly these two fields; without them the row
// is blank and never counts as "needs you" (DENE-1301). A member dragging a
// card is not asked for them.
func blockAttributionRejection(req UpdateIssueRequest, actorType string) string {
	if actorType != "agent" {
		return ""
	}
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	const hint = "智能体把票改成 blocked 要写明卡点类型和下一步：--block-kind（decision / permission / external / dependency / capacity）配 --block-action（一句话，80 字以内）。更推荐用 `multica issue close --outcome blocked`，它会一并写好。"
	if kind == "" || action == "" {
		return hint
	}
	if !closeprotocol.AllowedBlockKind(kind) {
		return fmt.Sprintf("--block-kind %q 不是允许的值。", kind) + hint
	}
	if len([]rune(action)) > 80 {
		return "--block-action 超过 80 字。" + hint
	}
	return ""
}

// persistBlockAttribution writes the kind and next step a status move into
// blocked carried, so the blocker card has the same two fields a blocked
// close writes.
func (h *Handler) persistBlockAttribution(ctx context.Context, issue db.Issue, req UpdateIssueRequest) {
	kind := strings.TrimSpace(deref(req.BlockKind))
	action := strings.TrimSpace(deref(req.BlockAction))
	if kind == "" || action == "" || !closeprotocol.AllowedBlockKind(kind) {
		return
	}
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockKind, kind)
	h.setIssueMetaString(ctx, issue, closeprotocol.KeyBlockAction, truncateRunes(action, 80))
}

// executorReached reports whether a comment's trigger results include a run
// for the issue's own executor — its agent, or a leader run under its squad.
// A run handed to someone else the comment happened to @ does not count.
func executorReached(issue db.Issue, enqueued map[string]commentEnqueueResult) bool {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false
	}
	assignee := uuidToString(issue.AssigneeID)
	for agentID, res := range enqueued {
		switch res.status {
		case DispatchQueued, DispatchCoalesced, DispatchSteered:
		default:
			continue
		}
		switch issue.AssigneeType.String {
		case "agent":
			if agentID == assignee {
				return true
			}
		case "squad":
			if res.execSquadID == assignee {
				return true
			}
		}
	}
	return false
}

// resumeBlockedOnReply moves a blocked ticket back to in_progress when a
// member's reply has just woken its executor (DENE-1301). Before this the
// ticket kept saying "blocked, needs you" until the executor closed again,
// although the person had already answered and the work was moving. The wait
// keys are dropped as on any move out of blocked, and the blocked close record
// is marked superseded rather than replaced: nobody closed anything.
func (h *Handler) resumeBlockedOnReply(ctx context.Context, issue db.Issue, enqueued map[string]commentEnqueueResult, summonWoke bool) {
	if issue.Status != issuestatus.Blocked || h.TxStarter == nil || !(summonWoke || executorReached(issue, enqueued)) {
		return
	}
	var prev, updated db.Issue
	err := func() error {
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		qtx := h.Queries.WithTx(tx)
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM issue WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, issue.ID, issue.WorkspaceID).Scan(&status); err != nil {
			return err
		}
		if status != issuestatus.Blocked {
			return pgx.ErrNoRows
		}
		prev, err = qtx.GetIssue(ctx, issue.ID)
		if err != nil {
			return err
		}
		updated, err = qtx.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: issuestatus.InProgress})
		if err != nil {
			return err
		}
		// §6.1: the blocker fields belong to a blocked ticket only.
		drop := append(blockwait.WaitKeys(), blockwait.KeyWatched, closeprotocol.KeyBlockKind, closeprotocol.KeyBlockAction)
		for _, key := range drop {
			if _, err := qtx.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: key}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if at := blockwait.MetaString(parseIssueMetadata(prev.Metadata), closeprotocol.KeyAt); at != "" {
			if err := setIssueMetaStringTx(ctx, qtx, updated, closeprotocol.KeySuperseded, at); err != nil {
				return err
			}
		}
		if updated, err = qtx.GetIssue(ctx, issue.ID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: resume on reply failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return
	}
	h.publishBlockStatus(prev, updated)
}
