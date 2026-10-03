package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/delivery"
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
	// Provider is empty for GitHub. gitlab/forgejo/gitea land in vcs_pull_request.
	Provider string     `json:"provider,omitempty"`
	Owner    string     `json:"owner"`
	Repo     string     `json:"repo"`
	Number   int32      `json:"number"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	URL      string     `json:"url"`
	Branch   string     `json:"branch"`
	SHA      string     `json:"sha"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
	// Snapshot fields from the caller's gh (DENE-906). Nil means the reporter
	// did not read them; an empty checks rollup means there are no checks.
	MergeableState   *string  `json:"mergeable_state,omitempty"`
	ChecksRollup     *string  `json:"checks_rollup,omitempty"`
	FailedCheckNames []string `json:"failed_check_names,omitempty"`
	ChecksRunning    int      `json:"checks_running,omitempty"`
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
		if provider := strings.ToLower(strings.TrimSpace(p.Provider)); provider != "" && provider != "github" {
			if err := h.persistReportedVCSPull(ctx, ws, p, idents, prefix); err != nil {
				return err
			}
			continue
		}
		state := strings.ToLower(p.State)
		switch state {
		case "", "opened":
			state = "open"
		}
		now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
		tx, err := h.TxStarter.Begin(ctx)
		if err != nil {
			return err
		}
		qtx := h.Queries.WithTx(tx)
		row, err := qtx.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
			WorkspaceID: ws, RepoOwner: p.Owner, RepoName: p.Repo, PrNumber: p.Number, Title: p.Title,
			State: state, HtmlUrl: p.URL, Branch: pgtype.Text{String: p.Branch, Valid: p.Branch != ""}, HeadSha: p.SHA,
			PrCreatedAt: now, PrUpdatedAt: now, MergedAt: timestamptzPtr(p.MergedAt),
			MergeableState: reportedMergeable(p.MergeableState),
			Source:         pgtype.Text{String: "daemon", Valid: true},
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := writeReportedCheckSnapshot(ctx, qtx, row.ID, p); err != nil {
			_ = tx.Rollback(ctx)
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
			issue, err := qtx.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)})
			if err != nil {
				continue
			}
			_, _ = qtx.LinkIssueToPullRequest(ctx, db.LinkIssueToPullRequestParams{IssueID: issue.ID, PullRequestID: row.ID})
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func reportedMergeable(state *string) pgtype.Text {
	if state == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.ToLower(strings.TrimSpace(*state)), Valid: true}
}

// writeReportedCheckSnapshot stores the gh check rollup where the close gate
// reads it: checks_rollup_state on the PR row, and one check-run row per
// failed or still-running check so the aggregate query fills
// failed_check_names and checks_running. A nil rollup leaves the previous
// snapshot alone. An empty head SHA cannot be joined back to the rollup, so
// the per-check rows are skipped.
func writeReportedCheckSnapshot(ctx context.Context, q *db.Queries, prID pgtype.UUID, p DaemonPullRequest) error {
	if p.ChecksRollup == nil || strings.TrimSpace(p.SHA) == "" {
		return nil
	}
	rollup := strings.ToLower(strings.TrimSpace(*p.ChecksRollup))
	var rollupText pgtype.Text
	if rollup != "" {
		rollupText = pgtype.Text{String: rollup, Valid: true}
	}
	state := ""
	if p.MergeableState != nil {
		state = strings.ToLower(strings.TrimSpace(*p.MergeableState))
	}
	n, err := q.UpdateGitHubPRSnapshot(ctx, db.UpdateGitHubPRSnapshotParams{
		ApiMergeable:        reportedAPIMergeable(state),
		ApiMergeStateStatus: reportedAPIState(state),
		ChecksRollupState:   rollupText,
		HeadSha:             p.SHA,
		FetchedAt:           pgtype.Timestamptz{Time: time.Now(), Valid: true},
		PrID:                prID,
	})
	if err != nil || n == 0 {
		return err
	}
	if err := q.DeleteGitHubPRCheckRuns(ctx, prID); err != nil {
		return err
	}
	ordinal := int32(0)
	for _, name := range p.FailedCheckNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := q.InsertGitHubPRCheckRun(ctx, db.InsertGitHubPRCheckRunParams{
			PrID: prID, HeadSha: p.SHA, Ordinal: ordinal, Name: name, Status: "completed",
			Conclusion: pgtype.Text{String: "failure", Valid: true},
		}); err != nil {
			return err
		}
		ordinal++
	}
	running := p.ChecksRunning
	if running > 100 {
		running = 100
	}
	for i := 0; i < running; i++ {
		if err := q.InsertGitHubPRCheckRun(ctx, db.InsertGitHubPRCheckRunParams{
			PrID: prID, HeadSha: p.SHA, Ordinal: ordinal, Name: fmt.Sprintf("running-%d", i+1), Status: "in_progress",
		}); err != nil {
			return err
		}
		ordinal++
	}
	return nil
}

