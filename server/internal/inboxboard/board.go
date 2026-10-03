// Package inboxboard sorts one person's inbox into its six lanes (DENE-882,
// moved server-side in DENE-975 so the web page, the CLI and an agent reading
// the inbox for its user all get the same answer).
//
// Every issue lands in at most one lane, checked in this order:
//
//	waiting — someone called the viewer and the call is still open, or the
//	          issue's parking record names the viewer as the next owner;
//	stalled — the parking record says it stopped without explaining why;
//	running — an agent is on it right now;
//	todo    — it is assigned to the viewer and in todo (DENE-975);
//	done    — it was finished today;
//	fresh   — it has unread inbox rows for the viewer but none of the lanes
//	          above takes it (DENE-901).
//
// Every row also carries how many unread inbox rows the viewer has on it, so
// a client can mark what is new.
//
// Build only reads server verdicts. It never decides on its own that an issue
// is stuck: the category comes from the parking record, the call from the
// summon table. It is a pure function; the handler fetches its inputs.
package inboxboard

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type Lane string

const (
	LaneWaiting Lane = "waiting"
	LaneStalled Lane = "stalled"
	LaneRunning Lane = "running"
	LaneTodo    Lane = "todo"
	LaneFresh   Lane = "fresh"
	LaneDone    Lane = "done"
)

// Owner is who a row points at: an agent, a member, a squad, or (for a
// parking record waiting on another ticket) an issue identifier.
type Owner struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// Row is one issue on the board.
type Row struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	// Status is the issue's current lifecycle key. Clients use it to offer a
	// reversible board action without fetching every issue detail separately.
	Status        string  `json:"status"`
	ParentIssueID *string `json:"parent_issue_id"`
	Lane          Lane    `json:"lane"`
	// Kind is what put the row in its lane: the summon source or
	// waiting_person for waiting, the parking category for stalled, running /
	// fresh / done otherwise.
	Kind string `json:"kind"`
	// StuckKind is the parking record's stuck kind, when the lane came from one.
	StuckKind string `json:"stuck_kind"`
	// Reason is why it stopped, in the server's or the caller's words.
	Reason string `json:"reason"`
	// Before is what happened before it stopped, from the parking summary.
	Before string `json:"before"`
	// From is who raised the row: the caller of a summon.
	From     *Owner `json:"from"`
	FromName string `json:"from_name"`
	// Next holds the next move; for a running row, who is on it.
	Next     *Owner `json:"next"`
	NextName string `json:"next_name"`
	// At is the moment the row is about: call time, verdict time, start, finish.
	At       time.Time       `json:"at"`
	Timeline json.RawMessage `json:"timeline"`
	// Unread is the viewer's unread inbox rows on this issue.
	Unread   int64  `json:"unread"`
	Children []*Row `json:"children"`
}

// Board is the six lanes, each newest first.
type Board struct {
	Waiting []*Row `json:"waiting"`
	Stalled []*Row `json:"stalled"`
	Running []*Row `json:"running"`
	Todo    []*Row `json:"todo"`
	Fresh   []*Row `json:"fresh"`
	Done    []*Row `json:"done"`
}

// Summon is an open call on the viewer.
type Summon struct {
	IssueID      string
	Identifier   string
	IssueTitle   string
	IssueStatus  string
	AssigneeType string
	AssigneeID   string
	CallerType   string
	CallerID     string
	CallerName   string
	Source       string
	Reason       string
	CreatedAt    time.Time
}

// Parking is an issue's latest parking record.
type Parking struct {
	IssueID        string
	Identifier     string
	Title          string
	ParentIssueID  string
	CurrentStatus  string
	RecordedStatus string
	Category       string
	StuckKind      string
	Unexplained    bool
	Summary        string
	SummarySource  string
	NextOwner      Owner
	Timeline       json.RawMessage
	EvaluatedAt    time.Time
}

