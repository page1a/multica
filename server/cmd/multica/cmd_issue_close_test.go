package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/ghpr"
	"github.com/multica-ai/multica/server/internal/projectmemory"
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
		"--knowledge-none",
		projectmemory.LocationKeys()[0],
	} {
		if !strings.Contains(cmd.Long, anchor) {
			t.Fatalf("long help should carry the close contract (missing %q), got %q", anchor, cmd.Long)
		}
	}
	for _, name := range []string{
		"outcome", "evidence", "evidence-stdin", "evidence-file", "allow-external-file",
		"summary", "parent", "blocked-by", "wake-at", "wait-condition", "wait-probe",
		"wait-timeout", "needs-human", "no-code", "verdict", "pr", "knowledge-none", "knowledge", "output",
	} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("issue close missing --%s", name)
		}
	}
	// DENE-1002: the help must list all seven outcomes, and each requirement
	// must be discoverable without reading the server.
	for _, outcome := range closeprotocol.OutcomeNames() {
		if !strings.Contains(cmd.Long, "--outcome "+outcome) {
			t.Errorf("long help does not document --outcome %s", outcome)
		}
	}
	outcomeUsage := cmd.Flags().Lookup("outcome").Usage
	for _, outcome := range closeprotocol.OutcomeNames() {
		if !strings.Contains(outcomeUsage, outcome) {
			t.Errorf("--outcome usage does not list %s: %q", outcome, outcomeUsage)
		}
	}
}

// DENE-1183: the outcome table lives only on the server. The CLI forwards a
// close it would once have refused locally and relays the server's reason.
func TestRunIssueCloseRelaysServerRefusal(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "33333333-3333-4333-8333-333333333333"
	refusal := closeprotocol.OutcomeRejection("finished")
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": refusal})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "finished")
	_ = cmd.Flags().Set("evidence", "PR #1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	err := runIssueClose(cmd, []string{issueID})
	if err == nil {
		t.Fatal("a server refusal must fail the command")
	}
	if got := cli.FormatError(err, false); !strings.Contains(got, refusal) {
		t.Fatalf("relayed error = %q, want the server's reason %q", got, refusal)
	}
	if len(paths) != 1 || paths[0] != "/api/issues/"+issueID+"/close" {
		t.Fatalf("paths = %v, want the close request only", paths)
	}
}

// A close that may merge locally asks the server's shape gate first, so a
// refused close (here: no knowledge audit) never reaches the merge.
func TestRunIssueCloseChecksShapeBeforeLocalMerge(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "33333333-3333-4333-8333-333333333334"
	origList, origMerge := ghListPRs, ghMergePR
	t.Cleanup(func() { ghListPRs, ghMergePR = origList, origMerge })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) {
		t.Fatal("a refused close must not look up or merge PRs")
		return nil, nil
	}
	ghMergePR = func(context.Context, string, string) error { t.Fatal("must not merge"); return nil }
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]any{"identifier": "DENE-3"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": closeprotocol.KnowledgeAuditRequiredMsg})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "PR #1")
	err := runIssueClose(cmd, []string{issueID})
	if err == nil || !strings.Contains(cli.FormatError(err, false), closeprotocol.KnowledgeAuditRequiredMsg) {
		t.Fatalf("err = %v, want the server's audit refusal", err)
	}
	last := paths[len(paths)-1]
	if last != "POST /api/issues/"+issueID+"/close/check" {
		t.Fatalf("requests = %v, want to stop at the shape check", paths)
	}
}

// A server that predates /close/check answers 404; the close still runs and
// the server's full gate still applies.
func TestRunIssueCloseSkipsMissingPreflight(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "33333333-3333-4333-8333-333333333335"
	origList := ghListPRs
	t.Cleanup(func() { ghListPRs = origList })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	closed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"identifier": "DENE-3"})
		case strings.HasSuffix(r.URL.Path, "/close/check"):
			http.NotFound(w, r)
		default:
			closed = true
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "in_review"})
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "in_review")
	_ = cmd.Flags().Set("evidence", "PR #1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if !closed {
		t.Fatal("the close must still be sent when the preflight is missing")
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
	_ = cmd.Flags().Set("knowledge-none", "true")
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
	audit, _ := body["knowledge_audit"].(map[string]any)
	if audit["none"] != true {
		t.Fatalf("knowledge_audit = %#v", body["knowledge_audit"])
	}
	delete(body, "knowledge_audit")
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
		if strings.HasSuffix(r.URL.Path, "/close/check") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
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
	_ = cmd.Flags().Set("knowledge", "agents=记录了收口要带知识审计")
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

// Checks still running are waited out in place (DENE-1219): the CLI asks
// again until the server merges and closes, then reports that.
func TestRunIssueCloseWaitsOutRunningChecks(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "33333333-3333-4333-8333-333333333336"
	origList := ghListPRs
	origSleep, origNow := closeSleep, closeNow
	t.Cleanup(func() { ghListPRs, closeSleep, closeNow = origList, origSleep, origNow })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	clock := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	closeNow = func() time.Time { return clock }
	slept := 0
	closeSleep = func(d time.Duration) { slept++; clock = clock.Add(d) }
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"identifier": "DENE-4"})
		case strings.HasSuffix(r.URL.Path, "/close/check"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/close"):
			attempts++
			if attempts < 3 {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "close_checks_pending", "error": "PR 的检查还没出结果"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "merged": true})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "PR #1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	_ = cmd.Flags().Set("output", "table")
	stderr := captureStderr(t)
	defer stderr.restore()
	if err := runIssueClose(cmd, []string{issueID}); err != nil {
		t.Fatalf("runIssueClose: %v", err)
	}
	if attempts != 3 || slept != 2 {
		t.Fatalf("attempts = %d, slept = %d; want 3 attempts with 2 waits", attempts, slept)
	}
}

// A wait that outlasts the limit stops with the server's answer and an
// instruction to close again, never a status write.
func TestRunIssueCloseStopsWaitingAtTheLimit(t *testing.T) {
	chdirWithDaemonTaskMarker(t)
	const issueID = "33333333-3333-4333-8333-333333333337"
	origList := ghListPRs
	origSleep, origNow := closeSleep, closeNow
	t.Cleanup(func() { ghListPRs, closeSleep, closeNow = origList, origSleep, origNow })
	ghListPRs = func(context.Context, string, ...string) ([]ghpr.PR, error) { return nil, nil }
	clock := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	closeNow = func() time.Time { return clock }
	closeSleep = func(d time.Duration) { clock = clock.Add(d) }
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"identifier": "DENE-5"})
		case strings.HasSuffix(r.URL.Path, "/close/check"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case strings.HasSuffix(r.URL.Path, "/close"):
			attempts++
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "close_checks_pending", "error": "PR 的检查还没出结果"})
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueCloseTestCmd()
	_ = cmd.Flags().Set("outcome", "done")
	_ = cmd.Flags().Set("evidence", "PR #1")
	_ = cmd.Flags().Set("knowledge-none", "true")
	stderr := captureStderr(t)
	defer stderr.restore()
	err := runIssueClose(cmd, []string{issueID})
	if err == nil || !strings.Contains(err.Error(), "再执行一次同样的 close") {
		t.Fatalf("err = %v, want the wait-limit instruction", err)
	}
	if attempts != int(closeWaitLimit/closeWaitInterval) {
		t.Fatalf("attempts = %d, want %d", attempts, int(closeWaitLimit/closeWaitInterval))
	}
}
