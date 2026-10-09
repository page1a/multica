package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/statecard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// "听汇报" (DENE-1667): a project's news since a person last heard it.
//
// Where a person has heard up to is kept per person + project, never per chat:
// hearing a project in one chat, another chat or on the project page moves one
// cursor (project_report_heard). The server decides what counts as news — every
// issue of the project whose status moved, or that was opened, after the
// cursor — so `multica project report`, the project page and the chat's
// progress bar all read the same set.
//
// Marking heard (POST …/report/heard, `--mark-heard`) records the cursor,
// reads the person's inbox rows on the covered issues, and — when a chat run
// marks it — gives that reply its follow-up buttons from the issues waiting on
// the person (see TaskService.chatReportQuickActions).

// projectReportFirstWindow is how far back a person's first report looks.
const projectReportFirstWindow = 7 * 24 * time.Hour

type ProjectReportSourceChat struct {
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Accessible bool   `json:"accessible"`
}

type ProjectReportItem struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	// Gist is the first plain line of the description (its 目标 line when it
	// has one): what the issue is for, in the author's words.
	Gist string `json:"gist,omitempty"`
	// FromStatus is the status before the first move inside the window; empty
	// for an issue opened inside it (Opened).
	FromStatus string `json:"from_status,omitempty"`
	// LatestSummary is the --summary of the newer of the issue's last close
	// and last handoff: what got done, where it is stuck, who must do what.
	// Omitted when that close or handoff carried none.
	LatestSummary string `json:"latest_summary,omitempty"`
	Opened        bool   `json:"opened"`
	ChangedAt     string `json:"changed_at"`
	// Phase is the report's three buckets: done, in_progress or waiting_you.
	Phase      string                   `json:"phase"`
	NeedsYou   bool                     `json:"needs_you"`
	SourceChat *ProjectReportSourceChat `json:"source_chat,omitempty"`
}

type ProjectReportCounts struct {
	Total      int `json:"total"`
	Done       int `json:"done"`
	InProgress int `json:"in_progress"`
	WaitingYou int `json:"waiting_you"`
}

type ProjectReportResponse struct {
	ProjectID    string `json:"project_id"`
	ProjectTitle string `json:"project_title"`
	// Since/Until bound the news: (since, until].
	Since string `json:"since"`
	Until string `json:"until"`
	// LastHeardAt is when the person last heard this project; nil the first
	// time, when Since falls back to a week ago.
	LastHeardAt *string                    `json:"last_heard_at"`
	Items       []ProjectReportItem        `json:"items"`
	Counts      ProjectReportCounts        `json:"counts"`
	Actions     []protocol.ChatQuickAction `json:"actions"`
	Marked      bool                       `json:"marked"`
	InboxRead   int                        `json:"inbox_read"`
}

const (
	projectReportPhaseDone       = "done"
	projectReportPhaseInProgress = "in_progress"
	projectReportPhaseWaitingYou = "waiting_you"
)

// projectReportPhaseOf buckets an issue for the person: finished is done, one
// assigned to them or with their unread call on it waits on them, the rest is
// in progress. The report and the chat's progress bar share it.
func projectReportPhaseOf(terminal, onPerson bool) (phase string, needsYou bool) {
	switch {
	case terminal:
		return projectReportPhaseDone, false
	case onPerson:
		return projectReportPhaseWaitingYou, true
	default:
		return projectReportPhaseInProgress, false
	}
}

// GetProjectReport answers what is new in a project since the caller last
// heard it, without moving the cursor.
func (h *Handler) GetProjectReport(w http.ResponseWriter, r *http.Request) {
	h.serveProjectReport(w, r, false)
}

// MarkProjectReportHeard builds the same report as of now and records it as
// heard: the cursor moves to its end and the covered inbox rows are read.
func (h *Handler) MarkProjectReportHeard(w http.ResponseWriter, r *http.Request) {
	h.serveProjectReport(w, r, true)
}

func (h *Handler) serveProjectReport(w http.ResponseWriter, r *http.Request, mark bool) {
	ctx := r.Context()
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	person, task, ok := h.projectReportPerson(w, r)
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerForUser(ctx, wsUUID, person)
	if err != nil {
		writeError(w, http.StatusForbidden, "the person this report is for is not a member of this workspace")
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectUUID, WorkspaceID: wsUUID})
	if err != nil || !viewer.canSeeProject(project) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	report, issueIDs, err := h.buildProjectReport(ctx, wsUUID, project, person, viewer, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build project report")
		return
	}
	if mark {
		if err := h.recordProjectReportHeard(ctx, wsUUID, person, task, &report, issueIDs); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record the report as heard")
			return
		}
	}
	writeJSON(w, http.StatusOK, report)
}

