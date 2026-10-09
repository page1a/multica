package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/receipt"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The project memory monitor (DENE-1681) is one read over what DENE-1661,
// DENE-1672 and DENE-1680 already record, for one project and one window:
// what deliveries wrote into and deleted from project memory, which finished
// tickets wrote nothing, whether sediment rounds are idling, and whether the
// tickets chats dispatched have reported back. The project page and
// `multica project memory monitor` read the same response. Everything is
// filtered for the person it is for: a ticket or chat they cannot see is
// left out, never named.

const (
	monitorDefaultDays = 14
	monitorMaxDays     = 90
	monitorWriteLimit  = 20
	monitorRowLimit    = 20
	monitorChatLimit   = 100
)

// MonitorWrite is a sediment with what it removed: the entries it marked
// superseded and the lines git saw deleted from the memory files.
type MonitorWrite struct {
	KnowledgeSedimentResponse
	// SourceAccessible is false for a chat sediment whose chat the person
	// cannot open: the row still counts, its title is withheld.
	SourceAccessible bool `json:"source_accessible"`
	// Superseded lists the entries marked 「已被 X 取代」.
	Superseded []string `json:"superseded"`
	// DeletedLines is the deleted-line total over the memory files; nil when
	// the delivery predates the count (an older CLI or a web close).
	DeletedLines *int `json:"deleted_lines"`
}

// MonitorUnsettled is a ticket that finished in the window without writing
// project memory. Reason is none (its close declared nothing qualified) or
// unaudited (it finished without a knowledge audit).
type MonitorUnsettled struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Reason     string `json:"reason"`
	ClosedAt   string `json:"closed_at"`
}

// MonitorRound is one sediment round. Idle is a round that ended without a
// sediment: it ran, and project memory did not change.
type MonitorRound struct {
	IssueID    string   `json:"issue_id"`
	Identifier string   `json:"identifier"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Layer      string   `json:"layer"`
	Gap        []string `json:"gap"`
	Wrote      bool     `json:"wrote"`
	Idle       bool     `json:"idle"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

type MonitorRounds struct {
	Opened int            `json:"opened"`
	Open   int            `json:"open"`
	Idle   int            `json:"idle"`
	Items  []MonitorRound `json:"items"`
}

// MonitorChatTicket is one dispatched ticket and where its receipt stands:
// open (nothing to report yet), reported (its card carries a conclusion), or
// no_conclusion (it reached a reportable status without an `issue close`,
// so its card said only the status).
type MonitorChatTicket struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Flow       string `json:"flow"`
	UpdatedAt  string `json:"updated_at"`
}

// MonitorChat is one chat's dispatched tickets in this project.
type MonitorChat struct {
	ChatSessionID string              `json:"chat_session_id"`
	Title         string              `json:"title,omitempty"`
	Accessible    bool                `json:"accessible"`
	Dispatched    int                 `json:"dispatched"`
	Reported      int                 `json:"reported"`
	NoConclusion  int                 `json:"no_conclusion"`
	Open          int                 `json:"open"`
	Tickets       []MonitorChatTicket `json:"tickets"`
}

type ProjectMemoryMonitorResponse struct {
	ProjectID string             `json:"project_id"`
	Days      int                `json:"days"`
	Since     string             `json:"since"`
	Writes    []MonitorWrite     `json:"writes"`
	Unsettled []MonitorUnsettled `json:"unsettled"`
	Rounds    MonitorRounds      `json:"rounds"`
	Chats     []MonitorChat      `json:"chats"`
}

const (
	monitorFlowOpen         = "open"
	monitorFlowReported     = "reported"
	monitorFlowNoConclusion = "no_conclusion"
)

func monitorDays(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return monitorDefaultDays, true
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > monitorMaxDays {
		return 0, false
	}
	return days, true
}

