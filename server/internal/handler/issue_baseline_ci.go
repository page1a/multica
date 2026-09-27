package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// BaseCheckReader reads the latest CI on a pull request's base branch
// (DENE-892). Tests set Handler.PRBaseChecks; production uses PRRefresh.
type BaseCheckReader interface {
	FetchBaseChecks(ctx context.Context, installationID int64, owner, repo string, number int32) (*ghsnapshot.BaseChecks, error)
}

// keyBaselineChecks lists, on a baseline fix issue, the check names it
// already covers, so a second let-through of the same check adds nothing.
const keyBaselineChecks = "baseline_ci.checks"

func (h *Handler) baseCheckReader() BaseCheckReader {
	if h.PRBaseChecks != nil {
		return h.PRBaseChecks
	}
	if h.PRRefresh != nil && h.PRRefresh.Enabled() {
		return h.PRRefresh
	}
	return nil
}

// gatePRSnapshots is the input both merge gates decide on. The base branch CI
// is read only for an open PR whose checks are red, since only there can it
// change the answer; a read that fails leaves Base nil and the gate stays
// conservative.
func (h *Handler) gatePRSnapshots(ctx context.Context, prs []db.ListPullRequestsByIssueRow) []blockwait.PRSnapshot {
	snapshots := make([]blockwait.PRSnapshot, 0, len(prs))
	for _, pr := range prs {
		snap := blockwait.PRSnapshot{
			Number:        int(pr.PrNumber),
			State:         pr.State,
			Mergeable:     pr.MergeableState.String,
			Checks:        pr.ChecksRollupState.String,
			URL:           pr.HtmlUrl,
			FailedChecks:  pr.FailedCheckNames,
			RunningChecks: int(pr.ChecksRunning),
		}
		if strings.EqualFold(pr.State, "open") && len(pr.FailedCheckNames) > 0 {
			snap.Base = h.readBaseChecks(ctx, pr)
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots
}

func (h *Handler) readBaseChecks(ctx context.Context, pr db.ListPullRequestsByIssueRow) *blockwait.BaseChecks {
	reader := h.baseCheckReader()
	if reader == nil {
		return nil
	}
	base, err := reader.FetchBaseChecks(ctx, pr.InstallationID, pr.RepoOwner, pr.RepoName, pr.PrNumber)
	if err != nil || base == nil {
		slog.Warn("close gate: base branch checks unavailable", "pr", pr.HtmlUrl, "error", err)
		return nil
	}
	return &blockwait.BaseChecks{Branch: base.Branch, Failed: base.FailedNames()}
}

// ensureBaselineFixIssue opens the "<base> 基线 CI 红" issue, or reuses the one
// still open, for checks a gate just let through. The title is the key: the
// create's duplicate guard hands back the open one. A check the reused issue
// already lists is not added again. It returns the fix issue's identifier, or
// "" when none could be written; the let-through stands either way.
func (h *Handler) ensureBaselineFixIssue(ctx context.Context, issue db.Issue, base string, names []string, actorType string, actorID pgtype.UUID) string {
	if len(names) == 0 || h.IssueService == nil {
		return ""
	}
	if base == "" {
		base = "主线"
	}
	creatorType, creatorID := actorType, actorID
	if (creatorType != "agent" && creatorType != "member") || !creatorID.Valid {
		creatorType, creatorID = issue.CreatorType, issue.CreatorID
	}
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	source := issueToResponse(issue, prefix).Identifier
	result, err := h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID:   issue.WorkspaceID,
		Title:         base + " 基线 CI 红",
		Description:   pgtype.Text{String: baselineFixDescription(base, names, source, uuidToString(issue.ID)), Valid: true},
		Status:        "todo",
		Priority:      "high",
		CreatorType:   creatorType,
		CreatorID:     creatorID,
		ProjectPinned: true,
	}, service.IssueCreateOpts{})
	var fix db.Issue
	switch {
	case err == nil:
		fix = result.Issue
		h.setIssueMetaString(ctx, fix, keyBaselineChecks, strings.Join(names, ","))
	case errors.Is(err, service.ErrActiveDuplicate) && result.DuplicateIssue != nil:
		fix = *result.DuplicateIssue
		covered := splitCheckNames(blockwait.MetaString(parseIssueMetadata(fix.Metadata), keyBaselineChecks))
		var added []string
		for _, name := range names {
			if !covered[name] {
				added = append(added, name)
				covered[name] = true
			}
		}
		if len(added) > 0 {
			all := make([]string, 0, len(covered))
			for name := range covered {
				all = append(all, name)
			}
			sort.Strings(all)
			h.setIssueMetaString(ctx, fix, keyBaselineChecks, strings.Join(all, ","))
			h.postBlockComment(ctx, fix, fmt.Sprintf("%s 上又有检查被当作主线原有失败放行：%s（来自 %s）。一并修。", base, strings.Join(added, "、"), source))
		}
	default:
		slog.Warn("close gate: baseline fix issue not written", "issue_id", uuidToString(issue.ID), "error", err)
		return ""
	}
	return issueToResponse(fix, prefix).Identifier
}

func baselineFixDescription(base string, names []string, source, sourceID string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## 现象\n\n`%s` 最新一次 CI 上这些检查是红的：\n\n", base)
	for _, name := range names {
		fmt.Fprintf(&b, "- `%s`\n", name)
	}
	fmt.Fprintf(&b, "\n关票门禁按同名检查比对，[%s](mention://issue/%s) 因为这些主线原有失败被放行合并。之后被同样放行的新检查会追加在评论里。\n\n", source, sourceID)
	fmt.Fprintf(&b, "## 要做的\n\n把 `%s` 上这些检查修回绿。修好之前，门禁不会把它们算在别的票头上；但同一个 job 里 PR 新加的失败也会被一起放过。\n", base)
	return b.String()
}

func splitCheckNames(raw string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

// trackBaselineFix opens or reuses the fix issue for a let-through decision
// and adds where the fix is tracked to its reason, which already names the
// checks.
func (h *Handler) trackBaselineFix(ctx context.Context, issue db.Issue, d *blockwait.Decision, actorType string, actorID pgtype.UUID) {
	if len(d.Inherited) == 0 {
		return
	}
	if fix := h.ensureBaselineFixIssue(ctx, issue, d.BaseBranch, d.Inherited, actorType, actorID); fix != "" {
		d.Reason += "修复票：" + fix + "。"
	}
}