// projectReportPerson is whose cursor a request reads and moves. A person
// calls for themselves. A run calls for the human it works for — the task's
// originator, else the creator of the chat it runs in — so `multica project
// report --mark-heard` in Kun's chat moves Kun's cursor, not the runtime
// owner's.
func (h *Handler) projectReportPerson(w http.ResponseWriter, r *http.Request) (pgtype.UUID, *db.AgentTaskQueue, bool) {
	if task, ok := h.taskFromRequestHeader(r); ok {
		if task.OriginatorUserID.Valid {
			return task.OriginatorUserID, &task, true
		}
		if task.ChatSessionID.Valid {
			if cs, err := h.Queries.GetChatSession(r.Context(), task.ChatSessionID); err == nil && cs.CreatorID.Valid {
				return cs.CreatorID, &task, true
			}
		}
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, nil, false
	}
	return parseUUID(userID), nil, true
}

func (h *Handler) buildProjectReport(ctx context.Context, wsUUID pgtype.UUID, project db.Project, person pgtype.UUID, viewer visibilityViewer, now time.Time) (ProjectReportResponse, []pgtype.UUID, error) {
	report := ProjectReportResponse{
		ProjectID:    uuidToString(project.ID),
		ProjectTitle: project.Title,
		Items:        []ProjectReportItem{},
		Actions:      []protocol.ChatQuickAction{},
	}
	since := now.Add(-projectReportFirstWindow)
	cursor, err := h.Queries.GetProjectReportCursor(ctx, db.GetProjectReportCursorParams{WorkspaceID: wsUUID, UserID: person, ProjectID: project.ID})
	switch {
	case err == nil:
		since = cursor.HeardUntil.Time
		heard := cursor.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		report.LastHeardAt = &heard
	case !errors.Is(err, pgx.ErrNoRows):
		return report, nil, err
	}
	report.Since = since.UTC().Format(time.RFC3339Nano)
	report.Until = now.UTC().Format(time.RFC3339Nano)
	window := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

	changes, err := h.Queries.ListProjectReportStatusChanges(ctx, db.ListProjectReportStatusChangesParams{
		WorkspaceID: wsUUID, ProjectID: project.ID, Since: window(since), Until: window(now),
	})
	if err != nil {
		return report, nil, err
	}
	opened, err := h.Queries.ListProjectReportCreatedIssues(ctx, db.ListProjectReportCreatedIssuesParams{
		WorkspaceID: wsUUID, ProjectID: project.ID, Since: window(since), Until: window(now),
	})
	if err != nil {
		return report, nil, err
	}

	type move struct {
		from    string
		opened  bool
		changed time.Time
	}
	moves := map[string]*move{}
	var order []pgtype.UUID
	touch := func(id pgtype.UUID) *move {
		key := uuidToString(id)
		if m, ok := moves[key]; ok {
			return m
		}
		m := &move{}
		moves[key] = m
		order = append(order, id)
		return m
	}
	for _, row := range opened {
		m := touch(row.ID)
		m.opened = true
		m.changed = row.CreatedAt.Time
	}
	for _, row := range changes {
		m := touch(row.IssueID)
		if !m.opened && m.from == "" {
			m.from = row.FromStatus
		}
		if row.CreatedAt.Time.After(m.changed) {
			m.changed = row.CreatedAt.Time
		}
	}
	if len(order) == 0 {
		return report, nil, nil
	}

	rows, err := h.Queries.ListProjectReportIssues(ctx, db.ListProjectReportIssuesParams{UserID: person, WorkspaceID: wsUUID, IssueIds: order})
	if err != nil {
		return report, nil, err
	}
	terminal, err := h.terminalIssueStatusKeys(ctx, wsUUID)
	if err != nil {
		return report, nil, err
	}
	isTerminal := map[string]bool{}
	for _, key := range terminal {
		isTerminal[key] = true
	}
	prefix := h.getIssuePrefix(ctx, wsUUID)
	chats := h.projectReportChatNamer(ctx, wsUUID, uuidToString(person))
	personID := uuidToString(person)

	var covered []pgtype.UUID
	for _, row := range rows {
		if !viewer.canSeeIssueFields(row.ID, row.Visibility, row.CreatorType, row.CreatorID, row.ProjectID, row.AssigneeType.String, row.AssigneeID) {
			continue
		}
		m := moves[uuidToString(row.ID)]
		// A status that went away and came back inside the window is no news.
		if m == nil || (!m.opened && m.from == row.Status) {
			continue
		}
		covered = append(covered, row.ID)
		item := ProjectReportItem{
			IssueID:    uuidToString(row.ID),
			Identifier: issueIdentifier(prefix, row.Number),
			Title:      row.Title,
			Status:     row.Status,
			Priority:   row.Priority,
			Gist:       projectReportGist(row.Description.String),
			FromStatus: m.from,
			Opened:     m.opened,
			ChangedAt:  m.changed.UTC().Format(time.RFC3339Nano),
		}
		item.LatestSummary = h.projectReportSummary(ctx, wsUUID, row.ID, issueMetaStrings(row.Metadata))
		if row.SourceChatID.Valid {
			item.SourceChat = chats(row.SourceChatID)
		}
		assignedToPerson := row.AssigneeType.String == "member" && uuidToString(row.AssigneeID) == personID
		item.Phase, item.NeedsYou = projectReportPhaseOf(isTerminal[row.Status], row.HasOpenCall || assignedToPerson)
		report.Items = append(report.Items, item)
	}

	phaseRank := map[string]int{projectReportPhaseWaitingYou: 0, projectReportPhaseInProgress: 1, projectReportPhaseDone: 2}
	sort.SliceStable(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if phaseRank[a.Phase] != phaseRank[b.Phase] {
			return phaseRank[a.Phase] < phaseRank[b.Phase]
		}
		return a.ChangedAt > b.ChangedAt
	})
	for _, item := range report.Items {
		report.Counts.Total++
		switch item.Phase {
		case projectReportPhaseDone:
			report.Counts.Done++
		case projectReportPhaseWaitingYou:
			report.Counts.WaitingYou++
		default:
			report.Counts.InProgress++
		}
	}
	report.Actions = projectReportActions(report.Items)
	return report, covered, nil
}

