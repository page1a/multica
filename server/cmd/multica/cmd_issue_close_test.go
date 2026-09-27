package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/ghpr"
)

// newIssueCloseTestCmd mirrors the shipped flag set so runIssueClose can be
// driven without cobra's argument parsing.
func newIssueCloseTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "close"}
	registerIssueCloseFlags(cmd)
	return cmd
}

func TestIssueCloseCommandRegistration(t *testing.T) {
	cmd, _, err := issueCmd.Find([]string{"close"})
	if err != nil {
		t.Fatalf("find issue close: %v", err)
	}
	if cmd != issueCloseCmd {
		t.Fatalf("found command = %q, want issue close", cmd.CommandPath())
	}
	for _, anchor := range []string{
		"--outcome",
		"--evidence",
		"--verdict pass",
		"one transaction",
	} {
		if !strings.Contains(cmd.Long, anchor) {
			t.Fatalf("long help should carry the close contract (missing %q), got %q", anchor, cmd.Long)
		}
	}
	for _, name := range []string{
		"outcome", "evidence", "evidence-stdin", "evidence-file", "allow-external-file",
		"summary", "parent", "blocked-by", "wake-at", "wait-condition", "wait-probe",
		"wait-timeout", "needs-human", "no-code", "verdict", "output",
	} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("issue close missing --%s", name)
		}
	}
}

func TestRunIssueCloseRejectsBadFlagsBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cases := []struct {
		name string
		set  map[string]string
		want string
	}{
		{"missing outcome", map[string]string{"evidence": "PR #1"}, "--outcome is required"},
		{"unknown outcome", map[string]string{"outcome": "finished", "evidence": "PR #1"}, "not a close outcome"},
		{"missing evidence", map[string]string{"outcome": "done"}, "--evidence"},
		{"verdict hold", map[string]string{"outcome": "done", "evidence": "PR #1", "verdict": "hold"}, "--verdict only accepts pass"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newIssueCloseTestCmd()
			for k, v := range tc.set {
				_ = cmd.Flags().Set(k, v)
			}
			err := runIssueClose(cmd, []string{"33333333-3333-4333-8333-333333333333"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestRunIssueCloseSendsExpectedRequest(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "44444444-4444-4444-8444-444444444444"
	var body map[string]any
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/issues/"+issueID+"/close" {
			t.Fatalf("path = %q, want /api/issues/%s/close", r.URL.Path, issueID)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "blocked",
			"prev_status":    "in_progress",
			"status_changed": true,
			"merged":         false,
			"woken":          []string{"DENE-1 到终态时叫醒"},
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "blocked")
	_ = cmd.Flags().Set("evidence", "PR #1 open; waiting on DENE-1")
	_ = cmd.Flags().Set("summary", "卡在依赖")
	_ = cmd.Flags().Set("blocked-by", "DENE-1")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	out, err := captureStdout(t, func() error {
		return runIssueClose(cmd, []string{issueID})
	})
	if err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if out != "" {
		t.Fatalf("table output should print nothing on stdout, got %q", out)
	}
	want := map[string]any{
		"outcome":    "blocked",
		"evidence":   "PR #1 open; waiting on DENE-1",
		"summary":    "卡在依赖",
		"blocked_by": "DENE-1",
	}
	if len(body) != len(want) {
		t.Fatalf("body = %#v, want exactly %#v", body, want)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("body[%q] = %#v, want %#v", k, body[k], v)
		}
	}
	got := stderr.read()
	for _, line := range []string{"Issue " + issueID + " closed: status blocked.", "  - DENE-1 到终态时叫醒"} {
		if !strings.Contains(got, line) {
			t.Fatalf("stderr = %q, want containing %q", got, line)
		}
	}
}

func TestRunIssueCloseVerdictPassReportsMerge(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "55555555-5555-4555-8555-555555555555"
	origList, origMerge := ghListPRs, ghMergePR
	t.Cleanup(func() { ghListPRs, ghMergePR = origList, origMerge })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	ghMergePR = func(context.Context, string, string) error { t.Fatal("must not merge"); return nil }
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A UUID reference first resolves its issue key for the gh lookup.
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"identifier": "DENE-5"})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "done",
			"merged": true,
			"pr_url": "https://github.com/o/r/pull/9",
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "checks green")
	_ = cmd.Flags().Set("verdict", "PASS")
	stderr := captureStderr(t)
	defer stderr.restore()
	out, err := captureStdout(t, func() error {
		return runIssueClose(cmd, []string{issueID})
	})
	if err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if body["verdict"] != "pass" || body["outcome"] != "done" {
		t.Fatalf("body = %#v, want verdict pass on outcome done", body)
	}
	if got := stderr.read(); !strings.Contains(got, "status done, PR merged.") {
		t.Fatalf("stderr = %q, want merge reported", got)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout JSON %q: %v", out, err)
	}
	if result["pr_url"] != "https://github.com/o/r/pull/9" {
		t.Fatalf("stdout = %#v", result)
	}
}
