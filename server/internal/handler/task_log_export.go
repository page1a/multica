package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logexport"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HeaderLogExportFilename carries the artifact's suggested file name. The
// header itself is the ordinary Content-Disposition, but for a browser client
// split across origins it is a *custom* signal: CORS exposes only the
// safelisted response headers, so unless the router lists this one in
// Access-Control-Expose-Headers the name arrives on the wire and then vanishes
// from `Response.headers`, and every export in the workspace silently falls
// back to one generic file name. Named here so the router's list cannot drift
// from the header the handler actually sets.
const HeaderLogExportFilename = "Content-Disposition"

// ExportTaskLogs builds the redacted, single-file log bundle for one task.
//
// This endpoint is the single generator behind three entry points: the web
// export dialog, the desktop export dialog, and `multica logs export`. They all
// receive the same bytes, so a bundle previewed in the UI is byte-for-byte the
// one the CLI writes and the one a git push would commit.
//
// GET /api/tasks/{taskId}/logs/export?scope=run|hours|task&hours=N
func (h *Handler) ExportTaskLogs(w http.ResponseWriter, r *http.Request) {
	task, taskID, ok := h.resolveExportTask(w, r)
	if !ok {
		return
	}

	scope, ok := parseExportScopeQuery(w, r)
	if !ok {
		return
	}

	bundle, body, ok := h.buildExportBundle(w, r, task, taskID, scope, h.exportNow())
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(HeaderLogExportFilename, `attachment; filename="`+logexport.FileName(bundle)+`"`)
	// Advertise the exact size before the first byte: without it the browser
	// falls back to chunked transfer and cannot render a real progress bar
	// while a large artifact streams.
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// PushTaskLogExport builds the same bundle as ExportTaskLogs and commits it to
// the workspace's configured git repository, returning a link the issue comment
// can carry instead of a large attachment.
//
// POST /api/tasks/{taskId}/logs/export/push
// body: {"scope":"run"|"hours"|"task","hours":N}
func (h *Handler) PushTaskLogExport(w http.ResponseWriter, r *http.Request) {
	task, taskID, ok := h.resolveExportTask(w, r)
	if !ok {
		return
	}

	scope, ok := parseExportScopeBody(w, r)
	if !ok {
		return
	}

	repo, token, ok := h.resolveLogExportRepo(w, r)
	if !ok {
		return
	}
	if h.LogExportPusher == nil {
		writeError(w, http.StatusConflict, "log export git push is not available on this deployment")
		return
	}

	// The download path and the push path share this call, so the committed
	// bytes and the previewed bytes are the same artifact under the same
	// redaction rules.
	bundle, body, ok := h.buildExportBundle(w, r, task, taskID, scope, h.exportNow())
	if !ok {
		return
	}

	filename := logexport.FileName(bundle)
	result, err := h.LogExportPusher.Push(r.Context(), repo, token, logexport.PushRequest{
		Filename: filename,
		Content:  body,
		Message:  exportCommitMessage(bundle),
	})
	if err != nil {
		// The pusher already strips credentials; repeating it here means a
		// future pusher regression cannot turn this 502 into a leak, and it
		// costs one string scan on a path that is already failing.
		safe := err.Error()
		if token != "" {
			safe = strings.ReplaceAll(safe, token, "***")
		}
		slog.Warn("push log export failed", append(logger.RequestAttrs(r), "task_id", taskID, "repo", logexport.WebURL(repo.URL), "error", safe)...)
		writeError(w, http.StatusBadGateway, "log export push failed: "+safe)
		return
	}

	response := pushTaskLogExportResponse{
		Pushed:            true,
		Filename:          filename,
		Path:              result.Path,
		URL:               result.URL,
		Branch:            result.Branch,
		Repo:              logexport.WebURL(repo.URL),
		SummaryMarkdown:   bundle.SummaryMarkdown,
		EntryCount:        bundle.EntryCount,
		RunCount:          bundle.RunCount,
		SizeBytes:         len(body),
		RedactionComplete: bundle.Redaction.Complete,
		RedactionNote:     bundle.Redaction.Note,
		Truncated:         bundle.Truncated,
	}
	writeJSON(w, http.StatusOK, response)
}

// pushTaskLogExportResponse is the push endpoint's wire contract. The client
// builds the comment link from `url` and shows `summary_markdown` verbatim, so
// both are the bundle's own values rather than a re-rendering.
type pushTaskLogExportResponse struct {
	Pushed            bool   `json:"pushed"`
	Filename          string `json:"filename"`
	Path              string `json:"path"`
	URL               string `json:"url"`
	Branch            string `json:"branch"`
	Repo              string `json:"repo"`
	SummaryMarkdown   string `json:"summary_markdown"`
	EntryCount        int    `json:"entry_count"`
	RunCount          int    `json:"run_count"`
	SizeBytes         int    `json:"size_bytes"`
	RedactionComplete bool   `json:"redaction_complete"`
	RedactionNote     string `json:"redaction_note,omitempty"`
	Truncated         bool   `json:"truncated"`
}

// resolveExportTask is the shared front half of both log-export handlers: parse
// the task id, load the task, and enforce the workspace boundary. Extracted so
// the download and the push cannot drift into answering differently about which
// tasks exist.
func (h *Handler) resolveExportTask(w http.ResponseWriter, r *http.Request) (db.AgentTaskQueue, string, bool) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return db.AgentTaskQueue{}, "", false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		if !isNotFound(err) {
			slog.Warn("get agent task failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to load task")
			return db.AgentTaskQueue{}, "", false
		}
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, "", false
	}

	// Same workspace boundary as the task-messages endpoint: a task in another
	// workspace must be indistinguishable from one that does not exist, and a
	// failed lookup is a retryable 5xx rather than a lie about existence.
	wsID, err := h.TaskService.ResolveTaskWorkspaceIDChecked(r.Context(), task)
	if err != nil {
		slog.Warn("resolve task workspace failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return db.AgentTaskQueue{}, "", false
	}
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.AgentTaskQueue{}, "", false
	}
	return task, taskID, true
}