// GetProjectMemoryMonitor is GET /api/projects/{id}/memory/monitor?days=N.
func (h *Handler) GetProjectMemoryMonitor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days, ok := monitorDays(r.URL.Query().Get("days"))
	if !ok {
		writeError(w, http.StatusBadRequest, "days must be a whole number from 1 to 90")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "project id")
	if !ok {
		return
	}
	// The same person a project report is for: the human a task acts for,
	// or the caller.
	person, _, ok := h.projectReportPerson(w, r)
	if !ok {
		return
	}
	viewer, err := h.visibilityViewerForUser(ctx, wsUUID, person)
	if err != nil {
		writeError(w, http.StatusForbidden, "the person this monitor is for is not a member of this workspace")
		return
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: projectUUID, WorkspaceID: wsUUID})
	if err != nil || !viewer.canSeeProject(project) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	since := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	writeJSON(w, http.StatusOK, h.buildProjectMemoryMonitor(ctx, project, uuidToString(person), viewer, days, since))
}

func (h *Handler) buildProjectMemoryMonitor(ctx context.Context, project db.Project, person string, viewer visibilityViewer, days int, since time.Time) ProjectMemoryMonitorResponse {
	sinceTS := pgtype.Timestamptz{Time: since, Valid: true}
	prefix := h.getIssuePrefix(ctx, project.WorkspaceID)
	chats := newMonitorChatAccess(h, person)
	return ProjectMemoryMonitorResponse{
		ProjectID: uuidToString(project.ID),
		Days:      days,
		Since:     since.Format(time.RFC3339),
		Writes:    h.monitorWrites(ctx, project, sinceTS, prefix, viewer, chats),
		Unsettled: h.monitorUnsettled(ctx, project, sinceTS, prefix, viewer),
		Rounds:    h.monitorRounds(ctx, project, sinceTS, prefix, viewer),
		Chats:     h.monitorChats(ctx, project, sinceTS, prefix, viewer, chats),
	}
}

// monitorChatAccess caches whether the person can open each chat.
type monitorChatAccess struct {
	h      *Handler
	person string
	seen   map[pgtype.UUID]*db.ChatSession
}

func newMonitorChatAccess(h *Handler, person string) *monitorChatAccess {
	return &monitorChatAccess{h: h, person: person, seen: map[pgtype.UUID]*db.ChatSession{}}
}

// open returns the chat when the person can see it, nil otherwise.
func (c *monitorChatAccess) open(ctx context.Context, id pgtype.UUID) *db.ChatSession {
	if session, ok := c.seen[id]; ok {
		return session
	}
	var visible *db.ChatSession
	if session, err := c.h.Queries.GetChatSession(ctx, id); err == nil {
		if access, err := c.h.chatAccessFor(ctx, session, c.person); err == nil && access.see {
			visible = &session
		}
	}
	c.seen[id] = visible
	return visible
}

func (h *Handler) monitorWrites(ctx context.Context, project db.Project, since pgtype.Timestamptz, prefix string, viewer visibilityViewer, chats *monitorChatAccess) []MonitorWrite {
	out := []MonitorWrite{}
	rows, err := h.Queries.ListProjectKnowledgeSedimentsSince(ctx, db.ListProjectKnowledgeSedimentsSinceParams{
		ProjectID: project.ID, WorkspaceID: project.WorkspaceID, Since: since, RowLimit: monitorWriteLimit,
	})
	if err != nil {
		slog.Warn("memory monitor: list sediments failed", "error", err, "project_id", uuidToString(project.ID))
		return out
	}
	for _, row := range rows {
		item := MonitorWrite{
			KnowledgeSedimentResponse: sedimentToResponse(db.KnowledgeSediment{
				ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, IssueID: row.IssueID,
				ChatSessionID: row.ChatSessionID, Changes: row.Changes, Verified: row.Verified, Mainline: row.Mainline,
				Commits: row.Commits, PrUrl: row.PrUrl, AuthorType: row.AuthorType, AuthorID: row.AuthorID, CreatedAt: row.CreatedAt,
				Layer: row.Layer,
			}),
			SourceAccessible: true,
			Superseded:       []string{},
		}
		if row.IssueID.Valid {
			issue, err := h.Queries.GetIssue(ctx, row.IssueID)
			if err != nil || !viewer.canSeeIssue(issue) {
				continue
			}
			identifier := issueIdentifier(prefix, row.IssueNumber.Int32)
			item.IssueIdentifier = &identifier
			item.SourceTitle = row.IssueTitle
		} else if chats.open(ctx, row.ChatSessionID) != nil {
			item.SourceTitle = row.ChatTitle
		} else {
			item.SourceAccessible = false
		}
		item.Sources = h.resolveSedimentSources(ctx, row.WorkspaceID, row.Sources, &viewer)
		for _, change := range item.Changes {
			if change.Action == closeprotocol.ActionSupersede && change.Entry != "" {
				item.Superseded = append(item.Superseded, change.Entry)
			}
		}
		item.DeletedLines = deletedLines(row.MemoryFiles)
		out = append(out, item)
	}
	return out
}

