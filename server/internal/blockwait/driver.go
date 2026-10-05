package blockwait

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Driver kinds (ADR-0006). Every open issue has at least one; DriverNone is
// the state the platform exists to remove.
const (
	DriverRun    = "run"
	DriverWait   = "wait"
	DriverPerson = "person"
	DriverNone   = "none"
)

// Undriven bookkeeping (DENE-1342). These keys outlive a status change on
// purpose: the patrol's revive count must survive the todo → blocked → todo
// flapping a failing seat produces.
const (
	KeyRevives     = "driver.revives"
	KeyRevivedAt   = "driver.revived_at"
	KeyEscalatedAt = "driver.escalated_at"

	// ReviveLimit is how many times the patrol reruns an undriven issue on its
	// own seat inside ReviveWindow before it escalates to the parent.
	ReviveLimit = 2
	// ReviveWindow resets the count: an issue that went undriven once today
	// and once last week is not the same stall.
	ReviveWindow = 24 * time.Hour
)

// Patrol actions for an issue nobody drives.
const (
	// ActionRevive reruns the undriven issue on its own seat.
	ActionRevive = "revive"
	// ActionEscalate wakes whoever owns the undriven issue's parent with the
	// disposition command, or a person when there is no parent.
	ActionEscalate = "escalate"
)

// DriverFacts is everything DriverOf reads. The caller resolves Status to a
// built-in key and Closed from the workspace catalog.
type DriverFacts struct {
	Status       string
	Closed       bool
	AssigneeType string
	ReviewerType string
	ActiveRun    bool
	// Wakeup is an enabled issue wakeup somebody created (system rules do not
	// count: the parent's child_done rule exists on every parent).
	Wakeup       bool
	OpenChildren bool
	Record       Record
	Watched      bool
	Undriven     UndrivenState
	Now          time.Time
}

// Driver is who or what moves an issue forward right now.
type Driver struct {
	Kind   string
	Reason string
	// Revives and Escalated are only set on DriverNone: what the patrol has
	// already tried.
	Revives   int
	Escalated bool
}

// DriverOf answers "who is moving this issue". ok is false for an issue that
// needs no driver: closed, or parked in backlog on purpose.
func DriverOf(f DriverFacts) (Driver, bool) {
	if f.Now.IsZero() {
		f.Now = time.Now()
	}
	if f.Closed || f.Status == "done" || f.Status == "cancelled" || f.Status == "backlog" {
		return Driver{}, false
	}
	if f.ActiveRun {
		return Driver{Kind: DriverRun, Reason: "有运行在跑或在排队"}, true
	}
	if f.Status == "in_review" {
		if f.ReviewerType == "member" {
			return Driver{Kind: DriverPerson, Reason: "等验收人"}, true
		}
		return Driver{Kind: DriverWait, Reason: "等验收"}, true
	}
	if strings.TrimSpace(f.Record.NeedsHuman) != "" {
		return Driver{Kind: DriverPerson, Reason: "等被点名的人"}, true
	}
	if f.AssigneeType == "member" {
		return Driver{Kind: DriverPerson, Reason: "执行人是成员"}, true
	}
	if f.Watched && f.Record.Structured() && (f.Status == "blocked" || f.Status == "in_progress") {
		return Driver{Kind: DriverWait, Reason: waitSentence(f.Record)}, true
	}
	if f.Watched && f.Status == "blocked" {
		// An unstructured block the patrol still watches: it is woken once
		// after QuietAfter, so somebody is still coming.
		return Driver{Kind: DriverWait, Reason: "阻塞没写在等什么，巡检会接回来"}, true
	}
	if f.Wakeup {
		return Driver{Kind: DriverWait, Reason: "有定好的叫醒"}, true
	}
	if f.OpenChildren {
		return Driver{Kind: DriverWait, Reason: "等子票"}, true
	}
	st := f.Undriven.Current(f.Now)
	return Driver{
		Kind:      DriverNone,
		Reason:    noneSentence(f),
		Revives:   st.Revives,
		Escalated: st.HasEscalated,
	}, true
}

func waitSentence(r Record) string {
	switch {
	case len(r.BlockedBy) > 0:
		return "等 " + strings.Join(r.BlockedBy, "、")
	case r.HasWakeAt:
		return "到点复查 " + r.WakeAt.UTC().Format(time.RFC3339)
	case strings.TrimSpace(r.WaitCondition) != "":
		return "等「" + strings.TrimSpace(r.WaitCondition) + "」"
	default:
		return "有记下的等待"
	}
}

