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
	"sort"
	"strings"
	"time"
)

// PR is the wire shape of /pull-requests/report entries.
//
// MergeableState, ChecksRollup, FailedCheckNames and ChecksRunning are the
// close gate's snapshot (DENE-906). Nil means this report did not read them
// (an older gh, or a parse failure) and the server keeps the previous values.
// A non-nil empty ChecksRollup means gh reported no checks.
type PR struct {
	// Provider is empty for a GitHub pull request. "gitlab" is a glab report.
	Provider         string     `json:"provider,omitempty"`
	Owner            string     `json:"owner"`
	Repo             string     `json:"repo"`
	Number           int32      `json:"number"`
	Title            string     `json:"title"`
	State            string     `json:"state"`
	URL              string     `json:"url"`
	Branch           string     `json:"branch"`
	SHA              string     `json:"sha"`
	IsDraft          bool       `json:"is_draft,omitempty"`
	MergedAt         *time.Time `json:"merged_at,omitempty"`
	MergeableState   *string    `json:"mergeable_state,omitempty"`
	ChecksRollup     *string    `json:"checks_rollup,omitempty"`
	FailedCheckNames []string   `json:"failed_check_names,omitempty"`
	ChecksRunning    int        `json:"checks_running,omitempty"`
	// ApprovedBy is the login of a current approving review (DENE-1678). Nil
	// means the report did not read reviews; "" means it did and found none.
	ApprovedBy *string    `json:"approved_by,omitempty"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
	// ApprovedHead is the commit that approval was given on.
	ApprovedHead string `json:"approved_head_sha,omitempty"`
}

// ReadyToMerge reports whether the gh snapshot is one the close gate would
// merge: merge state clean or has_hooks, and checks green or absent.
func (p PR) ReadyToMerge() bool {
	if p.MergeableState == nil || p.ChecksRollup == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*p.MergeableState)) {
	case "clean", "has_hooks":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(*p.ChecksRollup)) {
	case "", "success", "passing", "neutral", "skipped":
		return true
	default:
		return false
	}
}

type ghRow struct {
	Number            int32           `json:"number"`
	Title             string          `json:"title"`
	State             string          `json:"state"`
	URL               string          `json:"url"`
	HeadRefName       string          `json:"headRefName"`
	HeadRefOid        string          `json:"headRefOid"`
	IsDraft           bool            `json:"isDraft"`
	MergedAt          *time.Time      `json:"mergedAt"`
	Mergeable         string          `json:"mergeable"`
	MergeStateStatus  string          `json:"mergeStateStatus"`
	StatusCheckRollup json.RawMessage `json:"statusCheckRollup"`
	LatestReviews     json.RawMessage `json:"latestReviews"`
}

type ghReview struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	State       string     `json:"state"`
	SubmittedAt *time.Time `json:"submittedAt"`
	Commit      struct {
		Oid string `json:"oid"`
	} `json:"commit"`
}

// ghJSONFields is what `gh pr list --json` returns for the close gate.
// statusCheckRollup is gh's flattened export: a JSON array of CheckRun /
// StatusContext objects, or null when the commit has no checks. The first
// 100 contexts are all gh itself asks for.
const ghJSONFields = "number,title,state,url,headRefName,headRefOid,isDraft,mergedAt,mergeable,mergeStateStatus,statusCheckRollup,latestReviews"

// List runs `gh pr list --state all` in dir with the extra filter args
// (e.g. "--head", branch or "--search", "DENE-1").
func List(ctx context.Context, dir string, filter ...string) ([]PR, error) {
	args := append([]string{"pr", "list", "--state", "all", "--limit", "20", "--json", ghJSONFields}, filter...)
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
		pr := PR{Owner: owner, Repo: repo, Number: r.Number, Title: r.Title, State: strings.ToLower(r.State),
			URL: r.URL, Branch: r.HeadRefName, SHA: r.HeadRefOid, IsDraft: r.IsDraft, MergedAt: r.MergedAt}
		applySnapshot(&pr, r)
		applyApproval(&pr, r.LatestReviews)
		prs = append(prs, pr)
	}
	return prs, nil
}

// applySnapshot maps gh's mergeStateStatus and statusCheckRollup onto the
// report fields the close gate reads.
func applySnapshot(pr *PR, row ghRow) {
	state := strings.ToLower(strings.TrimSpace(row.MergeStateStatus))
	pr.MergeableState = &state
	rollup, failed, running, ok := summarizeChecks(row.StatusCheckRollup)
	if !ok {
		return
	}
	pr.ChecksRollup = &rollup
	pr.FailedCheckNames = failed
	pr.ChecksRunning = running
}

// applyApproval reads gh's latestReviews (each reviewer's newest review) and
// keeps the first APPROVED one. A dismissed approval is no longer APPROVED.
func applyApproval(pr *PR, raw json.RawMessage) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return
	}
	var reviews []ghReview
	if trimmed != "null" && json.Unmarshal(raw, &reviews) != nil {
		return
	}
	by := ""
	for _, r := range reviews {
		if strings.EqualFold(r.State, "approved") && r.Author.Login != "" {
			by = r.Author.Login
			pr.ApprovedAt = r.SubmittedAt
			pr.ApprovedHead = r.Commit.Oid
			break
		}
	}
	pr.ApprovedBy = &by
}

type ghCheckNode struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// summarizeChecks reduces gh's statusCheckRollup export to the gate's three
// fields. ok is false only when the payload is not null and not an array.
// A null or empty rollup is "no checks": rollup "", which the gate treats as
// green when the merge state is clean.
func summarizeChecks(raw json.RawMessage) (rollup string, failed []string, running int, ok bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil, 0, true
	}
	var nodes []ghCheckNode
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return "", nil, 0, false
	}
	chosen := map[string]checkView{}
	var order []string
	for _, node := range nodes {
		view, good := node.view()
		if !good {
			continue
		}
		prev, seen := chosen[view.name]
		if !seen {
			order = append(order, view.name)
			chosen[view.name] = view
			continue
		}
		chosen[view.name] = preferCheck(prev, view)
	}
	worst := ""
	for _, name := range order {
		view := chosen[name]
		switch {
		case view.conclusion == "error" || isFailureConclusion(view.conclusion):
			failed = append(failed, name)
		case view.status != "completed" || view.conclusion == "":
			running++
		}
		worst = worseRollup(worst, view.rollup())
	}
	if len(order) == 0 {
		return "", nil, 0, true
	}
	sort.Strings(failed)
	return worst, failed, running, true
}

type checkView struct {
	name, status, conclusion string
}

func (n ghCheckNode) view() (checkView, bool) {
	kind := n.TypeName
	if kind == "" {
		if n.Name != "" {
			kind = "CheckRun"
		} else if n.Context != "" {
			kind = "StatusContext"
		}
	}
	switch kind {
	case "CheckRun":
		if n.Name == "" {
			return checkView{}, false
		}
		return checkView{name: n.Name, status: normalizeRunStatus(n.Status), conclusion: strings.ToLower(n.Conclusion)}, true
	case "StatusContext":
		if n.Context == "" {
			return checkView{}, false
		}
		status, conclusion := normalizeStatusState(n.State)
		return checkView{name: n.Context, status: status, conclusion: conclusion}, true
	default:
		return checkView{}, false
	}
}

func (v checkView) rollup() string {
	switch {
	case v.conclusion == "error":
		return "error"
	case isFailureConclusion(v.conclusion):
		return "failure"
	case v.conclusion == "cancelled":
		return "cancelled"
	case v.status != "completed" || v.conclusion == "":
		return "pending"
	default:
		return "success"
	}
}

// preferCheck keeps one row per check name. A cancelled run beside a newer
// conclusion is the cancel-in-progress pattern and must not paint the check red.
func preferCheck(a, b checkView) checkView {
	if a.conclusion == "cancelled" && b.conclusion != "cancelled" {
		return b
	}
	if b.conclusion == "cancelled" && a.conclusion != "cancelled" {
		return a
	}
	if rollupRank(a.rollup()) >= rollupRank(b.rollup()) {
		return a
	}
	return b
}

func rollupRank(rollup string) int {
	switch rollup {
	case "error":
		return 5
	case "failure":
		return 4
	case "cancelled":
		return 3
	case "pending":
		return 2
	case "success":
		return 1
	default:
		return 0
	}
}

func worseRollup(a, b string) string {
	if rollupRank(b) > rollupRank(a) {
		return b
	}
	return a
}

func isFailureConclusion(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "action_required", "startup_failure", "stale":
		return true
	default:
		return false
	}
}

func normalizeRunStatus(s string) string {
	switch strings.ToUpper(s) {
	case "COMPLETED":
		return "completed"
	case "IN_PROGRESS":
		return "in_progress"
	default:
		if s == "" {
			return "queued"
		}
		return "queued"
	}
}

func normalizeStatusState(s string) (status, conclusion string) {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return "completed", "success"
	case "FAILURE":
		return "completed", "failure"
	case "ERROR":
		return "completed", "error"
	case "PENDING":
		return "in_progress", ""
	default:
		return "queued", ""
	}
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
