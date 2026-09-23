package routing

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Availability is why a seat can or cannot take a newly routed ticket.
//
// unknown is a missing observation. It is not available and it is not
// unavailable: the filter keeps the seat, and the prompt tells the model not
// to invent a number for it.
const (
	AvailabilityAvailable      = "available"
	AvailabilityCancelled      = "cancelled"
	AvailabilityDead           = "dead"
	AvailabilityDisabled       = "disabled"
	AvailabilityArchived       = "archived"
	AvailabilityUnreachable    = "unreachable"
	AvailabilityQuotaExhausted = "quota_exhausted"
	AvailabilityUnknown        = "unknown"
)

// QuotaFreshness is how old a provider observation may be before routing
// treats it as missing. A stale exhausted flag must not keep excluding a seat
// after the window has rolled, and a stale percentage must not be shown as
// current.
const QuotaFreshness = 24 * time.Hour

// SeatSnapshot is the summary a judge request carries for one candidate.
// Numbers the deployment did not observe are null, never zero.
type SeatSnapshot struct {
	AgentID      string        `json:"agent_id"`
	Tier         string        `json:"tier"`
	Model        string        `json:"model"`
	Availability string        `json:"availability"`
	Latency      *LatencyMS    `json:"latency_ms"`
	Quota        QuotaSnapshot `json:"quota"`
	ObservedAt   *string       `json:"observed_at"`
}

// LatencyMS is a percentile pair. A nil Latency on the snapshot means no
// samples, which is different from a seat that really finished in 0ms.
type LatencyMS struct {
	P50 *int64 `json:"p50"`
	P95 *int64 `json:"p95"`
}

// QuotaSnapshot is the seat's own provider window, not the workspace rollup.
type QuotaSnapshot struct {
	Remaining *float64 `json:"remaining"`
	ResetAt   *string  `json:"reset_at"`
}

// ProviderQuota is one subscribed provider's freshest observation.
type ProviderQuota struct {
	Provider    string   `json:"provider"`
	Status      string   `json:"status"`
	UsedPercent *float64 `json:"used_percent"`
	Remaining   *float64 `json:"remaining"`
	ResetAt     *string  `json:"reset_at"`
	ObservedAt  *string  `json:"observed_at"`
}

// RoutingFacts is what the store returns for one routing pass: per-seat
// summaries for the candidates it was asked about, and one row per subscribed
// provider. A provider with no fresh observation is still present, as unknown.
type RoutingFacts struct {
	Seats     map[string]SeatSnapshot
	Providers []ProviderQuota
}

// SeatFacts is the raw observation the store gathered. The routing package
// turns it into a snapshot so the classification lives in one tested place.
type SeatFacts struct {
	AgentID   string
	Model     string
	Tier      string
	Archived  bool
	Cancelled bool
	// WorkKnown distinguishes "this seat is switched off" from "we did not
	// read the flag". The zero bool would otherwise look disabled.
	WorkKnown   bool
	WorkEnabled bool
	// Runtime is "", "online", "unreachable", or "dead". Empty means the
	// read did not produce a classification.
	Runtime string
	Quota   QuotaObservation
	// Latencies are finished-task durations in milliseconds, newest last or
	// not — order does not matter. Empty means unknown, not zero.
	Latencies  []int64
	ObservedAt time.Time
}

// QuotaObservation is one provider window after the store has decoded it.
// A zero ObservedAt means there is no observation.
type QuotaObservation struct {
	Status      string
	ObservedAt  time.Time
	UsedPercent *float64
	Remaining   *float64
	ResetAt     *time.Time
}

// PlanWindow is the slice of a provider limit window routing understands.
// Anything else in the provider payload — account ids, raw errors — stays
// on the runtime row.
type PlanWindow struct {
	UsedPercent *float64
	Remaining   *float64
	ResetsAt    *time.Time
}

// Unselectable reports a definite reason to drop a seat before the judge is
// asked and again before a slot is written. unknown is not one of them.
func Unselectable(availability string) bool {
	switch availability {
	case AvailabilityCancelled, AvailabilityDead, AvailabilityDisabled,
		AvailabilityArchived, AvailabilityUnreachable, AvailabilityQuotaExhausted:
		return true
	default:
		return false
	}
}

