package inboxboard

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// The cases below are packages/core/home/board.test.ts, case for case: the
// rules moved server-side in DENE-975 and must give the same lanes.

const me = "user-me"

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func record(id string, edit func(*Parking)) Parking {
	p := Parking{
		IssueID:        id,
		Identifier:     "DENE-" + id,
		Title:          "title " + id,
		CurrentStatus:  "in_progress",
		RecordedStatus: "in_progress",
		Category:       "stalled_unclosed",
		StuckKind:      "no_close",
		Unexplained:    true,
		Summary:        "PR 已合入",
		SummarySource:  "agent",
		NextOwner:      Owner{Type: "agent", ID: "agent-1"},
		EvaluatedAt:    at("2026-09-26T08:00:00Z"),
	}
	if edit != nil {
		edit(&p)
	}
	return p
}

func summon(id string, edit func(*Summon)) Summon {
	s := Summon{
		IssueID:     id,
		Identifier:  "DENE-" + id,
		IssueTitle:  "title " + id,
		IssueStatus: "blocked",
		CallerType:  "agent",
		CallerID:    "agent-1",
		CallerName:  "孙悟空",
		Source:      "needs_human",
		Reason:      "要你先回答 3 个问题",
		CreatedAt:   at("2026-09-26T07:00:00Z"),
	}
	if edit != nil {
		edit(&s)
	}
	return s
}

func task(issueID string, edit func(*Task)) Task {
	t := Task{
		IssueID:      issueID,
		AgentID:      "agent-2",
		Status:       "running",
		DispatchedAt: at("2026-09-26T08:00:00Z"),
		StartedAt:    at("2026-09-26T08:01:00Z"),
		CreatedAt:    at("2026-09-26T08:00:00Z"),
	}
	if edit != nil {
		edit(&t)
	}
	return t
}

func issue(id string, edit func(*Issue)) Issue {
	i := Issue{
		ID:         id,
		Identifier: "DENE-" + id,
		Title:      "title " + id,
		Status:     "done",
		UpdatedAt:  at("2026-09-26T09:00:00Z"),
	}
	if edit != nil {
		edit(&i)
	}
	return i
}

func unread(id string, edit func(*Unread)) Unread {
	u := Unread{
		IssueID:     id,
		Identifier:  "DENE-" + id,
		Title:       "title " + id,
		UnreadCount: 1,
		LatestAt:    at("2026-09-26T08:30:00Z"),
	}
	if edit != nil {
		edit(&u)
	}
	return u
}

func ids(rows []*Row) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.IssueID)
	}
	return out
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCalledIssueWaitsEvenWhenRecordSaysStalled(t *testing.T) {
	b := Build(Input{UserID: me, Summons: []Summon{summon("1", nil)}, Parking: []Parking{record("1", nil)}})
	eq(t, ids(b.Waiting), []string{"1"})
	eq(t, ids(b.Stalled), []string{})
	w := b.Waiting[0]
	eq(t, w.Reason, "要你先回答 3 个问题")
	eq(t, w.Before, "PR 已合入")
	eq(t, w.FromName, "孙悟空")
	eq(t, *w.From, Owner{Type: "agent", ID: "agent-1"})
	eq(t, *w.Next, Owner{Type: "member", ID: me})
}

func TestOneRowPerIssueWithSeveralCalls(t *testing.T) {
	b := Build(Input{UserID: me, Summons: []Summon{
		summon("1", func(s *Summon) { s.Reason = "old"; s.CreatedAt = at("2026-09-26T01:00:00Z") }),
		summon("1", func(s *Summon) { s.Reason = "new"; s.CreatedAt = at("2026-09-26T02:00:00Z") }),
	}})
	eq(t, len(b.Waiting), 1)
	eq(t, b.Waiting[0].Reason, "new")
}

func TestParkingRecordNamingViewerWaits(t *testing.T) {
	b := Build(Input{UserID: me, Parking: []Parking{
		record("2", func(p *Parking) {
			p.Category, p.Unexplained, p.Summary = "waiting_person", false, "等你拍板"
			p.NextOwner = Owner{Type: "member", ID: me}
		}),
		record("3", func(p *Parking) {
			p.Category, p.Unexplained = "waiting_person", false
			p.NextOwner = Owner{Type: "member", ID: "someone-else"}
		}),
	}})
	eq(t, ids(b.Waiting), []string{"2"})
	eq(t, b.Waiting[0].Reason, "等你拍板")
}

