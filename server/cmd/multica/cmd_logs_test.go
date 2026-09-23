package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newLogsExportTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export"}
	cmd.Flags().String("scope", "run", "")
	cmd.Flags().Int("hours", 0, "")
	cmd.Flags().StringP("output-dir", "o", ".", "")
	cmd.Flags().String("output-file", "", "")
	cmd.Flags().Bool("stdout", false, "")
	cmd.Flags().Bool("report", false, "")
	cmd.Flags().String("mention", "", "")
	return cmd
}

const logsExportTestTask = "01a0b577-06b6-786e-8df6-3ebf70edf6d2"

// logsExportArtifact is a minimal but complete bundle: enough for the command
// to read the metadata it reports, and byte-compared on the way out.
const logsExportArtifact = "{\n  \"format\": \"multica.log-export\",\n  \"version\": 1,\n  \"task\": {\"id\": \"" + logsExportTestTask + "\", \"issue_id\": \"issue-9\", \"issue_identifier\": \"DENE-599\", \"scope\": {\"kind\": \"run\"}},\n  \"run_count\": 1,\n  \"entry_count\": 3,\n  \"truncated\": false,\n  \"summary_markdown\": \"## AI 摘要\\n\\nDENE-599 的运行 abc\"\n}\n"

// logsExportUnredactedArtifact is the same bundle shape with the server having
// recorded that the environment deny-list could not run.
const logsExportUnredactedArtifact = "{\n  \"format\": \"multica.log-export\",\n  \"version\": 1,\n  \"task\": {\"id\": \"" + logsExportTestTask + "\", \"issue_id\": \"issue-9\", \"issue_identifier\": \"DENE-599\", \"scope\": {\"kind\": \"run\"}},\n  \"redaction\": {\"pattern_rules\": true, \"env_deny_list\": false, \"complete\": false, \"note\": \"部分 agent 记录不存在，环境变量 deny-list 未完整生效\"},\n  \"run_count\": 1,\n  \"entry_count\": 1,\n  \"truncated\": false,\n  \"summary_markdown\": \"## AI 摘要\\n\\n脱敏不完整\"\n}\n"

func TestValidateLogsExportScope(t *testing.T) {
	tests := []struct {
		scope   string
		hours   int
		wantErr bool
	}{
		{scope: "run"},
		{scope: "task"},
		{scope: "hours", hours: 6},
		{scope: "hours", wantErr: true},
		{scope: "hours", hours: -1, wantErr: true},
		{scope: "run", hours: 6, wantErr: true},
		{scope: "task", hours: 6, wantErr: true},
		{scope: "everything", wantErr: true},
	}
	for _, tc := range tests {
		err := validateLogsExportScope(tc.scope, tc.hours)
		if tc.wantErr && err == nil {
			t.Fatalf("validateLogsExportScope(%q, %d) = nil, want error", tc.scope, tc.hours)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("validateLogsExportScope(%q, %d) = %v", tc.scope, tc.hours, err)
		}
	}
}

// TestRunLogsExportWritesArtifactVerbatim is the CLI half of "the CLI produces
// the same artifact as the dialog": whatever the server rendered is what lands
// on disk, unmodified.
func TestRunLogsExportWritesArtifactVerbatim(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tasks/"+logsExportTestTask+"/logs/export" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="log-export-DENE-599-abcd1234-run-20260921T120000Z.json"`)
		_, _ = io.WriteString(w, logsExportArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	outputDir := t.TempDir()
	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", outputDir)

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	if gotQuery != "scope=run" {
		t.Fatalf("query = %q, want scope=run", gotQuery)
	}

	dest := filepath.Join(outputDir, "log-export-DENE-599-abcd1234-run-20260921T120000Z.json")
	data, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if string(data) != logsExportArtifact {
		t.Fatalf("artifact was rewritten:\n%s", data)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, out)
	}
	if result["issue_identifier"] != "DENE-599" {
		t.Fatalf("stdout issue_identifier = %v", result["issue_identifier"])
	}
	if !strings.Contains(result["summary_markdown"].(string), "AI 摘要") {
		t.Fatalf("stdout summary = %v", result["summary_markdown"])
	}
}

func TestRunLogsExportHoursScopeSendsWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "hours" || r.URL.Query().Get("hours") != "6" {
			t.Fatalf("query = %q, want scope=hours&hours=6", r.URL.RawQuery)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="log-export.json"`)
		_, _ = io.WriteString(w, logsExportArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("scope", "hours")
	_ = cmd.Flags().Set("hours", "6")
	_ = cmd.Flags().Set("output-dir", t.TempDir())

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
}

// TestRunLogsExportReportsViaCommentAttachment pins the report path to the
// existing two-step chain — upload, then comment with the attachment and the
// owner mention — rather than a bespoke endpoint.
func TestRunLogsExportReportsViaCommentAttachment(t *testing.T) {
	var (
		uploadedIssue string
		uploadBody    []byte
		commentBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tasks/"+logsExportTestTask+"/logs/export":
			w.Header().Set("Content-Disposition", `attachment; filename="log-export-DENE-599-abcd1234-run.json"`)
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			uploadedIssue = r.FormValue("issue_id")
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			defer file.Close()
			uploadBody, _ = io.ReadAll(file)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7", "filename": "log-export.json"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{"assignee_type": "member", "assignee_id": "user-9"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			if err := json.NewDecoder(r.Body).Decode(&commentBody); err != nil {
				t.Fatalf("decode comment body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			// The ordinary configuration: this workspace has no log repository,
			// so the report must fall back to the comment attachment.
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"log export git repository is not configured"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	if uploadedIssue != "issue-9" {
		t.Fatalf("upload issue_id = %q, want issue-9", uploadedIssue)
	}
	if string(uploadBody) != logsExportArtifact {
		t.Fatalf("uploaded artifact differs from the exported one:\n%s", uploadBody)
	}
	content, _ := commentBody["content"].(string)
	if !strings.Contains(content, "mention://member/user-9") {
		t.Fatalf("comment missing the owner mention: %q", content)
	}
	if !strings.Contains(content, "AI 摘要") {
		t.Fatalf("comment missing the AI summary: %q", content)
	}
	ids, _ := commentBody["attachment_ids"].([]any)
	if len(ids) != 1 || ids[0] != "attachment-7" {
		t.Fatalf("attachment_ids = %v", commentBody["attachment_ids"])
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	report, _ := result["report"].(map[string]any)
	if report["comment_id"] != "comment-11" {
		t.Fatalf("report = %v", report)
	}
}

// TestRunLogsExportReportPrefersTheLogRepository pins the order: when the
// workspace has a log repository the bundle is committed there and the comment
// keeps only a link, so a bundle too large for the upload path still gets
// reported. The comment must also quote the summary the push returned — the
// server rebuilds the document, so the local copy's summary describes a
// slightly older file.
func TestRunLogsExportReportPrefersTheLogRepository(t *testing.T) {
	var (
		commentBody map[string]any
		pushBody    map[string]any
		uploaded    bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			if err := json.NewDecoder(r.Body).Decode(&pushBody); err != nil {
				t.Fatalf("decode push body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pushed":             true,
				"filename":           "log-export-DENE-599-abcd1234-run-20260921T120500Z.json",
				"path":               "logs/log-export-DENE-599-abcd1234-run-20260921T120500Z.json",
				"url":                "https://github.com/o/r/blob/kun/logs/log-export-DENE-599-abcd1234-run-20260921T120500Z.json",
				"branch":             "kun",
				"repo":               "https://github.com/o/r",
				"summary_markdown":   "## AI 摘要\n\n推送后的产物摘要",
				"entry_count":        3,
				"run_count":          1,
				"size_bytes":         1024,
				"redaction_complete": true,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{"assignee_type": "member", "assignee_id": "user-9"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			if err := json.NewDecoder(r.Body).Decode(&commentBody); err != nil {
				t.Fatalf("decode comment body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			uploaded = true
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	// The push request carries the scope and nothing else: sending the
	// artifact back up is the round trip the git path exists to avoid.
	if pushBody["scope"] != "run" {
		t.Fatalf("push body = %v, want scope run", pushBody)
	}
	if _, hasHours := pushBody["hours"]; hasHours {
		t.Fatalf("push body carried hours for a run-scoped export: %v", pushBody)
	}
	if uploaded {
		t.Fatalf("a successful push must not also upload the artifact")
	}

	content, _ := commentBody["content"].(string)
	if !strings.Contains(content, "推送后的产物摘要") {
		t.Fatalf("comment must quote the pushed document's summary: %q", content)
	}
	if !strings.Contains(content, "https://github.com/o/r/blob/kun/logs/") {
		t.Fatalf("comment missing the committed file link: %q", content)
	}
	if !strings.Contains(content, "mention://member/user-9") {
		t.Fatalf("comment missing the owner mention: %q", content)
	}
	if _, hasAttachment := commentBody["attachment_ids"]; hasAttachment {
		t.Fatalf("link-only comment carried an attachment: %v", commentBody)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	report, _ := result["report"].(map[string]any)
	if report["channel"] != "git" {
		t.Fatalf("report = %v, want channel git", report)
	}
	if report["path"] != "logs/log-export-DENE-599-abcd1234-run-20260921T120500Z.json" {
		t.Fatalf("report path = %v", report["path"])
	}
}

// TestRunLogsExportReportKeepsTheReportWhenThePushFails is the other half: a
// repository that exists but rejects the push must not cost the operator the
// report. The attachment path still runs and the reason is reported.
func TestRunLogsExportReportKeepsTheReportWhenThePushFails(t *testing.T) {
	var (
		commentBody map[string]any
		uploaded    []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":"git clone failed: could not read Username"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{"assignee_type": "member", "assignee_id": "user-9"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			defer file.Close()
			uploaded, _ = io.ReadAll(file)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	if string(uploaded) != logsExportArtifact {
		t.Fatalf("fallback uploaded %d bytes, want the verbatim artifact", len(uploaded))
	}
	ids, _ := commentBody["attachment_ids"].([]any)
	if len(ids) != 1 || ids[0] != "attachment-7" {
		t.Fatalf("attachment_ids = %v", commentBody["attachment_ids"])
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	report, _ := result["report"].(map[string]any)
	if report["channel"] != "attachment" {
		t.Fatalf("report = %v, want channel attachment", report)
	}
	if reason, _ := report["fallback_reason"].(string); !strings.Contains(reason, "could not read Username") {
		t.Fatalf("fallback_reason = %v", report["fallback_reason"])
	}
}

func TestRunLogsExportReportMentionOverride(t *testing.T) {
	var commentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			// The ordinary configuration: this workspace has no log repository,
			// so the report must fall back to the comment attachment.
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"log export git repository is not configured"}`)
		default:
			// An explicit --mention must skip the issue lookup entirely.
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")
	_ = cmd.Flags().Set("mention", "agent:agent-3")

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}
	content, _ := commentBody["content"].(string)
	if !strings.Contains(content, "mention://agent/agent-3") {
		t.Fatalf("comment missing the explicit mention: %q", content)
	}
}

