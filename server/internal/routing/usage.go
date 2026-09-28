package routing

import (
	"sort"
	"strings"
)

// Usage is how much account headroom a person says a seat has (DENE-922):
// 紧张 / 常规 / 充足. It is a tag, like the tier: the server cannot see the
// account behind a seat, and how much of it may be spent is a decision.
//
// The judge never sees it. It picks a rung; usage only orders the seats on
// that rung, so the prompt stays about strength and nothing else.
const (
	UsageTight  = "tight"
	UsageNormal = "normal"
	UsageAmple  = "ample"
)

// DefaultUsage is what an untagged seat reads as, and the column default.
const DefaultUsage = UsageNormal

var usageLabels = map[string]string{
	UsageTight:  "紧张",
	UsageNormal: "常规",
	UsageAmple:  "充足",
}

// UsageKeys lists the accepted keys, tightest first.
func UsageKeys() []string { return []string{UsageTight, UsageNormal, UsageAmple} }

// NormalizeUsage resolves a key or its label to the stored key. Empty is not
// a value here: usage always has one, so empty is refused like any other
// unknown spelling.
func NormalizeUsage(value string) (string, bool) {
	v := strings.TrimSpace(value)
	for key, label := range usageLabels {
		if strings.EqualFold(v, key) || v == label {
			return key, true
		}
	}
	return "", false
}

// UsageLabel is the display label for a usage key.
func UsageLabel(key string) string {
	if label, ok := usageLabels[key]; ok {
		return label
	}
	return usageLabels[DefaultUsage]
}

// UsageRank orders seats on one rung: ample first, tight last. An unknown or
// empty value reads as the default.
func UsageRank(key string) int {
	switch key {
	case UsageAmple:
		return 0
	case UsageTight:
		return 2
	default:
		return 1
	}
}

// SeatOrder is the workspace's choice of how seats on one rung are ordered.
// The zero value is the default: usage counts, and a rung never borrows from
// the rung above.
type SeatOrder struct {
	// IgnoreUsage turns 「用量优先」 off: seats on a rung go by name alone.
	IgnoreUsage bool
	// AllowUpshift is 「允许上调一档」: when every seat on a rung is tight,
	// the rung is served by an ample seat from the rung above instead.
	AllowUpshift bool
}

// SeatOrder reads the two switches off the settings block.
func (s Settings) SeatOrder() SeatOrder {
	return SeatOrder{
		IgnoreUsage:  s.UsagePriority != nil && !*s.UsagePriority,
		AllowUpshift: s.AllowUpshift,
	}
}

// WithSeatOrder returns a copy of the ladder that orders seats the way this
// workspace asked. Every pick that goes through the ladder — executor,
// acceptance seat, substitute — reads the same order from here.
func (l Ladder) WithSeatOrder(order SeatOrder) Ladder {
	l.order = order
	return l
}

// usageRank is UsageRank under this ladder's order: flat when usage is
// switched off, so the name decides.
func (l Ladder) usageRank(a Agent) int {
	if l.order.IgnoreUsage {
		return 0
	}
	return UsageRank(a.Usage)
}

// byUsage sorts seats in place: usage first (when it counts), then name,
// then id so the order is stable across calls.
func (l Ladder) byUsage(seats []Agent) {
	sort.SliceStable(seats, func(i, j int) bool {
		ri, rj := l.usageRank(seats[i]), l.usageRank(seats[j])
		if ri != rj {
			return ri < rj
		}
		if seats[i].Name != seats[j].Name {
			return seats[i].Name < seats[j].Name
		}
		return seats[i].ID < seats[j].ID
	})
}

// allTight reports whether every seat given is tagged tight.
func allTight(seats []Agent) bool {
	if len(seats) == 0 {
		return false
	}
	for _, a := range seats {
		if a.Usage != UsageTight {
			return false
		}
	}
	return true
}

// ampleOnly keeps the seats tagged ample.
func ampleOnly(seats []Agent) []Agent {
	out := make([]Agent, 0, len(seats))
	for _, a := range seats {
		if a.Usage == UsageAmple {
			out = append(out, a)
		}
	}
	return out
}
