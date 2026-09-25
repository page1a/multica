package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Issue-level canonical delivery (DENE-820).
//
// An issue is the delivery aggregate: exactly one of its branches is the
// canonical line that ships, every other branch a task or a person left
// behind is a rescue, an experiment, or — until someone says — unclassified.
// The record lives here on the server, not in any local checkout, because
// a checkout is one machine's view and the worktree GC there can only see
// files, never which line was the delivery.

// Delivery branch roles.
const (
	DeliveryRoleCanonical    = "canonical"
	DeliveryRoleRescue       = "rescue"
	DeliveryRoleExperiment   = "experiment"
	DeliveryRoleUnclassified = "unclassified"

	DeliveryResolutionAbsorbed  = "absorbed"
	DeliveryResolutionDiscarded = "discarded"

	DeliveryCleanupLive    = "live"
	DeliveryCleanupCleaned = "cleaned"
	DeliveryCleanupKept    = "kept"
)

// ErrDeliveryBranchInvalid rejects a branch name or classification the
// aggregate cannot accept. The message is safe to show to the caller.
var ErrDeliveryBranchInvalid = errors.New("delivery branch invalid")

// IssueDeliveryTask is one run that reported the branch.
type IssueDeliveryTask struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"`
	AgentID        string     `json:"agent_id"`
	AgentName      string     `json:"agent_name,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DurableWorkDir string     `json:"durable_work_dir,omitempty"`
}

// IssueDeliveryPullRequest is the PR whose head is the branch, when linked.
type IssueDeliveryPullRequest struct {
	Number   int32      `json:"number"`
	URL      string     `json:"url"`
	State    string     `json:"state"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

