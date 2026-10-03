package handler

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/delivery"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Delivery evidence answers one question for every source of a pull/merge
// request link — `issue close --pr`, a link inferred from the evidence or the
// timeline, and the issue's own delivery lookup: does this PR belong to this
// ticket, and how sure are we (DENE-1183). A linked open PR is what the done
// gate merges, so all three go through adoptPull and pullNamesIssue rather
// than each keeping its own rule.

// closeDeclare is a --pr link resolved before the close gate runs.
type closeDeclare struct {
	URL        string
	Verified   bool
	Unverified bool
}

// pullAdoption is how a link was proven to belong to the issue.
type pullAdoption int

const (
	// notAdopted: nothing ties the link to this issue.
	notAdopted pullAdoption = iota
	// adoptedLinked: the link is already on file for the issue.
	adoptedLinked
	// adoptedFetched: the provider returned the PR and it qualified (named
	// the issue, or the closer vouched for it with --pr); it is now linked.
	adoptedFetched
	// adoptedLookup: the issue's own delivery lookup found it.
	adoptedLookup
)

// adoptPull applies the adoption rules in order:
//
//  1. the link is already linked to the issue;
//  2. the provider confirms the PR, and — unless the closer vouched for it
//     with --pr (mustName == "") — its title or branch names the issue;
//  3. the issue's delivery lookup (memoised per request) found it.
//
// Rule 2 links the PR as a side effect, so the request's delivery view is
// invalidated when it fires.
func (h *Handler) adoptPull(ctx context.Context, issue db.Issue, ref delivery.Ref, mustName string) pullAdoption {
	if h.urlAlreadyLinked(ctx, issue.ID, ref.URL) {
		return adoptedLinked
	}
	if h.fetchAndLinkDeclared(ctx, issue, ref, mustName) {
		invalidateDelivery(ctx)
		return adoptedFetched
	}
	view, _ := h.ensureIssueDeliveries(ctx, issue)
	if deliveriesContainURL(view, ref.URL) {
		return adoptedLookup
	}
	return notAdopted
}

// resolveDeclaredPull checks a --pr link once. A link the provider confirms
// is stored and linked even when the title omits the issue key. A link that
// cannot be checked still returns, marked unverified, so the close proceeds.
func (h *Handler) resolveDeclaredPull(ctx context.Context, issue db.Issue, raw string) (closeDeclare, error) {
	ref, err := delivery.ParsePullURL(raw)
	if err != nil {
		return closeDeclare{}, err
	}
	d := closeDeclare{URL: ref.URL}
	if h.adoptPull(ctx, issue, ref, "") != notAdopted {
		d.Verified = true
	} else {
		d.Unverified = true
	}
	h.rememberDeclared(ctx, d)
	return d, nil
}

// resolveInferredPull adopts the first found link that provably belongs to
// this issue: already linked, found by the issue's own delivery lookup, or a
// PR whose title or branch names the issue identifier. Any other link — a
// related PR cited in review, an upstream reference — is ignored rather than
// linked, because a linked open PR is what the done gate merges.
func (h *Handler) resolveInferredPull(ctx context.Context, issue db.Issue, urls []string) (closeDeclare, bool) {
	if len(urls) == 0 {
		return closeDeclare{}, false
	}
	// The delivery lookup is asked at most once across all candidates.
	ctx = withDeliveryBag(ctx)
	ident := issueIdentifier(h.getIssuePrefix(ctx, issue.WorkspaceID), issue.Number)
	for _, raw := range urls {
		ref, err := delivery.ParsePullURL(raw)
		if err != nil {
			continue
		}
		if h.adoptPull(ctx, issue, ref, ident) != notAdopted {
			d := closeDeclare{URL: ref.URL, Verified: true}
			h.rememberDeclared(ctx, d)
			return d, true
		}
	}
	return closeDeclare{}, false
}

var pullURLToken = regexp.MustCompile(`https?://[^\s<>"']+`)

// findPullURLs collects pull/MR links from the submitted evidence, then from
// the recent issue timeline newest first. Agents commonly put the link in the
// initial review handoff and omit --pr on the later done close.
func (h *Handler) findPullURLs(ctx context.Context, issue db.Issue, evidence string) []string {
	texts := []string{evidence}
	if comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, Limit: 30,
	}); err == nil {
		for i := len(comments) - 1; i >= 0; i-- {
			texts = append(texts, comments[i].Content)
		}
	}
	var urls []string
	seen := map[string]bool{}
	for _, text := range texts {
		for _, raw := range pullURLToken.FindAllString(text, -1) {
			raw = strings.TrimRight(raw, ".,;:!?，。；：！？)]}>")
			if _, err := delivery.ParsePullURL(raw); err == nil && !seen[raw] {
				seen[raw] = true
				urls = append(urls, raw)
			}
		}
	}
	return urls
}

// pullNamesIssue reports whether a PR title or branch carries the issue
// identifier as a whole token: DENE-11 does not match DENE-1156. It is the
// one "names the issue" rule for inferred links and the delivery lookup.
func pullNamesIssue(pull delivery.Pull, ident string) bool {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return false
	}
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])` + regexp.QuoteMeta(ident) + `($|[^0-9])`)
	return re.MatchString(pull.Title) || re.MatchString(pull.Branch)
}

// fetchAndLinkDeclared reads the pull from its provider and links it to the
// issue. A non-empty mustName refuses to link a pull that does not name it.
func (h *Handler) fetchAndLinkDeclared(ctx context.Context, issue db.Issue, ref delivery.Ref, mustName string) bool {
	if !h.isVCSAvailable() || !h.isVCSConfigured() {
		return false
	}
	conns, err := h.Queries.ListVCSConnectionsByWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		return false
	}
	conn := matchConnection(conns, ref.Key, ref.Host)
	if conn == nil {
		return false
	}
	token, err := h.openVCSSecret(conn.AccessTokenEncrypted)
	if err != nil || token == "" || token == "local" {
		return false
	}
	pull, err := delivery.Fetch(ctx, h.deliveryClient(), ref.Provider, delivery.APIBase(ref.Provider, conn.InstanceUrl, ""), token, ref)
	if err != nil || pull.Number == 0 {
		return false
	}
	if mustName != "" && !pullNamesIssue(pull, mustName) {
		return false
	}
	if pull.URL == "" {
		pull.URL = ref.URL
	}
	if pull.Provider == "" {
		pull.Provider = ref.Provider
	}
	if err := h.persistDeliveryPull(ctx, issue, conn, pull); err != nil {
		slog.Warn("delivery: persist declared pull failed", "url", ref.URL, "error", err)
		return false
	}
	return true
}

func (h *Handler) urlAlreadyLinked(ctx context.Context, issueID pgtype.UUID, raw string) bool {
	gh, vcsRows, err := h.loadDeliveryRows(ctx, issueID)
	if err != nil {
		return false
	}
	return deliveriesContainURL(issueDeliveries{GitHub: gh, VCS: vcsRows}, raw)
}

func deliveriesContainURL(view issueDeliveries, raw string) bool {
	for _, row := range view.GitHub {
		if samePullURL(row.HtmlUrl, raw) {
			return true
		}
	}
	for _, row := range view.VCS {
		if samePullURL(row.HtmlUrl, raw) {
			return true
		}
	}
	return false
}