// TestRunLogsExportReportDoesNotWakeAgentAssignee is the F1 regression: a
// `--report` on an issue assigned to an agent must not emit
// `mention://agent/...`, because that mention queues another paid run — of the
// very agent whose failed run the operator is exporting. The human who filed
// the issue is notified instead.
func TestRunLogsExportReportDoesNotWakeAgentAssignee(t *testing.T) {
	var commentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"assignee_type": "agent", "assignee_id": "agent-3",
				"creator_type": "member", "creator_id": "user-9",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			// The ordinary configuration: this workspace has no log repository,
			// so the report must fall back to the comment attachment.
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"log export git repository is not configured"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	content, _ := commentBody["content"].(string)
	if strings.Contains(content, "mention://agent/") || strings.Contains(content, "mention://squad/") {
		t.Fatalf("default report would queue a run for the assignee: %q", content)
	}
	if !strings.Contains(content, "mention://member/user-9") {
		t.Fatalf("comment missing the human creator fallback: %q", content)
	}
}

// TestRunLogsExportReportSkipsMentionWhenNobodyHuman is the other half of F1:
// when neither the assignee nor the creator is a member, the report posts
// without a mention rather than guessing an agent to wake.
func TestRunLogsExportReportSkipsMentionWhenNobodyHuman(t *testing.T) {
	var commentBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs/export"):
			_, _ = io.WriteString(w, logsExportArtifact)
		case r.Method == http.MethodPost && r.URL.Path == "/api/upload-file":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "attachment-7"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/issue-9":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"assignee_type": "squad", "assignee_id": "squad-2",
				"creator_type": "agent", "creator_id": "agent-3",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/issue-9/comments":
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "comment-11"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/logs/export/push"):
			// The ordinary configuration: this workspace has no log repository,
			// so the report must fall back to the comment attachment.
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"log export git repository is not configured"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())
	_ = cmd.Flags().Set("report", "true")

	if _, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) }); err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	content, _ := commentBody["content"].(string)
	if strings.Contains(content, "mention://") {
		t.Fatalf("report mentioned a non-human owner: %q", content)
	}
	if !strings.Contains(content, "AI 摘要") {
		t.Fatalf("comment lost the summary: %q", content)
	}
}

