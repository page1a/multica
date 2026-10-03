package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/parking"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// statusTransition is the adjustment a move into in_review or done needs
// before the row is written. An empty value leaves the request alone.
type statusTransition struct {
	status       string
	reviewerType pgtype.Text
	reviewerID   pgtype.UUID
	setReviewer  bool
	block        blockwait.Record
	persistBlock bool
	note         string
	handoff      bool
	refuse       string
	noCode       string
	// merged / prURL report a linked PR the done gate merged on the way, so
	// `issue close` can say so instead of guessing from the note (DENE-859).
	merged bool
	prURL  string
}

func reviewerIsAssigned(issue db.Issue) bool {
	return issue.ReviewerType.Valid && issue.ReviewerType.String != "" && issue.ReviewerType.String != "none" && issue.ReviewerID.Valid
}

// guardSilentStall stops three quiet stalls at the status write.
//
// An agent moving an issue into in_review must have something to review: a
// linked PR that is open, draft, or merged, or an explicit no_code_reason for
// tickets that never carry code (docs, research). People are not gated. The
// empty reviewer slot is then filled with a different-family acceptance seat,
// for a child issue as much as for a parent, or the write is refused. A move
// to done while a linked PR is still open is either merged first or rewritten
// as a structured block.
//
// The child rule (DENE-869): routing.Route still treats a sub-issue as
// execution-only and never starts an independent judge chain for it. That is
// about *who decides*, not about whether the slot may stay empty. DENE-860 sat
// in in_review for two hours with reviewer_id NULL because this guard skipped
// children; the seat is now filled here by the same ladder fallback the parent
// gets, and ensureAcceptanceRunning starts it once the executor's run is gone.
//
// Every refusal it returns is also kept in issue_rejection, so the parking
// record (DENE-881) can say "送审被拒" after the run is gone.
func (h *Handler) guardSilentStall(ctx context.Context, issue db.Issue, nextStatus string, actorType, actorID string, noCodeReason string, assigneeType pgtype.Text, assigneeID pgtype.UUID, reviewerType pgtype.Text, reviewerID pgtype.UUID, reviewerExplicit bool) statusTransition {
	tr := h.decideSilentStall(ctx, issue, nextStatus, actorType, actorID, noCodeReason, assigneeType, assigneeID, reviewerType, reviewerID, reviewerExplicit)
	if tr.refuse != "" {
		h.recordRejection(ctx, issue, nextStatus, actorType, actorID, tr.refuse)
	}
	return tr
}

