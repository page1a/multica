package workspacelink

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/permission"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// MaxPageSize matches the issue list's cap.
const MaxPageSize = 100

// The view DTO is the whitelist. Every field here is a decision to let that
// piece of source data out; TestViewDTOFieldSet pins the set, so adding one
// means changing that test on purpose. Never embed an existing issue/project
// response type here.

// LinkedView is everything a viewer gets for one link and one page.
type LinkedView struct {
	LinkID   string             `json:"link_id"`
	Source   LinkedWorkspace    `json:"source"`
	Projects []LinkedProject    `json:"projects"`
	Issues   []LinkedIssue      `json:"issues"`
	Next     *string            `json:"next_cursor"`
	Statuses []LinkedStatusName `json:"statuses"`
}

// LinkedWorkspace is the source as the viewer sees it.
type LinkedWorkspace struct {
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

// LinkedProject is one ticked project. ID is only a filter key for this view.
// Its project context (description, resources, memory line) is shared with it
// (DENE-1643).
type LinkedProject struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Icon   *string `json:"icon"`
	Status string  `json:"status"`
	Done   int64   `json:"done"`
	Total  int64   `json:"total"`
	ProjectContext
}

// LinkedIssue is the status fields of one issue. No UUID, no description, no
// link anywhere.
type LinkedIssue struct {
	Identifier        string  `json:"identifier"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	Priority          string  `json:"priority"`
	ProjectID         string  `json:"project_id"`
	AssigneeName      *string `json:"assignee_name"`
	AssigneeAvatarURL *string `json:"assignee_avatar_url"`
	DueDate           *string `json:"due_date"`
	UpdatedAt         string  `json:"updated_at"`
}

// LinkedStatusName resolves the source's status keys (it may have custom
// ones the viewer's workspace does not know) to a name and lifecycle category.
type LinkedStatusName struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Page selects one page of issues.
type Page struct {
	// ProjectID narrows to one ticked project; invalid means all of them.
	ProjectID pgtype.UUID
	// Cursor is the opaque next_cursor of the previous page.
	Cursor string
	Limit  int
}

// View is the only way source data leaves its workspace. viewerWS is the
// caller's current workspace (the request header); role is the caller's tier
// there — for an agent, the tier of the human its task runs as, so an agent
// sees exactly what that person sees.
//
// Every refusal — no link, the wrong side, still pending, a guest — is
// ErrNotFound. Nothing is cached: the link and its projects are read on every
// call, so a revoke or an unticked project takes effect on the next request.
func (s *Service) View(ctx context.Context, viewerWS pgtype.UUID, role permission.Role, linkID pgtype.UUID, page Page) (LinkedView, error) {
	link, side, err := s.load(ctx, s.q, viewerWS, linkID)
	if err != nil {
		return LinkedView{}, err
	}
	if !Decide(OpView, side, Actor{Role: role}) || link.Status != "active" {
		return LinkedView{}, ErrNotFound
	}
	source := link.SourceWorkspaceID

	ws, err := s.q.GetWorkspace(ctx, source)
	if err != nil {
		return LinkedView{}, err
	}
	terminal, err := issuestatus.ExpandCategories(ctx, s.q, source, []string{
		issuestatus.CategoryDone, issuestatus.CategoryClosed,
	})
	if err != nil {
		return LinkedView{}, err
	}
	projectRows, err := s.q.ListLinkedViewProjects(ctx, db.ListLinkedViewProjectsParams{
		TerminalStatusKeys: terminal, LinkID: link.ID, SourceWorkspaceID: source,
	})
	if err != nil {
		return LinkedView{}, err
	}
	view := LinkedView{
		LinkID:   util.UUIDToString(link.ID),
		Source:   LinkedWorkspace{Name: ws.Name, AvatarURL: util.TextToPtr(ws.AvatarUrl)},
		Projects: make([]LinkedProject, 0, len(projectRows)),
		Issues:   []LinkedIssue{},
		Statuses: []LinkedStatusName{},
	}
	fullRows, err := s.q.ListLinkedReferenceProjects(ctx, db.ListLinkedReferenceProjectsParams{
		LinkID: link.ID, SourceWorkspaceID: source, ProjectIds: []pgtype.UUID{},
	})
	if err != nil {
		return LinkedView{}, err
	}
	contexts, err := s.projectContexts(ctx, source, fullRows)
	if err != nil {
		return LinkedView{}, err
	}
	ticked := false
	for _, p := range projectRows {
		pc, ok := contexts[p.ID.Bytes]
		if !ok {
			pc = ProjectContext{Resources: []LinkedResource{}}
		}
		view.Projects = append(view.Projects, LinkedProject{
			ID: util.UUIDToString(p.ID), Title: p.Title, Icon: util.TextToPtr(p.Icon),
			Status: p.Status, Done: p.DoneCount, Total: p.TotalCount, ProjectContext: pc,
		})
		if page.ProjectID.Valid && p.ID == page.ProjectID {
			ticked = true
		}
	}
	if page.ProjectID.Valid && !ticked {
		// A project that is no longer on the link reads like one never was.
		return LinkedView{}, ErrNotFound
	}

	limit := page.Limit
	if limit <= 0 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	params := db.ListLinkedViewIssuesParams{
		LinkID: link.ID, SourceWorkspaceID: source, ProjectID: page.ProjectID,
		PageLimit: int32(limit + 1),
	}
	if page.Cursor != "" {
		at, number, err := decodeCursor(page.Cursor)
		if err != nil {
			return LinkedView{}, invalid("invalid cursor")
		}
		params.CursorUpdatedAt = pgtype.Timestamptz{Time: at, Valid: true}
		params.CursorNumber = number
	}
	rows, err := s.q.ListLinkedViewIssues(ctx, params)
	if err != nil {
		return LinkedView{}, err
	}
	if len(rows) > limit {
		last := rows[limit-1]
		next := encodeCursor(last.UpdatedAt.Time, last.Number)
		view.Next = &next
		rows = rows[:limit]
	}
	used := map[string]bool{}
	for _, r := range rows {
		issue := LinkedIssue{
			Identifier: fmt.Sprintf("%s-%d", r.IssuePrefix, r.Number),
			Title:      r.Title,
			Status:     r.Status,
			Priority:   r.Priority,
			ProjectID:  util.UUIDToString(r.ProjectID),
			UpdatedAt:  util.TimestampToString(r.UpdatedAt),
		}
		if r.AssigneeName != "" {
			name := r.AssigneeName
			issue.AssigneeName = &name
			issue.AssigneeAvatarURL = util.TextToPtr(r.AssigneeAvatarUrl)
		}
		if r.DueDate.Valid {
			d := r.DueDate.Time.Format("2006-01-02")
			issue.DueDate = &d
		}
		used[r.Status] = true
		view.Issues = append(view.Issues, issue)
	}

	if len(used) > 0 {
		entries, err := s.q.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{WorkspaceID: source, IncludeArchived: true})
		if err != nil {
			return LinkedView{}, err
		}
		for _, e := range entries {
			if used[e.Key] {
				view.Statuses = append(view.Statuses, LinkedStatusName{Key: e.Key, Name: e.Name, Category: e.Category})
			}
		}
	}
	return view, nil
}

// The cursor is (updated_at, issue number), base64'd. Issue numbers are
// already public through the identifier, so the cursor carries no UUID.
func encodeCursor(at time.Time, number int32) string {
	raw := at.UTC().Format(time.RFC3339Nano) + "|" + strconv.Itoa(int(number))
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, int32, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, 0, err
	}
	at, num, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, 0, fmt.Errorf("cursor: missing separator")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, 0, err
	}
	n, err := strconv.ParseInt(num, 10, 32)
	if err != nil {
		return time.Time{}, 0, err
	}
	return t, int32(n), nil
}
