// Package statecard is the issue state card (DENE-1328): one short, server-
// built answer to "what is this ticket, what was decided, where does it
// stand, what did the last owner say, and what changed since I was last
// here". It replaces the cold-start scan of every comment.
//
// The card is derived, not stored. The goal is the title plus the goal's
// finish line; "现在在哪" is the close.* record; "上一棒交代" is the newer of
// the close summary and the handoff note; "你上次之后的变化" is computed from
// the caller's own previous run. Only the decisions list ("已拍板") has rows
// of its own, because people edit them one at a time.
//
// The package holds the rules — what a decision may look like, which record
// wins, how the card reads as text — so the HTTP handler, the CLI and the
// daemon brief all show the same card.
package statecard

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/receipt"
)

// Decision limits. A decision is one sentence someone settled, not a report.
const (
	MaxDecisionLen = 300
	MaxDecisions   = 30
	// MaxBatonLen bounds the handoff/close summary the card repeats.
	MaxBatonLen = 300
	// MaxThreads bounds "你上次之后的变化"; older threads are counted, not listed.
	MaxThreads = 10
	// MaxThreadTitleLen bounds a thread's title (its opening line).
	MaxThreadTitleLen = 80
)

// Decision sources.
const (
	SourceClose   = "close"
	SourceHandoff = "handoff"
	SourceManual  = "manual"
)

// Handoff metadata keys. `issue handoff --summary` writes them so the card's
// "上一棒交代" has the handoff's own words, not only the close summary.
const (
	KeyHandoffAt      = "handoff.at"
	KeyHandoffSummary = "handoff.summary"
	KeyHandoffTo      = "handoff.to"
	KeyHandoffByType  = "handoff.by_type"
	KeyHandoffByID    = "handoff.by_id"
)

// KeyLatestSummary is the --summary of the latest close or handoff, word for
// word, rewritten by every one of them — empty when that one carried none.
// The project report reads it as latest_summary (DENE-1691); write order is
// the order, so no timestamp comparison can let an older line through.
const KeyLatestSummary = "summary.latest"

// HandoffKeys lists every handoff key, for writers that replace the record.
var HandoffKeys = []string{KeyHandoffAt, KeyHandoffSummary, KeyHandoffTo, KeyHandoffByType, KeyHandoffByID}

// Anchor kinds: where "你上次之后的变化" is measured from.
const (
	AnchorLastRun     = "last_run"
	AnchorLastComment = "last_comment"
	AnchorExplicit    = "explicit"
	AnchorNone        = "none"
)

// ErrDecisionEmpty and ErrDecisionTooLong are the two shapes a decision can
// be refused for; the handler words the reply.
var (
	ErrDecisionEmpty   = errors.New("decision is empty")
	ErrDecisionTooLong = fmt.Errorf("decision is longer than %d characters", MaxDecisionLen)
)

// NormalizeDecision trims a decision and checks its length.
func NormalizeDecision(text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\x00", ""))
	if text == "" {
		return "", ErrDecisionEmpty
	}
	if utf8.RuneCountInString(text) > MaxDecisionLen {
		return "", ErrDecisionTooLong
	}
	return text, nil
}

// NormalizeDecisions checks every decision a close or handoff brings,
// dropping exact repeats within the batch, and refuses the batch when the
// issue would end up over MaxDecisions. existing is the issue's current
// count.
func NormalizeDecisions(texts []string, existing int) ([]string, error) {
	out := make([]string, 0, len(texts))
	seen := map[string]bool{}
	for _, t := range texts {
		n, err := NormalizeDecision(t)
		if err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if existing+len(out) > MaxDecisions {
		return nil, fmt.Errorf("a ticket keeps at most %d decisions (it has %d); merge or delete some on the issue page first", MaxDecisions, existing)
	}
	return out, nil
}

// Clip trims text and bounds it to max runes, marking a cut with "…".
func Clip(text string, max int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	r := []rune(text)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// FirstParagraph returns the first non-empty paragraph of a markdown body,
// joined onto one line — the summary a close prepends to its evidence.
func FirstParagraph(body string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, strings.TrimLeft(line, "# "))
	}
	return strings.Join(lines, " ")
}

// ThreadTitle is a thread's opening line, stripped of heading marks.
func ThreadTitle(body string) string {
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>*- "))
		if line != "" {
			return Clip(line, MaxThreadTitleLen)
		}
	}
	return ""
}

// Check is one item of a goal's finish line.
type Check struct {
	Description string `json:"description"`
	Status      string `json:"status"`
}