// deletedLines sums the deleted lines git reported; nil when the row
// carries no memory-file facts.
func deletedLines(raw []byte) *int {
	var files []closeprotocol.MemoryFile
	if json.Unmarshal(raw, &files) != nil || len(files) == 0 {
		return nil
	}
	total := 0
	for _, f := range files {
		total += f.Deleted
	}
	return &total
}

func (h *Handler) monitorUnsettled(ctx context.Context, project db.Project, since pgtype.Timestamptz, prefix string, viewer visibilityViewer) []MonitorUnsettled {
	out := []MonitorUnsettled{}
	issues, err := h.Queries.ListProjectDoneIssuesWithoutSediment(ctx, db.ListProjectDoneIssuesWithoutSedimentParams{
		WorkspaceID: project.WorkspaceID, ProjectID: project.ID, Since: since, RowLimit: monitorRowLimit,
	})
	if err != nil {
		slog.Warn("memory monitor: list unsettled failed", "error", err, "project_id", uuidToString(project.ID))
		return out
	}
	for _, issue := range issues {
		if !viewer.canSeeIssue(issue) {
			continue
		}
		meta := issueMetaStrings(issue.Metadata)
		reason := "unaudited"
		if audit, err := closeprotocol.ParseStoredKnowledgeAudit(meta[closeprotocol.KeyKnowledgeAudit]); err == nil && audit.None {
			reason = "none"
		}
		// close.at when the close recorded one, in the same format as every
		// other timestamp here; the row's last update otherwise.
		closedAt := timestampToString(issue.UpdatedAt)
		if at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(meta[closeprotocol.KeyAt])); err == nil {
			closedAt = at.In(time.Local).Format(time.RFC3339)
		}
		out = append(out, MonitorUnsettled{
			IssueID: uuidToString(issue.ID), Identifier: issueIdentifier(prefix, issue.Number),
			Title: issue.Title, Reason: reason, ClosedAt: closedAt,
		})
	}
	return out
}

