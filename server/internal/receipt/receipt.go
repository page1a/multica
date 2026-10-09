// Package receipt is a task's result as it flows back to whoever dispatched
// it (DENE-1672): the chat it came from and, later, its parent. A receipt is
// not a second summary — every field is read from what the issue already
// records: its status, the latest close (conclusion, summary, knowledge
// audit) and its linked pull requests.
package receipt

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PR is one pull request linked to the issue.
type PR struct {
	Number int32  `json:"number"`
	URL    string `json:"url"`
	State  string `json:"state"` // merged | closed | draft | open
}

// Receipt is one task's result.
type Receipt struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	// Conclusion and Summary come from the latest close, only while the
	// close still describes the current status.
	Conclusion string `json:"conclusion,omitempty"`
	Summary    string `json:"summary,omitempty"`
	ClosedAt   string `json:"closed_at,omitempty"`
	PRs        []PR   `json:"pull_requests"`
	// Knowledge is the close's knowledge audit in one line; empty when the
	// close recorded none.
	Knowledge string `json:"knowledge,omitempty"`
	UpdatedAt string `json:"updated_at"`
	// Children are the sub-tasks' receipts (DENE-1679): a parent reports for
	// its children, so its card carries their conclusions.
	Children []Receipt `json:"children,omitempty"`
}

// MaxChildren bounds the sub-task receipts one card or comment lists; the
// rest are counted.
const MaxChildren = 20

// maxChildSummary bounds a sub-task's conclusion inside its parent's list.
const maxChildSummary = 160

// MaxSummary bounds the summary a receipt carries; the evidence comment on
// the issue has the rest.
const MaxSummary = 300

var statusLabels = map[string]string{
	"backlog":     "搁置",
	"todo":        "待开始",
	"in_progress": "进行中",
	"in_review":   "待验收",
	"blocked":     "卡住了",
	"done":        "已完成",
	"cancelled":   "已取消",
}

// StatusLabel names a status for people. Custom keys pass through.
func StatusLabel(status string) string {
	if l, ok := statusLabels[status]; ok {
		return l
	}
	return status
}

// Reportable is true for the statuses a dispatcher must hear about: the work
// finished, stopped, or waits on someone else.
func Reportable(status string) bool {
	switch status {
	case "done", "cancelled", "blocked", "in_review":
		return true
	}
	return false
}

var prStateLabels = map[string]string{"merged": "已合并", "closed": "已关闭", "draft": "草稿", "open": "未合并"}

func prLabel(p PR) string {
	state := prStateLabels[p.State]
	if state == "" {
		state = p.State
	}
	if p.Number > 0 {
		return fmt.Sprintf("[#%d](%s) %s", p.Number, p.URL, state)
	}
	return fmt.Sprintf("[PR](%s) %s", p.URL, state)
}

// Clip cuts s to max runes on one line.
func Clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// Markdown is the receipt card's body in a chat. The issue mention renders as
// a chip carrying the title, so the head line adds only the status.
func (r Receipt) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s](mention://issue/%s) %s", r.Identifier, r.IssueID, StatusLabel(r.Status))
	if s := Clip(r.Summary, MaxSummary); s != "" {
		fmt.Fprintf(&b, "\n\n结论：%s", s)
	}
	if len(r.PRs) > 0 {
		labels := make([]string, 0, len(r.PRs))
		for _, p := range r.PRs {
			labels = append(labels, prLabel(p))
		}
		fmt.Fprintf(&b, "\n\nPR：%s", strings.Join(labels, "，"))
	}
	if r.Knowledge != "" {
		fmt.Fprintf(&b, "\n\n沉淀：%s", r.Knowledge)
	}
	if d := Digest(r.Children); d != "" {
		b.WriteString("\n\n" + d)
	}
	return b.String()
}

// Item is a sub-task's receipt as one markdown list item: the issue chip,
// status, conclusion, pull requests and knowledge on one line.
func (r Receipt) Item() string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s](mention://issue/%s) %s", r.Identifier, r.IssueID, StatusLabel(r.Status))
	if s := strings.TrimRight(Clip(r.Summary, maxChildSummary), "。.；; "); s != "" {
		fmt.Fprintf(&b, "：%s", s)
	}
	if len(r.PRs) > 0 {
		labels := make([]string, 0, len(r.PRs))
		for _, p := range r.PRs {
			labels = append(labels, prLabel(p))
		}
		fmt.Fprintf(&b, "；PR %s", strings.Join(labels, "，"))
	}
	if r.Knowledge != "" {
		fmt.Fprintf(&b, "；沉淀 %s", Clip(r.Knowledge, 120))
	}
	return b.String()
}

// Digest is the sub-task receipts as a markdown list under one heading;
// empty when there are none.
func Digest(children []Receipt) string {
	if len(children) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("子任务回执：")
	for i, c := range children {
		if i == MaxChildren {
			fmt.Fprintf(&b, "\n- 另有 %d 个子任务", len(children)-MaxChildren)
			break
		}
		b.WriteString("\n" + c.Item())
	}
	return b.String()
}

// Line is the receipt in one line, for a run's per-turn list and the CLI.
func (r Receipt) Line() string {
	parts := []string{fmt.Sprintf("%s %s — %s", r.Identifier, Clip(r.Title, 80), StatusLabel(r.Status))}
	if s := Clip(r.Summary, 160); s != "" {
		parts = append(parts, s)
	}
	for _, p := range r.PRs {
		state := prStateLabels[p.State]
		if state == "" {
			state = p.State
		}
		parts = append(parts, fmt.Sprintf("PR %s（%s）", p.URL, state))
	}
	return strings.Join(parts, " · ")
}