// Goal is "目标": the ticket title, and the finish line when the ticket has
// a goal.
type Goal struct {
	Title      string  `json:"title"`
	GoalStatus string  `json:"goal_status,omitempty"`
	FinishLine []Check `json:"finish_line"`
}

// Decision is one "已拍板" row.
type Decision struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Source     string `json:"source"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// Now is "现在在哪": the issue's status and the latest close record.
type Now struct {
	Status string `json:"status"`
	// Closed is false when the issue has never been closed under the
	// protocol; the close fields are then empty.
	Closed        bool   `json:"closed"`
	Conclusion    string `json:"conclusion,omitempty"`
	CloseStatus   string `json:"close_status,omitempty"`
	NextOwnerType string `json:"next_owner_type,omitempty"`
	NextOwnerID   string `json:"next_owner_id,omitempty"`
	WaitingOn     string `json:"waiting_on,omitempty"`
	// The wait the close recorded (block.*): a condition, a wake clock, or a
	// person who must decide. Empty once the record is stale.
	WaitCondition string `json:"wait_condition,omitempty"`
	WakeAt        string `json:"wake_at,omitempty"`
	NeedsHuman    string `json:"needs_human,omitempty"`
	ClosedAt      string `json:"closed_at,omitempty"`
	// Superseded: a person answered after the close and the executor was
	// woken, so the record describes the past, not the present (DENE-1301).
	Superseded bool `json:"superseded,omitempty"`
	// Stale: the issue moved to another status after the close.
	Stale bool `json:"stale,omitempty"`
	// ReviewSkip is why a done issue had no acceptance seat: its PR was
	// merged and already reviewed (DENE-1678, metadata review_skip).
	ReviewSkip string `json:"review_skip,omitempty"`
}

// Baton is "上一棒交代": what the last owner said when they let go.
type Baton struct {
	Kind      string `json:"kind"` // close | handoff
	Summary   string `json:"summary"`
	ByType    string `json:"by_type,omitempty"`
	ByID      string `json:"by_id,omitempty"`
	To        string `json:"to,omitempty"`
	At        string `json:"at"`
	CommentID string `json:"comment_id,omitempty"`
}

// Thread is one comment thread with activity since the anchor.
type Thread struct {
	ThreadID   string `json:"thread_id"`
	Title      string `json:"title"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	NewCount   int    `json:"new_count"`
	LastAt     string `json:"last_at"`
}

// Changes is "你上次之后的变化", computed for the caller.
type Changes struct {
	Anchor  string   `json:"anchor"`
	Since   string   `json:"since,omitempty"`
	Threads []Thread `json:"threads"`
	// More counts threads past MaxThreads that also changed.
	More int `json:"more,omitempty"`
}

// Card is the whole state card.
type Card struct {
	IssueID    string     `json:"issue_id"`
	Identifier string     `json:"identifier"`
	Goal       Goal       `json:"goal"`
	Source     *Source    `json:"source"`
	Decisions  []Decision `json:"decisions"`
	Now        Now        `json:"now"`
	Baton      *Baton     `json:"baton"`
	Changes    Changes    `json:"changes"`
	// Children are the sub-tasks' receipts the viewer can see (DENE-1679).
	Children []receipt.Receipt `json:"children"`
}

// MaxSourceExcerpt bounds the source message quoted on the card.
const MaxSourceExcerpt = 200

// Source is the chat an issue was dispatched from (DENE-1672): the executor
// reads what was asked, and where to read the rest.
type Source struct {
	ChatSessionID string `json:"chat_session_id"`
	ChatTitle     string `json:"chat_title"`
	MessageID     string `json:"message_id,omitempty"`
	Excerpt       string `json:"excerpt,omitempty"`
}

// DeriveNow reads "现在在哪" from the close record. meta is the issue's
// metadata flattened to strings.
func DeriveNow(meta map[string]string, status string) Now {
	now := Now{Status: status}
	if status == "done" {
		now.ReviewSkip = strings.TrimSpace(meta["review_skip"])
	}
	if !closeprotocol.Complete(meta) {
		return now
	}
	now.Closed = true
	now.Conclusion = meta[closeprotocol.KeyConclusion]
	now.CloseStatus = meta[closeprotocol.KeyStatus]
	now.NextOwnerType = meta[closeprotocol.KeyNextOwnerType]
	if now.NextOwnerType != closeprotocol.OwnerNone {
		now.NextOwnerID = strings.TrimSpace(meta[closeprotocol.KeyNextOwnerID])
	}
	now.WaitingOn = strings.TrimSpace(meta[closeprotocol.KeyWaitingOn])
	now.ClosedAt = strings.TrimSpace(meta[closeprotocol.KeyAt])
	now.Superseded = closeprotocol.Superseded(meta)
	now.Stale = !closeprotocol.StatusMatchesIssue(now.CloseStatus, status)
	if !now.Stale && !now.Superseded {
		now.WaitCondition = strings.TrimSpace(meta[blockwait.KeyWaitCondition])
		now.WakeAt = strings.TrimSpace(meta[blockwait.KeyWakeAt])
		now.NeedsHuman = strings.TrimSpace(meta[blockwait.KeyNeedsHuman])
	}
	return now
}

