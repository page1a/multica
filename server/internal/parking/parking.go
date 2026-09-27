// Package parking is the per-issue "parking record" (DENE-881): is the ticket
// running or parked, and if parked, why — done, awaiting review, blocked on a
// recorded wait, waiting on a person, or stopped without saying why.
//
// The judgement is deterministic and lives here as a pure function. It is
// evaluated only at a run's end (the completion and failure paths); there is
// no scan (DENE-520). A model may phrase the one-sentence summary, never the
// category.
package parking

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

const (
	StateRunning = "running"
	StateParked  = "parked"
)

// Categories. The three Stalled* values are "stopped without an explanation";
// everything else is an explained state.
const (
	CategoryRunning       = "running"
	CategoryDone          = "done"
	CategoryAwaitReview   = "awaiting_review"
	CategoryBlocked       = "blocked"
	CategoryWaitingPerson = "waiting_person"
	// CategoryDelegated is a parent whose sub-issues are still open: it is
	// deliberately left in progress while work continues below.
	CategoryDelegated = "delegated"
	// CategoryIdle is a ticket nobody has started, or one parked in backlog.
	CategoryIdle = "idle"

	// CategoryStalledDelivery: the last run failed or the platform refused
	// the hand-off (branch not recorded, PR not linked, review refused), and
	// nothing is queued behind it.
	CategoryStalledDelivery = "stalled_delivery"
	// CategoryStalledUnclosed: the last run completed, the ticket is still
	// open, and no close was recorded.
	CategoryStalledUnclosed = "stalled_unclosed"
	// CategoryStalledReplyUnclosed: the ticket is blocked, the agent was
	// woken and answered, but the wait was not recorded again — or it was
	// never recorded at all.
	CategoryStalledReplyUnclosed = "stalled_reply_unclosed"
)

// Unexplained reports whether a category is "stopped without explanation".
func Unexplained(category string) bool {
	switch category {
	case CategoryStalledDelivery, CategoryStalledUnclosed, CategoryStalledReplyUnclosed:
		return true
	default:
		return false
	}
}

// Stuck kinds carried beside a stalled or blocked category.
const (
	StuckBranchRefused = "branch_refused"
	StuckPRNotLinked   = "pr_not_linked"
	StuckReviewRefused = "review_refused"
	StuckCloseRefused  = "close_refused"
	StuckRunFailed     = "run_failed"
	StuckNoWaitRecord  = "no_wait_record"
	StuckWokenNoClose  = "woken_no_close"
	StuckNoClose       = "no_close"
)

// Rejection kinds written by the handler when a status move or close is
// refused. Kept here so writer and reader agree on the vocabulary.
const (
	RejectPRNotLinked = "pr_not_linked"
	RejectReview      = "review_refused"
	RejectClose       = "close_refused"
)

// Run is one agent_task_queue row on the issue.
type Run struct {
	ID            string
	Status        string
	CreatedAt     time.Time
	StartedAt     time.Time
	CompletedAt   time.Time
	FailureReason string
	Error         string
}

// Began is when the run started, or when it was queued if it never started.
func (r Run) Began() time.Time {
	if !r.StartedAt.IsZero() {
		return r.StartedAt
	}
	return r.CreatedAt
}

// PullRequest is one PR linked to the issue.
type PullRequest struct {
	Number   int
	State    string
	URL      string
	LinkedAt time.Time
	MergedAt time.Time
}

// Rejection is one platform refusal recorded against the issue.
type Rejection struct {
	Action string
	Kind   string
	Reason string
	At     time.Time
}

// Owner is who holds the next move. Type is agent / squad / member / issue
// (another ticket this one waits on) / none.
type Owner struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// Input is everything the judgement reads. Status is the issue's effective
// canonical status.
type Input struct {
	Status          string
	HasActiveTask   bool
	HasOpenChildren bool
	Meta            map[string]any
	// Runs is newest first.
	Runs         []Run
	PullRequests []PullRequest
	// Rejections are the refusals recorded since the last run began.
	Rejections []Rejection
	Assignee   Owner
	Reviewer   Owner
}