func reportedAPIMergeable(state string) pgtype.Text {
	switch state {
	case "":
		return pgtype.Text{}
	case "dirty":
		return pgtype.Text{String: "CONFLICTING", Valid: true}
	case "unknown":
		return pgtype.Text{String: "UNKNOWN", Valid: true}
	default:
		return pgtype.Text{String: "MERGEABLE", Valid: true}
	}
}

func reportedAPIState(state string) pgtype.Text {
	if state == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.ToUpper(state), Valid: true}
}

// persistReportedVCSPull stores a glab (or other local) report. A real token
// connection for that repository wins; otherwise a synthetic cli:// row keeps
// connection_id satisfied so the MR stays queryable.
func (h *Handler) persistReportedVCSPull(ctx context.Context, ws pgtype.UUID, p DaemonPullRequest, idents []string, prefix string) error {
	ref, err := delivery.ParsePullURL(p.URL)
	provider := strings.ToLower(strings.TrimSpace(p.Provider))
	if err == nil && provider == "" {
		provider = ref.Provider
	}
	if provider == "" || provider == "github" {
		return nil
	}
	if err != nil {
		ref = delivery.Ref{Provider: provider, Owner: p.Owner, Repo: p.Repo, Number: p.Number, URL: p.URL}
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	qtx := h.Queries.WithTx(tx)
	conn, err := h.reportConnection(ctx, qtx, ws, provider, ref)
	if err != nil {
		_ = tx.Rollback(ctx)
		if !h.isVCSConfigured() {
			slog.Warn("vcs report skipped: encryption key unset", "url", p.URL)
			return nil
		}
		return err
	}
	state := strings.ToLower(p.State)
	switch state {
	case "", "opened":
		state = "open"
	}
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	row, err := qtx.UpsertVCSPullRequest(ctx, db.UpsertVCSPullRequestParams{
		WorkspaceID: ws, ConnectionID: conn.ID, Provider: provider,
		RepoOwner: nonempty(ref.Owner, p.Owner), RepoName: nonempty(ref.Repo, p.Repo), PrNumber: p.Number,
		Title: p.Title, State: state, HtmlUrl: nonempty(p.URL, ref.URL),
		PrCreatedAt: now, PrUpdatedAt: now, HeadSha: p.SHA,
		Branch:   pgtype.Text{String: p.Branch, Valid: p.Branch != ""},
		MergedAt: timestamptzPtr(p.MergedAt),
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if p.MergeableState != nil || p.ChecksRollup != nil {
		mergeable, checks := "", ""
		if p.MergeableState != nil {
			mergeable = strings.ToLower(strings.TrimSpace(*p.MergeableState))
		}
		if p.ChecksRollup != nil {
			checks = gateChecks(*p.ChecksRollup)
		}
		if err := qtx.UpdateVCSPullRequestGate(ctx, db.UpdateVCSPullRequestGateParams{
			MergeableState: mergeable, ChecksRollupState: checks, ID: row.ID,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
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
		issue, err := qtx.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: ws, Number: int32(n)})
		if err != nil {
			continue
		}
		_, _ = qtx.LinkIssueToVCSPullRequest(ctx, db.LinkIssueToVCSPullRequestParams{
			IssueID: issue.ID, PullRequestID: row.ID,
		})
	}
	return tx.Commit(ctx)
}

func (h *Handler) reportConnection(ctx context.Context, q *db.Queries, ws pgtype.UUID, provider string, ref delivery.Ref) (db.VcsConnection, error) {
	conns, err := q.ListVCSConnectionsByWorkspace(ctx, ws)
	if err != nil {
		return db.VcsConnection{}, err
	}
	if conn := matchConnection(conns, ref.Key, ref.Host); conn != nil {
		return *conn, nil
	}
	instance := "cli://" + provider
	for _, conn := range conns {
		if conn.InstanceUrl == instance && conn.RepoUrl == "" {
			return conn, nil
		}
	}
	if !h.isVCSConfigured() {
		return db.VcsConnection{}, fmt.Errorf("vcs encryption key unset")
	}
	token, err := h.sealVCSSecret("local")
	if err != nil {
		return db.VcsConnection{}, err
	}
	secret, err := h.sealVCSSecret("local")
	if err != nil {
		return db.VcsConnection{}, err
	}
	return q.UpsertVCSConnection(ctx, db.UpsertVCSConnectionParams{
		WorkspaceID: ws, Provider: provider, InstanceUrl: instance, RepoUrl: "",
		AccountLogin: "local-cli", AccessTokenEncrypted: token, WebhookSecretEncrypted: secret,
	})
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
