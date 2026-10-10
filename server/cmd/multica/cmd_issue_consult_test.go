package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const consultTestIssueID = "11111111-1111-4111-8111-111111111111"

func newIssueConsultTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "consult"}
	addIssueConsultFlags(cmd)
	return cmd
}

func TestIssueConsultAsksThenWaitsForTheAnswer(t *testing.T) {
	var polls atomic.Int32
	base := "/api/issues/" + consultTestIssueID + "/consults"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["question"] != "Which index?" {
				t.Errorf("question = %q", body["question"])
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c-1", "status": "pending", "advisor_name": "孙悟饭"})
		case r.Method == http.MethodGet && r.URL.Path == base+"/c-1":
			if polls.Add(1) < 2 {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "c-1", "status": "pending", "advisor_name": "孙悟饭"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "c-1", "status": "answered", "advisor_name": "孙悟饭", "answer": "Use the composite one.", "tokens_used": 1200, "duration_seconds": 40})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	old := consultPollInterval
	consultPollInterval = time.Millisecond
	t.Cleanup(func() { consultPollInterval = old })

	cmd := newIssueConsultTestCmd()
	_ = cmd.Flags().Set("question", "Which index?")
	stderr := captureStderr(t)
	out, err := captureStdout(t, func() error { return runIssueConsult(cmd, []string{consultTestIssueID}) })
	_ = stderr.read()
	if err != nil {
		t.Fatalf("consult: %v", err)
	}
	if !strings.Contains(out, "Advice from 孙悟饭") || !strings.Contains(out, "Use the composite one.") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestIssueConsultPrintsTheRefusalAndExitsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "consult_limit_reached", "error": "this ticket already used its 3 consults; keep working on your own judgment", "limit": 3})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newIssueConsultTestCmd()
	_ = cmd.Flags().Set("question", "One more?")
	_ = cmd.Flags().Set("output", "json")
	out, err := captureStdout(t, func() error { return runIssueConsult(cmd, []string{consultTestIssueID}) })
	if err != nil {
		t.Fatalf("a refusal is an answer, not an error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q", out)
	}
	if got["status"] != "refused" || got["code"] != "consult_limit_reached" {
		t.Fatalf("refusal = %v", got)
	}
}
