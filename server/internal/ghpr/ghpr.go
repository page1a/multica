// Package ghpr reads pull-request state through the local gh CLI. It is the
// App-free source for the PR mirror: the daemon reports after a run and
// `multica issue close` refreshes right before the done gate reads it.
package ghpr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// PR is the wire shape of /pull-requests/report entries.
type PR struct {
	Owner    string     `json:"owner"`
	Repo     string     `json:"repo"`
	Number   int32      `json:"number"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	URL      string     `json:"url"`
	Branch   string     `json:"branch"`
	SHA      string     `json:"sha"`
	IsDraft  bool       `json:"is_draft,omitempty"`
	MergedAt *time.Time `json:"merged_at,omitempty"`
}

type ghRow struct {
	Number      int32      `json:"number"`
	Title       string     `json:"title"`
	State       string     `json:"state"`
	URL         string     `json:"url"`
	HeadRefName string     `json:"headRefName"`
	HeadRefOid  string     `json:"headRefOid"`
	IsDraft     bool       `json:"isDraft"`
	MergedAt    *time.Time `json:"mergedAt"`
}

// List runs `gh pr list --state all` in dir with the extra filter args
// (e.g. "--head", branch or "--search", "DENE-1").
func List(ctx context.Context, dir string, filter ...string) ([]PR, error) {
	args := append([]string{"pr", "list", "--state", "all", "--limit", "20", "--json", "number,title,state,url,headRefName,headRefOid,isDraft,mergedAt"}, filter...)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var rows []ghRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(rows))
	for _, r := range rows {
		owner, repo, ok := ownerRepo(r.URL)
		if !ok {
			continue
		}
		prs = append(prs, PR{Owner: owner, Repo: repo, Number: r.Number, Title: r.Title, State: strings.ToLower(r.State),
			URL: r.URL, Branch: r.HeadRefName, SHA: r.HeadRefOid, IsDraft: r.IsDraft, MergedAt: r.MergedAt})
	}
	return prs, nil
}

// Merge squash-merges one PR by URL.
func Merge(ctx context.Context, dir, prURL string) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "merge", prURL, "--squash")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr merge %s: %w: %s", prURL, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ownerRepo reads owner/repo from https://github.com/<owner>/<repo>/pull/<n>.
// The PR URL is used instead of the checkout's remote because managed
// checkouts may point origin at a local cache.
func ownerRepo(prURL string) (string, string, bool) {
	u, err := url.Parse(prURL)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