func (h *Handler) monitorRounds(ctx context.Context, project db.Project, since pgtype.Timestamptz, prefix string, viewer visibilityViewer) MonitorRounds {
	out := MonitorRounds{Items: []MonitorRound{}}
	rows, err := h.Queries.ListProjectSedimentRounds(ctx, db.ListProjectSedimentRoundsParams{
		WorkspaceID: project.WorkspaceID, ProjectID: uuidToString(project.ID), Since: since, RowLimit: monitorRowLimit,
	})
	if err != nil {
		slog.Warn("memory monitor: list rounds failed", "error", err, "project_id", uuidToString(project.ID))
		return out
	}
	for _, row := range rows {
		issue := row.Issue
		if !viewer.canSeeIssue(issue) {
			continue
		}
		terminal := issue.Status == "done" || issue.Status == "cancelled"
		layer := sedimentLayerWorker
		if isBossRound(issue) {
			layer = sedimentLayerBoss
		}
		gap := sedimentIssueGap(issue)
		if gap == nil {
			gap = []string{}
		}
		item := MonitorRound{
			IssueID: uuidToString(issue.ID), Identifier: issueIdentifier(prefix, issue.Number),
			Title: issue.Title, Status: issue.Status, Layer: layer, Gap: gap,
			Wrote: row.Wrote, Idle: terminal && !row.Wrote,
			CreatedAt: timestampToString(issue.CreatedAt), UpdatedAt: timestampToString(issue.UpdatedAt),
		}
		if issue.CreatedAt.Valid && !issue.CreatedAt.Time.Before(since.Time) {
			out.Opened++
		}
		if !terminal {
			out.Open++
		}
		if item.Idle {
			out.Idle++
		}
		out.Items = append(out.Items, item)
	}
	return out
}

func (h *Handler) monitorChats(ctx context.Context, project db.Project, since pgtype.Timestamptz, prefix string, viewer visibilityViewer, chats *monitorChatAccess) []MonitorChat {
	out := []MonitorChat{}
	issues, err := h.Queries.ListProjectChatDispatchedIssues(ctx, db.ListProjectChatDispatchedIssuesParams{
		WorkspaceID: project.WorkspaceID, ProjectID: project.ID, Since: since, RowLimit: monitorChatLimit,
	})
	if err != nil {
		slog.Warn("memory monitor: list chat tickets failed", "error", err, "project_id", uuidToString(project.ID))
		return out
	}
	effective := h.childStatusResolver(ctx)
	byChat := map[pgtype.UUID]int{}
	for _, issue := range issues {
		if !viewer.canSeeIssue(issue) {
			continue
		}
		// A sub-task of a ticket from the same chat reports through its
		// parent's card, as postSourceChatReceipt does.
		if issue.ParentIssueID.Valid {
			if parent, err := h.Queries.GetIssue(ctx, issue.ParentIssueID); err == nil && parent.OriginChatSessionID == issue.OriginChatSessionID {
				continue
			}
		}
		idx, ok := byChat[issue.OriginChatSessionID]
		if !ok {
			chat := MonitorChat{ChatSessionID: uuidToString(issue.OriginChatSessionID), Tickets: []MonitorChatTicket{}}
			if session := chats.open(ctx, issue.OriginChatSessionID); session != nil {
				chat.Accessible = true
				chat.Title = session.Title
			}
			out = append(out, chat)
			idx = len(out) - 1
			byChat[issue.OriginChatSessionID] = idx
		}
		status := issue.Status
		if s, err := effective(issue); err == nil && s != "" {
			status = s
		}
		flow := monitorFlow(issue, status)
		chat := &out[idx]
		chat.Dispatched++
		switch flow {
		case monitorFlowReported:
			chat.Reported++
		case monitorFlowNoConclusion:
			chat.NoConclusion++
		default:
			chat.Open++
		}
		chat.Tickets = append(chat.Tickets, MonitorChatTicket{
			IssueID: uuidToString(issue.ID), Identifier: issueIdentifier(prefix, issue.Number),
			Title: issue.Title, Status: status, Flow: flow, UpdatedAt: timestampToString(issue.UpdatedAt),
		})
	}
	return out
}

// monitorFlow reads a dispatched ticket's receipt the way issueReceipt does:
// a conclusion counts only while the close still describes the status.
func monitorFlow(issue db.Issue, status string) string {
	if !receipt.Reportable(status) {
		return monitorFlowOpen
	}
	meta := issueMetaStrings(issue.Metadata)
	if closeprotocol.Complete(meta) && closeprotocol.StatusMatchesIssue(meta[closeprotocol.KeyStatus], status) && !closeprotocol.Superseded(meta) {
		return monitorFlowReported
	}
	return monitorFlowNoConclusion
}
