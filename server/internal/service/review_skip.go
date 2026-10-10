package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Review skip (DENE-1678): an in_review ticket whose PR is already merged and
// already reviewed does not wait for the acceptance seat. Only facts the
// server holds count, and each merged PR needs its own review of the very
// head that merged: a GitHub approval of that commit, or a `verdict: pass` the
// issue's acceptance seat wrote while that commit was the PR's head. Nobody
// else's pass counts, one PR's review never covers another, and a push after
// the review voids it.
const (
	ReviewSkipGitHubApprove   = "github_approve"
	ReviewSkipPlatformVerdict = "platform_verdict"
	// MetaKeyReviewSkip holds the reason the ticket skipped acceptance, so the
	// issue page and `issue get` say why no acceptance seat ran.
	MetaKeyReviewSkip = "review_skip"
)

// ReviewSkip is the fact that let a ticket skip acceptance. When several
// merged PRs each had their own review, it names the newest PR's.
type ReviewSkip struct {
	Kind  string
	By    string
	PRURL string
	At    time.Time
}

// Reason is the one sentence the timeline and the CLI show.
func (r ReviewSkip) Reason() string {
	by := strings.TrimSpace(r.By)
	if by == "" {
		by = "验收席"
	}
	if r.Kind == ReviewSkipGitHubApprove {
		return fmt.Sprintf("PR %s 已合入，%s 在 GitHub 上批准过", r.PRURL, by)
	}
	return fmt.Sprintf("PR %s 已合入，验收席 %s 在票上给过审查通过", r.PRURL, by)
}

// ReviewSkipPR is one linked PR as the skip reads it. Head is the commit the
// PR last pointed at (for a merged PR, what merged); ApprovedHead is the
// commit the GitHub approval was given on. OpenedAt only picks which PR the
// reason names.
type ReviewSkipPR struct {
	URL          string
	State        string
	Head         string
	OpenedAt     time.Time
	ApprovedBy   string
	ApprovedAt   time.Time
	ApprovedHead string
}

// ReviewSkipVerdict is one comment carrying a verdict line, newest first.
// Seat is true when its author was the issue's acceptance seat. Heads is
// the PR head each linked PR had when the comment was written, by PR URL;
// only a pass has them.
type ReviewSkipVerdict struct {
	AuthorID string
	Seat     bool
	Content  string
	At       time.Time
	Heads    map[string]string
}

// DecideReviewSkip is the rule. Every linked PR must be finished (no open or
// draft one) with at least one merged. Only the acceptance seat's verdicts
// count, and never one by an executor. The newest of them decides first: a
// hold means the seat said no. Then every merged PR needs its own review of
// the head that merged: a GitHub approval given on that commit, or a seat
// pass recorded while that commit was the PR's head. A head the server does
// not know never matches. by is filled with the verdict author's id; the
// caller swaps in a name.
func DecideReviewSkip(prs []ReviewSkipPR, verdicts []ReviewSkipVerdict, executors map[string]bool) (ReviewSkip, bool) {
	var merged []ReviewSkipPR
	for _, pr := range prs {
		switch strings.ToLower(pr.State) {
		case "open", "draft":
			return ReviewSkip{}, false
		case "merged":
			merged = append(merged, pr)
		}
	}
	if len(merged) == 0 {
		return ReviewSkip{}, false
	}
	var pass *ReviewSkipVerdict
	for i := range verdicts {
		v := verdicts[i]
		if !v.Seat || executors[v.AuthorID] {
			continue
		}
		switch blockwait.Verdict(v.Content) {
		case "hold":
			if pass == nil {
				return ReviewSkip{}, false
			}
		case "pass":
			if pass == nil {
				pass = &v
			}
		}
	}
	var skip ReviewSkip
	var skipOpened time.Time
	for _, pr := range merged {
		var this ReviewSkip
		switch {
		case strings.TrimSpace(pr.ApprovedBy) != "" && sameHead(pr.ApprovedHead, pr.Head):
			this = ReviewSkip{Kind: ReviewSkipGitHubApprove, By: pr.ApprovedBy, PRURL: pr.URL, At: pr.ApprovedAt}
		case pass != nil && sameHead(pass.Heads[prKey(pr.URL)], pr.Head):
			this = ReviewSkip{Kind: ReviewSkipPlatformVerdict, By: pass.AuthorID, PRURL: pr.URL, At: pass.At}
		default:
			return ReviewSkip{}, false
		}
		if skip.PRURL == "" || pr.OpenedAt.After(skipOpened) {
			skip, skipOpened = this, pr.OpenedAt
		}
	}
	return skip, true
}

