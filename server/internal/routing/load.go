package routing

import (
	"strconv"

	"github.com/multica-ai/multica/server/internal/quotarelay"
)

// 负载 (DENE-1203): inside the rung and direction the ladder picked, the work
// goes to a seat that is not busy. The ladder alone always takes the first
// seat of the cell, so a batch of independent tickets created together all
// land on one seat while its siblings sit idle. When every seat is equally
// busy, the ladder's own pick stands.
//
// The order inside the cell is quotarelay's same-tier order — the one a quota
// handoff uses — with the seat's unfinished runs added after demotion. One
// order for a rung, wherever a rung is ordered.
//
// The rule ships behind a workspace switch that is off by default, separate
// from 接着做. While it is off the ladder's pick is written unchanged and the
// assignment comment says who the rule would have picked. With both switches
// on, 接着做 wins.

// LoadSeat is one seat of the ladder's cell at decision time.
type LoadSeat struct {
	Seat         Seat
	Availability string
	// Running is the seat's unfinished runs: queued, dispatched, running.
	Running   int
	Demoted   bool
	UsageRank int
}

// LoadSnapshot is the whole input of the rule. The caller assembles it from
// one read; the rule reads nothing else.
type LoadSnapshot struct {
	// Base is the ladder's pick. Its rung and direction define the cell.
	Base Seat
	// Seats is the roster's seats; the rule keeps the ones in the cell.
	Seats []LoadSeat
}

// LoadPick is the rule's answer.
type LoadPick struct {
	// Moved is true when the rule picks a seat other than the ladder's.
	Moved bool
	Seat  Seat
	// Running is the picked seat's unfinished runs.
	Running int
	Base    Seat
	// BaseRunning is the ladder pick's unfinished runs.
	BaseRunning int
}

// Detail is the "why not the ladder's seat" phrase, e.g.
// "孙悟空 正在跑 2 个活，孙悟空二号 空着".
func (p LoadPick) Detail() string {
	if !p.Moved {
		return ""
	}
	return p.Base.Name + " 正在跑 " + strconv.Itoa(p.BaseRunning) + " 个活，" + p.Seat.Name + loadPhrase(p.Running)
}

func loadPhrase(n int) string {
	if n == 0 {
		return " 空着"
	}
	return " 跑着 " + strconv.Itoa(n) + " 个"
}

// PickLoad applies the rule to a snapshot. It is pure.
//
// The cell is the seats on the base's rung with the base's direction, that
// can take work (unknown availability counts as able, as everywhere else in
// routing). Among them quotarelay.BestOnTier chooses: not demoted first, then
// fewest unfinished runs, then usage headroom, then name. The base is kept
// whenever the chosen seat is not strictly less busy — a tie is the ladder's
// order, not a new one.
//
// Busyness never outranks 紧张: unless the ladder's pick is itself tight, a
// tight seat stays out of the cell, so an idle 紧张 seat does not take work
// from a busy 常规 or 充足 one. 紧张 means "call it less". 充足 and 常规 are only
// an order, so load still spreads work between them.
//
// An upshifted base is left alone: 「允许上调一档」 already decided that cell.
func PickLoad(s LoadSnapshot) LoadPick {
	out := LoadPick{Seat: s.Base, Base: s.Base}
	pool := make([]quotarelay.Seat, 0, len(s.Seats))
	byID := make(map[string]LoadSeat, len(s.Seats))
	baseUsage, baseSeen := 0, false
	for _, ls := range s.Seats {
		if ls.Seat.ID == s.Base.ID {
			baseUsage, baseSeen = ls.UsageRank, true
		}
	}
	for _, ls := range s.Seats {
		if ls.Seat.ID == s.Base.ID {
			out.BaseRunning, out.Running = ls.Running, ls.Running
		}
		inCell := ls.Seat.ID != "" && ls.Seat.TierKey == s.Base.TierKey && ls.Seat.Direction == s.Base.Direction &&
			(!baseSeen || ls.UsageRank <= max(baseUsage, UsageRank(UsageNormal)))
		byID[ls.Seat.ID] = ls
		pool = append(pool, quotarelay.Seat{
			ID:        ls.Seat.ID,
			Name:      ls.Seat.Name,
			Tier:      ls.Seat.TierKey,
			Direction: ls.Seat.Direction,
			Eligible:  inCell && !Unselectable(ls.Availability),
			Demoted:   ls.Demoted,
			UsageRank: ls.UsageRank,
			Running:   ls.Running,
		})
	}
	if s.Base.ID == "" || s.Base.Upshifted {
		return out
	}
	best, ok := quotarelay.BestOnTier(pool, quotarelay.Seat{Direction: s.Base.Direction}, s.Base.TierKey)
	if !ok || best.ID == s.Base.ID || best.Running >= out.BaseRunning {
		return out
	}
	picked := byID[best.ID].Seat
	picked.TierLabel = s.Base.TierLabel
	out.Moved, out.Seat, out.Running = true, picked, best.Running
	return out
}

// LoadLine is the assignment comment's line about the rule, or "" when there
// is nothing to add: the rule agrees with the ladder, or the switch is on and
// the pick was written — the first line already says 负载 then. written is the
// seat the slot actually received.
func LoadLine(p LoadPick, enabled bool, written *Seat) string {
	if !p.Moved {
		return ""
	}
	if written != nil && written.ID == p.Seat.ID {
		return ""
	}
	prefix := "**负载**"
	if !enabled {
		prefix = "**负载（影子运行：开关没开，实际选择没改）**"
	}
	return prefix + "：按新规则会选 " + p.Seat.Name + "（负载：" + p.Detail() + "）。"
}
