package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	refreshIssuePullRequests(context.Background(), client, issueID, "DENE-875", true)

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

	refreshIssuePullRequests(context.Background(), client, "id", "55555555-5555-4555-8555-555555555555", true)
	if calls != 0 {
		t.Fatalf("unresolvable uuid should not query gh, calls = %d", calls)
	}
	refreshIssuePullRequests(context.Background(), client, "id", "DENE-1", false)
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
	refreshIssuePullRequests(context.Background(), client, uuid, uuid, false)
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
	refreshIssuePullRequests(context.Background(), client, "id", "DENE-904", false)
	_ = w.Close()
	os.Stderr = old
	body, _ := io.ReadAll(r)
	if !strings.Contains(string(body), "gh found no PR matching DENE-904") {
		t.Fatalf("stderr = %q, want no-match reason", body)
	}
}
