// Package glabmr reads merge requests through the local glab CLI. It is the
// GitLab counterpart of ghpr: the daemon and `issue close` report what the
// caller's own credentials can see when the server has no token.
package glabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/delivery"
	"github.com/multica-ai/multica/server/internal/ghpr"
)

// List runs `glab mr list --all` in dir. A missing glab, or a directory that is not
// a GitLab checkout, returns an error and no rows; callers keep the gh result.
func List(ctx context.Context, dir string) ([]ghpr.PR, error) {
	return run(ctx, dir, []string{"mr", "list", "--all", "--output", "json", "-P", "20"})
}

// View reads one merge request by its URL. glab still resolves a bare URL
// against the current checkout, so split the URL and pass an explicit project
// with -R; the caller's directory may belong to another repository entirely.
func View(ctx context.Context, dir, rawURL string) (ghpr.PR, error) {
	ref, err := delivery.ParsePullURL(rawURL)
	if err != nil || ref.Provider != "gitlab" {
		if err == nil {
			err = fmt.Errorf("URL is not a GitLab merge request")
		}
		return ghpr.PR{}, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ghpr.PR{}, err
	}
	projectURL := parsed.Scheme + "://" + parsed.Host + "/" + strings.Trim(ref.ProjectPath(), "/")
	rows, err := run(ctx, dir, []string{"mr", "view", strconv.Itoa(int(ref.Number)), "-R", projectURL, "--output", "json"})
	if err != nil {
		return ghpr.PR{}, err
	}
	if len(rows) != 1 {
		return ghpr.PR{}, fmt.Errorf("glab mr view returned %d merge requests", len(rows))
	}
	return rows[0], nil
}

func run(ctx context.Context, dir string, args []string) ([]ghpr.PR, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "glab", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var rows []glabMR
	if err := json.Unmarshal(out, &rows); err != nil {
		// `glab mr view` returns one object rather than a JSON array.
		var row glabMR
		if err := json.Unmarshal(out, &row); err != nil {
			return nil, err
		}
		rows = []glabMR{row}
	}
	prs := make([]ghpr.PR, 0, len(rows))
	for _, row := range rows {
		if row.IID == 0 || row.WebURL == "" {
			continue
		}
		ref, err := delivery.ParsePullURL(row.WebURL)
		if err != nil {
			continue
		}
		state := strings.ToLower(row.State)
		switch state {
		case "opened", "":
			state = "open"
		case "merged":
			state = "merged"
		case "closed":
			state = "closed"
		}
		if row.Draft && state == "open" {
			state = "draft"
		}
		prs = append(prs, ghpr.PR{
			Provider: "gitlab",
			Owner:    ref.Owner,
			Repo:     ref.Repo,
			Number:   row.IID,
			Title:    row.Title,
			State:    state,
			URL:      row.WebURL,
			Branch:   row.SourceBranch,
			SHA:      row.SHA,
			IsDraft:  row.Draft,
			MergedAt: row.MergedAt,
		})
	}
	return prs, nil
}

type glabMR struct {
	IID          int32      `json:"iid"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	WebURL       string     `json:"web_url"`
	SourceBranch string     `json:"source_branch"`
	SHA          string     `json:"sha"`
	Draft        bool       `json:"draft"`
	MergedAt     *time.Time `json:"merged_at"`
}
