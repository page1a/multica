package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Sub-issues deliver onto the parent's line (DENE-1537).
//
// A sub-issue splits one feature; it does not ship on its own. Its run works
// on its own branch forked from the parent's delivery branch, and `issue
// close --outcome done` merges those commits back into that branch. The
// parent then opens the one PR that carries the whole feature. A real
// conflict closes the sub-issue blocked with the conflicting files; the
// platform never resolves it.
//
// The line is opened at claim, only by a daemon that can do the merge-back
// (the CLI that runs `issue close` is the same binary). A sub-issue that
// already produced a branch of its own, or was created before this shipped,
// keeps one PR per issue.

const (
	DeliveryLineOpen     = "open"
	DeliveryLineMerged   = "merged"
	DeliveryLineConflict = "conflict"

	// deliveryLineBranchPrefix names the parent's line when the parent has
	// no canonical branch yet — the usual case, since a parent that only
	// plans never runs code itself. Not an agent name: every sub-issue,
	// whoever runs it, must land on the same branch.
	deliveryLineBranchPrefix = "agent/delivery/"

	maxDeliveryLineCommits = 200
)

// DeliveryLineRolloutAt is when sub-issues started delivering onto the
// parent's line. A sub-issue created earlier belongs to a feature whose
// siblings may already have shipped their own PRs; it keeps that behaviour.
var DeliveryLineRolloutAt = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

var deliveryCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// DeliveryLineCommit is one commit a merge-back added to the parent's line.
type DeliveryLineCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// DeliveryMergeReport is what `issue close` reports after it merged the
// sub-issue's commits into the parent's branch, or found it could not.
type DeliveryMergeReport struct {
	// Status is merged or conflict.
	Status       string               `json:"status"`
	Branch       string               `json:"branch"`
	SourceBranch string               `json:"source_branch,omitempty"`
	Tip          string               `json:"tip,omitempty"`
	Commits      []DeliveryLineCommit `json:"commits"`
	// ConflictFiles is set for a conflict: the files git could not merge.
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

// IssueDeliveryLine is a sub-issue's view: whose branch it delivers onto and
// how far the merge-back got.
type IssueDeliveryLine struct {
	OwnerIssueID    string               `json:"owner_issue_id"`
	OwnerIdentifier string               `json:"owner_identifier"`
	OwnerTitle      string               `json:"owner_title,omitempty"`
	Branch          string               `json:"branch"`
	Status          string               `json:"status"`
	SourceBranch    string               `json:"source_branch,omitempty"`
	MergedTip       string               `json:"merged_tip,omitempty"`
	Commits         []DeliveryLineCommit `json:"commits"`
	ConflictFiles   []string             `json:"conflict_files"`
	MergedAt        *time.Time           `json:"merged_at,omitempty"`
}

// IssueDeliveryContribution is one sub-issue on the owner's side.
type IssueDeliveryContribution struct {
	IssueID       string               `json:"issue_id"`
	Identifier    string               `json:"identifier"`
	Title         string               `json:"title"`
	IssueStatus   string               `json:"issue_status"`
	Status        string               `json:"status"`
	Branch        string               `json:"branch,omitempty"`
	Commits       []DeliveryLineCommit `json:"commits"`
	ConflictFiles []string             `json:"conflict_files"`
	MergedAt      *time.Time           `json:"merged_at,omitempty"`
}

// DeliveryLineClaim is what a capable daemon is told at claim.
type DeliveryLineClaim struct {
	OwnerIssueID    string `json:"owner_issue_id"`
	OwnerIdentifier string `json:"owner_identifier"`
	// Branch is the parent's delivery branch. It may not exist on the
	// machine yet: the first sub-issue to merge back creates it.
	Branch string `json:"branch"`
}

// GetIssueDeliveryLine returns the sub-issue's line, or nil when it delivers
// on its own.
func GetIssueDeliveryLine(ctx context.Context, q *db.Queries, issueID pgtype.UUID) (*db.IssueDeliveryLine, error) {
	row, err := q.GetIssueDeliveryLine(ctx, issueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// OpenIssueDeliveryLine returns the sub-issue's line, opening it when the
// sub-issue qualifies: it has a parent, was created after the rollout, and
// has never produced a branch of its own. nil means "delivers on its own".
func OpenIssueDeliveryLine(ctx context.Context, q *db.Queries, issue db.Issue) (*db.IssueDeliveryLine, error) {
	if existing, err := GetIssueDeliveryLine(ctx, q, issue.ID); err != nil || existing != nil {
		return existing, err
	}
	if !issue.ParentIssueID.Valid {
		return nil, nil
	}
	if !issue.CreatedAt.Valid || issue.CreatedAt.Time.Before(DeliveryLineRolloutAt) {
		return nil, nil
	}
	branches, err := q.ListIssueDeliveryBranches(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	if len(branches) > 0 {
		return nil, nil
	}
	tasks, err := q.ListTasksByIssue(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.BranchName.Valid && strings.TrimSpace(t.BranchName.String) != "" {
			return nil, nil
		}
	}
	row, err := q.InsertIssueDeliveryLine(ctx, db.InsertIssueDeliveryLineParams{
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		OwnerIssueID: issue.ParentIssueID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GetIssueDeliveryLine(ctx, q, issue.ID)
		}
		return nil, err
	}
	return &row, nil
}

// DeliveryLineTarget is the branch the owner's line lives on: its canonical
// delivery branch when it has one, otherwise the name the first merge-back
// will create.
func DeliveryLineTarget(ctx context.Context, q *db.Queries, owner db.Issue, ownerIdentifier string) (string, error) {
	canonical, err := q.GetIssueCanonicalDeliveryBranch(ctx, owner.ID)
	if err == nil {
		return canonical.BranchName, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	return deliveryLineBranchPrefix + sanitizeDeliveryKey(ownerIdentifier, owner), nil
}

func sanitizeDeliveryKey(identifier string, owner db.Issue) string {
	key := strings.ToLower(strings.TrimSpace(identifier))
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		out = "issue-" + strings.ReplaceAll(util.UUIDToString(owner.ID), "-", "")[:12]
	}
	return out
}

// DeliveryLineClaimFor resolves what a claiming daemon is told for line.
func DeliveryLineClaimFor(ctx context.Context, q *db.Queries, line db.IssueDeliveryLine) (*DeliveryLineClaim, error) {
	owner, err := q.GetIssue(ctx, line.OwnerIssueID)
	if err != nil {
		return nil, err
	}
	identifier, err := issueIdentifierFor(ctx, q, owner)
	if err != nil {
		return nil, err
	}
	branch, err := DeliveryLineTarget(ctx, q, owner, identifier)
	if err != nil {
		return nil, err
	}
	return &DeliveryLineClaim{
		OwnerIssueID:    util.UUIDToString(owner.ID),
		OwnerIdentifier: identifier,
		Branch:          branch,
	}, nil
}

func issueIdentifierFor(ctx context.Context, q *db.Queries, issue db.Issue) (string, error) {
	ws, err := q.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		return "", err
	}
	return IssueIdentifier(ws.IssuePrefix, issue.Number), nil
}

// ValidateDeliveryMergeReport checks a report against the branch the server
// expects, before anything is written.
func ValidateDeliveryMergeReport(rep DeliveryMergeReport, expectBranch string) error {
	branch, err := normalizeDeliveryBranch(rep.Branch)
	if err != nil {
		return err
	}
	if branch != expectBranch {
		return fmt.Errorf("%w: merged into %s, but this sub-issue delivers onto %s", ErrDeliveryBranchInvalid, branch, expectBranch)
	}
	switch rep.Status {
	case DeliveryLineMerged:
		if !deliveryCommitSHA.MatchString(rep.Tip) {
			return fmt.Errorf("%w: merge report has no tip commit", ErrDeliveryBranchInvalid)
		}
		for _, c := range rep.Commits {
			if !deliveryCommitSHA.MatchString(c.SHA) {
				return fmt.Errorf("%w: %q is not a commit", ErrDeliveryBranchInvalid, c.SHA)
			}
		}
	case DeliveryLineConflict:
		if len(rep.ConflictFiles) == 0 {
			return fmt.Errorf("%w: a conflict report must name the conflicting files", ErrDeliveryBranchInvalid)
		}
	default:
		return fmt.Errorf("%w: merge status must be merged or conflict, got %q", ErrDeliveryBranchInvalid, rep.Status)
	}
	return nil
}

// RecordDeliveryMerge writes a validated report onto the sub-issue's line.
// A merge also makes the branch the owner's canonical line when it has none,
// so the owner's own run continues it and its PR is opened from it.
func RecordDeliveryMerge(ctx context.Context, q *db.Queries, line db.IssueDeliveryLine, rep DeliveryMergeReport) (db.IssueDeliveryLine, error) {
	source := pgtype.Text{String: strings.TrimSpace(rep.SourceBranch), Valid: strings.TrimSpace(rep.SourceBranch) != ""}
	branch := pgtype.Text{String: strings.TrimSpace(rep.Branch), Valid: true}
	if rep.Status == DeliveryLineConflict {
		files := make([]string, 0, len(rep.ConflictFiles))
		for _, f := range rep.ConflictFiles {
			if f = strings.TrimSpace(f); f != "" {
				files = append(files, f)
			}
		}
		return q.MarkIssueDeliveryLineConflict(ctx, db.MarkIssueDeliveryLineConflictParams{
			BranchName: branch, SourceBranch: source, ConflictFiles: files, IssueID: line.IssueID,
		})
	}
	commits := rep.Commits
	if len(commits) > maxDeliveryLineCommits {
		commits = commits[:maxDeliveryLineCommits]
	}
	if commits == nil {
		commits = []DeliveryLineCommit{}
	}
	encoded, err := json.Marshal(commits)
	if err != nil {
		return db.IssueDeliveryLine{}, err
	}
	row, err := q.MarkIssueDeliveryLineMerged(ctx, db.MarkIssueDeliveryLineMergedParams{
		BranchName: branch, SourceBranch: source,
		MergedTip: pgtype.Text{String: rep.Tip, Valid: true},
		Commits:   encoded, IssueID: line.IssueID,
	})
	if err != nil {
		return db.IssueDeliveryLine{}, err
	}
	if _, err := q.GetIssueCanonicalDeliveryBranch(ctx, line.OwnerIssueID); errors.Is(err, pgx.ErrNoRows) {
		owner, getErr := q.GetIssue(ctx, line.OwnerIssueID)
		if getErr != nil {
			return row, getErr
		}
		if _, setErr := SetIssueCanonicalDeliveryBranch(ctx, q, owner, branch.String); setErr != nil {
			return row, setErr
		}
	} else if err != nil {
		return row, err
	}
	return row, nil
}

// DeliveryMergeEvidence is the section appended to the close comment.
func DeliveryMergeEvidence(rep DeliveryMergeReport, ownerIdentifier string) string {
	var b strings.Builder
	if rep.Status == DeliveryLineConflict {
		fmt.Fprintf(&b, "**并回父票 %s 的分支 `%s` 时冲突**，平台没有自动解，这张子票落 blocked。冲突文件：\n\n", ownerIdentifier, rep.Branch)
		for _, f := range rep.ConflictFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n解法：在本票分支上 `git merge " + rep.Branch + "`，解掉冲突并提交，再 `multica issue close --outcome done`。")
		return b.String()
	}
	if len(rep.Commits) == 0 {
		fmt.Fprintf(&b, "**已在父票 %s 的分支 `%s` 上**：没有新提交需要并回。", ownerIdentifier, rep.Branch)
		return b.String()
	}
	fmt.Fprintf(&b, "**已提交到父票 %s 的分支 `%s`**（%d 个提交，分支现在在 `%s`）：\n\n", ownerIdentifier, rep.Branch, len(rep.Commits), shortSHA(rep.Tip))
	for i, c := range rep.Commits {
		if i == 20 {
			fmt.Fprintf(&b, "- ……另外 %d 个\n", len(rep.Commits)-20)
			break
		}
		fmt.Fprintf(&b, "- `%s` %s\n", shortSHA(c.SHA), c.Subject)
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortSHA(sha string) string {
	if len(sha) > 9 {
		return sha[:9]
	}
	return sha
}

func decodeDeliveryCommits(raw []byte) []DeliveryLineCommit {
	out := []DeliveryLineCommit{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = []DeliveryLineCommit{}
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// BuildIssueDeliveryLine is the sub-issue's view, or nil when it delivers on
// its own.
func BuildIssueDeliveryLine(ctx context.Context, q *db.Queries, issue db.Issue) (*IssueDeliveryLine, error) {
	line, err := GetIssueDeliveryLine(ctx, q, issue.ID)
	if err != nil || line == nil {
		return nil, err
	}
	owner, err := q.GetIssue(ctx, line.OwnerIssueID)
	if err != nil {
		return nil, err
	}
	identifier, err := issueIdentifierFor(ctx, q, owner)
	if err != nil {
		return nil, err
	}
	branch := line.BranchName.String
	if branch == "" {
		if branch, err = DeliveryLineTarget(ctx, q, owner, identifier); err != nil {
			return nil, err
		}
	}
	return &IssueDeliveryLine{
		OwnerIssueID:    util.UUIDToString(owner.ID),
		OwnerIdentifier: identifier,
		OwnerTitle:      owner.Title,
		Branch:          branch,
		Status:          line.Status,
		SourceBranch:    line.SourceBranch.String,
		MergedTip:       line.MergedTip.String,
		Commits:         decodeDeliveryCommits(line.Commits),
		ConflictFiles:   nonNilStrings(line.ConflictFiles),
		MergedAt:        tsPtr(line.MergedAt),
	}, nil
}

// BuildIssueDeliveryContributions lists the sub-issues delivering onto
// issue's line.
func BuildIssueDeliveryContributions(ctx context.Context, q *db.Queries, issue db.Issue) ([]IssueDeliveryContribution, error) {
	rows, err := q.ListIssueDeliveryLinesByOwner(ctx, issue.ID)
	if err != nil {
		return nil, err
	}
	out := make([]IssueDeliveryContribution, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ws, err := q.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out = append(out, IssueDeliveryContribution{
			IssueID:       util.UUIDToString(row.IssueID),
			Identifier:    IssueIdentifier(ws.IssuePrefix, row.IssueNumber),
			Title:         row.IssueTitle,
			IssueStatus:   row.IssueStatus,
			Status:        row.Status,
			Branch:        row.BranchName.String,
			Commits:       decodeDeliveryCommits(row.Commits),
			ConflictFiles: nonNilStrings(row.ConflictFiles),
			MergedAt:      tsPtr(row.MergedAt),
		})
	}
	return out, nil
}

// IssueDeliveryLineSummary is the compact form for issue listings: whose
// line a sub-issue delivers onto and how far it got.
type IssueDeliveryLineSummary struct {
	OwnerIssueID string `json:"owner_issue_id"`
	Status       string `json:"status"`
	Branch       string `json:"branch,omitempty"`
	CommitCount  int    `json:"commit_count"`
}

// DeliveryLineSummaries returns the summary for every id that has a line.
func DeliveryLineSummaries(ctx context.Context, q *db.Queries, ids []pgtype.UUID) (map[string]IssueDeliveryLineSummary, error) {
	out := map[string]IssueDeliveryLineSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListIssueDeliveryLinesForIssues(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[util.UUIDToString(row.IssueID)] = IssueDeliveryLineSummary{
			OwnerIssueID: util.UUIDToString(row.OwnerIssueID),
			Status:       row.Status,
			Branch:       row.BranchName.String,
			CommitCount:  len(decodeDeliveryCommits(row.Commits)),
		}
	}
	return out, nil
}

// contributionMergeBlocker says why the owner's PR must not merge while a
// sub-issue's commits are not on its line: a merge-back that conflicted.
// A cancelled sub-issue never blocks.
func contributionMergeBlocker(contributions []IssueDeliveryContribution) string {
	for _, c := range contributions {
		if c.IssueStatus == issuestatus.Cancelled {
			continue
		}
		if c.Status == DeliveryLineConflict {
			return fmt.Sprintf("子票 %s 并回本票分支时冲突（%s），先解掉再合", c.Identifier, strings.Join(c.ConflictFiles, ", "))
		}
	}
	return ""
}