// CloseNote is the close summary as the card knows it: the close's progress
// line when it belongs to this close, else the evidence comment's opening.
type CloseNote struct {
	Summary   string
	ByType    string
	ByID      string
	CommentID string
}

// DeriveBaton picks the newer of the close and the handoff. A close without
// a usable summary still counts — its evidence opening stands in — so a
// later handoff never hides behind an older close.
func DeriveBaton(meta map[string]string, close CloseNote) *Baton {
	var closeBaton, handoffBaton *Baton
	if closeprotocol.Complete(meta) {
		if s := Clip(close.Summary, MaxBatonLen); s != "" {
			closeBaton = &Baton{
				Kind: "close", Summary: s, ByType: close.ByType, ByID: close.ByID,
				At: strings.TrimSpace(meta[closeprotocol.KeyAt]), CommentID: close.CommentID,
			}
			if closeBaton.CommentID == "" {
				closeBaton.CommentID = strings.TrimSpace(meta[closeprotocol.KeyEvidenceCommentID])
			}
		}
	}
	if at := strings.TrimSpace(meta[KeyHandoffAt]); at != "" {
		if s := Clip(meta[KeyHandoffSummary], MaxBatonLen); s != "" {
			handoffBaton = &Baton{
				Kind: "handoff", Summary: s, ByType: meta[KeyHandoffByType], ByID: meta[KeyHandoffByID],
				To: meta[KeyHandoffTo], At: at,
			}
		}
	}
	switch {
	case closeBaton == nil:
		return handoffBaton
	case handoffBaton == nil:
		return closeBaton
	case after(handoffBaton.At, closeBaton.At):
		return handoffBaton
	default:
		return closeBaton
	}
}

// LatestSummary is the report's latest_summary (DENE-1691): the latest close
// or handoff --summary as stored under KeyLatestSummary. Issues closed before
// that key existed fall back to the newer of the last close and the last
// summarised handoff; nothing else stands in, so an older line is never
// passed off as the latest conclusion.
func LatestSummary(meta map[string]string, closeSummary string) string {
	if latest, ok := meta[KeyLatestSummary]; ok {
		return strings.TrimSpace(latest)
	}
	handoffAt := strings.TrimSpace(meta[KeyHandoffAt])
	if closeprotocol.Complete(meta) && (handoffAt == "" || !after(handoffAt, strings.TrimSpace(meta[closeprotocol.KeyAt]))) {
		return strings.TrimSpace(closeSummary)
	}
	if handoffAt == "" {
		return ""
	}
	return strings.TrimSpace(meta[KeyHandoffSummary])
}

func after(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a > b
	}
	return ta.After(tb)
}

// SummaryBelongsToClose reports whether a close progress line written at
// progressAt was written by the close recorded at closeAt. The close writes
// close.at inside its transaction and the progress line right after commit,
// so a line from this close is never older than close.at; an older line is
// a previous close's summary and must not be shown as this one's.
func SummaryBelongsToClose(progressAt time.Time, closeAt string) bool {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(closeAt))
	if err != nil {
		return false
	}
	// close.at has second precision; allow the truncated second.
	return !progressAt.Before(at)
}

// Caller identifies who asks for the card; the change list leaves out their
// own comments and is measured from their own previous visit.
type Caller struct {
	Type string // agent | member
	ID   string
}

// ChooseAnchor picks where "你上次之后的变化" starts. An explicit since wins;
// an agent is measured from its previous run on the issue; a person from
// their own last comment. With none, every thread counts as new.
func ChooseAnchor(caller Caller, explicit, previousRun, lastComment *time.Time) (string, *time.Time) {
	switch {
	case explicit != nil:
		return AnchorExplicit, explicit
	case caller.Type == "agent" && previousRun != nil:
		return AnchorLastRun, previousRun
	case caller.Type == "member" && lastComment != nil:
		return AnchorLastComment, lastComment
	}
	return AnchorNone, nil
}