func noneSentence(f DriverFacts) string {
	switch {
	case f.AssigneeType == "":
		return "没有执行人，也没有运行"
	case f.Status == "todo":
		return "有执行人，但没有运行"
	case f.Status == "in_progress":
		return "进行中，但没有运行，也没记下等待"
	case f.Status == "blocked":
		return "阻塞，但巡检没在看它"
	default:
		return "没有运行、等待或被点名的人"
	}
}

// UndrivenState is what the patrol already did for one undriven issue.
type UndrivenState struct {
	Revives      int
	LastRevive   time.Time
	EscalatedAt  time.Time
	HasEscalated bool
}

// ParseUndriven reads the bookkeeping keys out of issue metadata.
func ParseUndriven(meta map[string]any) UndrivenState {
	var st UndrivenState
	if n, err := strconv.Atoi(MetaString(meta, KeyRevives)); err == nil && n > 0 {
		st.Revives = n
	}
	if t, err := time.Parse(time.RFC3339, MetaString(meta, KeyRevivedAt)); err == nil {
		st.LastRevive = t
	}
	if t, err := time.Parse(time.RFC3339, MetaString(meta, KeyEscalatedAt)); err == nil {
		st.EscalatedAt = t
		st.HasEscalated = true
	}
	return st
}

// Current drops a count or an escalation older than ReviveWindow.
func (s UndrivenState) Current(now time.Time) UndrivenState {
	if s.LastRevive.IsZero() || now.Sub(s.LastRevive) >= ReviveWindow {
		s.Revives = 0
	}
	if s.HasEscalated && now.Sub(s.EscalatedAt) >= ReviveWindow {
		s.HasEscalated = false
		s.EscalatedAt = time.Time{}
	}
	return s
}

// DecideUndriven is the patrol's step for an issue nobody drives: rerun it on
// its own seat up to ReviveLimit times, then escalate once, then hold. ref
// names the issue in the sentence; target is the Decision's Target.
func DecideUndriven(ref, target, why string, st UndrivenState, now time.Time) Decision {
	st = st.Current(now)
	if !st.LastRevive.IsZero() && now.Sub(st.LastRevive) < QuietAfter {
		// The last rerun has not had time to start; do not burn the count.
		return Decision{Action: ActionHold}
	}
	if st.Revives < ReviveLimit {
		return Decision{
			Action: ActionRevive,
			Target: target,
			Reason: fmt.Sprintf("%s 没人在推进（%s），平台在原席位重跑一次（第 %d/%d 次）。", ref, why, st.Revives+1, ReviveLimit),
		}
	}
	if !st.HasEscalated {
		return Decision{
			Action: ActionEscalate,
			Target: target,
			Reason: fmt.Sprintf("%s 没人在推进（%s），原席位已经重跑 %d 次还是停着。", ref, why, st.Revives),
		}
	}
	return Decision{Action: ActionHold, Reason: ref + " 已经上报，等处置。"}
}

// UndrivenFollowUp is the metadata the patrol writes on the undriven issue
// itself after a revive or an escalation.
func (d Decision) UndrivenFollowUp(st UndrivenState, now time.Time) map[string]string {
	set := map[string]string{}
	stamp := now.UTC().Format(time.RFC3339)
	switch d.Action {
	case ActionRevive:
		set[KeyRevives] = strconv.Itoa(st.Current(now).Revives + 1)
		set[KeyRevivedAt] = stamp
	case ActionEscalate:
		set[KeyEscalatedAt] = stamp
	}
	return set
}

// UndrivenKeys are dropped when a disposition resets the bookkeeping.
func UndrivenKeys() []string {
	return []string{KeyRevives, KeyRevivedAt, KeyEscalatedAt}
}

// Disposition actions on an undriven issue (DENE-1342). One server-checked
// command, `multica issue dispose`, carries them.
const (
	DisposeRerun   = "rerun"
	DisposeReroute = "reroute"
	DisposeSplit   = "split"
	DisposeCancel  = "cancel"
)

// ValidDisposition reports whether action is one of the four.
func ValidDisposition(action string) bool {
	switch action {
	case DisposeRerun, DisposeReroute, DisposeSplit, DisposeCancel:
		return true
	}
	return false
}

// DispositionMenu is the sentence an escalation leaves for the parent's owner.
func DispositionMenu(ref string) string {
	return fmt.Sprintf("请选一种处置：`multica issue dispose %[1]s --action rerun`（原席位再跑）、`--action reroute`（交给路由重新选执行人）、`--action split --into \"标题\"`（拆成子票）、`--action cancel --reason \"...\"`（取消）。", ref)
}
