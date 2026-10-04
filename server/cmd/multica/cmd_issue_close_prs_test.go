package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/ghpr"
)

// A pass verdict merges the open PR naming the issue, re-reads gh, and reports
// the merged state before the close — the App-free path of DENE-875.
func TestRefreshIssuePullRequestsMergesAndReports(t *testing.T) {
	const issueID = "55555555-5555-4555-8555-555555555555"
	merged := false
	mergedAt := time.Date(2026, 9, 26, 4, 0, 0, 0, time.UTC)
	origList, origMerge := ghListPRs, ghMergePR
	t.Cleanup(func() { ghListPRs, ghMergePR = origList, origMerge })
	ghListPRs = func(_ context.Context, _ string, filter ...string) ([]ghpr.PR, error) {
		if filter[0] != "--search" {
			return nil, nil
		}
		pr := ghpr.PR{Owner: "o", Repo: "r", Number: 7, Title: "DENE-875: gate", State: "open", URL: "https://github.com/o/r/pull/7", Branch: "agent/agent/dene-875"}
		unrelated := ghpr.PR{Owner: "o", Repo: "r", Number: 8, Title: "follow-up of DENE-8750", State: "open", URL: "https://github.com/o/r/pull/8"}
		if merged {
			pr.State, pr.MergedAt = "merged", &mergedAt
		}
		return []ghpr.PR{pr, unrelated}, nil
	}
	var mergedURLs []string
	ghMergePR = func(_ context.Context, _ string, url string) error {
		mergedURLs = append(mergedURLs, url)
		merged = true
		return nil
	}
	var reported []ghpr.PR
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/issues/"+issueID+"/pull-requests/report" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body struct {
			PullRequests []ghpr.PR `json:"pull_requests"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		reported = body.PullRequests
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := cli.NewAPIClient(srv.URL, "", "mat_test")
	refreshIssuePullRequests(context.Background(), client, issueID, "DENE-875", true, false)

	if len(mergedURLs) != 1 || mergedURLs[0] != "https://github.com/o/r/pull/7" {
		t.Fatalf("merged = %v, want only PR 7", mergedURLs)
	}
	if len(reported) != 1 || reported[0].State != "merged" || reported[0].MergedAt == nil {
		t.Fatalf("reported = %+v, want PR 7 merged", reported)
	}
}

// Without a verdict nothing is merged. UUID references are resolved to their
// issue key before gh is queried.
func TestRefreshIssuePullRequestsReadOnlyAndSkip(t *testing.T) {
	origList, origMerge := ghListPRs, ghMergePR
	t.Cleanup(func() { ghListPRs, ghMergePR = origList, origMerge })
	calls := 0
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) {
		calls++
		return []ghpr.PR{{Owner: "o", Repo: "r", Number: 7, Title: "DENE-1 x", State: "open", URL: "https://github.com/o/r/pull/7"}}, nil
	}
	ghMergePR = func(context.Context, string, string) error { t.Fatal("must not merge"); return nil }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "", "mat_test")

	refreshIssuePullRequests(context.Background(), client, "id", "55555555-5555-4555-8555-555555555555", true, false)
	if calls != 0 {
		t.Fatalf("unresolvable uuid should not query gh, calls = %d", calls)
	}
	refreshIssuePullRequests(context.Background(), client, "id", "DENE-1", false, false)
	if calls == 0 {
		t.Fatal("identifier reference should read gh")
	}
}

func TestRefreshIssuePullRequestsUUIDResolvesIssueKey(t *testing.T) {
	const uuid = "55555555-5555-4555-8555-555555555555"
	origList := ghListPRs
	t.Cleanup(func() { ghListPRs = origList })
	var filters []string
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) {
		filters = append(filters, "query")
		return []ghpr.PR{{Title: "DENE-904: fix", Branch: "agent/dene-904", State: "open", URL: "https://github.com/o/r/pull/9"}}, nil
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/" + uuid:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid, "identifier": "DENE-904"})
		case "/api/issues/id/pull-requests/report":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "", "mat_test")
	refreshIssuePullRequests(context.Background(), client, uuid, uuid, false, false)
	if len(filters) == 0 {
		t.Fatal("UUID reference should query gh after resolving the issue key")
	}
}

func TestRefreshIssuePullRequestsNoMatchReportsReason(t *testing.T) {
	origList := ghListPRs
	t.Cleanup(func() { ghListPRs = origList })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "", "mat_test")
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	refreshIssuePullRequests(context.Background(), client, "id", "DENE-904", false, false)
	_ = w.Close()
	os.Stderr = old
	body, _ := io.ReadAll(r)
	if !strings.Contains(string(body), "gh found no PR matching DENE-904") {
		t.Fatalf("stderr = %q, want no-match reason", body)
	}
}

// DENE-906: a plain --outcome done squash-merges only when gh says the open PR
// is clean and green and the linked row is daemon-sourced (or not linked yet).
// Dirty or red is reported as-is, which is what the close gate blocks on.
func TestOutcomeDoneMergesCleanDaemonSnapshot(t *testing.T) {
	const issueID = "55555555-5555-4555-8555-555555555555"
	const prURL = "https://github.com/o/r/pull/906"
	cases := []struct {
		name       string
		source     string
		mergeable  string
		rollup     string
		failed     []string
		wantMerge  bool
		wantAction string
		wantReason string
	}{
		{name: "clean green daemon", source: "daemon", mergeable: "clean", rollup: "success", wantMerge: true, wantAction: blockwait.ReleaseDone},
		{name: "clean green no linked row", source: "", mergeable: "clean", rollup: "", wantMerge: true, wantAction: blockwait.ReleaseDone},
		{name: "clean green app row", source: "github_app", mergeable: "clean", rollup: "success", wantMerge: false, wantAction: blockwait.ReleaseMerge},
		{name: "dirty", source: "daemon", mergeable: "dirty", rollup: "success", wantMerge: false, wantAction: blockwait.ReleaseHold, wantReason: "合并冲突"},
		{name: "red check", source: "daemon", mergeable: "clean", rollup: "failure", failed: []string{"backend"}, wantMerge: false, wantAction: blockwait.ReleaseHold, wantReason: "检查是红的"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged := false
			mergedAt := time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)
			origList, origMerge := ghListPRs, ghMergePR
			t.Cleanup(func() { ghListPRs, ghMergePR = origList, origMerge })
			ms, rollup := tc.mergeable, tc.rollup
			base := ghpr.PR{
				Owner: "o", Repo: "r", Number: 906, Title: "DENE-906: gate", State: "open",
				URL: prURL, Branch: "agent/agent/dene-906", SHA: "abc",
				MergeableState: &ms, ChecksRollup: &rollup, FailedCheckNames: tc.failed,
			}
			ghListPRs = func(_ context.Context, _ string, filter ...string) ([]ghpr.PR, error) {
				if len(filter) == 0 || filter[0] != "--search" {
					return nil, nil
				}
				pr := base
				if merged {
					pr.State, pr.MergedAt = "merged", &mergedAt
				}
				return []ghpr.PR{pr}, nil
			}
			merges := 0
			ghMergePR = func(_ context.Context, _ string, got string) error {
				merges++
				if got != prURL {
					t.Errorf("merge url = %s", got)
				}
				merged = true
				return nil
			}
			var reported []ghpr.PR
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/issues/" + issueID + "/pull-requests":
					prs := []any{}
					if tc.source != "" {
						prs = append(prs, map[string]any{"source": tc.source, "state": "open", "html_url": prURL})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"pull_requests": prs})
				case "/api/issues/" + issueID + "/pull-requests/report":
					var body struct {
						PullRequests []ghpr.PR `json:"pull_requests"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					reported = body.PullRequests
					w.WriteHeader(http.StatusAccepted)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			client := cli.NewAPIClient(srv.URL, "", "mat_test")
			refreshIssuePullRequests(context.Background(), client, issueID, "DENE-906", false, true)

			if tc.wantMerge && merges != 1 {
				t.Fatalf("merges = %d, want 1", merges)
			}
			if !tc.wantMerge && merges != 0 {
				t.Fatalf("merges = %d, want 0", merges)
			}
			if len(reported) != 1 {
				t.Fatalf("reported = %+v", reported)
			}
			got := reported[0]
			snap := blockwait.PRSnapshot{
				Number: int(got.Number), State: got.State, URL: got.URL,
				FailedChecks: got.FailedCheckNames, RunningChecks: got.ChecksRunning,
			}
			if got.MergeableState != nil {
				snap.Mergeable = *got.MergeableState
			}
			if got.ChecksRollup != nil {
				snap.Checks = *got.ChecksRollup
			}
			decision := blockwait.DecideClose([]blockwait.PRSnapshot{snap}, time.Now())
			if decision.Action != tc.wantAction {
				t.Fatalf("gate action = %s, want %s (%s)", decision.Action, tc.wantAction, decision.Record.WaitCondition)
			}
			if tc.wantReason != "" && !strings.Contains(decision.Record.WaitCondition, tc.wantReason) {
				t.Fatalf("wait condition = %q, want %q", decision.Record.WaitCondition, tc.wantReason)
			}
			if tc.wantReason == "检查是红的" && !strings.Contains(decision.Record.WaitCondition, "backend") {
				t.Fatalf("wait condition = %q, want the check name", decision.Record.WaitCondition)
			}
		})
	}
}