// recordRejection keeps a refused status move. Best effort: losing the row
// only costs the parking record one reason, never the refusal itself.
func (h *Handler) recordRejection(ctx context.Context, issue db.Issue, nextStatus, actorType, actorID, reason string) {
	kind := parking.RejectClose
	switch {
	case strings.Contains(reason, "没有关联的 PR"):
		kind = parking.RejectPRNotLinked
	case nextStatus == issuestatus.InReview:
		kind = parking.RejectReview
	}
	actor := pgtype.UUID{}
	if id, err := util.ParseUUID(actorID); err == nil {
		actor = id
	}
	if err := h.Queries.CreateIssueRejection(ctx, db.CreateIssueRejectionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   actorType,
		ActorID:     actor,
		Action:      "status:" + nextStatus,
		Kind:        kind,
		Reason:      reason,
	}); err != nil {
		slog.Warn("record rejection failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
}

func (h *Handler) decideSilentStall(ctx context.Context, issue db.Issue, nextStatus string, actorType, actorID string, noCodeReason string, assigneeType pgtype.Text, assigneeID pgtype.UUID, reviewerType pgtype.Text, reviewerID pgtype.UUID, reviewerExplicit bool) statusTransition {
	var tr statusTransition
	switch nextStatus {
	case issuestatus.InReview:
		if issue.Status == issuestatus.InReview {
			return tr
		}
		if actorType == "agent" {
			if refuse := h.refuseReviewWithoutDelivery(ctx, issue, noCodeReason); refuse != "" {
				tr.refuse = refuse
				return tr
			}
			if reason := strings.TrimSpace(noCodeReason); reason != "" {
				tr.noCode = reason
				tr.note = "执行人声明这张票没有代码交付：" + reason + "。"
			}
		}
		// `none` is a routing verdict meaning that this ticket does not need
		// acceptance. It is not a reviewer who can receive an in_review handoff.
		// A close request must follow the done path instead of creating an
		// unowned in_review ticket. Explicit reviewer fields remain supported for
		// callers that deliberately override routing (DENE-1156).
		if strings.EqualFold(strings.TrimSpace(reviewerType.String), "none") && !reviewerExplicit {
			tr.refuse = "这张票的路由判定为不需要验收，不能进入 in_review；请改用 `multica issue close --outcome done`。如果确实需要验收，先指定 reviewer 再重试。"
			return tr
		}
		if reviewerChosen(reviewerType, reviewerID, reviewerExplicit) {
			return tr
		}
		if assigneeType.String != "agent" || !assigneeID.Valid {
			return tr
		}
		seat := h.fillAcceptanceSeat(ctx, issue, assigneeID)
		seat.note = tr.note + seat.note
		seat.noCode = tr.noCode
		return seat
	case issuestatus.Done:
		if issue.Status == issuestatus.Done {
			return tr
		}
		return h.guardDoneWithOpenPull(ctx, issue, actorType, actorID, noCodeReason)
	default:
		return tr
	}
}

// reviewDeliveryStates are the PR states that count as "there is something to
// review". A closed-unmerged PR is not a delivery.
var reviewDeliveryStates = map[string]bool{"open": true, "draft": true, "merged": true}

// refuseReviewWithoutDelivery is the review gate: the sentence that refuses
// an agent's move to in_review when the issue has no linked PR to review and
// no declared reason for having none. Empty means the move may proceed.
func (h *Handler) refuseReviewWithoutDelivery(ctx context.Context, issue db.Issue, noCodeReason string) string {
	if strings.TrimSpace(noCodeReason) != "" {
		return ""
	}
	if declaredFrom(ctx).Unverified {
		return ""
	}
	view, err := h.ensureIssueDeliveries(ctx, issue)
	prs := view.GateRows()
	if err != nil {
		slog.Warn("review gate: list pull requests failed", "issue_id", uuidToString(issue.ID), "error", err)
		prs, err = h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
		if err != nil {
			return "进不了待验收：没能读到这张票关联的 PR，稍后再试。"
		}
	}
	for _, pr := range prs {
		if reviewDeliveryStates[strings.ToLower(pr.State)] {
			return ""
		}
	}
	if view.Denied {
		return "没权限：保存的令牌读不了这个仓库。平台已经叫仓库登记人来接上。本机有发起人的登录时先跑 `multica connection add --from-gh --yes`；试不了就不要自己设等待条件。"
	}
	key := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	return fmt.Sprintf("进不了待验收：%s 没有关联的 PR，验收人没有东西可看。没有 GitHub App 时，请用票号重跑 `multica issue close %s`，并检查 PR 标题或分支里包含 %s；纯文档或调研类没有代码交付的票，用 `--no-code <原因>` 说明。也可以 `multica issue close %s --pr <链接>`。", key, key, key, key)
}

func reviewerChosen(reviewerType pgtype.Text, reviewerID pgtype.UUID, explicit bool) bool {
	if !reviewerType.Valid || strings.TrimSpace(reviewerType.String) == "" {
		return false
	}
	if reviewerType.String == "none" {
		return true
	}
	if explicit && reviewerID.Valid {
		return true
	}
	return reviewerID.Valid
}

// fillAcceptanceSeat picks the acceptance seat and, when none can be picked,
// leaves the refusal on the issue. The status write it guards keeps the issue
// where it was.
func (h *Handler) fillAcceptanceSeat(ctx context.Context, issue db.Issue, assigneeID pgtype.UUID) statusTransition {
	tr := h.pickAcceptanceSeat(ctx, issue, assigneeID)
	if tr.refuse != "" {
		// A close must never leave an in_review ticket with nobody to wake.
		// Convert an unseatable review into a structured human decision block;
		// the caller persists it together with the status write.
		why := tr.refuse
		tr.refuse = ""
		tr.status = issuestatus.Blocked
		tr.persistBlock = true
		tr.block, tr.note = h.reviewSeatBlock(ctx, issue, why)
	}
	return tr
}

func (h *Handler) reviewSeatBlock(ctx context.Context, issue db.Issue, why string) (blockwait.Record, string) {
	managers, err := h.Queries.ListWorkspaceManagerUserIDs(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("acceptance seat: list managers failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
	rec := blockwait.Record{}
	if len(managers) > 0 && managers[0].Valid {
		rec.NeedsHuman = uuidToString(managers[0])
	} else {
		rec = blockwait.FailureWake(time.Now(), "验收席由人来定", 1)
	}
	reason := why + "。平台补不上验收席，这张票改成阻塞，等人指定验收席后再送审。"
	return rec, reason
}

// pickAcceptanceSeat chooses a different-family acceptance seat for an agent
// executor. It writes nothing and posts nothing: the refusal, when there is
// one, is in tr.refuse for the caller to deliver in its own words.
func (h *Handler) pickAcceptanceSeat(ctx context.Context, issue db.Issue, assigneeID pgtype.UUID) statusTransition {
	var tr statusTransition
	if h.Routing == nil {
		tr.refuse = "进不了待验收：这台服务没有验收席名册"
		return tr
	}
	view := routing.Issue{
		ID:           uuidToString(issue.ID),
		Title:        issue.Title,
		Status:       issuestatus.InReview,
		AssigneeType: "agent",
		AssigneeID:   uuidToString(assigneeID),
	}
	if issue.ProjectID.Valid {
		if project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
			ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			view.ProjectName = project.Title
		}
	}
	ref, why, ok := h.Routing.PickAcceptanceSeat(ctx, uuidToString(issue.WorkspaceID), view)
	if !ok {
		if why == "" {
			why = "选不出和执行席不同的验收席"
		}
		tr.refuse = "进不了待验收：" + why
		return tr
	}
	reviewerID, err := util.ParseUUID(ref.ID)
	if err != nil {
		tr.refuse = "进不了待验收：选中的验收席没有有效身份"
		return tr
	}
	name := ref.Name
	if name == "" {
		name = "另一个席位"
	}
	tr.setReviewer = true
	tr.reviewerType = pgtype.Text{String: "agent", Valid: true}
	tr.reviewerID = reviewerID
	tr.note = fmt.Sprintf("验收席是空的。已补上 %s，和执行席不是同一个人（%s）。", name, why)
	active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("acceptance seat: active check failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
	if active {
		tr.note += "当前这轮还没结束，结束后再把票交给这个验收席。"
		return tr
	}
	tr.handoff = true
	return tr
}

func (h *Handler) guardDoneWithOpenPull(ctx context.Context, issue db.Issue, actorType, actorID, noCodeReason string) statusTransition {
	var tr statusTransition
	view, ensureErr := h.ensureIssueDeliveries(ctx, issue)
	var prs []db.ListPullRequestsByIssueRow
	var err error
	if ensureErr != nil {
		slog.Warn("close gate: delivery lookup failed", "issue_id", uuidToString(issue.ID), "error", ensureErr)
		prs, err = h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	} else {
		prs = view.GateRows()
	}
	if err != nil {
		slog.Warn("close gate: list pull requests failed", "issue_id", uuidToString(issue.ID), "error", err)
		tr.status = issuestatus.Blocked
		tr.persistBlock = true
		tr.block = blockwait.FailureWake(time.Now(), "关单前没能读到关联的 PR", 1)
		tr.note = "这张票要关，但没能核对关联的 PR。先改成阻塞，不标完成。"
		return tr
	}
	deliveryBranchCount := 0
	if delivery, deliveryErr := service.BuildIssueDelivery(ctx, h.Queries, issue); deliveryErr != nil {
		slog.Warn("close gate: build delivery failed", "issue_id", uuidToString(issue.ID), "error", deliveryErr)
		tr.status = issuestatus.Blocked
		tr.persistBlock = true
		tr.block = blockwait.FailureWake(time.Now(), "关单前没能核对交付线", 1)
		tr.note = "这张票要关，但没能核对交付线。先改成阻塞，不标完成。"
		return tr
	} else if delivery != nil {
		deliveryBranchCount = len(delivery.Branches)
	}
	// An agent may not close a ticket whose acceptance seat is someone else.
	// A child is the exception: it cannot enter in_review (the close refuses
	// it), so its seat accepts it through the parent's tree review. Refusing
	// done too left child executors with no exit at all (DENE-928/931).
	// Without a seat the agent may close, but only through the merge gate below
	// or, when no PR is linked, an explicit no-code declaration.
	if actorType == "agent" && !issue.ParentIssueID.Valid && reviewerIsAssigned(issue) && actorID != uuidToString(issue.ReviewerID) {
		tr.refuse = "执行人不能直接关单：请用 `multica issue close --outcome in_review` 交给验收席。"
		return tr
	}
	// The gate only guards what the platform can see: a linked PR. Code that
	// lives where the platform cannot look (an intranet GitLab MR) is declared
	// through --no-code with the link as the reason, same as docs-only work;
	// the evidence comment and the acceptance seat carry the proof (DENE-943).
	if actorType == "agent" && len(prs) == 0 {
		if declared := declaredFrom(ctx); declared.Unverified {
			tr.note = "申报的链接没能核实，按未核实放行：" + declared.URL
			return tr
		}
		if reason := strings.TrimSpace(noCodeReason); reason != "" {
			tr.noCode = reason
			tr.note = "执行人声明这张票没有平台可见的 PR：" + reason + "。"
			return tr
		}
		if view.Denied {
			tr.refuse = "没权限：保存的令牌读不了这个仓库。平台已经叫仓库登记人来接上。本机有发起人的登录时先跑 `multica connection add --from-gh --yes`；试不了就不要自己设等待条件。"
			return tr
		}
		// The delivery lookup has already classified the missing delivery and
		// supplied the next command. Keep that single source of truth here so a
		// close refusal cannot fall back to the old generic PR wording.
		if view.Gap != nil {
			tr.refuse = view.Gap.Message + " 下一步：" + view.Gap.NextCommand
			return tr
		}
		// This is only a defensive fallback for an unexpected empty lookup.
		if deliveryBranchCount > 0 {
			tr.refuse = "交付查询没有返回 PR 或 MR。下一步：multica issue close " + view.Ident + " --pr <链接>"
			return tr
		}
		tr.refuse = "交付查询没有返回 PR 或 MR。下一步：multica issue close " + view.Ident + " --no-code <原因>"
		return tr
	}
	if actorType == "agent" && strings.TrimSpace(noCodeReason) != "" && hasOpenPull(prs) {
		tr.refuse = "这张票已经关联了 PR，不能用 `--no-code` 跳过合入门禁。"
		return tr
	}
	// Without a GitHub App the server cannot merge. A gh snapshot that already
	// says the PR is dirty or red still blocks here, with that reason (DENE-906).
	// A clean green PR, or one whose merge state was never reported, is refused
	// back to the closing agent: it has gh and a block would wait on nothing.
	if !hasOpenPull(prs) && !hasMergedPull(prs) {
		if url := firstDraftURL(prs); url != "" {
			tr.status = issuestatus.Blocked
			tr.persistBlock = true
			tr.block = blockwait.Record{
				WaitCondition:  "草稿还没合并 " + url,
				HasWakeAt:      true,
				WakeAt:         time.Now().Add(blockwait.QuietAfter),
				HasWaitTimeout: true,
				WaitTimeout:    time.Now().Add(blockwait.QuietAfter),
			}
			tr.note = "找到了但还没合并：" + url + "。它还是草稿，先标成准备好再合。"
			return tr
		}
	}
	if actorType == "agent" && hasOpenPull(prs) && !h.serverCanMergeOpen(ctx, issue.WorkspaceID, prs) && !openPullSnapshotBlocks(prs) {
		target := mergeTarget(prs)
		tr.refuse = fmt.Sprintf("这台服务没有合并权限，合不了 %s。确认检查通过后用 `%s` 自己合，再重跑这条 close；要别人验收就改用 `--outcome in_review`。", target, localMergeCommand(target))
		return tr
	}
	decision := blockwait.DecideClose(h.gatePRSnapshots(ctx, prs), time.Now())
	switch decision.Action {
	case blockwait.ReleaseDone:
		return tr
	case blockwait.ReleaseMerge:
		actor, _ := util.ParseUUID(actorID)
		h.trackBaselineFix(ctx, issue, &decision, actorType, actor)
		if err := h.mergeOpenPulls(ctx, issue.WorkspaceID, prs); err != nil {
			rec := decision.Record
			if !rec.Structured() {
				rec = blockwait.FailureWake(time.Now(), "关联 PR 没能合并", 1)
			}
			if reason := mergeFailureCondition(err, prs); reason != "" {
				rec.WaitCondition = reason
			}
			tr.status = issuestatus.Blocked
			tr.persistBlock = true
			tr.block = rec
			tr.note = "这张票要关，关联 PR 看起来能合并，但合并没有成功。先改成阻塞，不标完成。"
			return tr
		}
		tr.note = decision.Reason
		if !strings.Contains(tr.note, "已合并") {
			tr.note += " PR 已合并。"
		}
		tr.merged = true
		for _, pr := range prs {
			if strings.EqualFold(pr.State, "open") && pr.HtmlUrl != "" {
				tr.prURL = pr.HtmlUrl
				break
			}
		}
		return tr
	default:
		tr.status = issuestatus.Blocked
		tr.persistBlock = true
		tr.block = decision.Record
		tr.note = decision.Reason
		return tr
	}
}

func (h *Handler) mergeOpenPulls(ctx context.Context, ws pgtype.UUID, prs []db.ListPullRequestsByIssueRow) error {
	var merged int
	for i := range prs {
		pr := prs[i]
		if !strings.EqualFold(pr.State, "open") {
			continue
		}
		if err := h.mergeGatePull(ctx, ws, pr); err != nil {
			return err
		}
		merged++
	}
	if merged == 0 {
		return errPullNotMergeable
	}
	return nil
}

// openPullSnapshotBlocks is true when an open PR's gh snapshot is already a
// reason to block: conflict, branch protection, or checks that are red or
// still running. Empty and unknown merge state stay out, so a report that
// never carried a snapshot still goes back to the agent instead of parking.
func openPullSnapshotBlocks(prs []db.ListPullRequestsByIssueRow) bool {
	for _, pr := range prs {
		if !strings.EqualFold(pr.State, "open") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(pr.MergeableState.String)) {
		case "dirty", "behind", "blocked", "unstable":
			return true
		}
		switch strings.ToLower(strings.TrimSpace(pr.ChecksRollupState.String)) {
		case "failure", "error", "failing", "cancelled", "pending", "expected", "queued", "in_progress":
			return true
		}
	}
	return false
}

func hasMergedPull(prs []db.ListPullRequestsByIssueRow) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "merged") {
			return true
		}
	}
	return false
}

