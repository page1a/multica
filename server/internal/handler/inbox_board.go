package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	// "done today" is counted in the caller's zone; the image may ship no zoneinfo.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/inboxboard"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// inboxBoardErrNoPerson: an agent asked for the board from a run no person
// started, so there is no inbox to read.
const inboxBoardErrNoPerson = "inbox_board_no_person"

// Caps on what the board reads. The web page read the same amounts through
// the old per-lane endpoints.
const (
	inboxBoardParkingLimit = 500
	inboxBoardDoneLimit    = 100
	inboxBoardTodoLimit    = 100
)

// InboxBoardResponse is the inbox in its six lanes (DENE-975).
type InboxBoardResponse struct {
	inboxboard.Board
	// ViewerID is the person whose inbox this is: the caller, or for an
	// agent the person who started its run.
	ViewerID string `json:"viewer_id"`
	// AsOf is the database clock when the board was read. Pass it back as
	// unread_since to keep this visit's unread markers after marking all read.
	AsOf        string  `json:"as_of"`
	UnreadSince *string `json:"unread_since"`
	TZ          string  `json:"tz"`
	DayStart    string  `json:"day_start"`
	// UnreadMarkable is how many unread rows mark-all-read would clear (rows
	// an open call hangs on stay unread). Meaningful on a live read.
	UnreadMarkable int64 `json:"unread_markable"`
}

// GetInboxBoard — GET /api/inbox/board — one person's inbox in six lanes:
// waiting / stalled / running / todo / fresh / done today. The lane rules live in
// internal/inboxboard; this only gathers their inputs.
//
//   - tz: IANA zone "done today" is counted in (default UTC).
//   - unread_since: RFC3339; rows read at or after it still count as unread,
//     so a page that marked everything read keeps its arrival markers.
//
// Read-only: it never changes read state. An agent caller reads the inbox of
// the person who started its run, with that person's visibility; a run no
// person started directly is refused.
func (h *Handler) GetInboxBoard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wsUUID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(ctx), "workspace id")
	if !ok {
		return
	}
	q := r.URL.Query()
	tzName := strings.TrimSpace(q.Get("tz"))
	if tzName == "" {
		tzName = "UTC"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "tz must be an IANA time zone, e.g. Asia/Shanghai")
		return
	}
	var unreadSince pgtype.Timestamptz
	if raw := strings.TrimSpace(q.Get("unread_since")); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unread_since must be an RFC3339 timestamp")
			return
		}
		unreadSince = pgtype.Timestamptz{Time: t, Valid: true}
	}

	userUUID, ok := h.inboxBoardPerson(w, r, wsUUID)
	if !ok {
		return
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: userUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusForbidden, "the inbox's owner is not a member of this workspace")
		return
	}
	viewer, err := h.visibilityViewerForUser(ctx, wsUUID, userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve viewer")
		return
	}

	// The database clock, not ours: read_at is written by it.
	var asOf time.Time
	if err := h.DB.QueryRow(ctx, "SELECT now()").Scan(&asOf); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read clock")
		return
	}
	dayStart := time.Date(asOf.In(loc).Year(), asOf.In(loc).Month(), asOf.In(loc).Day(), 0, 0, 0, 0, loc)

	in, markable, err := h.gatherInboxBoard(ctx, wsUUID, userUUID, member.Role, viewer, unreadSince, dayStart)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	board := inboxboard.Build(in)
	board.NameOwners(h.inboxBoardNamer(ctx, wsUUID))
	resp := InboxBoardResponse{
		Board:          board,
		ViewerID:       uuidToString(userUUID),
		AsOf:           asOf.UTC().Format(time.RFC3339Nano),
		UnreadSince:    timestampToNanoPtr(unreadSince),
		TZ:             loc.String(),
		DayStart:       dayStart.Format(time.RFC3339),
		UnreadMarkable: markable,
	}
	writeJSON(w, http.StatusOK, resp)
}

// inboxBoardPerson is whose inbox the request reads: a person reads their
// own; an agent reads the one of the person who started this run by hand
// (the same direct_human rule cross-workspace chat reads use).
func (h *Handler) inboxBoardPerson(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID) (pgtype.UUID, bool) {
	userID := requestUserID(r)
	if actorType, _ := h.resolveActor(r, userID, uuidToString(wsUUID)); actorType != "agent" {
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return pgtype.UUID{}, false
		}
		id, err := util.ParseUUID(userID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return pgtype.UUID{}, false
		}
		return id, true
	}
	refuse := func(msg string) (pgtype.UUID, bool) {
		writeErrorCode(w, http.StatusForbidden, inboxBoardErrNoPerson, msg)
		return pgtype.UUID{}, false
	}
	taskUUID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		return refuse("this request carries no run, so there is no person whose inbox to read")
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil {
		return refuse("this request carries no run, so there is no person whose inbox to read")
	}
	if isTerminalTaskStatus(task.Status) {
		return refuse("this run has finished; a finished run no longer reads anyone's inbox")
	}
	if !task.OriginatorUserID.Valid || task.OriginatorSource.String != "direct_human" {
		return refuse(fmt.Sprintf(
			"this run was started by %s, not directly by a person; the inbox board only reads the inbox of the person who started the run",
			describeOriginatorSource(task.OriginatorSource.String)))
	}
	return task.OriginatorUserID, true
}