// A local GitLab connection must refresh the exact linked MR, even when the
// caller's checkout is another repository and the MR is already merged (so the
// default glab list would otherwise omit it).
func TestRefreshIssuePullRequestsUsesLinkedMergedGitLabMR(t *testing.T) {
	const issueID = "55555555-5555-4555-8555-555555555555"
	const mrURL = "http://gitlab.example/acme/game/-/merge_requests/490"
	mergedAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	origGH, origList, origView := ghListPRs, glabListMRs, glabViewMR
	t.Cleanup(func() { ghListPRs, glabListMRs, glabViewMR = origGH, origList, origView })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) {
		return nil, fmt.Errorf("gh unavailable")
	}
	glabListMRs = func(context.Context, string) ([]ghpr.PR, error) {
		t.Fatal("must query the linked MR directly before listing the checkout")
		return nil, nil
	}
	glabViewMR = func(_ context.Context, _ string, rawURL string) (ghpr.PR, error) {
		if rawURL != mrURL {
			t.Fatalf("view URL = %q, want %q", rawURL, mrURL)
		}
		return ghpr.PR{Provider: "gitlab", Owner: "acme", Repo: "game", Number: 490,
			Title: "Fix production issue", State: "merged", URL: mrURL, SHA: "27b88db00", MergedAt: &mergedAt}, nil
	}
	var reported []ghpr.PR
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/" + issueID + "/pull-requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"pull_requests": []map[string]any{{"provider": "gitlab", "html_url": mrURL, "state": "open"}}})
		case "/api/issues/" + issueID + "/pull-requests/report":
			var body struct {
				PullRequests []ghpr.PR `json:"pull_requests"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reported = body.PullRequests
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := cli.NewAPIClient(srv.URL, "", "mat_test")

	refreshIssuePullRequests(context.Background(), client, issueID, "DENE-1156", false, false)
	if len(reported) != 1 || reported[0].Provider != "gitlab" || reported[0].State != "merged" || reported[0].MergedAt == nil {
		t.Fatalf("reported = %+v, want merged GitLab MR", reported)
	}
}
