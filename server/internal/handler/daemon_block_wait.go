package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/blockwait"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxProbeOutput = 2048

type DaemonBlockWait struct {
	ID            string `json:"id"`
	Identifier    string `json:"identifier"`
	WaitCondition string `json:"wait_condition,omitempty"`
	WaitProbe     string `json:"wait_probe"`
	WaitTimeout   string `json:"wait_timeout,omitempty"`
	WorkDir       string `json:"work_dir,omitempty"`
	ProbeStatus   string `json:"probe_status,omitempty"`
	ProbeAt       string `json:"probe_at,omitempty"`
	ProbeOutput   string `json:"probe_output,omitempty"`
}

type daemonBlockWaitReport struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output,omitempty"`
}

func (h *Handler) ListDaemonBlockWaits(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspaceId")
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	parsedWorkspaceID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	runtimeID := strings.TrimSpace(r.URL.Query().Get("runtime_id"))
	rows, err := h.Queries.ListDaemonBlockWaits(r.Context(), parsedWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list block waits")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), parsedWorkspaceID)
	items := make([]DaemonBlockWait, 0, len(rows))
	for _, issue := range rows {
		var selectedTask *db.AgentTaskQueue
		if tasks, taskErr := h.Queries.ListTasksByIssue(r.Context(), issue.ID); taskErr == nil {
			if len(tasks) > 0 && (runtimeID == "" || (tasks[0].RuntimeID.Valid && uuidToString(tasks[0].RuntimeID) == runtimeID)) {
				selectedTask = &tasks[0]
			}
		}
		if runtimeID != "" && selectedTask == nil {
			continue
		}
		meta := parseIssueMetadata(issue.Metadata)
		item := DaemonBlockWait{
			ID:            uuidToString(issue.ID),
			Identifier:    issueIdentifier(prefix, issue.Number),
			WaitCondition: blockwait.MetaString(meta, blockwait.KeyWaitCondition),
			WaitProbe:     blockwait.MetaString(meta, blockwait.KeyWaitProbe),
			ProbeStatus:   blockwait.MetaString(meta, blockwait.KeyProbeStatus),
			ProbeAt:       blockwait.MetaString(meta, blockwait.KeyProbeAt),
			ProbeOutput:   blockwait.MetaString(meta, blockwait.KeyProbeOutput),
		}
		item.WaitTimeout = blockwait.MetaString(meta, blockwait.KeyWaitTimeout)
		// The matching task owns the reusable work directory.
		if selectedTask != nil {
			task := selectedTask
			if task.DurableWorkDir.Valid && strings.TrimSpace(task.DurableWorkDir.String) != "" {
				item.WorkDir = task.DurableWorkDir.String
			} else if task.WorkDir.Valid && strings.TrimSpace(task.WorkDir.String) != "" {
				item.WorkDir = task.WorkDir.String
			}
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"waits": items})
}

func (h *Handler) ReportDaemonBlockWait(w http.ResponseWriter, r *http.Request) {
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), issueID)
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	if !h.requireDaemonWorkspaceAccess(w, r, uuidToString(issue.WorkspaceID)) {
		return
	}
	if issue.Status != "blocked" {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "reason": "issue is no longer blocked"})
		return
	}
	meta := parseIssueMetadata(issue.Metadata)
	if strings.TrimSpace(blockwait.MetaString(meta, blockwait.KeyWaitProbe)) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"accepted": false, "reason": "issue has no wait probe"})
		return
	}
	var req daemonBlockWaitReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid wait probe report")
		return
	}
	status := blockwait.ProbeStatusForExitCode(req.ExitCode)
	output := truncateMetadataString(strings.TrimSpace(req.Output), maxProbeOutput)
	if status == blockwait.ProbePending {
		if deadline, parseErr := time.Parse(time.RFC3339, blockwait.MetaString(meta, blockwait.KeyWaitTimeout)); parseErr == nil && !time.Now().Before(deadline) {
			status = blockwait.ProbeFailed
			if output == "" {
				output = "等待期限已过，探针仍未就绪"
			}
		}
	}
	previous := blockwait.MetaString(meta, blockwait.KeyProbeStatus)
	lastNotified := blockwait.MetaString(meta, blockwait.KeyProbeNotified)
	now := time.Now().UTC().Format(time.RFC3339)
	h.setIssueMetaString(r.Context(), issue, blockwait.KeyProbeStatus, string(status))
	h.setIssueMetaString(r.Context(), issue, blockwait.KeyProbeAt, now)
	h.setIssueMetaString(r.Context(), issue, blockwait.KeyProbeOutput, output)

	notified := previous != string(status) && lastNotified != string(status) && status != blockwait.ProbePending
	if notified {
		h.setIssueMetaString(r.Context(), issue, blockwait.KeyProbeNotified, string(status))
		reason := "等待的条件已经就绪，接着把原来那一步做完。"
		if status == blockwait.ProbeFailed {
			reason = "等待探针报告失败，请根据输出修复后继续。"
			if output != "" {
				reason += " 输出：" + output
			}
		}
		h.wakeIssueOwner(r.Context(), issue, reason, false)
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true, "status": status, "notified": notified, "checked_at": now})
}