// projectReportActions are the buttons under a report: settle what waits on
// the person, otherwise speed up what is moving, and always "都知道了". Three at
// most — the chat renders three.
func projectReportActions(items []ProjectReportItem) []protocol.ChatQuickAction {
	var waiting, moving []ProjectReportItem
	for _, item := range items {
		switch item.Phase {
		case projectReportPhaseWaitingYou:
			waiting = append(waiting, item)
		case projectReportPhaseInProgress:
			moving = append(moving, item)
		}
	}
	var actions []protocol.ChatQuickAction
	// A ticket in review is the person's verdict to give; anything else
	// waiting on them (stuck, assigned, an open call) first needs the ask.
	settle := func(item ProjectReportItem) protocol.ChatQuickAction {
		if item.Status == "in_review" {
			return protocol.ChatQuickAction{
				Label:  item.Identifier + " 看过了，没问题",
				Prompt: item.Identifier + " 看过了，没问题。把我的结论记到这张票上，按通过推进。",
			}
		}
		return protocol.ChatQuickAction{
			Label:  item.Identifier + " 卡在哪",
			Prompt: item.Identifier + " 卡在哪？讲清楚要我做什么，我回答后记到票上。",
		}
	}
	switch {
	case len(waiting) == 1 && waiting[0].Status == "in_review":
		actions = append(actions, settle(waiting[0]), protocol.ChatQuickAction{
			Label:  waiting[0].Identifier + " 要改",
			Prompt: waiting[0].Identifier + " 要改。先问我改什么，再把意见记到这张票上叫回执行人。",
		})
	case len(waiting) == 1:
		actions = append(actions, settle(waiting[0]))
	case len(waiting) > 1:
		actions = append(actions, settle(waiting[0]), settle(waiting[1]))
	case len(moving) > 0:
		actions = append(actions, protocol.ChatQuickAction{
			Label:  moving[0].Identifier + " 加急",
			Prompt: moving[0].Identifier + " 加急。把这张票的优先级调到紧急，并在票上说明是我要求的。",
		})
	}
	if len(items) > 0 {
		actions = append(actions, protocol.ChatQuickAction{Label: service.ChatReportAckLabel, Prompt: "都知道了，这些不用再处理。"})
	}
	if len(actions) > 0 {
		actions[0].Primary = true
	}
	return actions
}

