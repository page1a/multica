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
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
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
	}
	if prev.Status == "in_review" && next.Status != "in_review" {
		h.deleteIssueMeta(ctx, next, blockwait.KeyReleased)
		h.deleteIssueMeta(ctx, next, blockwait.KeyReviewNudged)
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
	meta := parseIssueMetadata(issue.Metadata)
	if blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass {
		return
	}
	h.setIssueMetaString(ctx, issue, blockwait.KeyReleased, blockwait.ReleasedPass)
	h.releaseAcceptedIssue(ctx, issue, blockwait.Decision{Reason: "验收已经通过。"})
}

func (h *Handler) authorIsReviewer(issue db.Issue, comment db.Comment) bool {
	if !issue.ReviewerType.Valid || !issue.ReviewerID.Valid || !comment.AuthorID.Valid {
		return false
	}
	return issue.ReviewerType.String == comment.AuthorType && issue.ReviewerID == comment.AuthorID
}

func (h *Handler) releaseAcceptedIssue(ctx context.Context, issue db.Issue, seed blockwait.Decision) {
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("block wait: list pull requests failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	snapshots := make([]blockwait.PRSnapshot, 0, len(prs))
	for _, pr := range prs {
		snapshots = append(snapshots, blockwait.PRSnapshot{
			Number:    int(pr.PrNumber),
			State:     pr.State,
			Mergeable: pr.MergeableState.String,
			Checks:    pr.ChecksRollupState.String,
			URL:       pr.HtmlUrl,
		})
	}
	decision := blockwait.DecideRelease(snapshots, time.Now())
	if seed.Reason != "" && decision.Reason != "" {
		decision.Reason = seed.Reason + decision.Reason
	}
	// The delivery aggregate (DENE-820) decides whether anything is still
	// unaccounted for before the pass is allowed to close or merge. An
	// unresolved rescue line or an unclassified second line turns the pass
	// into a structured block: the reviewer said the work is good, but the
	// platform cannot yet say which branch that work is on.
	var delivery *service.IssueDelivery
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
			decision.Action = blockwait.ReleaseBlock
			decision.Reason = "验收已经通过，但交付线还没对齐：" + blocker + "。先标成阻塞，`multica issue delivery <issue>` 看现场。"
			decision.Record.WaitCondition = "交付线对齐：" + blocker
			if !decision.Record.HasWakeAt {
				decision.Record.HasWakeAt = true
				decision.Record.WakeAt = time.Now().Add(blockwait.QuietAfter).UTC()
			}
			h.blockAcceptedIssue(ctx, issue, decision)
			h.wakeIssueOwner(ctx, issue, "验收已经通过，但这张票的交付线还没对齐："+blocker+"。请用 `multica issue delivery` 归类分支或换 canonical，再把票推回验收。", false)
			return
		}
	}
	switch decision.Action {
	case blockwait.ReleaseDone:
		h.finishAcceptedIssue(ctx, issue, decision.Reason)
	case blockwait.ReleaseBlock:
		h.blockAcceptedIssue(ctx, issue, decision)
	case blockwait.ReleaseMerge:
		h.mergeAcceptedIssue(ctx, issue, prs, delivery, decision)
	}
}

func (h *Handler) finishAcceptedIssue(ctx context.Context, issue db.Issue, reason string) {
	updated, err := h.Queries.CompleteIssueFromReview(ctx, db.CompleteIssueFromReviewParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Statuses:    []string{issue.Status},
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("block wait: close after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		}
		return
	}
	h.syncBlockWait(ctx, issue, updated)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, reason)
	h.notifyParentOfChildDone(ctx, issue, updated)
	h.notifyWaitersOfIssueDone(ctx, issue, updated)
}

func (h *Handler) blockAcceptedIssue(ctx context.Context, issue db.Issue, decision blockwait.Decision) {
	updated, err := h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Status:      "blocked",
	})
	if err != nil {
		slog.Warn("block wait: block after pass failed", "error", err, "issue_id", uuidToString(issue.ID))
		return
	}
	h.syncBlockWait(ctx, issue, updated)
	h.persistBlockRecord(ctx, updated, decision.Record)
	h.publishBlockStatus(issue, updated)
	h.postBlockComment(ctx, updated, decision.Reason)
}

