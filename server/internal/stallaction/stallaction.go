// Package stallaction owns the stall action state machine (DENE-1149): the
// stall.* issue-metadata keys, which transitions between them are legal, and
// the clocks that bound them. Review (announce), Keep, Undo and the patrol's
// expiry/cancel/parent-completion all go through this package, so a rule
// about one transition cannot drift away from the others.
//
// The package is pure: callers pass the ticket's status and metadata plus a
// Probe for the facts that need the database, and receive a Patch to write.
// Status writes, comments, inbox rows and events stay with the caller.
//
// Two invariants DENE-1176 restored live here rather than in a handler:
//   - an expired announcement re-checks the ticket before it cancels
//     (Expire), so a ticket that visibly resumed is kept, not cancelled;
//   - Undo only applies while the ticket still carries the status the system
//     wrote, so it never overwrites a person's later change.
package stallaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

// Flat metadata keys. Values are strings (the KV surface is primitive-only);
// stall.candidate may also arrive as a JSON boolean from a trusted agent.
const (
	KeyAction      = "stall.action"
	KeyPrevious    = "stall.previous_status"
	KeyAnnouncedAt = "stall.announced_at"
	KeyReviewUntil = "stall.review_until"
	KeyRevertUntil = "stall.revert_until"
	KeyReason      = "stall.reason"
	KeyCandidate   = "stall.candidate"
	// KeyJudgedAt is the last_activity_at snapshot inspected by the model,
	// rather than the request time. Activity arriving while it thinks must
	// requalify the ticket on the next patrol.
	KeyJudgedAt = "stall.judged_at"
)

// Values of stall.action.
const (
	ActionCandidate = "candidate"
	ActionAnnounced = "announced"
	ActionKept      = "kept"
	ActionCancelled = "cancelled"
	ActionParent    = "parent_completed"
	ActionRevoked   = "revoked"
)

const (
	QuietAfter      = 36 * time.Hour
	AnnouncementFor = 24 * time.Hour
	RevertFor       = 7 * 24 * time.Hour
)

// Rejection is a transition the state machine refuses. Its text is the
// user-facing reason the HTTP surface returns with 409 Conflict.
type Rejection string

func (r Rejection) Error() string { return string(r) }

const (
	ErrTerminal        Rejection = "已完成或已取消的票不能进入停滞公示"
	ErrPaused          Rejection = "有意暂停的票不会进入停滞处理"
	ErrLinkedPR        Rejection = "这张票已有关联 PR，不能进入停滞公示；请先完成验收或明确处理"
	ErrNothingToKeep   Rejection = "这张票当前没有待保留的停滞公示"
	ErrReviewClosed    Rejection = "公示期已结束"
	ErrNothingToUndo   Rejection = "这张票没有可撤销的自动停滞处理"
	ErrUndoExpired     Rejection = "撤销期限已过"
	ErrMissingPrevious Rejection = "缺少自动处理前的状态，无法安全撤销"
)

// ErrLookup wraps a Probe failure. It is not a refusal of the transition, so
// callers report it as an internal error.
var ErrLookup = errors.New("stall action lookup failed")

// Patch is the set of stall.* keys a transition writes. Keys it does not name
// keep their current values.
type Patch map[string]string

// State is the parsed stall.* record of one ticket.
type State struct {
	Action      string
	Previous    string
	AnnouncedAt string
	ReviewUntil string
	RevertUntil string
	Reason      string
	JudgedAt    string
	candidate   bool
}

// Read parses the stall.* keys out of issue metadata.
func Read(meta map[string]any) State {
	candidate, _ := meta[KeyCandidate].(bool)
	return State{
		Action:      metaString(meta, KeyAction),
		Previous:    metaString(meta, KeyPrevious),
		AnnouncedAt: metaString(meta, KeyAnnouncedAt),
		ReviewUntil: metaString(meta, KeyReviewUntil),
		RevertUntil: metaString(meta, KeyRevertUntil),
		Reason:      metaString(meta, KeyReason),
		JudgedAt:    metaString(meta, KeyJudgedAt),
		candidate:   candidate || metaString(meta, KeyCandidate) == "true",
	}
}