// buildExportBundle is the one generator behind both endpoints. Keeping the
// reads, the deny-list, the build, and the marshal in a single function is what
// makes the pushed artifact provably identical to the downloaded one.
func (h *Handler) buildExportBundle(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, taskID string, scope logexport.Scope, now time.Time) (logexport.Bundle, []byte, bool) {
	runs, messages, ok := h.collectExportEntries(w, r, task, scope, now)
	if !ok {
		return logexport.Bundle{}, nil, false
	}

	target := logexport.Target{
		TaskID:  uuidToString(task.ID),
		AgentID: uuidToString(task.AgentID),
	}
	if issue, issueErr := h.loadExportIssue(r, task); issueErr == nil && issue != nil {
		target.IssueID = uuidToString(issue.ID)
		target.IssueIdentifier = issueIdentifier(h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number)
		target.IssueTitle = issue.Title
	}

	env, agentNames, envGap, envOK := h.collectExportEnv(w, r, task.AgentID, runs)
	if !envOK {
		return logexport.Bundle{}, nil, false
	}
	target.AgentName = agentNames[uuidToString(task.AgentID)]

	bundle, err := logexport.Build(logexport.Input{
		GeneratedAt:  now,
		Scope:        scope,
		Target:       target,
		Runs:         runs,
		Messages:     messages,
		Env:          env,
		EnvLookupGap: envGap,
	})
	if err != nil {
		slog.Error("build log export failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to build log export")
		return logexport.Bundle{}, nil, false
	}

	body, err := logexport.Marshal(bundle)
	if err != nil {
		slog.Error("marshal log export failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to build log export")
		return logexport.Bundle{}, nil, false
	}
	return bundle, body, true
}

// resolveLogExportRepo reads the workspace settings and resolves the usable
// repository plus its token. Every failure is a 409 naming the missing piece:
// the client's fallback (upload the artifact as an attachment) is the right
// answer to all of them, and none of them is worth retrying blindly.
func (h *Handler) resolveLogExportRepo(w http.ResponseWriter, r *http.Request) (logexport.GitRepo, string, bool) {
	wsUUID, err := util.ParseUUID(middleware.WorkspaceIDFromContext(r.Context()))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return logexport.GitRepo{}, "", false
	}
	ws, err := h.Queries.GetWorkspace(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("get workspace for log export failed", append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workspace settings")
		return logexport.GitRepo{}, "", false
	}

	repo, ok := logexport.ParseSettings(ws.Settings).Repo()
	if !ok {
		writeError(w, http.StatusConflict, "log export git repository is not configured")
		return logexport.GitRepo{}, "", false
	}

	sealed := storedLogExportSealedToken(ws.Settings)
	token := h.openLogExportToken(sealed)
	if sealed != "" && token == "" {
		// A token IS stored; this deployment just cannot open it. Pushing
		// anonymously would look like the credential was used, so this is a
		// configuration error, not a fallback.
		slog.Warn("open log export git token failed", append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusConflict, "log export git token cannot be opened (no MULTICA_LOG_EXPORT_SECRET_KEY or JWT_SECRET)")
		return logexport.GitRepo{}, "", false
	}
	return repo, token, true
}