// Event is one line of the timeline.
type Event struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

// Record is the judgement.
type Record struct {
	State       string
	Category    string
	StuckKind   string
	Unexplained bool
	NextOwner   Owner
	// Reasons are short human labels for what went wrong, used by the fixed
	// summary and handed to the model as facts.
	Reasons []string
	// CloseCurrent reports that a close record was written during (or after)
	// the last run and still matches the status — the agent's own words are
	// then the close evidence.
	CloseCurrent bool
	Timeline     []Event
}

// Classify is the whole rule.
func Classify(in Input) Record {
	rec := Record{State: StateParked, Timeline: Timeline(in)}
	var last *Run
	if len(in.Runs) > 0 {
		last = &in.Runs[0]
	}
	rec.CloseCurrent = closeCurrent(in.Meta, in.Status, last)
	conclusion := blockwait.MetaString(in.Meta, closeprotocol.KeyConclusion)

	switch in.Status {
	case "done", "cancelled":
		rec.Category = CategoryDone
		rec.NextOwner = Owner{Type: "none"}
		return rec
	}
	if in.HasActiveTask {
		rec.State = StateRunning
		rec.Category = CategoryRunning
		rec.NextOwner = in.Assignee
		return rec
	}

	switch in.Status {
	case "in_review":
		if rec.CloseCurrent && conclusion == closeprotocol.ConclusionAwaitingHuman {
			rec.Category = CategoryWaitingPerson
			rec.NextOwner = closeOwner(in.Meta, in.Reviewer)
			return rec
		}
		rec.Category = CategoryAwaitReview
		rec.NextOwner = in.Reviewer
		return rec

	case "blocked":
		wait := blockwait.ParseMetadata(in.Meta)
		woken := blockwait.MetaString(in.Meta, blockwait.KeyWokenBy) != ""
		switch {
		case !wait.Structured() && conclusion != closeprotocol.ConclusionBlocked:
			rec.Category = CategoryStalledReplyUnclosed
			rec.StuckKind = StuckNoWaitRecord
			rec.Reasons = []string{"挂在阻塞，但没有记录在等什么"}
			rec.NextOwner = in.Assignee
		case woken && last != nil && last.Status == "completed" && !rec.CloseCurrent:
			rec.Category = CategoryStalledReplyUnclosed
			rec.StuckKind = StuckWokenNoClose
			rec.Reasons = []string{"被叫醒后回了话，没有重新收口"}
			rec.NextOwner = in.Assignee
		case strings.TrimSpace(wait.NeedsHuman) != "":
			rec.Category = CategoryWaitingPerson
			rec.NextOwner = Owner{Type: "member", ID: strings.TrimSpace(wait.NeedsHuman)}
		default:
			rec.Category = CategoryBlocked
			rec.StuckKind = blockedKind(in.Meta, wait)
			if len(wait.BlockedBy) > 0 {
				rec.NextOwner = Owner{Type: "issue", ID: wait.BlockedBy[0]}
			} else {
				rec.NextOwner = in.Assignee
			}
		}
		rec.Unexplained = Unexplained(rec.Category)
		return rec

	case "backlog":
		rec.Category = CategoryIdle
		rec.NextOwner = in.Assignee
		return rec
	}

	// in_progress, todo, and custom started statuses.
	if rec.CloseCurrent && conclusion == closeprotocol.ConclusionAwaitingHuman {
		rec.Category = CategoryWaitingPerson
		rec.NextOwner = closeOwner(in.Meta, in.Assignee)
		return rec
	}
	if in.Status == "in_progress" && in.HasOpenChildren {
		rec.Category = CategoryDelegated
		rec.NextOwner = in.Assignee
		return rec
	}
	if last == nil {
		rec.Category = CategoryIdle
		rec.NextOwner = in.Assignee
		return rec
	}
	rec.NextOwner = in.Assignee
	if last.Status == "failed" || len(in.Rejections) > 0 {
		rec.Category = CategoryStalledDelivery
		rec.StuckKind, rec.Reasons = deliveryReasons(last, in.Rejections)
		rec.Unexplained = true
		return rec
	}
	if last.Status == "completed" {
		rec.Category = CategoryStalledUnclosed
		rec.StuckKind = StuckNoClose
		rec.Reasons = []string{"运行结束，票没有收口"}
		for _, pr := range in.PullRequests {
			if strings.EqualFold(pr.State, "merged") {
				rec.Reasons = append(rec.Reasons, "PR 已合并")
				break
			}
		}
		rec.Unexplained = true
		return rec
	}
	// Cancelled runs: somebody stopped it on purpose.
	rec.Category = CategoryIdle
	return rec
}

