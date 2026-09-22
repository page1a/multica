package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logexport"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ExportTaskLogs builds the redacted, single-file log bundle for one task.
//
// This endpoint is the single generator behind three entry points: the web
// export dialog, the desktop export dialog, and `multica logs export`. They all
// receive the same bytes, so a bundle previewed in the UI is byte-for-byte the
// one the CLI writes and the one a git push would commit.
//
// GET /api/tasks/{taskId}/logs/export?scope=run|hours|task&hours=N
func (h *Handler) ExportTaskLogs(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		if !isNotFound(err) {
			slog.Warn("get agent task failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to load task")
			return
		}
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Same workspace boundary as the task-messages endpoint: a task in another
	// workspace must be indistinguishable from one that does not exist, and a
	// failed lookup is a retryable 5xx rather than a lie about existence.
	wsID, err := h.TaskService.ResolveTaskWorkspaceIDChecked(r.Context(), task)
	if err != nil {
		slog.Warn("resolve task workspace failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return
	}
	if wsID == "" || wsID != middleware.WorkspaceIDFromContext(r.Context()) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	scope, ok := parseExportScope(w, r)
	if !ok {
		return
	}

	now := time.Now().UTC()
	runs, messages, ok := h.collectExportEntries(w, r, task, scope, now)
	if !ok {
		return
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
		return
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
		return
	}

	body, err := logexport.Marshal(bundle)
	if err != nil {
		slog.Error("marshal log export failed", append(logger.RequestAttrs(r), "task_id", taskID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to build log export")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="`+logexport.FileName(bundle)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
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

// parseExportScope validates the scope/hours pair, writing the 400 itself so
// the caller cannot silently fall back to a wider range than it asked for.
func parseExportScope(w http.ResponseWriter, r *http.Request) (logexport.Scope, bool) {
	hours := 0
	if raw := r.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid hours parameter")
			return logexport.Scope{}, false
		}
		hours = parsed
	}
	scope, err := logexport.ParseScope(r.URL.Query().Get("scope"), hours)
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
