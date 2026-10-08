package routing

import "strings"

// Dispatch modes (DENE-1600, ADR-0008). routing_tier says how strong a seat
// is; dispatch_mode says whether automatic dispatch may pick it at all.
const (
	// DispatchAuto is the default: routing, quota relay and seat relay may
	// pick the seat.
	DispatchAuto = "auto"
	// DispatchMentionOnly takes work by @mention, assignment and delegation
	// and is never picked automatically. It is not work_enabled=false, which
	// refuses even a mention.
	DispatchMentionOnly = "mention_only"
)

// DispatchModeKeys is the accepted values, in the order a picker lists them.
func DispatchModeKeys() []string { return []string{DispatchAuto, DispatchMentionOnly} }

// NormalizeDispatchMode reads a stored or typed mode. Empty reads as auto.
func NormalizeDispatchMode(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", DispatchAuto:
		return DispatchAuto, true
	case DispatchMentionOnly:
		return DispatchMentionOnly, true
	default:
		return "", false
	}
}

// SeatState is what a seat's own record says about taking automatic work.
// Every field's zero value is the selectable one, so a caller that never read
// a fact does not exclude the seat by accident.
type SeatState struct {
	Archived bool
	// Disabled is work_enabled=false.
	Disabled bool
	// NoRuntime is a seat with no runtime bound: nothing can run it.
	NoRuntime bool
	// DispatchMode is the stored mode; empty reads as auto.
	DispatchMode string
}

// SelectContext is what the calling path knows on top of the record.
type SelectContext struct {
	// NeedTier makes an untagged seat unselectable. The relays rank by rung,
	// so a seat with no rung has no place in their order; initial dispatch
	// still places untagged seats by the name convention and leaves it off.
	NeedTier bool
	Tier     string
	// Availability is the seat's snapshot classification when the path read
	// one; empty when it did not.
	Availability string
	// BreakerOpen is an open quota breaker on the seat.
	BreakerOpen bool
}

// Reasons SeatSelectable gives for a seat it refuses.
const (
	ReasonArchived    = "archived"
	ReasonDisabled    = "disabled"
	ReasonMentionOnly = "mention_only"
	ReasonNoRuntime   = "no_runtime"
	ReasonUntiered    = "untiered"
	ReasonBreakerOpen = "breaker_open"
)

// SeatSelectable is the one answer to "may automatic dispatch pick this seat
// now". Initial dispatch, quota relay, seat relay and same-tier substitution
// all ask it, so a new condition lands on every path at once. The reason is
// empty when the seat is selectable.
//
// Point-to-point paths — @mention, assignment, delegation, waking the
// current holder — do not ask: mention_only exists for exactly those.
func SeatSelectable(s SeatState, c SelectContext) (bool, string) {
	switch {
	case s.Archived:
		return false, ReasonArchived
	case s.Disabled:
		return false, ReasonDisabled
	case isMentionOnly(s.DispatchMode):
		return false, ReasonMentionOnly
	case s.NoRuntime:
		return false, ReasonNoRuntime
	case c.NeedTier && strings.TrimSpace(c.Tier) == "":
		return false, ReasonUntiered
	case c.BreakerOpen:
		return false, ReasonBreakerOpen
	case Unselectable(c.Availability):
		return false, c.Availability
	}
	return true, ""
}

// isMentionOnly treats an unreadable mode as auto: the column is
// constrained, so anything else is a caller that never read it.
func isMentionOnly(mode string) bool {
	key, _ := NormalizeDispatchMode(mode)
	return key == DispatchMentionOnly
}

// autoPickable is SeatSelectable for a roster seat on the routing paths.
func (a Agent) autoPickable() bool {
	ok, _ := SeatSelectable(a.State, SelectContext{})
	return ok
}