// Task is an agent task from the workspace snapshot.
type Task struct {
	IssueID      string
	AgentID      string
	Status       string
	StartedAt    time.Time // zero when unset
	DispatchedAt time.Time // zero when unset
	CreatedAt    time.Time
}

func (t Task) began() time.Time {
	if !t.StartedAt.IsZero() {
		return t.StartedAt
	}
	if !t.DispatchedAt.IsZero() {
		return t.DispatchedAt
	}
	return t.CreatedAt
}

// Issue is the little of an issue the running, todo and done lanes need.
type Issue struct {
	ID            string
	Identifier    string
	Title         string
	Status        string
	ParentIssueID string
	AssigneeType  string
	AssigneeID    string
	UpdatedAt     time.Time
}

// Unread is one ticket's unread inbox rows for the viewer.
type Unread struct {
	IssueID       string
	Identifier    string
	Title         string
	ParentIssueID string
	UnreadCount   int64
	LatestAt      time.Time
}

type Input struct {
	UserID  string
	Summons []Summon
	Parking []Parking
	Tasks   []Task
	// RunningIssues are the issues the running tasks point at, for titles
	// and parents.
	RunningIssues []Issue
	// TodoIssues are candidates for the todo lane; only the ones in todo and
	// assigned to the viewer land there.
	TodoIssues []Issue
	// DoneIssues are the issues finished today.
	DoneIssues []Issue
	Unread     []Unread
}

func isRunningTask(status string) bool { return status == "dispatched" || status == "running" }

func isClosed(status string) bool { return status == "done" || status == "cancelled" }

// spokenSummary is the parking summary, unless it is only the fixed wording
// for its category.
func spokenSummary(p *Parking) string {
	if p == nil || p.SummarySource == "template" {
		return ""
	}
	return strings.TrimSpace(p.Summary)
}

// recordIsCurrent: a record still describes the issue only while nobody moved
// it by hand since.
func recordIsCurrent(p Parking) bool { return p.CurrentStatus == p.RecordedStatus }

func owner(typ, id string) *Owner {
	if typ == "" || typ == "none" || id == "" {
		return nil
	}
	return &Owner{Type: typ, ID: id}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var emptyTimeline = json.RawMessage("[]")

func timeline(t json.RawMessage) json.RawMessage {
	if len(t) == 0 {
		return emptyTimeline
	}
	return t
}

func newestFirst(rows []*Row) []*Row {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].At.After(rows[j].At) })
	return rows
}

// FoldChildren nests rows under a parent in the same lane. A child whose
// parent sits in a different lane, or nowhere, stays a row of its own.
func FoldChildren(rows []*Row) []*Row {
	byID := make(map[string]*Row, len(rows))
	for _, row := range rows {
		row.Children = []*Row{}
		byID[row.IssueID] = row
	}
	top := make([]*Row, 0, len(rows))
	for _, row := range rows {
		var parent *Row
		if row.ParentIssueID != nil {
			parent = byID[*row.ParentIssueID]
		}
		if parent != nil && parent != row {
			parent.Children = append(parent.Children, row)
		} else {
			top = append(top, row)
		}
	}
	return top
}