func TestDropsStalledRecordsMovedSinceOrRunningAgain(t *testing.T) {
	b := Build(Input{
		UserID: me,
		Parking: []Parking{
			record("1", nil),
			record("2", func(p *Parking) { p.CurrentStatus = "done" }),
			record("3", func(p *Parking) { p.CurrentStatus = "in_review" }),
			record("4", nil),
			record("5", func(p *Parking) { p.Unexplained, p.Category = false, "blocked" }),
		},
		Tasks:         []Task{task("4", nil)},
		RunningIssues: []Issue{issue("4", func(i *Issue) { i.Status = "in_progress" })},
	})
	eq(t, ids(b.Stalled), []string{"1"})
	eq(t, ids(b.Running), []string{"4"})
}

func TestHidesTemplateWordingAsBefore(t *testing.T) {
	b := Build(Input{UserID: me, Parking: []Parking{
		record("1", func(p *Parking) { p.Summary, p.SummarySource = "运行已结束", "template" }),
	}})
	eq(t, b.Stalled[0].Before, "")
}

func TestRunningIssuesOnceWithTheAgentOnThem(t *testing.T) {
	b := Build(Input{
		UserID: me,
		Tasks: []Task{
			task("7", nil),
			task("7", func(t *Task) { t.AgentID = "agent-3"; t.StartedAt = at("2026-09-26T09:00:00Z") }),
			task("8", func(t *Task) { t.Status = "queued" }),
			task("", nil),
		},
		RunningIssues: []Issue{issue("7", func(i *Issue) { i.Status = "in_progress" })},
	})
	eq(t, len(b.Running), 1)
	eq(t, b.Running[0].IssueID, "7")
	eq(t, *b.Running[0].Next, Owner{Type: "agent", ID: "agent-2"})
}

func TestFoldsDoneChildrenUnderDoneParent(t *testing.T) {
	b := Build(Input{UserID: me, DoneIssues: []Issue{
		issue("p", nil),
		issue("c1", func(i *Issue) { i.ParentIssueID = "p" }),
		issue("c2", func(i *Issue) { i.ParentIssueID = "p" }),
		issue("x", func(i *Issue) { i.ParentIssueID = "elsewhere" }),
	}})
	top := ids(b.Done)
	sort.Strings(top)
	eq(t, top, []string{"p", "x"})
	for _, r := range b.Done {
		if r.IssueID == "p" {
			eq(t, ids(r.Children), []string{"c1", "c2"})
		}
	}
}

func TestMarksEveryLaneWithUnreadCount(t *testing.T) {
	b := Build(Input{
		UserID:        me,
		Summons:       []Summon{summon("w", nil)},
		Parking:       []Parking{record("s", nil)},
		Tasks:         []Task{task("r", nil)},
		RunningIssues: []Issue{issue("r", func(i *Issue) { i.Status = "in_progress" })},
		DoneIssues:    []Issue{issue("p", nil), issue("c", func(i *Issue) { i.ParentIssueID = "p" })},
		Unread: []Unread{
			unread("w", func(u *Unread) { u.UnreadCount = 2 }),
			unread("s", nil),
			unread("r", func(u *Unread) { u.UnreadCount = 3 }),
			unread("c", nil),
		},
	})
	eq(t, b.Waiting[0].Unread, int64(2))
	eq(t, b.Stalled[0].Unread, int64(1))
	eq(t, b.Running[0].Unread, int64(3))
	eq(t, ids(b.Done), []string{"p"})
	eq(t, b.Done[0].Unread, int64(0))
	eq(t, b.Done[0].Children[0].Unread, int64(1))
	eq(t, ids(b.Fresh), []string{})
}

func TestUnreadTicketsNoLaneTookAreFresh(t *testing.T) {
	b := Build(Input{
		UserID:     me,
		DoneIssues: []Issue{issue("d", nil)},
		Unread: []Unread{
			unread("d", nil),
			unread("p", func(u *Unread) { u.LatestAt = at("2026-09-26T07:00:00Z") }),
			unread("c", func(u *Unread) { u.ParentIssueID = "p"; u.LatestAt = at("2026-09-26T09:00:00Z") }),
			unread("old", func(u *Unread) { u.UnreadCount = 0 }),
		},
	})
	eq(t, ids(b.Done), []string{"d"})
	eq(t, ids(b.Fresh), []string{"c", "p"})
	for _, r := range b.Fresh {
		if r.Lane != LaneFresh || len(r.Children) != 0 {
			t.Fatalf("fresh row %s: lane %s, %d children", r.IssueID, r.Lane, len(r.Children))
		}
	}
	eq(t, b.Fresh[0].Identifier, "DENE-c")
	eq(t, b.Fresh[0].Unread, int64(1))
}

