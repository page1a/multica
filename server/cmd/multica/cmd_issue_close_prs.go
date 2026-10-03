package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/delivery"
	"github.com/multica-ai/multica/server/internal/ghpr"
	"github.com/multica-ai/multica/server/internal/glabmr"
)

// Swappable for tests; production shells out to gh in the working directory.
var (
	ghListPRs   = ghpr.List
	ghMergePR   = ghpr.Merge
	glabListMRs = glabmr.List
	glabViewMR  = glabmr.View
)

var closeIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d+$`)

// refreshIssuePullRequests reports the issue's PR state from the caller's gh
// before the close request, so the done gate reads the real merge state on a
// workspace without a GitHub App (DENE-875). With merge set (a `--verdict pass`
// done), open non-draft PRs naming the issue are squash-merged first.
// mergeReady (a plain `--outcome done`) does the same only when every open
// non-draft PR's gh snapshot is clean and green and the linked rows are
// daemon-sourced — the gate cannot merge those itself (DENE-906). Failures
// only warn: the server gate stays fail-closed and blocks the close with a reason.
func refreshIssuePullRequests(ctx context.Context, client *cli.APIClient, issueID, ident string, merge, mergeReady bool) {
	refreshIssuePullRequestsWithURL(ctx, client, issueID, ident, "", merge, mergeReady)
}

func refreshIssuePullRequestsWithURL(ctx context.Context, client *cli.APIClient, issueID, ident, declaredURL string, merge, mergeReady bool) {
	ident, ok := resolvePRIdentifier(ctx, client, issueID, ident)
	if !ok {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	list := func() ([]ghpr.PR, error) {
		prs, err := ghListPRs(ctx, dir, "--search", ident+" in:title")
		if err != nil {
			return nil, err
		}
		if branch := currentGitBranch(dir); branch != "" && branch != "HEAD" {
			if more, err := ghListPRs(ctx, dir, "--head", branch); err == nil {
				prs = append(prs, more...)
			}
		}
		return namingIssue(prs, ident), nil
	}
	// Start with the normal GitHub lookup. Only when it cannot find a
	// delivery do we ask the server for an exact linked GitLab URL; this keeps
	// the refresh cheap for the common GitHub path.
	prs, err := list()
	if err != nil || len(prs) == 0 {
		linked := linkedPullURLs(ctx, client, issueID)
		if strings.TrimSpace(declaredURL) != "" {
			linked = append(linked, declaredURL)
		}
		seenURLs := map[string]bool{}
		for _, rawURL := range linked {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" || seenURLs[rawURL] || !isGitLabMRURL(rawURL) {
				continue
			}
			seenURLs[rawURL] = true
			if mr, viewErr := glabViewMR(ctx, dir, rawURL); viewErr == nil {
				prs = append(prs, mr)
			}
		}
		if len(prs) > 0 {
			err = nil
		} else if mrs, gerr := glabListMRs(ctx, dir); gerr == nil {
			if named := namingIssue(mrs, ident); len(named) > 0 {
				prs = named
				err = nil
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not read PR state with gh (%v); the close gate uses what the server already knows\n", err)
		return
	}
	if len(prs) == 0 {
		fmt.Fprintf(os.Stderr, "  ! gh found no PR matching %s in the title or current branch; check the PR title or branch contains the issue key\n", ident)
		return
	}
	if !merge && mergeReady && !reportsGitLab(prs) && openNonDraftReady(prs) && linkedPullsAreDaemonOnly(ctx, client, issueID) {
		merge = true
	}
	if merge {
		mergedAny := false
		for _, pr := range prs {
			if pr.Provider == "gitlab" || pr.Provider == "forgejo" || pr.Provider == "gitea" {
				continue
			}
			if pr.State != "open" || pr.IsDraft {
				continue
			}
			if err := ghMergePR(ctx, dir, pr.URL); err != nil {
				fmt.Fprintf(os.Stderr, "  ! %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "  - merged %s\n", pr.URL)
			mergedAny = true
		}
		if mergedAny {
			if again, err := list(); err == nil {
				prs = again
			}
		}
	}
	if err := client.PostJSON(ctx, "/api/issues/"+issueID+"/pull-requests/report", map[string]any{"pull_requests": prs}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not report PR state: %v\n", err)
	}
}

// resolvePRIdentifier turns a UUID issue reference into its human-facing key,
// which is what gh can search for. A failed lookup is reported explicitly so
// the caller never silently skips the App-free PR refresh.
func resolvePRIdentifier(ctx context.Context, client *cli.APIClient, issueID, ident string) (string, bool) {
	if closeIdentRe.MatchString(ident) {
		return ident, true
	}
	if !uuidRegexp.MatchString(ident) {
		fmt.Fprintf(os.Stderr, "  ! cannot refresh PR state: %q is neither an issue key nor a full UUID\n", ident)
		return "", false
	}
	var issue map[string]any
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID), &issue); err != nil {
		fmt.Fprintf(os.Stderr, "  ! cannot resolve issue UUID %s to an issue key for gh PR lookup: %v\n", ident, err)
		return "", false
	}
	key, _ := issue["identifier"].(string)
	key = strings.TrimSpace(key)
	if !closeIdentRe.MatchString(key) {
		fmt.Fprintf(os.Stderr, "  ! issue UUID %s did not return a usable issue key; cannot look up PRs with gh\n", ident)
		return "", false
	}
	return key, true
}

var pullURLToken = regexp.MustCompile(`https?://[^\s<>"']+`)