func (h *Handler) gatherInboxBoard(
	ctx context.Context,
	wsUUID, userUUID pgtype.UUID,
	role string,
	viewer visibilityViewer,
	unreadSince pgtype.Timestamptz,
	dayStart time.Time,
) (inboxboard.Input, int64, error) {
	in := inboxboard.Input{UserID: uuidToString(userUUID)}
	prefix := h.getIssuePrefix(ctx, wsUUID)

	summons, err := h.Queries.ListOpenIssueSummonsForRecipient(ctx, db.ListOpenIssueSummonsForRecipientParams{
		WorkspaceID: wsUUID, RecipientID: userUUID,
	})
	if err != nil {
		return in, 0, fmt.Errorf("failed to list summons")
	}
	for _, s := range summons {
		if !viewer.canSeeIssueFields(s.IssueID, s.IssueVisibility, s.IssueCreatorType, s.IssueCreatorID,
			s.IssueProjectID, s.IssueAssigneeType, s.IssueAssigneeID) {
			continue
		}
		in.Summons = append(in.Summons, inboxboard.Summon{
			IssueID:     uuidToString(s.IssueID),
			Identifier:  issueIdentifier(prefix, s.IssueNumber),
			IssueTitle:  s.IssueTitle,
			IssueStatus: s.IssueStatus,
			CallerType:  s.CallerType,
			CallerID:    uuidToString(s.CallerID),
			CallerName:  s.CallerName,
			Source:      s.Source,
			Reason:      s.Reason,
			CreatedAt:   s.CreatedAt.Time,
		})
	}

	parking, err := h.Queries.ListWorkspaceParkingRecords(ctx, db.ListWorkspaceParkingRecordsParams{
		WorkspaceID: wsUUID, RowLimit: inboxBoardParkingLimit,
	})
	if err != nil {
		return in, 0, fmt.Errorf("failed to list parking records")
	}

	snapshot, err := h.Queries.ListWorkspaceAgentTaskSnapshot(ctx, wsUUID)
	if err != nil {
		return in, 0, fmt.Errorf("failed to list agent tasks")
	}
	allowed, ok := h.accessibleAgentIDs(ctx, uuidToString(wsUUID), "member", uuidToString(userUUID), role)
	if !ok {
		return in, 0, fmt.Errorf("failed to resolve agent access")
	}
	var runningIDs []pgtype.UUID
	seenRunning := map[string]bool{}
	for _, t := range snapshot {
		if _, ok := allowed[uuidToString(t.AgentID)]; !ok {
			continue
		}
		in.Tasks = append(in.Tasks, inboxboard.Task{
			IssueID:      uuidToString(t.IssueID),
			AgentID:      uuidToString(t.AgentID),
			Status:       t.Status,
			StartedAt:    t.StartedAt.Time,
			DispatchedAt: t.DispatchedAt.Time,
			CreatedAt:    t.CreatedAt.Time,
		})
		if id := uuidToString(t.IssueID); t.IssueID.Valid && (t.Status == "running" || t.Status == "dispatched") && !seenRunning[id] {
			seenRunning[id] = true
			runningIDs = append(runningIDs, t.IssueID)
		}
	}

	// One visibility lookup covers the running issues and the parked ones:
	// a parking record is only on the board if its issue is visible.
	lookup := append([]pgtype.UUID(nil), runningIDs...)
	for _, p := range parking {
		lookup = append(lookup, p.IssueID)
	}
	visible := map[string]db.ListInboxBoardIssuesByIDsRow{}
	if len(lookup) > 0 {
		rows, err := h.Queries.ListInboxBoardIssuesByIDs(ctx, db.ListInboxBoardIssuesByIDsParams{WorkspaceID: wsUUID, Ids: lookup})
		if err != nil {
			return in, 0, fmt.Errorf("failed to list issues")
		}
		for _, row := range rows {
			if viewer.canSeeIssueFields(row.ID, row.Visibility, row.CreatorType, row.CreatorID,
				row.ProjectID, row.AssigneeType, row.AssigneeID) {
				visible[uuidToString(row.ID)] = row
			}
		}
	}
	for _, p := range parking {
		if _, ok := visible[uuidToString(p.IssueID)]; !ok {
			continue
		}
		in.Parking = append(in.Parking, inboxboard.Parking{
			IssueID:        uuidToString(p.IssueID),
			Identifier:     issueIdentifier(prefix, p.Number),
			Title:          p.Title,
			ParentIssueID:  uuidToString(p.ParentIssueID),
			CurrentStatus:  p.CurrentStatus,
			RecordedStatus: p.RecordedStatus,
			Category:       p.Category,
			StuckKind:      p.StuckKind,
			Unexplained:    p.Unexplained,
			Summary:        p.Summary,
			SummarySource:  p.SummarySource,
			NextOwner:      inboxboard.Owner{Type: p.NextOwnerType, ID: p.NextOwnerID},
			Timeline:       p.Timeline,
			EvaluatedAt:    p.EvaluatedAt.Time,
		})
	}
	for _, id := range runningIDs {
		row, ok := visible[uuidToString(id)]
		if !ok {
			continue
		}
		in.RunningIssues = append(in.RunningIssues, inboxboard.Issue{
			ID:            uuidToString(row.ID),
			Identifier:    issueIdentifier(prefix, row.Number),
			Title:         row.Title,
			Status:        row.Status,
			ParentIssueID: uuidToString(row.ParentIssueID),
			UpdatedAt:     row.UpdatedAt.Time,
		})
	}

	todo, err := h.Queries.ListInboxBoardTodoIssues(ctx, db.ListInboxBoardTodoIssuesParams{
		WorkspaceID: wsUUID, UserID: userUUID,
	})
	if err != nil {
		return in, 0, fmt.Errorf("failed to list todo issues")
	}
	for _, row := range todo {
		if len(in.TodoIssues) >= inboxBoardTodoLimit {
			break
		}
		if !viewer.canSeeIssueFields(row.ID, row.Visibility, row.CreatorType, row.CreatorID,
			row.ProjectID, row.AssigneeType, row.AssigneeID) {
			continue
		}
		in.TodoIssues = append(in.TodoIssues, inboxboard.Issue{
			ID:            uuidToString(row.ID),
			Identifier:    issueIdentifier(prefix, row.Number),
			Title:         row.Title,
			Status:        row.Status,
			ParentIssueID: uuidToString(row.ParentIssueID),
			AssigneeType:  row.AssigneeType,
			AssigneeID:    uuidToString(row.AssigneeID),
			UpdatedAt:     row.UpdatedAt.Time,
		})
	}

	done, err := h.Queries.ListInboxBoardDoneIssues(ctx, db.ListInboxBoardDoneIssuesParams{
		WorkspaceID: wsUUID,
		DayStart:    pgtype.Timestamptz{Time: dayStart, Valid: true},
		DayEnd:      pgtype.Timestamptz{Time: dayStart.AddDate(0, 0, 1), Valid: true},
	})
	if err != nil {
		return in, 0, fmt.Errorf("failed to list done issues")
	}
	for _, row := range done {
		if len(in.DoneIssues) >= inboxBoardDoneLimit {
			break
		}
		if !viewer.canSeeIssueFields(row.ID, row.Visibility, row.CreatorType, row.CreatorID,
			row.ProjectID, row.AssigneeType, row.AssigneeID) {
			continue
		}
		in.DoneIssues = append(in.DoneIssues, inboxboard.Issue{
			ID:            uuidToString(row.ID),
			Identifier:    issueIdentifier(prefix, row.Number),
			Title:         row.Title,
			Status:        row.Status,
			ParentIssueID: uuidToString(row.ParentIssueID),
			UpdatedAt:     row.UpdatedAt.Time,
		})
	}

	unread, err := h.Queries.ListUnreadInboxIssues(ctx, db.ListUnreadInboxIssuesParams{
		WorkspaceID: wsUUID, RecipientID: userUUID, UnreadSince: unreadSince,
	})
	if err != nil {
		return in, 0, fmt.Errorf("failed to list unread inbox issues")
	}
	var markable int64
	for _, u := range unread {
		if !u.PersonalOnly && !viewer.canSeeIssueFields(u.IssueID, u.IssueVisibility, u.IssueCreatorType,
			u.IssueCreatorID, u.IssueProjectID, u.IssueAssigneeType, u.IssueAssigneeID) {
			continue
		}
		markable += u.UnreadCount - u.HeldCount
		in.Unread = append(in.Unread, inboxboard.Unread{
			IssueID:       uuidToString(u.IssueID),
			Identifier:    issueIdentifier(prefix, u.IssueNumber),
			Title:         u.IssueTitle,
			ParentIssueID: uuidToString(u.IssueParentIssueID),
			UnreadCount:   u.UnreadCount,
			LatestAt:      u.LatestAt.Time,
		})
	}
	return in, markable, nil
}

// inboxBoardNamer names the members, agents and squads rows point at. A name
// that cannot be loaded stays empty; the id is still on the row.
func (h *Handler) inboxBoardNamer(ctx context.Context, wsUUID pgtype.UUID) func(inboxboard.Owner) string {
	names := map[string]string{}
	if members, err := h.Queries.ListMembersWithUser(ctx, wsUUID); err == nil {
		for _, m := range members {
			names["member:"+uuidToString(m.UserID)] = m.UserName
		}
	}
	if agents, err := h.Queries.ListAllAgents(ctx, wsUUID); err == nil {
		for _, a := range agents {
			names["agent:"+uuidToString(a.ID)] = a.Name
		}
	}
	if squads, err := h.Queries.ListSquads(ctx, wsUUID); err == nil {
		for _, s := range squads {
			names["squad:"+uuidToString(s.ID)] = s.Name
		}
	}
	return func(o inboxboard.Owner) string {
		if o.Type == "issue" {
			return o.ID
		}
		return names[o.Type+":"+o.ID]
	}
}