func firstDraftURL(prs []db.ListPullRequestsByIssueRow) string {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "draft") && pr.HtmlUrl != "" {
			return pr.HtmlUrl
		}
	}
	return ""
}

func localMergeCommand(url string) string {
	switch {
	case strings.Contains(url, "/-/merge_requests/"):
		return "glab mr merge " + url + " --squash --yes"
	case strings.Contains(url, "/pulls/") && !strings.Contains(url, "github.com"):
		return "先在网页上合并 " + url + "，再重跑 close"
	case url == "" || url == "关联 PR":
		return "gh pr merge --squash <url>"
	default:
		return "gh pr merge --squash " + url
	}
}

func hasOpenPull(prs []db.ListPullRequestsByIssueRow) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "open") {
			return true
		}
	}
	return false
}

func mergeTarget(prs []db.ListPullRequestsByIssueRow) string {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "open") && pr.HtmlUrl != "" {
			return pr.HtmlUrl
		}
	}
	return "关联 PR"
}

func mergeFailureCondition(err error, prs []db.ListPullRequestsByIssueRow) string {
	label := mergeTarget(prs)
	switch {
	case errors.Is(err, errPullMergeUnavailable):
		return label + " 这台服务没有合并权限"
	case errors.Is(err, errPullNotMergeable):
		return label + " 合不进去"
	default:
		return label + " 合并没有成功"
	}
}