// EligibleSeats drops candidates whose snapshot gives a definite reason.
// A candidate with no snapshot is kept: missing data is unknown, and unknown
// is not a reason to exclude.
func EligibleSeats(candidates []Seat, snaps map[string]SeatSnapshot) []Seat {
	out := make([]Seat, 0, len(candidates))
	for _, candidate := range candidates {
		snap, ok := snaps[candidate.ID]
		if ok && Unselectable(snap.Availability) {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// ObserveQuota collapses provider windows into one observation. A stale or
// empty reading comes back zero, which the snapshot renders as unknown
// rather than as a fabricated remainder.
func ObserveQuota(status string, observedAt time.Time, windows []PlanWindow, now time.Time) QuotaObservation {
	if !quotaFresh(observedAt, now) {
		return QuotaObservation{}
	}
	obs := QuotaObservation{Status: strings.ToLower(strings.TrimSpace(status)), ObservedAt: observedAt}
	var used *float64
	var remaining *float64
	var reset *time.Time
	for _, window := range windows {
		if window.UsedPercent != nil && (used == nil || *window.UsedPercent > *used) {
			value := *window.UsedPercent
			used = &value
		}
		if window.Remaining != nil && (remaining == nil || *window.Remaining < *remaining) {
			value := *window.Remaining
			remaining = &value
		}
		if window.ResetsAt != nil && (reset == nil || window.ResetsAt.Before(*reset)) {
			value := *window.ResetsAt
			reset = &value
		}
	}
	obs.UsedPercent = used
	obs.Remaining = remaining
	obs.ResetAt = reset
	return obs
}

func quotaFresh(observed, now time.Time) bool {
	if observed.IsZero() || now.IsZero() {
		return false
	}
	age := now.Sub(observed)
	return age >= 0 && age <= QuotaFreshness
}

func quotaExhausted(obs QuotaObservation, now time.Time) bool {
	if !quotaFresh(obs.ObservedAt, now) {
		return false
	}
	// A reset that has already passed, with no usage number still saying
	// "full", is an expired flag. Treating it as exhausted would keep a seat
	// out after the window rolled.
	if obs.ResetAt != nil && !obs.ResetAt.After(now) && obs.UsedPercent == nil && obs.Remaining == nil {
		return false
	}
	if obs.Status == "exhausted" {
		return true
	}
	if obs.UsedPercent != nil && *obs.UsedPercent >= 100 {
		return true
	}
	if obs.Remaining != nil && *obs.Remaining <= 0 {
		return true
	}
	return false
}

// InterpretSeat classifies one seat. The order is the order of the reasons
// a person would give: retired, switched off, cancelled, runtime dead,
// quota, runtime unreachable, then available. Anything not observed stays
// unknown.
func InterpretSeat(facts SeatFacts, now time.Time) SeatSnapshot {
	availability := AvailabilityUnknown
	switch {
	case facts.Archived:
		availability = AvailabilityArchived
	case facts.WorkKnown && !facts.WorkEnabled:
		availability = AvailabilityDisabled
	case facts.Cancelled:
		availability = AvailabilityCancelled
	case facts.Runtime == "dead":
		availability = AvailabilityDead
	case quotaExhausted(facts.Quota, now):
		availability = AvailabilityQuotaExhausted
	case facts.Runtime == "unreachable":
		availability = AvailabilityUnreachable
	case facts.Runtime == "online":
		availability = AvailabilityAvailable
	}
	snap := SeatSnapshot{
		AgentID:      facts.AgentID,
		Tier:         orUnknown(facts.Tier),
		Model:        orUnknown(facts.Model),
		Availability: availability,
		Quota:        quotaJSON(facts.Quota, now),
		ObservedAt:   timeJSON(facts.ObservedAt),
	}
	if p50, p95 := percentiles(facts.Latencies); p50 != nil {
		snap.Latency = &LatencyMS{P50: p50, P95: p95}
	}
	return snap
}

// InterpretProvider renders one subscribed provider. Missing and stale
// observations are unknown, with null numbers.
func InterpretProvider(provider string, obs QuotaObservation, now time.Time) ProviderQuota {
	quota := ProviderQuota{Provider: strings.ToLower(strings.TrimSpace(provider)), Status: AvailabilityUnknown}
	if !quotaFresh(obs.ObservedAt, now) {
		return quota
	}
	quota.ObservedAt = timeJSON(obs.ObservedAt)
	quota.UsedPercent = obs.UsedPercent
	quota.Remaining = obs.Remaining
	quota.ResetAt = timeJSONPtr(obs.ResetAt)
	if quotaExhausted(obs, now) {
		quota.Status = AvailabilityQuotaExhausted
		return quota
	}
	if obs.Status == "available" || obs.Status == "" {
		quota.Status = AvailabilityAvailable
		return quota
	}
	quota.Status = AvailabilityUnknown
	return quota
}

func quotaJSON(obs QuotaObservation, now time.Time) QuotaSnapshot {
	if !quotaFresh(obs.ObservedAt, now) {
		return QuotaSnapshot{}
	}
	return QuotaSnapshot{Remaining: obs.Remaining, ResetAt: timeJSONPtr(obs.ResetAt)}
}

func orUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return AvailabilityUnknown
	}
	return value
}

func timeJSON(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format(time.RFC3339)
	return &value
}

func timeJSONPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return timeJSON(*t)
}