func metaString(meta map[string]any, key string) string {
	if v, ok := meta[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// Marked reports an explicit candidate marker written by a trusted agent.
func (s State) Marked() bool { return s.candidate }

// JudgedFor reports whether the model already judged the ticket at (or after)
// this activity snapshot, so the patrol must not ask again.
func (s State) JudgedFor(activityAt time.Time) bool {
	judgedAt, err := time.Parse(time.RFC3339Nano, s.JudgedAt)
	return err == nil && !activityAt.After(judgedAt)
}

// Ticket is what every transition needs to know about the issue itself.
type Ticket struct {
	Status string
	Meta   map[string]any
}

func (t Ticket) state() State { return Read(t.Meta) }

// Paused reports a close record that deliberately parks the ticket. Paused
// tickets never enter or advance the stall machine.
func (t Ticket) Paused() bool {
	return closeprotocol.ExplainedPause(blockwait.MetaString(t.Meta, closeprotocol.KeyConclusion))
}

func terminal(status string) bool { return status == "done" || status == "cancelled" }

// Probe answers the facts that need the database. It is asked lazily, in the
// order the rules need them, so a failing lookup never hides an earlier
// refusal.
type Probe interface {
	HasLinkedPR(ctx context.Context) (bool, error)
	HasActiveRun(ctx context.Context) (bool, error)
	CommentedSince(ctx context.Context, at time.Time) (bool, error)
}

// Announcement is the result of a successful Announce.
type Announcement struct {
	Patch       Patch
	Reason      string
	ReviewUntil time.Time
}

// Announce starts the 24-hour public notice. reason is the reviewer's (or the
// model's) explanation; artifactCheck is the server's own delivery check,
// which is always appended so no announcement omits it.
func Announce(ctx context.Context, t Ticket, probe Probe, reason, artifactCheck string, now time.Time) (Announcement, error) {
	if terminal(t.Status) {
		return Announcement{}, ErrTerminal
	}
	if t.Paused() {
		return Announcement{}, ErrPaused
	}
	// A duplicate-looking ticket can still carry the delivery that invalidates
	// the duplicate claim. The reviewer must inspect that delivery first, so
	// no cancellation clock starts for a ticket with a linked PR.
	linked, err := probe.HasLinkedPR(ctx)
	if err != nil {
		return Announcement{}, fmt.Errorf("%w: linked pull requests: %v", ErrLookup, err)
	}
	if linked {
		return Announcement{}, ErrLinkedPR
	}
	now = now.UTC()
	until := now.Add(AnnouncementFor)
	full := strings.TrimSpace(reason) + "；" + artifactCheck + "。"
	return Announcement{
		Patch: Patch{
			KeyAction:      ActionAnnounced,
			KeyAnnouncedAt: now.Format(time.RFC3339),
			KeyReviewUntil: until.Format(time.RFC3339),
			KeyReason:      full,
		},
		Reason:      full,
		ReviewUntil: until,
	}, nil
}

// Keep is the "保留" button: it stops an announced ticket from being
// cancelled, but only while the notice window is still open.
func Keep(t Ticket, now time.Time) (Patch, error) {
	s := t.state()
	if s.Action != ActionAnnounced {
		return nil, ErrNothingToKeep
	}
	if until, err := time.Parse(time.RFC3339, s.ReviewUntil); err != nil || now.UTC().After(until) {
		return nil, ErrReviewClosed
	}
	return Patch{KeyAction: ActionKept, KeyReason: "Kun 在公示期内选择保留，系统不会自动取消。"}, nil
}

// ExpiryVerdict is what the patrol does with an announcement.
type ExpiryVerdict int

const (
	// ExpiryWait leaves the ticket alone: not announced, paused, or the
	// notice window is still open.
	ExpiryWait ExpiryVerdict = iota
	// ExpiryKeep keeps the ticket because work visibly resumed during the
	// notice; Expiry.Patch records why.
	ExpiryKeep
	// ExpiryCancel cancels the ticket; Expiry.Patch records the old status
	// so Undo can restore it.
	ExpiryCancel
)

// Expiry is the patrol's decision for one announced ticket.
type Expiry struct {
	Verdict ExpiryVerdict
	Patch   Patch
	// Why is the interception reason (ExpiryKeep) or the cancellation reason
	// (ExpiryCancel), ready for the evidence comment.
	Why string
}

// Expire decides an announcement whose clock may have run out. An expired
// announcement always re-checks the ticket before cancelling (DENE-1176):
// work resuming during the notice window is itself an interception, and a
// lookup that fails keeps the ticket rather than guessing.
func Expire(ctx context.Context, t Ticket, probe Probe, now time.Time) Expiry {
	s := t.state()
	if s.Action != ActionAnnounced || t.Paused() || terminal(t.Status) {
		return Expiry{Verdict: ExpiryWait}
	}
	until, err := time.Parse(time.RFC3339, s.ReviewUntil)
	if err != nil || now.UTC().Before(until) {
		return Expiry{Verdict: ExpiryWait}
	}
	if why := interception(ctx, t.Status, s, probe); why != "" {
		return Expiry{
			Verdict: ExpiryKeep,
			Patch:   Patch{KeyAction: ActionKept, KeyReason: why + "，系统不会自动取消。"},
			Why:     why,
		}
	}
	reason := s.Reason + " 公示到期无人保留，系统自动取消。"
	return Expiry{
		Verdict: ExpiryCancel,
		Patch: Patch{
			KeyAction:      ActionCancelled,
			KeyPrevious:    t.Status,
			KeyRevertUntil: now.UTC().Add(RevertFor).Format(time.RFC3339),
			KeyReason:      reason,
		},
		Why: reason,
	}
}

// interception explains why an expired announcement must not cancel the
// ticket. The announcement's own system comment does not count as resumed
// work; the Probe only counts non-system comments.
func interception(ctx context.Context, status string, s State, probe Probe) string {
	if status == "backlog" {
		return "公示期内票被放回待规划"
	}
	if linked, err := probe.HasLinkedPR(ctx); err != nil {
		return "无法核对关联 PR"
	} else if linked {
		return "公示期内关联了 PR"
	}
	if active, err := probe.HasActiveRun(ctx); err != nil {
		return "无法核对进行中的运行"
	} else if active {
		return "公示期内有运行在跑"
	}
	announcedAt, err := time.Parse(time.RFC3339, s.AnnouncedAt)
	if err != nil {
		return ""
	}
	resumed, err := probe.CommentedSince(ctx, announcedAt)
	if err != nil {
		return "无法核对公示期内的评论"
	}
	if resumed {
		return "公示期内有人或智能体继续推进"
	}
	return ""
}

// CompleteParent records the patrol closing a quiet parent whose children are
// all terminal. The previous status is kept so Undo can restore it.
func CompleteParent(previousStatus string, now time.Time) Patch {
	now = now.UTC()
	return Patch{
		KeyAction:      ActionParent,
		KeyPrevious:    previousStatus,
		KeyAnnouncedAt: now.Format(time.RFC3339),
		KeyRevertUntil: now.Add(RevertFor).Format(time.RFC3339),
		KeyReason:      "所有子任务均已完成，系统按规则自动收口；可在 7 天内撤销。",
	}
}

// Reversal is the result of a successful Undo.
type Reversal struct {
	// Restore is the status to write back.
	Restore string
	Patch   Patch
}

// appliedStatus is the status an automatic action wrote.
func appliedStatus(action string) string {
	if action == ActionCancelled {
		return "cancelled"
	}
	return "done"
}

// Undo reverses an automatic close or cancel within its seven-day window.
// It only applies while the ticket still carries the status the system
// wrote (DENE-1176): a person who already moved the ticket on must not have
// that change silently overwritten.
func Undo(t Ticket, now time.Time) (Reversal, error) {
	s := t.state()
	if s.Action != ActionParent && s.Action != ActionCancelled {
		return Reversal{}, ErrNothingToUndo
	}
	until, err := time.Parse(time.RFC3339, s.RevertUntil)
	if err != nil || now.UTC().After(until) {
		return Reversal{}, ErrUndoExpired
	}
	if t.Status != appliedStatus(s.Action) {
		return Reversal{}, Rejection(fmt.Sprintf("票状态已被改为 `%s`，不再撤销自动停滞处理", t.Status))
	}
	if s.Previous == "" {
		return Reversal{}, ErrMissingPrevious
	}
	return Reversal{
		Restore: s.Previous,
		Patch:   Patch{KeyAction: ActionRevoked, KeyReason: "自动停滞处理已撤销。"},
	}, nil
}
