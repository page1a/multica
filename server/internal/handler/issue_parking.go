package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ParkingOwner is who holds the next move on a parked ticket. Type is
// "agent", "member", "issue" (waiting on another ticket) or "" when nobody
// is named; ID is a uuid, or an identifier like DENE-12 for type "issue".
type ParkingOwner struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ParkingRecordResponse is one issue's latest parking record (DENE-881).
type ParkingRecordResponse struct {
	IssueID        string          `json:"issue_id"`
	Identifier     string          `json:"identifier"`
	Number         int32           `json:"number"`
	Title          string          `json:"title"`
	ParentIssueID  *string         `json:"parent_issue_id"`
	CurrentStatus  string          `json:"current_status"`
	RecordedStatus string          `json:"recorded_status"`
	State          string          `json:"state"`
	Category       string          `json:"category"`
	StuckKind      string          `json:"stuck_kind"`
	Unexplained    bool            `json:"unexplained"`
	Summary        string          `json:"summary"`
	SummarySource  string          `json:"summary_source"`
	NextOwner      ParkingOwner    `json:"next_owner"`
	TaskID         *string         `json:"task_id"`
	Timeline       json.RawMessage `json:"timeline"`
	EvaluatedAt    string          `json:"evaluated_at"`
}

const (
	parkingListDefaultLimit = 200
	parkingListMaxLimit     = 1000
)

// ListIssueParkingRecords — GET /api/issues/parking — lists the latest
// parking record of every issue in the workspace that has one, newest
// verdict first. The list is flat; parent_issue_id lets a client nest
// children under their parent. ?unexplained_only=true keeps only the tickets
// that stopped without saying why; ?limit caps the rows (default 200, max
// 1000).
func (h *Handler) ListIssueParkingRecords(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	limit := parkingListDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, parkingListMaxLimit)
	}
	unexplainedOnly, _ := strconv.ParseBool(r.URL.Query().Get("unexplained_only"))

	rows, err := h.Queries.ListWorkspaceParkingRecords(r.Context(), db.ListWorkspaceParkingRecordsParams{
		WorkspaceID:     wsUUID,
		UnexplainedOnly: unexplainedOnly,
		RowLimit:        int32(limit),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list parking records")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	out := make([]ParkingRecordResponse, 0, len(rows))
	for _, row := range rows {
		timeline := json.RawMessage(row.Timeline)
		if len(timeline) == 0 {
			timeline = json.RawMessage("[]")
		}
		out = append(out, ParkingRecordResponse{
			IssueID:        uuidToString(row.IssueID),
			Identifier:     issueIdentifier(prefix, row.Number),
			Number:         row.Number,
			Title:          row.Title,
			ParentIssueID:  uuidToPtr(row.ParentIssueID),
			CurrentStatus:  row.CurrentStatus,
			RecordedStatus: row.RecordedStatus,
			State:          row.State,
			Category:       row.Category,
			StuckKind:      row.StuckKind,
			Unexplained:    row.Unexplained,
			Summary:        row.Summary,
			SummarySource:  row.SummarySource,
			NextOwner:      ParkingOwner{Type: row.NextOwnerType, ID: row.NextOwnerID},
			TaskID:         uuidToPtr(row.TaskID),
			Timeline:       timeline,
			EvaluatedAt:    timestampToString(row.EvaluatedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": out})
}