func Build(in Input) Board {
	records := make(map[string]*Parking, len(in.Parking))
	for i := range in.Parking {
		records[in.Parking[i].IssueID] = &in.Parking[i]
	}
	placed := map[string]bool{}

	// The earliest-started live task per issue, in first-seen order so equal
	// timestamps sort the same way every time.
	active := map[string]Task{}
	var activeOrder []string
	for _, t := range in.Tasks {
		if t.IssueID == "" || !isRunningTask(t.Status) {
			continue
		}
		seen, ok := active[t.IssueID]
		if !ok {
			activeOrder = append(activeOrder, t.IssueID)
		}
		if !ok || t.began().Before(seen.began()) {
			active[t.IssueID] = t
		}
	}

	// --- waiting: open calls on the viewer, newest call per issue.
	waiting := []*Row{}
	summons := append([]Summon(nil), in.Summons...)
	sort.SliceStable(summons, func(i, j int) bool { return summons[i].CreatedAt.After(summons[j].CreatedAt) })
	for _, s := range summons {
		if placed[s.IssueID] || isClosed(s.IssueStatus) {
			continue
		}
		placed[s.IssueID] = true
		record := records[s.IssueID]
		next := owner(s.AssigneeType, s.AssigneeID)
		if next == nil {
			next = owner("member", in.UserID)
		}
		row := &Row{
			IssueID:    s.IssueID,
			Identifier: s.Identifier,
			Title:      s.IssueTitle,
			Status:     s.IssueStatus,
			Lane:       LaneWaiting,
			Kind:       s.Source,
			Reason:     strings.TrimSpace(s.Reason),
			Before:     spokenSummary(record),
			FromName:   s.CallerName,
			Next:       next,
			At:         s.CreatedAt,
			Timeline:   emptyTimeline,
		}
		if record != nil {
			row.ParentIssueID = optional(record.ParentIssueID)
			row.Timeline = timeline(record.Timeline)
		}
		if s.CallerType != "system" {
			row.From = owner(s.CallerType, s.CallerID)
		}
		waiting = append(waiting, row)
	}
	// A parking record that names the viewer counts too, even without a call row.
	for _, r := range in.Parking {
		if placed[r.IssueID] || in.UserID == "" {
			continue
		}
		if r.Category != "waiting_person" || !recordIsCurrent(r) {
			continue
		}
		if r.NextOwner.Type != "member" || r.NextOwner.ID != in.UserID {
			continue
		}
		if _, running := active[r.IssueID]; isClosed(r.CurrentStatus) || running {
			continue
		}
		placed[r.IssueID] = true
		waiting = append(waiting, &Row{
			IssueID:       r.IssueID,
			Identifier:    r.Identifier,
			Title:         r.Title,
			Status:        r.CurrentStatus,
			ParentIssueID: optional(r.ParentIssueID),
			Lane:          LaneWaiting,
			Kind:          "waiting_person",
			StuckKind:     r.StuckKind,
			Reason:        spokenSummary(&r),
			Next:          owner(r.NextOwner.Type, r.NextOwner.ID),
			At:            r.EvaluatedAt,
			Timeline:      timeline(r.Timeline),
		})
	}

	// --- stalled: the server's "stopped without saying why".
	stalled := []*Row{}
	for _, r := range in.Parking {
		if placed[r.IssueID] || !r.Unexplained || !recordIsCurrent(r) {
			continue
		}
		if _, running := active[r.IssueID]; isClosed(r.CurrentStatus) || running {
			continue
		}
		placed[r.IssueID] = true
		stalled = append(stalled, &Row{
			IssueID:       r.IssueID,
			Identifier:    r.Identifier,
			Title:         r.Title,
			Status:        r.CurrentStatus,
			ParentIssueID: optional(r.ParentIssueID),
			Lane:          LaneStalled,
			Kind:          r.Category,
			StuckKind:     r.StuckKind,
			Before:        spokenSummary(&r),
			Next:          owner(r.NextOwner.Type, r.NextOwner.ID),
			At:            r.EvaluatedAt,
			Timeline:      timeline(r.Timeline),
		})
	}

	// --- running: one row per issue an agent is on.
	issuesByID := make(map[string]Issue, len(in.RunningIssues))
	for _, i := range in.RunningIssues {
		issuesByID[i.ID] = i
	}
	running := []*Row{}
	for _, issueID := range activeOrder {
		if placed[issueID] {
			continue
		}
		task := active[issueID]
		issue, hasIssue := issuesByID[issueID]
		record := records[issueID]
		row := &Row{
			IssueID:  issueID,
			Lane:     LaneRunning,
			Kind:     "running",
			Next:     &Owner{Type: "agent", ID: task.AgentID},
			At:       task.began(),
			Timeline: emptyTimeline,
		}
		switch {
		case hasIssue:
			row.Identifier, row.Title, row.Status, row.ParentIssueID = issue.Identifier, issue.Title, issue.Status, optional(issue.ParentIssueID)
		case record != nil:
			row.Identifier, row.Title, row.Status, row.ParentIssueID = record.Identifier, record.Title, record.CurrentStatus, optional(record.ParentIssueID)
		}
		if row.Identifier == "" {
			continue
		}
		placed[issueID] = true
		running = append(running, row)
	}

	// --- todo: the viewer's assigned work nobody has picked up yet.
	todo := []*Row{}
	for _, issue := range in.TodoIssues {
		if placed[issue.ID] || issue.Status != "todo" || in.UserID == "" {
			continue
		}
		if issue.AssigneeType != "member" || issue.AssigneeID != in.UserID {
			continue
		}
		placed[issue.ID] = true
		todo = append(todo, &Row{
			IssueID:       issue.ID,
			Identifier:    issue.Identifier,
			Title:         issue.Title,
			Status:        issue.Status,
			ParentIssueID: optional(issue.ParentIssueID),
			Lane:          LaneTodo,
			Kind:          "todo",
			Next:          owner("member", in.UserID),
			At:            issue.UpdatedAt,
			Timeline:      emptyTimeline,
		})
	}

	// --- done today.
	done := []*Row{}
	for _, issue := range in.DoneIssues {
		if placed[issue.ID] || issue.Status != "done" {
			continue
		}
		placed[issue.ID] = true
		done = append(done, &Row{
			IssueID:       issue.ID,
			Identifier:    issue.Identifier,
			Title:         issue.Title,
			Status:        issue.Status,
			ParentIssueID: optional(issue.ParentIssueID),
			Lane:          LaneDone,
			Kind:          "done",
			Before:        spokenSummary(records[issue.ID]),
			At:            issue.UpdatedAt,
			Timeline:      emptyTimeline,
		})
	}

	// --- fresh: unread tickets no other lane took, one row each.
	unreadByID := map[string]Unread{}
	var unreadOrder []string
	for _, u := range in.Unread {
		if _, ok := unreadByID[u.IssueID]; !ok {
			unreadOrder = append(unreadOrder, u.IssueID)
		}
		unreadByID[u.IssueID] = u
	}
	fresh := []*Row{}
	for _, id := range unreadOrder {
		u := unreadByID[id]
		if placed[id] || u.UnreadCount <= 0 {
			continue
		}
		placed[id] = true
		fresh = append(fresh, &Row{
			IssueID:       id,
			Identifier:    u.Identifier,
			Title:         u.Title,
			ParentIssueID: optional(u.ParentIssueID),
			Lane:          LaneFresh,
			Kind:          "fresh",
			Before:        spokenSummary(records[id]),
			At:            u.LatestAt,
			Timeline:      emptyTimeline,
		})
	}

	mark := func(rows []*Row) []*Row {
		for _, row := range rows {
			row.Unread = unreadByID[row.IssueID].UnreadCount
			row.Children = []*Row{}
		}
		return newestFirst(rows)
	}

	return Board{
		Waiting: FoldChildren(mark(waiting)),
		Stalled: FoldChildren(mark(stalled)),
		Running: FoldChildren(mark(running)),
		Todo:    FoldChildren(mark(todo)),
		// A ticket per row: a child here is new on its own account, not part
		// of its parent's story.
		Fresh: mark(fresh),
		Done:  FoldChildren(mark(done)),
	}
}

// NameOwners fills NextName on every row, children included, so a reader
// that is not a browser (the CLI, an agent) can say who holds the move.
func (b *Board) NameOwners(name func(Owner) string) {
	var walk func(rows []*Row)
	walk = func(rows []*Row) {
		for _, row := range rows {
			if row.Next != nil {
				row.NextName = name(*row.Next)
			}
			walk(row.Children)
		}
	}
	for _, lane := range [][]*Row{b.Waiting, b.Stalled, b.Running, b.Todo, b.Fresh, b.Done} {
		walk(lane)
	}
}
