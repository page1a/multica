package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/inboxboard"
	"github.com/multica-ai/multica/server/internal/projectboard"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const projectBoardParkingLimit = 5000

type ProjectBoardResponse struct {
	projectboard.Board
	ProjectIDs []string `json:"project_ids"`
	AsOf       string   `json:"as_of"`
}

// GetProjectBoard returns every open issue visible to the caller in the
// selected projects. An empty project_ids set means the whole workspace.
// Lane decisions use the same parking records and active task snapshot as the
// inbox board; stale is a fixed 36-hour (1.5-day) last-activity threshold.
func (h *Handler) GetProjectBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerFor(r, wsUUID)
	if err != nil {
		writeError(w, http.StatusForbidden, "failed to resolve workspace visibility")
		return
	}
	projectIDs, projectSet, ok := parseProjectBoardIDs(w, r)
	if !ok {
		return
	}
	terminal, err := h.terminalIssueStatusKeys(ctx, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve terminal issue statuses")
		return
	}
	issues, err := h.Queries.ListOpenIssues(ctx, db.ListOpenIssuesParams{
		WorkspaceID:        wsUUID,
		TerminalStatusKeys: terminal,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list open issues")
		return
	}

	toOwner := func(typ string, id pgtype.UUID) projectboard.Owner {
		return projectboard.Owner{Type: typ, ID: uuidToString(id)}
	}
	boardIssues := make([]projectboard.Issue, 0, len(issues))
	for _, issue := range issues {
		projectID := uuidToString(issue.ProjectID)
		if projectSet != nil && (!issue.ProjectID.Valid || !projectSet[projectID]) {
			continue
		}
		if !viewer.canSeeIssueFields(issue.ID, issue.Visibility, issue.CreatorType, issue.CreatorID, issue.ProjectID, issue.AssigneeType.String, issue.AssigneeID) {
			continue
		}
		metadata := map[string]any{}
		if len(issue.Metadata) > 0 {
			_ = json.Unmarshal(issue.Metadata, &metadata)
		}
		last := issue.LastActivityAt.Time
		if !issue.LastActivityAt.Valid {
			last = issue.UpdatedAt.Time
		}
		boardIssues = append(boardIssues, projectboard.Issue{
			ID: uuidToString(issue.ID), Identifier: issueIdentifier(h.getIssuePrefix(ctx, wsUUID), issue.Number), Title: issue.Title,
			ProjectID: projectID, Status: issue.Status, ParentIssueID: uuidToString(issue.ParentIssueID),
			Assignee: toOwner(issue.AssigneeType.String, issue.AssigneeID), LastActivityAt: last, Metadata: metadata,
		})
	}

	parkingRows, err := h.Queries.ListWorkspaceParkingRecords(ctx, db.ListWorkspaceParkingRecordsParams{WorkspaceID: wsUUID, RowLimit: projectBoardParkingLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list parking records")
		return
	}
	parking := make([]projectboard.Parking, 0, len(parkingRows))
	for _, row := range parkingRows {
		parking = append(parking, projectboard.Parking{IssueID: uuidToString(row.IssueID), CurrentStatus: row.CurrentStatus, RecordedStatus: row.RecordedStatus, Category: row.Category, StuckKind: row.StuckKind, Unexplained: row.Unexplained, Summary: row.Summary, NextOwner: projectboard.Owner{Type: row.NextOwnerType, ID: row.NextOwnerID}, EvaluatedAt: row.EvaluatedAt.Time})
	}
	snapshot, err := h.Queries.ListWorkspaceAgentTaskSnapshot(ctx, wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent tasks")
		return
	}
	tasks := make([]projectboard.Task, 0, len(snapshot))
	for _, task := range snapshot {
		tasks = append(tasks, projectboard.Task{IssueID: uuidToString(task.IssueID), AgentID: uuidToString(task.AgentID), Status: task.Status, StartedAt: task.StartedAt.Time, DispatchedAt: task.DispatchedAt.Time, CreatedAt: task.CreatedAt.Time})
	}

	board := projectboard.Build(boardIssues, parking, tasks, time.Now().UTC())
	inboxNames := h.inboxBoardNamer(ctx, wsUUID)
	board.NameOwners(func(owner projectboard.Owner) string {
		return inboxNames(inboxboard.Owner{Type: owner.Type, ID: owner.ID})
	})
	writeJSON(w, http.StatusOK, ProjectBoardResponse{Board: board, ProjectIDs: projectIDs, AsOf: time.Now().UTC().Format(time.RFC3339Nano)})
}

func parseProjectBoardIDs(w http.ResponseWriter, r *http.Request) ([]string, map[string]bool, bool) {
	values := r.URL.Query()["project_ids"]
	var raw []string
	for _, value := range values {
		raw = append(raw, strings.Split(value, ",")...)
	}
	if len(raw) == 0 {
		return []string{}, nil, true
	}
	ids := make([]string, 0, len(raw))
	set := make(map[string]bool, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := util.ParseUUID(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "project_ids must contain project ids")
			return nil, nil, false
		}
		text := uuidToString(id)
		if set[text] {
			continue
		}
		set[text] = true
		ids = append(ids, text)
	}
	return ids, set, true
}