// exportNow reads the clock both handlers stamp a bundle with. It exists so a
// test can freeze time and assert that the pushed artifact and the downloaded
// one are the same bytes.
func (h *Handler) exportNow() time.Time {
	if h.logExportNow != nil {
		return h.logExportNow()
	}
	return time.Now().UTC()
}

// exportCommitMessage names the artifact and its range so the repository's
// history reads like a log rather than a pile of identical "export" commits.
func exportCommitMessage(bundle logexport.Bundle) string {
	subject := bundle.Task.IssueIdentifier
	if subject == "" {
		subject = bundle.Task.ID
	}
	scope := string(bundle.Task.Scope.Kind)
	if bundle.Task.Scope.Kind == logexport.ScopeHours {
		scope = "last" + strconv.Itoa(bundle.Task.Scope.Hours) + "h"
	}
	return "chore(log-export): " + subject + " " + scope
}

// collectExportEnv reads the environment of every agent whose runs the bundle
// carries, so the value deny-list covers all exported runs and not only the
// requested one. The environment IS the deny-list input: without it, a secret
// whose value has no recognizable token shape travels in the artifact.
//
// Two failures are kept apart, because they have different honest answers:
//
//   - The agent row cannot be read at all (a database error): the request is
//     retryable, so it fails with a 5xx. Shipping a bundle that silently ran
//     without the deny-list is exactly the bug this guards against.
//   - The row is readable but its stored environment cannot be decoded: the
//     export proceeds and reports the gap through the artifact, so the reader
//     sees "redaction incomplete" instead of the summary's old, false
//     "已自动脱敏".
func (h *Handler) collectExportEnv(w http.ResponseWriter, r *http.Request, targetAgentID pgtype.UUID, runs []logexport.Run) (env, names map[string]string, gap string, ok bool) {
	env = map[string]string{}
	names = map[string]string{}
	seen := map[string]struct{}{}

	ids := make([]pgtype.UUID, 0, len(runs)+1)
	if targetAgentID.Valid {
		ids = append(ids, targetAgentID)
	}
	for _, run := range runs {
		id, err := util.ParseUUID(run.AgentID)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}

	for _, id := range ids {
		key := uuidToString(id)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		agent, err := h.Queries.GetAgent(r.Context(), id)
		if err != nil {
			slog.Warn("get agent for log export failed", append(logger.RequestAttrs(r), "agent_id", key, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to load agent environment")
			return nil, nil, "", false
		}
		names[key] = agent.Name

		values, decodeErr := unmarshalCustomEnvChecked(agent)
		if decodeErr != nil {
			slog.Warn("decode agent env for log export failed", append(logger.RequestAttrs(r), "agent_id", key, "error", decodeErr)...)
			gap = "部分 agent 的环境变量无法解析，deny-list 未完整生效"
			continue
		}
		for name, value := range values {
			env[name] = value
		}
	}
	return env, names, gap, true
}

// parseExportScopeQuery reads the scope/hours pair from the download endpoint's
// query string.
func parseExportScopeQuery(w http.ResponseWriter, r *http.Request) (logexport.Scope, bool) {
	hours := 0
	if raw := r.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid hours parameter")
			return logexport.Scope{}, false
		}
		hours = parsed
	}
	return validateExportScope(w, r.URL.Query().Get("scope"), hours)
}

