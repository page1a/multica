package routing

import (
	"sort"
	"strings"
)

// 接着做 (DENE-1202): a ticket that continues earlier work goes back to the
// seat that did that work, when that seat can still take it. The earlier work
// is a related ticket — the previous stage under the same parent, the parent
// itself, or a ticket created in the same batch — and its executor is the
// candidate. The rule sits after the person's words and a person's hand
// (those tickets arrive with the slot already held) and before the ladder's
// tier + direction pick.
//
// The rule ships behind a workspace switch that is off by default: while it
// is off the ladder's pick is written unchanged, and the assignment comment
// says who the rule WOULD have picked, so a person can read a few days of
// shadow verdicts before turning it on.

// Relation is how a related ticket is related to the one being routed.
type Relation string

const (
	// RelationPreviousStage — a sibling under the same parent, one stage
	// earlier. The most direct continuation there is.
	RelationPreviousStage Relation = "previous_stage"
	// RelationParent — the ticket's parent.
	RelationParent Relation = "parent"
	// RelationSameBatch — created by the same agent run.
	RelationSameBatch Relation = "same_batch"
)

// relationRank is the order candidates are tried in, lower first.
var relationRank = map[Relation]int{
	RelationPreviousStage: 0,
	RelationParent:        1,
	RelationSameBatch:     2,
}

// relationLabel is the relation in the comment's words.
var relationLabel = map[Relation]string{
	RelationPreviousStage: "上一阶段",
	RelationParent:        "父票",
	RelationSameBatch:     "同批",
}

// RelatedTicket is one ticket whose executor may continue this one. The store
// fills it; only tickets held by an agent are listed.
type RelatedTicket struct {
	// Identifier is the human key (DENE-N), for the comment.
	Identifier string
	Relation   Relation
	// ExecutorID is the agent holding that ticket.
	ExecutorID string
}

// ContinuationSeat is what is known about one related executor at decision
// time. OnRoster false means the roster does not list it: work switched off,
// or archived.
type ContinuationSeat struct {
	Seat     Seat
	OnRoster bool
	// Unpickable is SeatSelectable's refusal for the seat's own record,
	// e.g. mention_only: it takes work when named, never by this rule.
	Unpickable   string
	Availability string
}

// ContinuationSnapshot is the whole input of the rule. The caller assembles
// it; the rule reads nothing else.
type ContinuationSnapshot struct {
	Related []RelatedTicket
	// Seats is keyed by agent id. A related executor missing here is treated
	// as off the roster.
	Seats map[string]ContinuationSeat
	// Scene is the ticket's scene (DENE-1477). Direction is the single-domain
	// spelling, read only when Scene is generic.
	Scene     Scene
	Direction string
	// RequiredTier is the rung routing judged for this ticket; a continuation
	// seat must sit on it or above.
	RequiredTier string
	// TierOrder is the ladder's rung keys, strongest first.
	TierOrder []string
}

// ContinuationPick is the rule's answer.
type ContinuationPick struct {
	// OK is true when a related executor can take the ticket.
	OK   bool
	Seat Seat
	From RelatedTicket
	// Skipped says, per related executor that did not qualify, why not.
	Skipped []string
}

// Detail is the "which earlier work" phrase, e.g. "DENE-12（上一阶段）的执行人".
func (p ContinuationPick) Detail() string {
	if !p.OK {
		return ""
	}
	return continuationDetail(p.From)
}

func continuationDetail(t RelatedTicket) string {
	return t.Identifier + "（" + relationLabel[t.Relation] + "）的执行人"
}