// sameHead is true only when both commits are known and equal. Reports may
// carry an abbreviated sha, so a prefix of at least seven characters counts.
func sameHead(reviewed, merged string) bool {
	reviewed, merged = strings.ToLower(strings.TrimSpace(reviewed)), strings.ToLower(strings.TrimSpace(merged))
	if len(reviewed) < 7 || len(merged) < 7 {
		return false
	}
	return strings.HasPrefix(merged, reviewed) || strings.HasPrefix(reviewed, merged)
}

// prKey is how a pass's recorded heads are looked up by PR URL.
func prKey(url string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(url), "/"))
}

// RecordReviewPass keeps, for a comment that carries `verdict: pass`, the head
// of every PR linked to its issue: the version that pass reviewed. Any other
// comment is ignored. A failed write only means that pass never lets the
// ticket skip acceptance, so callers log and move on.
func RecordReviewPass(ctx context.Context, q *db.Queries, comment db.Comment) error {
	if !comment.IssueID.Valid || blockwait.Verdict(comment.Content) != "pass" {
		return nil
	}
	return q.RecordReviewPassHeads(ctx, db.RecordReviewPassHeadsParams{CommentID: comment.ID, IssueID: comment.IssueID})
}

// FindReviewSkip reads the issue's PRs and verdict comments and applies
// DecideReviewSkip. The acceptance seats are the issue's reviewer and the
// seats a reviewer relay recorded. The executors whose verdict never counts
// are the author of the evidence comment of the close that sent the ticket
// to review, plus any ids the caller names (the closing agent itself, before
// that close lands). Earlier closes are not kept, so an executor who neither
// made the latest close nor is named by the caller is only kept out by not
// being an acceptance seat.
func FindReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue, executors ...pgtype.UUID) (ReviewSkip, bool, error) {
	gh, err := q.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	vcs, err := q.ListVCSPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	prs := make([]ReviewSkipPR, 0, len(gh)+len(vcs))
	for _, pr := range gh {
		prs = append(prs, ReviewSkipPR{URL: pr.HtmlUrl, State: pr.State, Head: pr.HeadSha, OpenedAt: pr.PrCreatedAt.Time,
			ApprovedBy: pr.ApprovedBy.String, ApprovedAt: pr.ApprovedAt.Time, ApprovedHead: pr.ApprovedHeadSha.String})
	}
	for _, pr := range vcs {
		prs = append(prs, ReviewSkipPR{URL: pr.HtmlUrl, State: pr.State, Head: pr.HeadSha, OpenedAt: pr.PrCreatedAt.Time})
	}
	if len(prs) == 0 {
		return ReviewSkip{}, false, nil
	}
	rows, err := q.ListIssueVerdictComments(ctx, issue.ID)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	heads, err := reviewPassHeads(ctx, q, rows)
	if err != nil {
		return ReviewSkip{}, false, err
	}
	seats := acceptanceSeats(issue)
	verdicts := make([]ReviewSkipVerdict, 0, len(rows))
	for _, row := range rows {
		id := util.UUIDToString(row.AuthorID)
		verdicts = append(verdicts, ReviewSkipVerdict{AuthorID: id, Seat: seats[row.AuthorType+":"+id], Content: row.Content,
			At: row.CreatedAt.Time, Heads: heads[util.UUIDToString(row.ID)]})
	}
	excluded := map[string]bool{}
	for _, id := range executors {
		if id.Valid {
			excluded[util.UUIDToString(id)] = true
		}
	}
	if closer, ok := reviewCloser(ctx, q, issue); ok {
		excluded[closer] = true
	}
	skip, ok := DecideReviewSkip(prs, verdicts, excluded)
	if !ok {
		return ReviewSkip{}, false, nil
	}
	if skip.Kind == ReviewSkipPlatformVerdict {
		skip.By = reviewAuthorName(ctx, q, rows, skip.By)
	}
	return skip, true, nil
}

