package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DaemonPullRequestReport is the App-free PR mirror contract. The reporter
// (daemon after a run, CLI before a close) owns discovery because gh is local;
// the server only persists the snapshot and links it by identifier.
type DaemonPullRequestReport struct {
	WorkspaceID  string              `json:"workspace_id"`
	PullRequests []DaemonPullRequest `json:"pull_requests"`
}
type DaemonPullRequest struct {
	Owner    string     `json:"owner"`
	Repo     string     `json:"repo"`
	Number   int32      `json:"number"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	URL      string     `json:"url"`
	Branch   string     `json:"branch"`
	SHA      string     `json:"sha"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

func (h *Handler) ReportDaemonPullRequests(w http.ResponseWriter, r *http.Request) {
	var req DaemonPullRequestReport
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "invalid report")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, req.WorkspaceID) {
		return
	}
	if err := h.persistReportedPullRequests(r.Context(), parseUUID(req.WorkspaceID), req.PullRequests, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "persist PR failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// ReportIssuePullRequests is the same mirror scoped to one issue: `multica
// issue close` refreshes the PR state from the caller's gh right before the
// done gate reads it, so a reviewer's `gh pr merge` is visible without an App.
// Only PRs whose title or branch names this issue are linked to it.
func (h *Handler) ReportIssuePullRequests(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req DaemonPullRequestReport
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeError(w, http.StatusBadRequest, "invalid report")
		return
	}
	ident := issueIdentifier(h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number)
	if err := h.persistReportedPullRequests(r.Context(), issue.WorkspaceID, req.PullRequests, ident); err != nil {
		writeError(w, http.StatusInternalServerError, "persist PR failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// persistReportedPullRequests upserts the reported PRs (source=daemon) and
// links each to the issues its title/branch names. onlyIdent, when set,
// skips PRs that do not name that identifier at all.
func (h *Handler) persistReportedPullRequests(ctx context.Context, ws pgtype.UUID, prs []DaemonPullRequest, onlyIdent string) error {
	prefix := h.getIssuePrefix(ctx, ws)
	for _, p := range prs {
		if p.Owner == "" || p.Repo == "" || p.Number == 0 {
			continue
		}
		idents := extractIdentifiers(p.Title, p.Branch)
		if onlyIdent != "" && !containsFold(idents, onlyIdent) {
			continue
		}
		state := strings.ToLower(p.State)
		if state == "" {
			state = "open"
		}
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		row, err := h.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
			WorkspaceID: ws, RepoOwner: p.Owner, RepoName: p.Repo, PrNumber: p.Number, Title: p.Title,
			State: state, HtmlUrl: p.URL, Branch: pgtype.Text{String: p.Branch, Valid: p.Branch != ""}, HeadSha: p.SHA,
			PrCreatedAt: now, PrUpdatedAt: now, MergedAt: timestamptzPtr(p.MergedAt), Source: pgtype.Text{String: "daemon", Valid: true},
		})
		if err != nil {
			return err
		}
		for _, ident := range idents {
			pfx, num, ok := strings.Cut(ident, "-")
			if !ok || !strings.EqualFold(pfx, prefix) {
				continue
			}
			n, err := strconv.Atoi(num)
			if err != nil {
				continue
			}
			issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)})
			if err != nil {
				continue
			}
			_ = h.Queries.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{IssueID: issue.ID, PullRequestID: row.ID, CloseIntent: true})
		}
	}
	return nil
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

func timestamptzPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
