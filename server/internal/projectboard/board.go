// Package projectboard builds the workspace-wide project task board.
//
// Unlike the inbox board this projection is not scoped to one person's
// notifications. It is a read-only panorama of every open issue the caller
// can see, using the platform's parking records and live task snapshot as
// facts for the lane decision.
package projectboard

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
	LaneStale   Lane = "stale"
)

type Owner struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type Issue struct {
	ID             string
	Identifier     string
	Title          string
	ProjectID      string
	Status         string
	ParentIssueID  string
	Assignee       Owner
	LastActivityAt time.Time
	Metadata       map[string]any
}

type Parking struct {
	IssueID        string
	CurrentStatus  string
	RecordedStatus string
	Category       string
	StuckKind      string
	Unexplained    bool
	Summary        string
	NextOwner      Owner
	EvaluatedAt    time.Time
}

type Task struct {
	IssueID      string
	AgentID      string
	Status       string
	StartedAt    time.Time
	DispatchedAt time.Time
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

type Row struct {
	IssueID        string  `json:"issue_id"`
	Identifier     string  `json:"identifier"`
	Title          string  `json:"title"`
	ProjectID      string  `json:"project_id"`
	Status         string  `json:"status"`
	ParentIssueID  *string `json:"parent_issue_id,omitempty"`
	Lane           Lane    `json:"lane"`
	LastActivityAt string  `json:"last_activity_at"`
	Next           *Owner  `json:"next,omitempty"`
	NextName       string  `json:"next_name,omitempty"`
	StuckKind      string  `json:"stuck_kind,omitempty"`
	StalledReason  string  `json:"stalled_reason,omitempty"`
	Paused         bool    `json:"paused,omitempty"`
	PauseReason    string  `json:"pause_reason,omitempty"`
}

type Board struct {
	Waiting []*Row `json:"waiting"`
	Stalled []*Row `json:"stalled"`
	Running []*Row `json:"running"`
	Todo    []*Row `json:"todo"`
	Stale   []*Row `json:"stale"`
}

func activeTaskByIssue(tasks []Task) map[string]Task {
	active := map[string]Task{}
	for _, task := range tasks {
		if task.IssueID == "" || (task.Status != "queued" && task.Status != "dispatched" && task.Status != "running" && task.Status != "waiting_local_directory") {
			continue
		}
		prev, ok := active[task.IssueID]
		if !ok || task.began().Before(prev.began()) {
			active[task.IssueID] = task
		}
	}
	return active
}

func intentionalPause(issue Issue, record *Parking) (bool, string) {
	if issue.Status == "backlog" {
		return true, "已放回待规划"
	}
	if record != nil && record.CurrentStatus == record.RecordedStatus && record.Category == "deferred" {
		if strings.TrimSpace(record.Summary) != "" {
			return true, strings.TrimSpace(record.Summary)
		}
		return true, "平台记录为有意暂停"
	}
	for _, key := range []string{"block.wake_at", "block.wait_timeout", "block.blocked_by", "close.waiting_on"} {
		if value, ok := issue.Metadata[key]; ok && strings.TrimSpace(toString(value)) != "" {
			return true, "平台记录为有意等待：" + strings.TrimSpace(toString(value))
		}
	}
	return false, ""
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func owner(typ, id string) *Owner {
	if strings.TrimSpace(typ) == "" || typ == "none" || strings.TrimSpace(id) == "" {
		return nil
	}
	return &Owner{Type: typ, ID: id}
}

func parentPointer(id string) *string {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return &id
}

func row(issue Issue, lane Lane, now time.Time, next *Owner, record *Parking, task *Task) *Row {
	last := issue.LastActivityAt
	if last.IsZero() {
		last = now
	}
	r := &Row{IssueID: issue.ID, Identifier: issue.Identifier, Title: issue.Title, ProjectID: issue.ProjectID, Status: issue.Status, ParentIssueID: parentPointer(issue.ParentIssueID), Lane: lane, LastActivityAt: last.UTC().Format(time.RFC3339Nano), Next: next}
	if record != nil {
		r.StuckKind = record.StuckKind
		r.StalledReason = strings.TrimSpace(record.Summary)
	}
	if task != nil {
		r.Next = owner("agent", task.AgentID)
	}
	return r
}

func Build(issues []Issue, parking []Parking, tasks []Task, now time.Time) Board {
	records := make(map[string]*Parking, len(parking))
	for i := range parking {
		if parking[i].CurrentStatus == parking[i].RecordedStatus {
			records[parking[i].IssueID] = &parking[i]
		}
	}
	active := activeTaskByIssue(tasks)
	placed := make(map[string]bool, len(issues))
	board := Board{}
	for _, issue := range issues {
		record := records[issue.ID]
		pause, pauseReason := intentionalPause(issue, record)
		if task, ok := active[issue.ID]; ok {
			r := row(issue, LaneRunning, now, owner("agent", task.AgentID), record, &task)
			board.Running = append(board.Running, r)
			placed[issue.ID] = true
			continue
		}
		if record != nil && (record.Category == "waiting_person" || record.Category == "awaiting_review" || (record.Category == "blocked" && record.NextOwner.Type != "" && record.NextOwner.Type != "none")) {
			r := row(issue, LaneWaiting, now, &record.NextOwner, record, nil)
			board.Waiting = append(board.Waiting, r)
			placed[issue.ID] = true
			continue
		}
		if record != nil && record.Unexplained {
			r := row(issue, LaneStalled, now, &record.NextOwner, record, nil)
			board.Stalled = append(board.Stalled, r)
			placed[issue.ID] = true
			continue
		}
		if pause || issue.Status == "todo" || issue.Status == "backlog" {
			r := row(issue, LaneTodo, now, owner(issue.Assignee.Type, issue.Assignee.ID), record, nil)
			r.Paused, r.PauseReason = pause, pauseReason
			board.Todo = append(board.Todo, r)
			placed[issue.ID] = true
			continue
		}
		last := issue.LastActivityAt
		if !pause && !last.IsZero() && now.Sub(last) > 36*time.Hour {
			r := row(issue, LaneStale, now, owner(issue.Assignee.Type, issue.Assignee.ID), record, nil)
			if r.StalledReason == "" {
				r.StalledReason = "超过 1.5 天没有活动"
			}
			board.Stale = append(board.Stale, r)
			placed[issue.ID] = true
			continue
		}
		// Every open issue must be visible in the panorama. If it has no live
		// task or current parking verdict yet, keep it in the actionable todo
		// lane until the platform records a more specific next move.
		r := row(issue, LaneTodo, now, owner(issue.Assignee.Type, issue.Assignee.ID), record, nil)
		board.Todo = append(board.Todo, r)
		placed[issue.ID] = true
	}
	for _, rows := range [][]*Row{board.Waiting, board.Stalled, board.Running, board.Todo, board.Stale} {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastActivityAt > rows[j].LastActivityAt })
	}
	return board
}

func (b *Board) NameOwners(name func(Owner) string) {
	for _, rows := range [][]*Row{b.Waiting, b.Stalled, b.Running, b.Todo, b.Stale} {
		for _, row := range rows {
			if row.Next != nil {
				row.NextName = name(*row.Next)
			}
		}
	}
}