// reviewPassHeads reads the PR heads recorded for the pass comments, keyed by
// comment id, then by PR URL.
func reviewPassHeads(ctx context.Context, q *db.Queries, rows []db.ListIssueVerdictCommentsRow) (map[string]map[string]string, error) {
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		if blockwait.Verdict(row.Content) == "pass" {
			ids = append(ids, row.ID)
		}
	}
	heads := map[string]map[string]string{}
	if len(ids) == 0 {
		return heads, nil
	}
	recorded, err := q.ListReviewPassHeads(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range recorded {
		id := util.UUIDToString(r.CommentID)
		if heads[id] == nil {
			heads[id] = map[string]string{}
		}
		heads[id][prKey(r.PrUrl)] = r.HeadSha
	}
	return heads, nil
}

// acceptanceSeats is who has been this issue's acceptance seat, keyed
// "type:id": the reviewer now, and both sides of a recorded reviewer relay.
func acceptanceSeats(issue db.Issue) map[string]bool {
	seats := map[string]bool{}
	if issue.ReviewerType.Valid && issue.ReviewerID.Valid {
		seats[issue.ReviewerType.String+":"+util.UUIDToString(issue.ReviewerID)] = true
	}
	var meta struct {
		ReviewerRelay reviewerRelayMeta `json:"reviewer_relay"`
	}
	if len(issue.Metadata) > 0 && json.Unmarshal(issue.Metadata, &meta) == nil {
		for _, id := range []string{meta.ReviewerRelay.OriginalID, meta.ReviewerRelay.ReplacementID} {
			if id = strings.TrimSpace(id); id != "" {
				seats["agent:"+id] = true
			}
		}
	}
	return seats
}

// reviewCloser is the author of the last close's evidence comment.
func reviewCloser(ctx context.Context, q *db.Queries, issue db.Issue) (string, bool) {
	var meta map[string]any
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &meta) != nil {
		return "", false
	}
	raw, _ := meta[closeprotocol.KeyEvidenceCommentID].(string)
	id, err := util.ParseUUID(raw)
	if err != nil {
		return "", false
	}
	comment, err := q.GetComment(ctx, id)
	if err != nil || !comment.AuthorID.Valid {
		return "", false
	}
	return util.UUIDToString(comment.AuthorID), true
}

func reviewAuthorName(ctx context.Context, q *db.Queries, rows []db.ListIssueVerdictCommentsRow, authorID string) string {
	for _, row := range rows {
		if util.UUIDToString(row.AuthorID) != authorID {
			continue
		}
		switch row.AuthorType {
		case "agent":
			if agent, err := q.GetAgent(ctx, row.AuthorID); err == nil && strings.TrimSpace(agent.Name) != "" {
				return agent.Name
			}
		case "member":
			if user, err := q.GetUser(ctx, row.AuthorID); err == nil && strings.TrimSpace(user.Name) != "" {
				return user.Name
			}
		}
		break
	}
	return ""
}

// RecordReviewSkip keeps the skip on the issue for the page and the CLI.
// Metadata values the clients read are primitives, so the reason and the kind
// are two string keys rather than one object.
func RecordReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue, skip ReviewSkip) error {
	for key, value := range map[string]string{
		MetaKeyReviewSkip:           skip.Reason(),
		MetaKeyReviewSkip + ".kind": skip.Kind,
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			Key: key, Value: raw, ID: issue.ID, WorkspaceID: issue.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ClearReviewSkip drops an earlier skip once the ticket is back in review.
func ClearReviewSkip(ctx context.Context, q *db.Queries, issue db.Issue) {
	for _, key := range []string{MetaKeyReviewSkip, MetaKeyReviewSkip + ".kind"} {
		_, _ = q.DeleteIssueMetadataKey(ctx, db.DeleteIssueMetadataKeyParams{Key: key, ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	}
}