// TestRunLogsExportStdoutIsOnlyTheBundle is the F2 regression: stdout is the
// artifact's channel, so `multica logs export <id> --stdout > bundle.json`
// must yield exactly one JSON document. The metadata JSON moves to stderr
// instead of being appended after the bundle.
func TestRunLogsExportStdoutIsOnlyTheBundle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="log-export-DENE-599.json"`)
		_, _ = io.WriteString(w, logsExportArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("stdout", "true")

	errCap := captureStderr(t)
	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	errOut := errCap.read()
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	if out != logsExportArtifact {
		t.Fatalf("stdout is not exactly the bundle:\n%s", out)
	}
	var bundle map[string]any
	if err := json.Unmarshal([]byte(out), &bundle); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if bundle["format"] != "multica.log-export" {
		t.Fatalf("stdout is not the bundle: %v", bundle)
	}
	if _, isMetadata := bundle["task_id"]; isMetadata {
		t.Fatalf("stdout carried the metadata document as well: %v", bundle)
	}

	// The metadata still has to be reachable, and stderr is where it lives now.
	var meta map[string]any
	if err := json.Unmarshal([]byte(errOut), &meta); err != nil {
		t.Fatalf("stderr metadata is not JSON: %v\n%s", err, errOut)
	}
	if meta["task_id"] != logsExportTestTask {
		t.Fatalf("stderr metadata = %v", meta)
	}
}

// TestRunLogsExportSurfacesIncompleteRedaction pins the CLI half of F3: a
// bundle whose environment deny-list did not run reports that in its machine
// readable result, not only in the summary prose.
func TestRunLogsExportSurfacesIncompleteRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, logsExportUnredactedArtifact)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")

	cmd := newLogsExportTestCmd()
	_ = cmd.Flags().Set("output-dir", t.TempDir())

	out, err := captureStdout(t, func() error { return runLogsExport(cmd, []string{logsExportTestTask}) })
	if err != nil {
		t.Fatalf("runLogsExport: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	if complete, ok := result["redaction_complete"]; !ok || complete != false {
		t.Fatalf("redaction_complete = %v (present=%v), want false", complete, ok)
	}
	if note, _ := result["redaction_note"].(string); note == "" {
		t.Fatalf("missing redaction_note: %v", result)
	}
}

func TestFilenameFromHeaders(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{header: `attachment; filename="log-export-a.json"`, want: "log-export-a.json"},
		{header: `attachment; filename="../../etc/passwd"`, want: "passwd"},
		{header: "", want: ""},
		{header: "inline", want: ""},
	}
	for _, tc := range tests {
		got := filenameFromHeaders(http.Header{"Content-Disposition": []string{tc.header}})
		if got != tc.want {
			t.Fatalf("filenameFromHeaders(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}