// finishStatusTransition applies a handoff and posts the human note after the
// status row is committed. It returns the issue to publish, which may be the
// reloaded row when the acceptance seat just took the ticket.
func (h *Handler) finishStatusTransition(ctx context.Context, issue db.Issue, tr statusTransition) db.Issue {
	if tr.handoff && tr.reviewerID.Valid {
		if err := (routingStore{h: h}).Handoff(ctx, uuidToString(issue.WorkspaceID), uuidToString(issue.ID), "agent", uuidToString(tr.reviewerID)); err != nil {
			slog.Warn("acceptance handoff failed", "issue_id", uuidToString(issue.ID), "error", err)
			tr.note += "验收席补上了，但这轮没能启动。"
		} else {
			tr.note += "验收已经开始。"
			if fresh, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
				ID: issue.ID, WorkspaceID: issue.WorkspaceID,
			}); err == nil {
				issue = fresh
			}
		}
	}
	if tr.note != "" {
		h.postBlockComment(ctx, issue, tr.note)
	}
	return issue
}

// ensureAcceptanceRunning starts the acceptance seat once the executor's run
// is gone. The status write itself waits out that run; this is the later pass.
func (h *Handler) ensureAcceptanceRunning(ctx context.Context, workspaceID, issueID string) {
	if h == nil || h.Queries == nil || workspaceID == "" || issueID == "" {
		return
	}
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return
	}
	id, err := util.ParseUUID(issueID)
	if err != nil {
		return
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: wsID})
	if err != nil || issue.Status != issuestatus.InReview {
		return
	}
	if !issue.ReviewerType.Valid || issue.ReviewerType.String != "agent" || !issue.ReviewerID.Valid {
		return
	}
	if issue.AssigneeType.String == "agent" && issue.AssigneeID == issue.ReviewerID {
		return
	}
	active, err := h.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil || active {
		return
	}
	if err := (routingStore{h: h}).Handoff(ctx, workspaceID, issueID, "agent", uuidToString(issue.ReviewerID)); err != nil {
		slog.Warn("acceptance handoff failed", "issue_id", issueID, "error", err)
		return
	}
	h.postBlockComment(ctx, issue, "执行这轮结束了，验收席开始检查。")
}