// Render is the card as text: what `multica issue context` prints and what
// a brief can inline. Short labels, one line per fact.
func Render(c Card) string {
	var b strings.Builder
	head := c.Identifier
	if head == "" {
		head = c.IssueID
	}
	fmt.Fprintf(&b, "## 状态卡 %s\n\n", head)

	fmt.Fprintf(&b, "目标：%s\n", c.Goal.Title)
	for _, ch := range c.Goal.FinishLine {
		mark := "[ ]"
		if ch.Status == "passed" {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "  %s %s\n", mark, ch.Description)
	}
	if c.Source != nil {
		title := c.Source.ChatTitle
		if title == "" {
			title = "未命名聊天"
		}
		fmt.Fprintf(&b, "来源：聊天「%s」（multica chat history --session %s）\n", title, c.Source.ChatSessionID)
		if c.Source.Excerpt != "" {
			fmt.Fprintf(&b, "  原话：%s\n", c.Source.Excerpt)
		}
	}

	b.WriteString("\n已拍板：")
	if len(c.Decisions) == 0 {
		b.WriteString("无\n")
	} else {
		b.WriteString("\n")
		for _, d := range c.Decisions {
			fmt.Fprintf(&b, "  - %s\n", d.Text)
		}
	}

	fmt.Fprintf(&b, "\n现在在哪：%s", c.Now.Status)
	if c.Now.Closed {
		fmt.Fprintf(&b, "；上次收尾 %s（%s）", c.Now.Conclusion, c.Now.ClosedAt)
		if c.Now.WaitingOn != "" {
			fmt.Fprintf(&b, "，在等 %s", c.Now.WaitingOn)
		}
		if c.Now.WaitCondition != "" {
			fmt.Fprintf(&b, "，在等：%s", c.Now.WaitCondition)
		}
		if c.Now.WakeAt != "" {
			fmt.Fprintf(&b, "，%s 叫醒", c.Now.WakeAt)
		}
		if c.Now.NeedsHuman != "" {
			fmt.Fprintf(&b, "，等人拍板 %s", c.Now.NeedsHuman)
		}
		if c.Now.NextOwnerType != "" && c.Now.NextOwnerType != closeprotocol.OwnerNone {
			fmt.Fprintf(&b, "，下一棒 %s %s", c.Now.NextOwnerType, c.Now.NextOwnerID)
		}
		if c.Now.Superseded {
			b.WriteString("；之后有人回复，记录已过时")
		} else if c.Now.Stale {
			b.WriteString("；状态已变，记录已过时")
		}
	} else {
		b.WriteString("；还没收尾过")
	}
	if c.Now.ReviewSkip != "" {
		fmt.Fprintf(&b, "；跳过验收：%s", c.Now.ReviewSkip)
	}
	b.WriteString("\n")

	b.WriteString("\n上一棒交代：")
	if c.Baton == nil {
		b.WriteString("无\n")
	} else {
		label := "收尾"
		if c.Baton.Kind == "handoff" {
			label = "交棒"
			if c.Baton.To != "" {
				label += "给 " + c.Baton.To
			}
		}
		fmt.Fprintf(&b, "（%s，%s）%s\n", label, c.Baton.At, c.Baton.Summary)
	}

	if len(c.Children) > 0 {
		b.WriteString("\n子任务回执：\n")
		for i, r := range c.Children {
			if i == receipt.MaxChildren {
				fmt.Fprintf(&b, "  - 另有 %d 个子任务\n", len(c.Children)-receipt.MaxChildren)
				break
			}
			fmt.Fprintf(&b, "  - %s\n", r.Line())
		}
	}

	b.WriteString("\n你上次之后的变化：")
	switch {
	case len(c.Changes.Threads) == 0 && c.Changes.Anchor == AnchorNone:
		b.WriteString("没有评论\n")
	case len(c.Changes.Threads) == 0:
		fmt.Fprintf(&b, "%s 之后没有新评论\n", c.Changes.Since)
	default:
		if c.Changes.Since != "" {
			fmt.Fprintf(&b, "%s 之后\n", c.Changes.Since)
		} else {
			b.WriteString("首次查看，全部线程\n")
		}
		for _, t := range c.Changes.Threads {
			fmt.Fprintf(&b, "  - %s（%d 条新，--thread %s）\n", t.Title, t.NewCount, t.ThreadID)
		}
		if c.Changes.More > 0 {
			fmt.Fprintf(&b, "  - 另有 %d 个线程有新评论\n", c.Changes.More)
		}
	}
	return b.String()
}