func pullURLFromText(text string) string {
	for _, raw := range pullURLToken.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:!?，。；：！？)]}>")
		if _, err := delivery.ParsePullURL(raw); err == nil {
			return raw
		}
	}
	return ""
}

func isGitLabMRURL(raw string) bool {
	ref, err := delivery.ParsePullURL(raw)
	return err == nil && ref.Provider == "gitlab"
}

type linkedPullURLResponse struct {
	PullRequests []struct {
		URL      string `json:"html_url"`
		Provider string `json:"provider"`
	} `json:"pull_requests"`
}

func linkedPullURLs(ctx context.Context, client *cli.APIClient, issueID string) []string {
	var body linkedPullURLResponse
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/pull-requests", &body); err != nil {
		return nil
	}
	urls := make([]string, 0, len(body.PullRequests))
	for _, pr := range body.PullRequests {
		if isGitLabMRURL(pr.URL) || strings.EqualFold(pr.Provider, "gitlab") {
			urls = append(urls, pr.URL)
		}
	}
	return urls
}

// namingIssue keeps PRs whose title or branch carries the identifier and
// drops duplicates from the two gh queries.
func namingIssue(prs []ghpr.PR, ident string) []ghpr.PR {
	re := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])` + regexp.QuoteMeta(ident) + `($|[^0-9])`)
	seen := map[string]bool{}
	out := prs[:0:0]
	for _, pr := range prs {
		if seen[pr.URL] || !(re.MatchString(pr.Title) || re.MatchString(pr.Branch)) {
			continue
		}
		seen[pr.URL] = true
		out = append(out, pr)
	}
	return out
}

// openNonDraftReady is true when there is at least one open non-draft PR and
// every one of them is clean with green (or absent) checks.
func openNonDraftReady(prs []ghpr.PR) bool {
	saw := false
	for _, pr := range prs {
		if pr.State != "open" || pr.IsDraft {
			continue
		}
		if !pr.ReadyToMerge() {
			return false
		}
		saw = true
	}
	return saw
}

// linkedPullsAreDaemonOnly reports whether every open linked PR was mirrored
// by gh rather than a GitHub App. No linked row yet counts as daemon-only:
// this close is about to create that row. A lookup failure does not merge.
func linkedPullsAreDaemonOnly(ctx context.Context, client *cli.APIClient, issueID string) bool {
	var body struct {
		PullRequests []struct {
			Source string `json:"source"`
			State  string `json:"state"`
		} `json:"pull_requests"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+url.PathEscape(issueID)+"/pull-requests", &body); err != nil {
		fmt.Fprintf(os.Stderr, "  ! could not read linked PR sources (%v); not merging from gh\n", err)
		return false
	}
	for _, pr := range body.PullRequests {
		if strings.EqualFold(pr.State, "open") && pr.Source != "daemon" {
			return false
		}
	}
	return true
}

func reportsGitLab(prs []ghpr.PR) bool {
	for _, pr := range prs {
		switch pr.Provider {
		case "gitlab", "forgejo", "gitea":
			return true
		}
	}
	return false
}

func currentGitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