// parseExportScopeBody reads the same pair from the push endpoint's JSON body.
func parseExportScopeBody(w http.ResponseWriter, r *http.Request) (logexport.Scope, bool) {
	var req struct {
		Scope string `json:"scope"`
		Hours int    `json:"hours"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return logexport.Scope{}, false
	}
	return validateExportScope(w, req.Scope, req.Hours)
}

// validateExportScope validates the scope/hours pair, writing the 400 itself so
// the caller cannot silently fall back to a wider range than it asked for.
func validateExportScope(w http.ResponseWriter, rawScope string, hours int) (logexport.Scope, bool) {
	scope, err := logexport.ParseScope(rawScope, hours)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return logexport.Scope{}, false
	}
	return scope, true
}

// collectExportEntries reads the runs and transcript rows a scope covers.
func (h *Handler) collectExportEntries(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, scope logexport.Scope, now time.Time) ([]logexport.Run, []logexport.Message, bool) {
	rows := []db.AgentTaskQueue{task}
	if scope.Kind != logexport.ScopeRun {
		listed, err := h.Queries.ListTasksByIssue(r.Context(), task.IssueID)
		if err != nil {
			slog.Warn("list issue tasks for export failed", append(logger.RequestAttrs(r), "issue_id", uuidToString(task.IssueID), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to list task runs")
			return nil, nil, false
		}
		rows = visibleTaskHistory(listed)
	}

	// The hours scope is a lookback, so a run that had already finished before
	// the window opened can contribute nothing. Dropping those rows here keeps
	// a long issue history from paying for a transcript read it will discard.
	var windowFrom time.Time
	if scope.Kind == logexport.ScopeHours {
		windowFrom = now.Add(-time.Duration(scope.Hours) * time.Hour)
	}

	runs := make([]logexport.Run, 0, len(rows))
	messages := make([]logexport.Message, 0)
	for _, row := range rows {
		if scope.Kind == logexport.ScopeHours && runEndedBefore(row, windowFrom) {
			continue
		}
		runs = append(runs, exportRun(row))
		msgs, err := h.Queries.ListTaskMessages(r.Context(), row.ID)
		if err != nil {
			slog.Warn("list task messages for export failed", append(logger.RequestAttrs(r), "task_id", uuidToString(row.ID), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to read task messages")
			return nil, nil, false
		}
		for _, m := range msgs {
			messages = append(messages, exportMessage(m))
		}
	}

	// Keep the requested run present even when the window dropped it: the
	// bundle's identity is that run, and Build needs its metadata regardless.
	if len(runs) == 0 {
		runs = append(runs, exportRun(task))
	}
	return runs, messages, true
}

func runEndedBefore(row db.AgentTaskQueue, cutoff time.Time) bool {
	if row.CompletedAt.Valid {
		return row.CompletedAt.Time.Before(cutoff)
	}
	if row.StartedAt.Valid {
		return row.StartedAt.Time.Before(cutoff)
	}
	return row.CreatedAt.Valid && row.CreatedAt.Time.Before(cutoff)
}

func (h *Handler) loadExportIssue(r *http.Request, task db.AgentTaskQueue) (*db.Issue, error) {
	if !task.IssueID.Valid {
		return nil, nil
	}
	if uuidToString(task.IssueID) == "" {
		return nil, nil
	}
	issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

func exportRun(row db.AgentTaskQueue) logexport.Run {
	return logexport.Run{
		TaskID:        uuidToString(row.ID),
		AgentID:       uuidToString(row.AgentID),
		Status:        row.Status,
		StartedAt:     timePtr(row.StartedAt),
		CompletedAt:   timePtr(row.CompletedAt),
		ExitCode:      deriveExitCode(row),
		FailureReason: textValue(row.FailureReason),
		Error:         textValue(row.Error),
	}
}

func exportMessage(row db.TaskMessage) logexport.Message {
	msg := logexport.Message{
		TaskID:          uuidToString(row.TaskID),
		Seq:             row.Seq,
		Type:            row.Type,
		Tool:            textValue(row.Tool),
		Content:         textValue(row.Content),
		Output:          textValue(row.Output),
		OutputTruncated: row.OutputTruncated.Valid && row.OutputTruncated.Bool,
	}
	if row.CreatedAt.Valid {
		msg.At = row.CreatedAt.Time.UTC()
	}
	if len(row.Input) > 0 {
		var decoded any
		if err := json.Unmarshal(row.Input, &decoded); err == nil {
			msg.Input = decoded
		}
	}
	return msg
}

// deriveExitCode reports the run's exit code. The daemon does not persist an
// exit code today, so a terminal status supplies the honest minimum: completed
// runs exited 0, failed runs exited non-zero. A `result.exit_code` is preferred
// when a daemon does report one, so this stays correct once it does.
func deriveExitCode(row db.AgentTaskQueue) *int {
	if len(row.Result) > 0 {
		var result struct {
			ExitCode *int `json:"exit_code"`
		}
		if err := json.Unmarshal(row.Result, &result); err == nil && result.ExitCode != nil {
			return result.ExitCode
		}
	}
	code := 0
	switch row.Status {
	case "completed":
		return &code
	case "failed":
		code = 1
		return &code
	default:
		return nil
	}
}

func timePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

func textValue(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