// IssueDeliveryBranch is one line of work the issue owns.
type IssueDeliveryBranch struct {
	Branch        string     `json:"branch"`
	Role          string     `json:"role"`
	Resolution    string     `json:"resolution,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	CleanupStatus string     `json:"cleanup_status"`
	CleanupNote   string     `json:"cleanup_note,omitempty"`
	CleanedAt     *time.Time `json:"cleaned_at,omitempty"`
	// Recorded is false for a branch only task rows know about — reported by
	// a run before this table existed. It is shown as unclassified and can be
	// classified like any other; classifying it writes the row.
	Recorded    bool                      `json:"recorded"`
	Tasks       []IssueDeliveryTask       `json:"tasks"`
	PullRequest *IssueDeliveryPullRequest `json:"pull_request,omitempty"`
}

// IssueDeliveryCleanupItem is one non-canonical site and whether it may go.
type IssueDeliveryCleanupItem struct {
	Branch string `json:"branch"`
	Role   string `json:"role"`
	// Allowed says the server-side facts permit removing the site. The
	// local side still requires merge evidence before deleting anything.
	Allowed       bool     `json:"allowed"`
	Reason        string   `json:"reason,omitempty"`
	CleanupStatus string   `json:"cleanup_status"`
	WorkDirs      []string `json:"work_dirs"`
}

// IssueDelivery is the aggregate view.
type IssueDelivery struct {
	IssueID     string                `json:"issue_id"`
	IssueStatus string                `json:"issue_status"`
	Canonical   *IssueDeliveryBranch  `json:"canonical,omitempty"`
	Branches    []IssueDeliveryBranch `json:"branches"`
	// Merged is true once the canonical PR merged (or the issue is closed
	// without one), which is when non-canonical sites become cleanable.
	Merged bool `json:"merged"`
	// Problems lists what stops this issue from having one clean delivery
	// truth: no canonical, an unclassified line, an open rescue. Empty means
	// the aggregate is consistent.
	Problems    []string                   `json:"problems"`
	CleanupPlan []IssueDeliveryCleanupItem `json:"cleanup_plan"`
}

// RecordIssueDeliveryBranch files the branch a task reported under its
// issue. The first branch an issue sees becomes canonical; any later one is
// recorded as unclassified. The second return is true exactly when this call
// created a NEW non-canonical line, which the caller must say out loud on
// the issue — a second delivery line must never appear silently.
func RecordIssueDeliveryBranch(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (*db.IssueDeliveryBranch, bool, error) {
	if q == nil || !task.IssueID.Valid || !task.BranchName.Valid {
		return nil, false, nil
	}
	branch := strings.TrimSpace(task.BranchName.String)
	if branch == "" {
		return nil, false, nil
	}
	issue, err := q.GetIssue(ctx, task.IssueID)
	if err != nil {
		return nil, false, err
	}
	role := DeliveryRoleUnclassified
	if _, err := q.GetIssueCanonicalDeliveryBranch(ctx, task.IssueID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
		role = DeliveryRoleCanonical
	}
	row, err := q.InsertIssueDeliveryBranch(ctx, db.InsertIssueDeliveryBranchParams{
		IssueID:     task.IssueID,
		WorkspaceID: issue.WorkspaceID,
		BranchName:  branch,
		Role:        role,
		FirstTaskID: task.ID,
		AgentID:     task.AgentID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			// Two first reports racing on the partial unique index: the
			// loser re-reads and files as unclassified.
			if role == DeliveryRoleCanonical && isUniqueViolation(err) {
				task.BranchName = pgtype.Text{String: branch, Valid: true}
				return RecordIssueDeliveryBranch(ctx, q, task)
			}
			return nil, false, err
		}
		existing, getErr := q.GetIssueDeliveryBranch(ctx, db.GetIssueDeliveryBranchParams{IssueID: task.IssueID, BranchName: branch})
		if getErr != nil {
			return nil, false, getErr
		}
		return &existing, false, nil
	}
	return &row, row.Role != DeliveryRoleCanonical, nil
}

// UnclassifiedDeliveryLineNotice is the system comment posted when a run
// lands on a branch that is not the issue's canonical line.
func UnclassifiedDeliveryLineNotice(canonical, branch string) string {
	return fmt.Sprintf("这次 run 的产出落在了分支 `%s`，不是这张票的交付线 `%s`。它现在是「未归类」的第二条线，验收合并前必须表态：`multica issue delivery classify <issue> %s --role rescue|experiment`，或者 `multica issue delivery set-canonical <issue> %s` 把交付线换过去。", branch, canonical, branch, branch)
}

// SetIssueCanonicalDeliveryBranch makes branch the issue's canonical line.
// The previous canonical, if different, becomes an open rescue so its work
// is accounted for before the new line merges.
func SetIssueCanonicalDeliveryBranch(ctx context.Context, q *db.Queries, issue db.Issue, branch string) (*db.IssueDeliveryBranch, error) {
	branch, err := normalizeDeliveryBranch(branch)
	if err != nil {
		return nil, err
	}
	if current, err := q.GetIssueCanonicalDeliveryBranch(ctx, issue.ID); err == nil {
		if current.BranchName == branch {
			return &current, nil
		}
		if err := q.DemoteIssueCanonicalDeliveryBranch(ctx, issue.ID); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, err := ensureDeliveryBranchRow(ctx, q, issue, branch); err != nil {
		return nil, err
	}
	row, err := q.SetIssueDeliveryBranchRole(ctx, db.SetIssueDeliveryBranchRoleParams{
		IssueID: issue.ID, BranchName: branch, Role: DeliveryRoleCanonical,
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ClassifyIssueDeliveryBranch names a non-canonical line. role is rescue or
// experiment; resolution is empty (still open), absorbed, or discarded. The
// canonical line cannot be classified away — switch it with
// SetIssueCanonicalDeliveryBranch instead, which keeps its work accounted.
func ClassifyIssueDeliveryBranch(ctx context.Context, q *db.Queries, issue db.Issue, branch, role, resolution string) (*db.IssueDeliveryBranch, error) {
	branch, err := normalizeDeliveryBranch(branch)
	if err != nil {
		return nil, err
	}
	switch role {
	case DeliveryRoleRescue, DeliveryRoleExperiment:
	default:
		return nil, fmt.Errorf("%w: role must be rescue or experiment, got %q", ErrDeliveryBranchInvalid, role)
	}
	switch resolution {
	case "", DeliveryResolutionAbsorbed, DeliveryResolutionDiscarded:
	default:
		return nil, fmt.Errorf("%w: resolution must be absorbed or discarded, got %q", ErrDeliveryBranchInvalid, resolution)
	}
	existing, err := ensureDeliveryBranchRow(ctx, q, issue, branch)
	if err != nil {
		return nil, err
	}
	if existing.Role == DeliveryRoleCanonical {
		return nil, fmt.Errorf("%w: %s is the canonical line; pick another canonical first", ErrDeliveryBranchInvalid, branch)
	}
	row, err := q.SetIssueDeliveryBranchRole(ctx, db.SetIssueDeliveryBranchRoleParams{
		IssueID:    issue.ID,
		BranchName: branch,
		Role:       role,
		Resolution: pgtype.Text{String: resolution, Valid: resolution != ""},
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// RecordIssueDeliveryCleanup writes what happened to a non-canonical site.
func RecordIssueDeliveryCleanup(ctx context.Context, q *db.Queries, issue db.Issue, branch, status, note string) (*db.IssueDeliveryBranch, error) {
	branch, err := normalizeDeliveryBranch(branch)
	if err != nil {
		return nil, err
	}
	switch status {
	case DeliveryCleanupCleaned, DeliveryCleanupKept, DeliveryCleanupLive:
	default:
		return nil, fmt.Errorf("%w: cleanup status must be cleaned, kept or live, got %q", ErrDeliveryBranchInvalid, status)
	}
	existing, err := ensureDeliveryBranchRow(ctx, q, issue, branch)
	if err != nil {
		return nil, err
	}
	if existing.Role == DeliveryRoleCanonical && status == DeliveryCleanupCleaned {
		return nil, fmt.Errorf("%w: %s is the canonical line and is never cleaned up", ErrDeliveryBranchInvalid, branch)
	}
	row, err := q.SetIssueDeliveryBranchCleanup(ctx, db.SetIssueDeliveryBranchCleanupParams{
		IssueID:       issue.ID,
		BranchName:    branch,
		CleanupStatus: status,
		CleanupNote:   pgtype.Text{String: note, Valid: note != ""},
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// BuildIssueDelivery assembles the aggregate: recorded branches, branches
// only task rows know, the runs behind each, the PR on each, and the
// cleanup plan for everything that is not canonical.
func BuildIssueDelivery(ctx context.Context, q *db.Queries, issue db.Issue) (*IssueDelivery, error) {
	rows, err := q.ListIssueDeliveryBranches(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	tasks, err := q.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	prs, err := q.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	vcsPRs, err := q.ListVCSPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return nil, err
	}

	byBranch := map[string]*IssueDeliveryBranch{}
	order := []string{}
	add := func(name string) *IssueDeliveryBranch {
		if b, ok := byBranch[name]; ok {
			return b
		}
		b := &IssueDeliveryBranch{Branch: name, Role: DeliveryRoleUnclassified, CleanupStatus: DeliveryCleanupLive, Tasks: []IssueDeliveryTask{}}
		byBranch[name] = b
		order = append(order, name)
		return b
	}
	for _, row := range rows {
		b := add(row.BranchName)
		b.Role = row.Role
		b.Recorded = true
		b.Resolution = row.Resolution.String
		b.ResolvedAt = tsPtr(row.ResolvedAt)
		b.CleanupStatus = row.CleanupStatus
		b.CleanupNote = row.CleanupNote.String
		b.CleanedAt = tsPtr(row.CleanedAt)
	}

	agentNames := map[string]string{}
	for _, t := range tasks {
		if !t.BranchName.Valid || strings.TrimSpace(t.BranchName.String) == "" {
			continue
		}
		b := add(strings.TrimSpace(t.BranchName.String))
		agentID := util.UUIDToString(t.AgentID)
		name, ok := agentNames[agentID]
		if !ok {
			if agent, err := q.GetAgent(ctx, t.AgentID); err == nil {
				name = agent.Name
			}
			agentNames[agentID] = name
		}
		b.Tasks = append(b.Tasks, IssueDeliveryTask{
			ID:             util.UUIDToString(t.ID),
			Status:         t.Status,
			AgentID:        agentID,
			AgentName:      name,
			CompletedAt:    tsPtr(t.CompletedAt),
			DurableWorkDir: t.DurableWorkDir.String,
		})
	}
	for _, pr := range prs {
		if !pr.Branch.Valid {
			continue
		}
		if b, ok := byBranch[pr.Branch.String]; ok {
			b.PullRequest = pickNewerPR(b.PullRequest, &IssueDeliveryPullRequest{Number: pr.PrNumber, URL: pr.HtmlUrl, State: pr.State, MergedAt: tsPtr(pr.MergedAt)})
		}
	}
	for _, pr := range vcsPRs {
		if !pr.Branch.Valid {
			continue
		}
		if b, ok := byBranch[pr.Branch.String]; ok {
			b.PullRequest = pickNewerPR(b.PullRequest, &IssueDeliveryPullRequest{Number: pr.PrNumber, URL: pr.HtmlUrl, State: pr.State, MergedAt: tsPtr(pr.MergedAt)})
		}
	}

	out := &IssueDelivery{
		IssueID:     util.UUIDToString(issue.ID),
		IssueStatus: issue.Status,
		Branches:    make([]IssueDeliveryBranch, 0, len(order)),
		Problems:    []string{},
		CleanupPlan: []IssueDeliveryCleanupItem{},
	}
	sort.SliceStable(order, func(i, j int) bool {
		ci, cj := byBranch[order[i]].Role == DeliveryRoleCanonical, byBranch[order[j]].Role == DeliveryRoleCanonical
		if ci != cj {
			return ci
		}
		return i < j
	})
	for _, name := range order {
		b := byBranch[name]
		out.Branches = append(out.Branches, *b)
		if b.Role == DeliveryRoleCanonical {
			c := *b
			out.Canonical = &c
		}
	}

	issueClosed := issue.Status == issuestatus.Done || issue.Status == issuestatus.Cancelled
	if out.Canonical == nil {
		if len(out.Branches) > 0 {
			out.Problems = append(out.Problems, "没有 canonical 交付线：用 set-canonical 指定一条")
		}
		out.Merged = issueClosed
	} else {
		out.Merged = issueClosed || (out.Canonical.PullRequest != nil && out.Canonical.PullRequest.MergedAt != nil)
	}
	for _, b := range out.Branches {
		if b.Role == DeliveryRoleCanonical {
			continue
		}
		item := IssueDeliveryCleanupItem{Branch: b.Branch, Role: b.Role, CleanupStatus: b.CleanupStatus, WorkDirs: []string{}}
		for _, t := range b.Tasks {
			if t.DurableWorkDir != "" {
				item.WorkDirs = append(item.WorkDirs, t.DurableWorkDir)
			}
		}
		switch {
		case b.Role == DeliveryRoleUnclassified:
			out.Problems = append(out.Problems, fmt.Sprintf("分支 %s 未归类：标成 rescue 或 experiment，或换成 canonical", b.Branch))
			item.Reason = "未归类的线不能清理"
		case b.Role == DeliveryRoleRescue && b.Resolution == "":
			out.Problems = append(out.Problems, fmt.Sprintf("救援分支 %s 还没归并进 canonical，也没有声明放弃", b.Branch))
			item.Reason = "救援线还没归并或放弃"
		case b.CleanupStatus == DeliveryCleanupCleaned:
			item.Reason = "已清理"
		case !out.Merged:
			item.Reason = "canonical PR 还没合并"
		default:
			item.Allowed = true
		}
		out.CleanupPlan = append(out.CleanupPlan, item)
	}
	return out, nil
}

// DeliveryMergeBlocker says why the issue's canonical PR must not merge yet,
// or "" when delivery is consistent. openPRBranches are the head branches of
// the PRs currently open for the issue.
func DeliveryMergeBlocker(d *IssueDelivery, openPRBranches []string) string {
	if d == nil {
		return ""
	}
	for _, b := range d.Branches {
		switch {
		case b.Role == DeliveryRoleUnclassified && b.Recorded:
			return fmt.Sprintf("分支 %s 是未归类的第二条交付线，先 classify 或 set-canonical", b.Branch)
		case b.Role == DeliveryRoleRescue && b.Resolution == "":
			return fmt.Sprintf("救援分支 %s 还没归并进 canonical，也没有声明放弃", b.Branch)
		}
	}
	if d.Canonical != nil && len(openPRBranches) > 0 {
		for _, head := range openPRBranches {
			if head == d.Canonical.Branch {
				return ""
			}
		}
		return fmt.Sprintf("开着的 PR 不在 canonical 分支 %s 上（%s）", d.Canonical.Branch, strings.Join(openPRBranches, ", "))
	}
	return ""
}

func ensureDeliveryBranchRow(ctx context.Context, q *db.Queries, issue db.Issue, branch string) (*db.IssueDeliveryBranch, error) {
	row, err := q.InsertIssueDeliveryBranch(ctx, db.InsertIssueDeliveryBranchParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, BranchName: branch, Role: DeliveryRoleUnclassified,
	})
	if err == nil {
		return &row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	existing, err := q.GetIssueDeliveryBranch(ctx, db.GetIssueDeliveryBranchParams{IssueID: issue.ID, BranchName: branch})
	if err != nil {
		return nil, err
	}
	return &existing, nil
}

func normalizeDeliveryBranch(branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.ContainsAny(branch, " \t\n~^:?*[\\") || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return "", fmt.Errorf("%w: %q is not a branch name", ErrDeliveryBranchInvalid, branch)
	}
	return branch, nil
}

func pickNewerPR(cur, next *IssueDeliveryPullRequest) *IssueDeliveryPullRequest {
	if cur == nil {
		return next
	}
	// A merged PR is the fact that matters; otherwise keep the newer number.
	if next.MergedAt != nil && cur.MergedAt == nil {
		return next
	}
	if cur.MergedAt != nil && next.MergedAt == nil {
		return cur
	}
	if next.Number > cur.Number {
		return next
	}
	return cur
}

func tsPtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time.UTC()
	return &t
}
