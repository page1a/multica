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
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		slog.Warn("review gate: list pull requests failed", "issue_id", uuidToString(issue.ID), "error", err)
		return "进不了待验收：没能读到这张票关联的 PR，稍后再试。"
	}
	for _, pr := range prs {
		if reviewDeliveryStates[strings.ToLower(pr.State)] {
			return ""
		}
	}
	key := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	return fmt.Sprintf("进不了待验收：%s 没有关联的 PR，验收人没有东西可看。没有 GitHub App 时，请用票号重跑 `multica issue close %s`，并检查 PR 标题或分支里包含 %s；纯文档或调研类没有代码交付的票，用 `--no-code <原因>` 说明。", key, key, key)
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
		h.postBlockComment(ctx, issue, tr.refuse+"。这张票保持原来的状态。")
	}
	return tr
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
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
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
	// Without a seat the agent may close, but only through the merge gate below
	// or, when neither a PR nor a delivery branch exists, an explicit no-code
	// declaration.
	if actorType == "agent" && reviewerIsAssigned(issue) && actorID != uuidToString(issue.ReviewerID) {
		tr.refuse = "执行人不能直接关单：请用 `multica issue close --outcome in_review` 交给验收席。"
		return tr
	}
	if actorType == "agent" && len(prs) == 0 && deliveryBranchCount == 0 {
		if strings.TrimSpace(noCodeReason) == "" {
			tr.refuse = "这张票没有 PR 或交付分支，执行人关单必须带 `--no-code <原因>`。"
			return tr
		}
		tr.noCode = strings.TrimSpace(noCodeReason)
		tr.note = "执行人声明这张票没有代码交付：" + tr.noCode + "。"
		return tr
	}
	if actorType == "agent" && strings.TrimSpace(noCodeReason) != "" {
		tr.refuse = "这张票已经有 PR 或交付分支，不能用 `--no-code` 跳过合入门禁。"
		return tr
	}
	// No PR yet is something the closing agent can fix in this run. Parking it
	// as blocked waited on an event nobody produces (DENE-899): refuse instead.
	if actorType == "agent" && len(prs) == 0 && deliveryBranchCount > 0 {
		tr.refuse = "这张票有交付分支，但平台查不到它的 PR。先用 `gh pr create` 开 PR（标题带票号，打向主线），再重跑这条 close；close 会用本机 gh 把 PR 报给平台。"
		return tr
	}
	// Without a GitHub App the server can neither read an open PR's checks nor
	// merge it, so any block here waits on nothing. The closing agent has gh.
	if actorType == "agent" && hasOpenPull(prs) && !h.canMergePulls() {
		tr.refuse = fmt.Sprintf("这台服务没有合并权限，合不了 %s。确认检查通过后用 `gh pr merge --squash` 自己合，再重跑这条 close；要别人验收就改用 `--outcome in_review`。", mergeTarget(prs))
		return tr
	}
	decision := blockwait.DecideClose(h.gatePRSnapshots(ctx, prs), time.Now())
	switch decision.Action {
	case blockwait.ReleaseDone:
		return tr
	case blockwait.ReleaseMerge:
		actor, _ := util.ParseUUID(actorID)
		h.trackBaselineFix(ctx, issue, &decision, actorType, actor)
		if err := h.mergeOpenPulls(ctx, prs); err != nil {
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

func (h *Handler) mergeOpenPulls(ctx context.Context, prs []db.ListPullRequestsByIssueRow) error {
	var merged int
	for i := range prs {
		pr := prs[i]
		if !strings.EqualFold(pr.State, "open") {
			continue
		}
		if err := h.mergePullRequest(ctx, pr.InstallationID, pr.RepoOwner, pr.RepoName, int(pr.PrNumber)); err != nil {
			return err
		}
		merged++
	}
	if merged == 0 {
		return errPullNotMergeable
	}
	return nil
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
