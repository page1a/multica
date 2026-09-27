package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Reading an issue by its link (DENE-897).
//
// `multica issue get <url>` and `multica issue comment list <url>` accept the
// web / desktop issue URL wherever they accept an issue key. When the link
// points into the token's own workspace it is just another spelling of the
// key. When it points into another workspace the server serves it through
// GET /api/links/issue (read-only, judged as the human who started this run)
// and every payload comes with a provenance block that names where it came
// from; the CLI prints that notice before the content so a reader — a person
// or a model — does not mistake another workspace's issue for local data.

// looksLikeIssueLink reports whether input is an issue URL rather than a key
// or a UUID: the "/issues/" path segment is the anchor, exactly as the server
// parses it.
func looksLikeIssueLink(input string) bool {
	if !strings.Contains(input, "/issues/") {
		return false
	}
	return strings.Contains(input, "://") || strings.HasPrefix(input, "/")
}

// linkProvenance mirrors handler.linkProvenance.
type linkProvenance struct {
	WorkspaceID     string `json:"workspace_id"`
	WorkspaceSlug   string `json:"workspace_slug"`
	WorkspaceName   string `json:"workspace_name"`
	IssueID         string `json:"issue_id"`
	IssueIdentifier string `json:"issue_identifier"`
	CrossWorkspace  bool   `json:"cross_workspace"`
	ReadAs          string `json:"read_as,omitempty"`
	ReadAt          string `json:"read_at"`
	Notice          string `json:"notice"`
}

// linkedIssue is GET /api/links/issue.
type linkedIssue struct {
	Issue      map[string]any   `json:"issue"`
	Runs       []map[string]any `json:"runs"`
	Provenance linkProvenance   `json:"provenance"`
}

func linkReadPath(route, link string, params url.Values) string {
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("url", link)
	return route + "?" + q.Encode()
}

// fetchLinkedIssue reads the issue a link points to.
func fetchLinkedIssue(ctx context.Context, client *cli.APIClient, link string) (linkedIssue, error) {
	var out linkedIssue
	if err := client.GetJSON(ctx, linkReadPath("/api/links/issue", link, nil), &out); err != nil {
		return linkedIssue{}, linkReadRequestError(err)
	}
	return out, nil
}

// linkReadRequestError turns a refused link read into what the user should
// read. FormatError collapses every 403 into generic "no access" copy so a
// refusal cannot confirm a resource exists; these refusals are about the
// CALLER's standing, not the resource, so they opt out through their stable
// server code — never the English sentence.
func linkReadRequestError(err error) error {
	switch cli.ServerErrorCode(err) {
	case "link_no_originator":
		return cli.WithUserMessage("this run has no person behind it who could read that workspace: a link into another workspace is read as the human who started the run directly, and this run was not started that way (or has already finished)", err)
	case "link_not_member":
		return cli.WithUserMessage("the person this run acts for is not a member of the workspace this link points to, so it cannot be read on their behalf", err)
	case "link_write_forbidden":
		return cli.WithUserMessage("an issue in another workspace is read-only through its link; changes to it must be made from inside that workspace", err)
	}
	return err
}

// printLinkNotice puts the provenance notice on stderr, before the content,
// on every cross-workspace read.
func printLinkNotice(p linkProvenance) {
	if !p.CrossWorkspace {
		return
	}
	fmt.Fprintf(os.Stderr, "Cross-workspace read: %s (%s) · %s\n", p.IssueIdentifier, p.WorkspaceSlug, p.Notice)
}

// resolveIssueReadTarget is resolveIssueRef for the READ commands: a link into
// another workspace is not refused but returned as a linked read, so the
// command can switch to the /api/links routes. Every other input resolves the
// usual way.
func resolveIssueReadTarget(ctx context.Context, client *cli.APIClient, input string) (ref resolvedID, linked *linkedIssue, err error) {
	trimmed := strings.TrimSpace(input)
	if !looksLikeIssueLink(trimmed) {
		ref, err = resolveIssueRef(ctx, client, trimmed)
		return ref, nil, err
	}
	li, err := fetchLinkedIssue(ctx, client, trimmed)
	if err != nil {
		return resolvedID{}, nil, err
	}
	ref = resolvedID{ID: li.Provenance.IssueID, Display: li.Provenance.IssueIdentifier}
	if ref.Display == "" {
		ref.Display = ref.ID
	}
	if !li.Provenance.CrossWorkspace {
		return ref, nil, nil
	}
	return ref, &li, nil
}