func percentiles(samples []int64) (p50, p95 *int64) {
	if len(samples) == 0 {
		return nil, nil
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 = int64Ptr(nearestRank(sorted, 0.50))
	p95 = int64Ptr(nearestRank(sorted, 0.95))
	return p50, p95
}

func nearestRank(sorted []int64, p float64) int64 {
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func int64Ptr(v int64) *int64 { return &v }

// UnknownSeat is the summary for a candidate nobody subscribed a reading
// for. It keeps the seat choosable and does not invent latency or quota.
func UnknownSeat(seat Seat) SeatSnapshot {
	return SeatSnapshot{
		AgentID:      seat.ID,
		Tier:         orUnknown(seat.TierKey),
		Model:        AvailabilityUnknown,
		Availability: AvailabilityUnknown,
		Quota:        QuotaSnapshot{},
	}
}

// OrderSeatViews lists every candidate, ineligible ones last and faster
// observed seats earlier. The ladder order of candidate_tiers is untouched:
// latency is a hint in this list, not a new rung.
func OrderSeatViews(candidates []Seat, snaps map[string]SeatSnapshot) []SeatSnapshot {
	type row struct {
		snap  SeatSnapshot
		index int
	}
	rows := make([]row, 0, len(candidates))
	for i, candidate := range candidates {
		snap, ok := snaps[candidate.ID]
		if !ok {
			snap = UnknownSeat(candidate)
		} else {
			if snap.AgentID == "" {
				snap.AgentID = candidate.ID
			}
			if snap.Tier == "" || snap.Tier == AvailabilityUnknown {
				snap.Tier = orUnknown(candidate.TierKey)
			}
			if snap.Model == "" {
				snap.Model = AvailabilityUnknown
			}
			if snap.Availability == "" {
				snap.Availability = AvailabilityUnknown
			}
		}
		rows = append(rows, row{snap: snap, index: i})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if Unselectable(left.snap.Availability) != Unselectable(right.snap.Availability) {
			return !Unselectable(left.snap.Availability)
		}
		leftP, leftOK := latencyP50(left.snap)
		rightP, rightOK := latencyP50(right.snap)
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && leftP != rightP {
			return leftP < rightP
		}
		return left.index < right.index
	})
	out := make([]SeatSnapshot, len(rows))
	for i, row := range rows {
		out[i] = row.snap
	}
	return out
}

func latencyP50(snap SeatSnapshot) (int64, bool) {
	if snap.Latency == nil || snap.Latency.P50 == nil {
		return 0, false
	}
	return *snap.Latency.P50, true
}

// EnsureProviderQuotas returns one row per subscribed provider. A provider
// the store did not observe is unknown, with null numbers.
func EnsureProviderQuotas(got []ProviderQuota, keys []string) []ProviderQuota {
	by := make(map[string]ProviderQuota, len(got))
	for _, quota := range got {
		by[strings.ToLower(quota.Provider)] = quota
	}
	out := make([]ProviderQuota, 0, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		quota, ok := by[key]
		if !ok {
			out = append(out, ProviderQuota{Provider: key, Status: AvailabilityUnknown})
			continue
		}
		quota.Provider = key
		if quota.Status == "" {
			quota.Status = AvailabilityUnknown
		}
		out = append(out, quota)
	}
	return out
}