// recordProjectReportHeard moves the person's cursor to the report's end and
// reads their inbox rows on the covered issues up to that moment.
func (h *Handler) recordProjectReportHeard(ctx context.Context, wsUUID, person pgtype.UUID, task *db.AgentTaskQueue, report *ProjectReportResponse, covered []pgtype.UUID) error {
	since, _ := time.Parse(time.RFC3339Nano, report.Since)
	until, _ := time.Parse(time.RFC3339Nano, report.Until)
	actions, err := json.Marshal(report.Actions)
	if err != nil {
		return err
	}
	params := db.InsertProjectReportHeardParams{
		WorkspaceID: wsUUID,
		UserID:      person,
		ProjectID:   parseUUID(report.ProjectID),
		HeardSince:  pgtype.Timestamptz{Time: since, Valid: true},
		HeardUntil:  pgtype.Timestamptz{Time: until, Valid: true},
		ItemCount:   int32(len(report.Items)),
		Actions:     actions,
	}
	if task != nil {
		params.TaskID = task.ID
		params.ChatSessionID = task.ChatSessionID
	}
	if _, err := h.Queries.InsertProjectReportHeard(ctx, params); err != nil {
		return err
	}
	report.Marked = true
	if len(covered) == 0 {
		return nil
	}
	read, err := h.Queries.MarkProjectReportInboxRead(ctx, db.MarkProjectReportInboxReadParams{
		WorkspaceID: wsUUID, UserID: person, IssueIds: covered,
		Until: pgtype.Timestamptz{Time: until, Valid: true},
	})
	if err != nil {
		return err
	}
	report.InboxRead = len(read)
	if len(read) > 0 {
		personID := uuidToString(person)
		h.publish(protocol.EventInboxBatchRead, uuidToString(wsUUID), "member", personID, map[string]any{
			"recipient_id": personID,
			"count":        len(read),
		})
	}
	return nil
}

// projectReportChatNamer names source chats as the person sees them: a chat
// they cannot open is still a chat, without its title.
func (h *Handler) projectReportChatNamer(ctx context.Context, wsUUID pgtype.UUID, personID string) func(pgtype.UUID) *ProjectReportSourceChat {
	cache := map[string]*ProjectReportSourceChat{}
	return func(id pgtype.UUID) *ProjectReportSourceChat {
		key := uuidToString(id)
		if chat, ok := cache[key]; ok {
			return chat
		}
		chat := &ProjectReportSourceChat{ID: key}
		if session, err := h.Queries.GetChatSessionInWorkspace(ctx, db.GetChatSessionInWorkspaceParams{ID: id, WorkspaceID: wsUUID}); err == nil {
			if access, err := h.chatAccessFor(ctx, session, personID); err == nil && access.see {
				chat.Title = session.Title
				chat.Accessible = true
			}
		}
		cache[key] = chat
		return chat
	}
}

// projectReportGist is the line an issue is for: its 目标 line when the
// description has one, else the first plain line.
func projectReportGist(description string) string {
	lines := strings.Split(description, "\n")
	pick := ""
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(strings.TrimLeft(line, "#* "), "目标") {
			rest := strings.TrimSpace(strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(line, "#* "), "目标"), "*:： "))
			if rest == "" && i+1 < len(lines) {
				for _, next := range lines[i+1:] {
					if next = cleanProjectReportLine(next); next != "" {
						rest = next
						break
					}
				}
			}
			if rest = cleanProjectReportLine(rest); rest != "" {
				return rest
			}
		}
		if pick == "" {
			pick = cleanProjectReportLine(line)
		}
	}
	return pick
}

// projectReportSummary is an issue's latest conclusion line, word for word
// from statecard.KeyLatestSummary. Only issues closed before that key existed
// read the close's progress line, and only when it belongs to that close —
// the evidence never stands in for it.
func (h *Handler) projectReportSummary(ctx context.Context, wsUUID, issueID pgtype.UUID, meta map[string]string) string {
	var closeSummary string
	if _, stored := meta[statecard.KeyLatestSummary]; !stored && closeprotocol.Complete(meta) {
		row, err := h.Queries.GetLatestIssueProgressBySource(ctx, db.GetLatestIssueProgressBySourceParams{
			IssueID: issueID, WorkspaceID: wsUUID, Source: progress.SourceClose,
		})
		if err == nil && row.CreatedAt.Valid && statecard.SummaryBelongsToClose(row.CreatedAt.Time, meta[closeprotocol.KeyAt]) {
			closeSummary = row.Text
		}
	}
	return statecard.LatestSummary(meta, closeSummary)
}

func cleanProjectReportLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "<") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimLeft(line, "-*>0123456789.) "))
	line = strings.Trim(line, "*_ ")
	if r := []rune(line); len(r) > 120 {
		line = string(r[:120]) + "…"
	}
	return line
}