func (h *Handler) mergeAcceptedIssue(ctx context.Context, issue db.Issue, prs []db.ListPullRequestsByIssueRow, delivery *service.IssueDelivery, decision blockwait.Decision) {
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
		h.finishAcceptedIssue(ctx, issue, decision.Reason)
		return
	}
	err := h.mergePullRequest(ctx, open.InstallationID, open.RepoOwner, open.RepoName, int(open.PrNumber))
	if err == nil {
		h.finishAcceptedIssue(ctx, issue, decision.Reason+" PR 已合并。")
		return
	}
	decision.Action = blockwait.ReleaseBlock
	if errors.Is(err, errPullMergeUnavailable) {
		decision.Reason = decision.Reason + " 这台服务没有合并权限，已改成阻塞并叫醒执行人去合并。"
		decision.Record.WaitCondition = "验收已通过，等待执行人合并 " + open.HtmlUrl
	} else if errors.Is(err, errPullNotMergeable) {
		decision.Reason = fmt.Sprintf("验收已经通过，但 %s 现在合不进去。先标成阻塞，到点再看。", open.HtmlUrl)
		decision.Record.WaitCondition = open.HtmlUrl + " 合不进去"
	} else {
		decision.Reason = "验收已经通过，合并没有成功。先标成阻塞，到点再试。"
		decision.Record.WaitCondition = "合并 " + open.HtmlUrl + " 没有成功"
	}
	if !decision.Record.HasWakeAt {
		decision.Record.HasWakeAt = true
		decision.Record.WakeAt = time.Now().Add(blockwait.QuietAfter).UTC()
	}
	h.blockAcceptedIssue(ctx, issue, decision)
	if errors.Is(err, errPullMergeUnavailable) {
		h.wakeIssueOwner(ctx, issue, "验收已经通过。请合并关联的 PR，然后把这张票关了。合不进去就让它停在阻塞上。", false)
	}
}

var (
	errPullMergeUnavailable = errors.New("pull merge unavailable")
	errPullNotMergeable     = errors.New("pull request is not mergeable")
)

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

func (h *Handler) patrolOne(ctx context.Context, issue db.Issue) bool {
	meta := parseIssueMetadata(issue.Metadata)
	if blockwait.MetaString(meta, blockwait.KeyWatched) != blockwait.WatchedYes {
		return false
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
	decision := blockwait.DecidePatrol(blockwait.PatrolInput{
		Status:         issue.Status,
		Quiet:          quiet,
		Record:         rec,
		Blockers:       blockers,
		Now:            time.Now(),
		LastPatrol:     last,
		HasLastPatrol:  hasLast,
		HasPassComment: h.passInThisRound(ctx, issue, round, hasRound),
		ReleasedPass:   hasRound && blockwait.MetaString(meta, blockwait.KeyReleased) == blockwait.ReleasedPass,
		SegmentNudged:  blockwait.MetaString(meta, blockwait.KeySegmentNudged) == "1",
		ReviewNudged:   blockwait.MetaString(meta, blockwait.KeyReviewNudged) == "1",
		ReviewerHuman:  issue.ReviewerType.Valid && issue.ReviewerType.String == "member",
		ReviewerEmpty:  reviewerSlotEmpty(issue),
	})
	switch decision.Action {
	case blockwait.ActionRelease, blockwait.ActionWake, blockwait.ActionSeat:
	default:
		return false
	}
	h.applyPatrolFollowUp(ctx, issue, meta, decision)
	switch decision.Action {
	case blockwait.ActionRelease:
		h.releaseAcceptedIssue(ctx, issue, decision)
	case blockwait.ActionWake:
		h.wakeIssueOwner(ctx, issue, decision.Reason, decision.CommentOnly)
	case blockwait.ActionSeat:
		h.seatQuietReview(ctx, issue, decision.Reason)
	}
	return true
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
	h.blockAcceptedIssue(ctx, issue, blockwait.Decision{
		Record: rec,
		Reason: mention + why + "平台补不上验收席，这张票改成阻塞，等人指定验收席后再送审（`multica issue update <issue> --reviewer <name>`）。",
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
// handoff belongs to the disabled-seat path.
func (h *Handler) wakeIssueOwner(ctx context.Context, issue db.Issue, reason string, commentOnly bool) {
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
	if commentOnly || !targetType.Valid || !targetID.Valid || !comment.ID.Valid {
		return
	}
	waker := issue
	waker.AssigneeType = targetType
	waker.AssigneeID = targetID
	h.dispatchWaitingOnAssigneeTrigger(ctx, waker, comment.ID)
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
	replacement, _, ok := h.substituteAgent(ctx, issue.WorkspaceID, agent, avoid, "")
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