// closeCurrent: a close record exists, matches the live status, and was
// written no earlier than the last run began.
func closeCurrent(meta map[string]any, status string, last *Run) bool {
	at := blockwait.MetaString(meta, closeprotocol.KeyAt)
	if at == "" {
		return false
	}
	if s := blockwait.MetaString(meta, closeprotocol.KeyStatus); s != "" && s != status {
		return false
	}
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return false
	}
	if last == nil {
		return true
	}
	// close.at has second precision; a close in the same second the run
	// started still belongs to it.
	return !t.Before(last.Began().Truncate(time.Second))
}

func closeOwner(meta map[string]any, fallback Owner) Owner {
	typ := blockwait.MetaString(meta, closeprotocol.KeyNextOwnerType)
	id := blockwait.MetaString(meta, closeprotocol.KeyNextOwnerID)
	if typ == "" || typ == closeprotocol.OwnerNone || id == "" {
		return fallback
	}
	return Owner{Type: typ, ID: id}
}

func blockedKind(meta map[string]any, wait blockwait.Record) string {
	if k := blockwait.MetaString(meta, closeprotocol.KeyBlockKind); k != "" {
		return k
	}
	switch {
	case len(wait.BlockedBy) > 0:
		return closeprotocol.BlockDependency
	case wait.HasWakeAt:
		return "clock"
	case strings.TrimSpace(wait.WaitCondition) != "":
		return "condition"
	default:
		return ""
	}
}

// deliveryReasons names what failed, most specific first.
func deliveryReasons(last *Run, rejections []Rejection) (string, []string) {
	var kinds, labels []string
	add := func(kind, label string) {
		for _, l := range labels {
			if l == label {
				return
			}
		}
		kinds = append(kinds, kind)
		labels = append(labels, label)
	}
	if last != nil && last.Status == "failed" {
		if strings.Contains(last.Error, "refusing to record branch") {
			add(StuckBranchRefused, "平台拒绝记录分支")
		} else {
			add(StuckRunFailed, failureLabel(last.FailureReason))
		}
	}
	for _, r := range rejections {
		switch r.Kind {
		case RejectPRNotLinked:
			add(StuckPRNotLinked, "PR 未关联，送审被拒")
		case RejectReview:
			add(StuckReviewRefused, "送审被拒")
		default:
			add(StuckCloseRefused, "关单被拒")
		}
	}
	if len(kinds) == 0 {
		return StuckRunFailed, []string{"运行失败"}
	}
	return kinds[0], labels
}

var failureLabels = map[string]string{
	"timeout":                                     "运行超时",
	"task_time_limit":                             "运行到达时限",
	"runtime_offline":                             "运行时离线",
	"runtime_reconnect_timeout":                   "运行时重连超时",
	"runtime_recovery":                            "运行时重启中断",
	"queued_expired":                              "排队过期",
	"iteration_limit":                             "达到轮次上限",
	"agent_blocked":                               "智能体自报卡住",
	"environment_prepare_failed":                  "工作环境准备失败",
	"agent_error.provider_quota_limit":            "模型额度耗尽",
	"agent_error.provider_auth_or_access":         "模型鉴权失败",
	"agent_error.context_overflow":                "上下文超限",
	"agent_error.provider_capacity_or_rate_limit": "模型限流",
}