// PickContinuation applies the rule to a snapshot. It is pure: the same
// snapshot always yields the same answer, and nothing outside the snapshot is
// consulted.
//
// Related tickets are tried previous stage first, then parent, then same
// batch, in the order given within each relation. The first executor that
// clears every check wins:
//
//   - can take work: on the roster, and not disabled, offline, out of quota,
//     or archived;
//   - strong enough: its rung is the judged rung or stronger;
//   - right direction: a generic seat, or the ticket's own direction.
func PickContinuation(s ContinuationSnapshot) ContinuationPick {
	related := make([]RelatedTicket, 0, len(s.Related))
	for _, t := range s.Related {
		if t.ExecutorID != "" {
			related = append(related, t)
		}
	}
	sort.SliceStable(related, func(i, j int) bool {
		return relationRank[related[i].Relation] < relationRank[related[j].Relation]
	})

	rank := make(map[string]int, len(s.TierOrder))
	for i, key := range s.TierOrder {
		rank[strings.ToLower(key)] = i
	}
	required, requiredKnown := rank[strings.ToLower(s.RequiredTier)]

	var out ContinuationPick
	seen := map[string]bool{}
	for _, t := range related {
		if seen[t.ExecutorID] {
			continue
		}
		seen[t.ExecutorID] = true
		seat, ok := s.Seats[t.ExecutorID]
		name := seat.Seat.Name
		if name == "" {
			name = t.ExecutorID
		}
		if why := continuationBlocker(seat, ok, rank, required, requiredKnown, s.scene()); why != "" {
			out.Skipped = append(out.Skipped, continuationDetail(t)+" "+name+" "+why)
			continue
		}
		out.OK, out.Seat, out.From = true, seat.Seat, t
		return out
	}
	return out
}

// continuationBlocker is why one related executor cannot continue, "" when it
// can.
func continuationBlocker(seat ContinuationSeat, known bool, rank map[string]int, required int, requiredKnown bool, scene Scene) string {
	if !known || !seat.OnRoster {
		return "已停用或已归档"
	}
	if seat.Unpickable == ReasonMentionOnly {
		return "仅点名，不自动派单"
	}
	if seat.Unpickable == ReasonOutOfProject {
		return "限定了别的项目"
	}
	if seat.Unpickable != "" {
		return "不能自动派单"
	}
	switch seat.Availability {
	case AvailabilityDisabled:
		return "已停用"
	case AvailabilityDead, AvailabilityUnreachable:
		return "runtime 不在线"
	case AvailabilityQuotaExhausted:
		return "额度耗尽"
	case AvailabilityArchived, AvailabilityCancelled:
		return "已归档"
	}
	have, ok := rank[strings.ToLower(seat.Seat.TierKey)]
	if !ok {
		return "不在档位表上"
	}
	if requiredKnown && have > required {
		return "档位不够（" + seat.Seat.TierLabel + "档低于这张票判的档）"
	}
	if DomainFit(scene, seat.Seat.Direction) == FitOther {
		return "方向不对（" + seat.Seat.Direction + "）"
	}
	return ""
}

// ContinuationLine is the assignment comment's line about the rule, or ""
// when there is nothing to add: no related executor to talk about, or the
// switch is on and the pick was written — the first line already says
// 接着做 then. written is the seat the slot actually received.
func ContinuationLine(p ContinuationPick, enabled bool, written *Seat) string {
	if !p.OK && len(p.Skipped) == 0 {
		return ""
	}
	if enabled && p.OK && written != nil && written.ID == p.Seat.ID {
		return ""
	}
	prefix := "**接着做**"
	if !enabled {
		prefix = "**接着做（影子运行：开关没开，实际选择没改）**"
	}
	if !p.OK {
		return prefix + "：没有能接着做的原执行人——" + strings.Join(p.Skipped, "；") + "，按档位选。"
	}
	if written != nil && written.ID == p.Seat.ID {
		return prefix + "：按新规则也会选 " + p.Seat.Name + "（接着做：" + p.Detail() + "）。"
	}
	return prefix + "：按新规则会选 " + p.Seat.Name + "（接着做：" + p.Detail() + "）。"
}

func (s ContinuationSnapshot) scene() Scene {
	if !s.Scene.Generic() {
		return s.Scene
	}
	return SceneOf(s.Direction)
}