func TestNoUnreadLeavesRowsUnmarked(t *testing.T) {
	b := Build(Input{UserID: me, Parking: []Parking{record("1", nil)}})
	eq(t, b.Stalled[0].Unread, int64(0))
	eq(t, ids(b.Fresh), []string{})
}

func TestFoldChildrenLeavesChildWhoseParentIsElsewhere(t *testing.T) {
	p := "p"
	eq(t, ids(FoldChildren([]*Row{{IssueID: "c", ParentIssueID: &p}})), []string{"c"})
}

func TestEmptyLanesMarshalAsArrays(t *testing.T) {
	b := Build(Input{})
	for name, lane := range map[string][]*Row{"waiting": b.Waiting, "stalled": b.Stalled, "running": b.Running, "todo": b.Todo, "fresh": b.Fresh, "done": b.Done} {
		if lane == nil {
			t.Fatalf("%s lane is nil, would marshal as null", name)
		}
	}
}

func TestNameOwnersNamesNestedRows(t *testing.T) {
	b := Build(Input{UserID: me, DoneIssues: []Issue{issue("p", nil)}, Parking: []Parking{
		record("s", nil),
		record("c", func(p *Parking) { p.ParentIssueID = "s"; p.NextOwner = Owner{Type: "member", ID: "m-1"} }),
	}})
	b.NameOwners(func(o Owner) string { return o.Type + "/" + o.ID })
	eq(t, b.Stalled[0].NextName, "agent/agent-1")
	eq(t, b.Stalled[0].Children[0].NextName, "member/m-1")
	eq(t, b.Done[0].NextName, "")
}

func todoIssue(id string, edit func(*Issue)) Issue {
	return issue(id, func(i *Issue) {
		i.Status = "todo"
		i.AssigneeType = "member"
		i.AssigneeID = me
		if edit != nil {
			edit(i)
		}
	})
}

func TestTodoLaneTakesViewersTodoIssues(t *testing.T) {
	b := Build(Input{UserID: me, TodoIssues: []Issue{
		todoIssue("old", func(i *Issue) { i.UpdatedAt = at("2026-09-26T07:00:00Z") }),
		todoIssue("new", nil),
		todoIssue("c", func(i *Issue) { i.ParentIssueID = "new" }),
	}})
	eq(t, ids(b.Todo), []string{"new", "old"})
	eq(t, ids(b.Todo[0].Children), []string{"c"})
	eq(t, b.Todo[0].Lane, LaneTodo)
	eq(t, *b.Todo[0].Next, Owner{Type: "member", ID: me})
}

func TestTodoLaneSkipsOthersWorkAndOtherStatuses(t *testing.T) {
	b := Build(Input{UserID: me, TodoIssues: []Issue{
		todoIssue("mine", nil),
		todoIssue("theirs", func(i *Issue) { i.AssigneeID = "someone-else" }),
		todoIssue("agent", func(i *Issue) { i.AssigneeType = "agent"; i.AssigneeID = me }),
		todoIssue("backlog", func(i *Issue) { i.Status = "backlog" }),
		todoIssue("doing", func(i *Issue) { i.Status = "in_progress" }),
	}})
	eq(t, ids(b.Todo), []string{"mine"})
	// Nobody to compare against: no todo lane at all.
	eq(t, ids(Build(Input{TodoIssues: []Issue{todoIssue("mine", nil)}}).Todo), []string{})
}

func TestTodoLaneLeavesEarlierLanesAlone(t *testing.T) {
	b := Build(Input{
		UserID:        me,
		Summons:       []Summon{summon("w", nil)},
		Parking:       []Parking{record("s", nil)},
		Tasks:         []Task{task("r", nil)},
		RunningIssues: []Issue{todoIssue("r", nil)},
		TodoIssues:    []Issue{todoIssue("w", nil), todoIssue("s", nil), todoIssue("r", nil), todoIssue("t", nil)},
		Unread:        []Unread{unread("t", func(u *Unread) { u.UnreadCount = 2 })},
	})
	eq(t, ids(b.Waiting), []string{"w"})
	eq(t, ids(b.Stalled), []string{"s"})
	eq(t, ids(b.Running), []string{"r"})
	eq(t, ids(b.Todo), []string{"t"})
	eq(t, b.Todo[0].Unread, int64(2))
	// An unread todo ticket sits in todo, not again in fresh.
	eq(t, ids(b.Fresh), []string{})
}