func failureLabel(reason string) string {
	if l, ok := failureLabels[reason]; ok {
		return "运行失败（" + l + "）"
	}
	return "运行失败"
}

// Timeline lays runs, PRs, refusals, and the close on one clock, oldest
// first, capped to the newest 30 events.
func Timeline(in Input) []Event {
	var out []Event
	for _, r := range in.Runs {
		if !r.StartedAt.IsZero() {
			out = append(out, Event{At: r.StartedAt, Kind: "run_started"})
		}
		if r.CompletedAt.IsZero() {
			continue
		}
		switch r.Status {
		case "completed":
			out = append(out, Event{At: r.CompletedAt, Kind: "run_completed"})
		case "failed":
			detail := r.FailureReason
			if e := clip(r.Error, 160); e != "" {
				if detail != "" {
					detail += ": "
				}
				detail += e
			}
			out = append(out, Event{At: r.CompletedAt, Kind: "run_failed", Detail: detail})
		case "cancelled":
			out = append(out, Event{At: r.CompletedAt, Kind: "run_cancelled"})
		}
	}
	for _, pr := range in.PullRequests {
		label := fmt.Sprintf("#%d %s", pr.Number, pr.State)
		if !pr.LinkedAt.IsZero() {
			out = append(out, Event{At: pr.LinkedAt, Kind: "pr_linked", Detail: label})
		}
		if !pr.MergedAt.IsZero() {
			out = append(out, Event{At: pr.MergedAt, Kind: "pr_merged", Detail: fmt.Sprintf("#%d", pr.Number)})
		}
	}
	for _, r := range in.Rejections {
		out = append(out, Event{At: r.At, Kind: "rejected", Detail: r.Action + ": " + clip(r.Reason, 160)})
	}
	if at := blockwait.MetaString(in.Meta, closeprotocol.KeyAt); at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			out = append(out, Event{At: t, Kind: "closed", Detail: blockwait.MetaString(in.Meta, closeprotocol.KeyConclusion)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > 30 {
		out = out[len(out)-30:]
	}
	return out
}

// FallbackSummary is the fixed sentence used when neither the agent's own
// words nor the model are available.
func FallbackSummary(rec Record) string {
	reasons := strings.Join(rec.Reasons, "，")
	switch rec.Category {
	case CategoryStalledDelivery:
		if rec.StuckKind == StuckRunFailed || rec.StuckKind == StuckBranchRefused {
			if strings.HasPrefix(reasons, "运行失败") {
				return reasons
			}
			return "运行失败：" + reasons
		}
		return "交付卡住：" + reasons
	case CategoryStalledUnclosed:
		return "运行已结束：" + reasons
	case CategoryStalledReplyUnclosed:
		return "回了话没收口：" + reasons
	case CategoryRunning:
		return "正在运行"
	case CategoryDone:
		return "已完成"
	case CategoryAwaitReview:
		return "已交付，等验收"
	case CategoryWaitingPerson:
		return "有意停下，等人决定"
	case CategoryBlocked:
		return "卡住，等待已记录"
	case CategoryDelegated:
		return "子任务推进中"
	default:
		return "尚未开跑"
	}
}

// ErrorOnly reports whether what the agent said is just the run's error echoed
// back (the daemon posts a failed hand-off as an agent comment).
func ErrorOnly(said, runErr string) bool {
	said = strings.TrimSpace(said)
	if said == "" {
		return true
	}
	if strings.HasPrefix(said, "local_directory worktree:") {
		return true
	}
	runErr = strings.TrimSpace(runErr)
	if runErr == "" {
		return false
	}
	probe := runErr
	if utf8.RuneCountInString(probe) > 60 {
		probe = string([]rune(probe)[:60])
	}
	return strings.Contains(said, probe)
}

// Clip collapses whitespace and cuts s to n runes.
func Clip(s string, n int) string { return clip(s, n) }

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
