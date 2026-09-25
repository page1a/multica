// Package quotarelay decides what a provider-quota failure means for one
// seat, and which other seat may continue the same issue.
//
// Weekly account exhaustion and a specialisation's own model exhaustion are
// different scopes. A model the seat only inherits, and any other seat that
// happens to name the same model string, are outside the breaker.
package quotarelay

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// Kind is the quota failure this package is willing to break on.
type Kind string

const (
	KindNone             Kind = ""
	KindWeeklyAgent      Kind = "weekly_agent"
	KindSpecializedModel Kind = "specialized_model"
	// KindProviderCapacity is an in-place retry budget that ran out, not a
	// weekly quota. The seat cools down briefly so the same model is not
	// handed the issue again on the next tick.
	KindProviderCapacity Kind = "provider_capacity"
	// KindBalanceExhausted is a paid account with no money left (402,
	// insufficient balance, credits exhausted). Unlike a weekly window or a
	// capacity miss it never comes back by itself: someone has to top up or
	// switch the account, then turn the seat back on.
	KindBalanceExhausted Kind = "balance_exhausted"
)

const (
	ScopeAgent = "agent"
	ScopeModel = "model"

	ConditionWeekly           = "next weekly reset"
	ConditionParsedReset      = "provider reset hint"
	ConditionModelWindow      = "specialized model quota window"
	ConditionCapacityCooldown = "provider capacity cooldown"
	ConditionManual           = "top up or switch the account, then re-enable the seat"
	modelQuotaWindow          = time.Hour

	// manualRecoverAfter parks a balance breaker's recover_at far enough out
	// that the timed recovery sweep never reopens the seat. Only a person
	// turning the seat back on closes it.
	manualRecoverAfter = 100 * 365 * 24 * time.Hour

	// OpenAIHouse and AnthropicHouse are the provider values ladder.json
	// already stores. A capacity handoff uses them to leave a GPT seat and
	// to prefer a Claude seat; it does not invent a second family list.
	OpenAIHouse    = "openai"
	AnthropicHouse = "anthropic"

	WaitNoSeat = "no same-tier or one-tier-down seat"
	WaitNoTier = "failed seat has no routing tier"
)

// Binding is the seat's model as dispatch will actually run it.
// OwnModel is true only when this seat is a specialisation that does not
// inherit its base role's runtime and carries its own model string.
type Binding struct {
	Model    string
	OwnModel bool
}

// Plan is the breaker to open for one failure. RecoverAt is when the seat
// may take work again; Condition is the human-readable recovery rule.
type Plan struct {
	Kind      Kind
	ModelKey  string
	RecoverAt time.Time
	Condition string
}

func (p Plan) Scope() string {
	if p.Kind == KindSpecializedModel {
		return ScopeModel
	}
	return ScopeAgent
}

// ShouldInspect reports whether a failed task should enter the relay.
// Quota exhaustion always does. A capacity or rate-limit failure does too,
// but the caller must still refuse it while in-place retries remain — this
// function does not see the attempt counter.
func ShouldInspect(failureReason, errorText string) bool {
	_, ok := PlanFor(failureReason, errorText, Binding{}, time.Time{})
	return ok
}

// IsCapacityFailure reports a provider capacity or rate-limit failure.
// An explicit capacity reason wins even when the text also mentions quota.
func IsCapacityFailure(reason, text string) bool {
	return isCapacityFailure(reason, text)
}

// PlanFor classifies one failure. ok is false for anything that is not a
// quota exhaustion or a capacity / rate-limit failure. Network errors stay
// out. A capacity plan is not a weekly breaker: the seat comes back after
// the same hour a specialized-model window uses, because the miss is
// transient and the in-place retries have already waited.
func PlanFor(failureReason, errorText string, binding Binding, now time.Time) (Plan, bool) {
	if isCapacityFailure(failureReason, errorText) {
		plan := Plan{Kind: KindProviderCapacity, Condition: ConditionCapacityCooldown}
		if !now.IsZero() {
			plan.RecoverAt = now.Add(modelQuotaWindow)
		}
		return plan, true
	}
	if !isQuotaFailure(failureReason, errorText) {
		return Plan{}, false
	}
	if balanceWorded(errorText) {
		plan := Plan{Kind: KindBalanceExhausted, Condition: ConditionManual}
		if !now.IsZero() {
			plan.RecoverAt = now.Add(manualRecoverAfter)
		}
		return plan, true
	}
	kind := kindOf(errorText, binding)
	plan := Plan{Kind: kind, Condition: ConditionWeekly}
	if kind == KindSpecializedModel {
		plan.ModelKey = strings.TrimSpace(binding.Model)
		plan.Condition = ConditionModelWindow
	}
	if reset, ok := parseResetsIn(errorText, now); ok {
		plan.RecoverAt = reset
		plan.Condition = ConditionParsedReset
		return plan, true
	}
	if kind == KindSpecializedModel {
		plan.RecoverAt = now.Add(modelQuotaWindow)
		return plan, true
	}
	plan.RecoverAt = nextWeeklyReset(now)
	return plan, true
}

func isQuotaFailure(reason, text string) bool {
	if reason == string(taskfailure.ReasonAgentProviderQuotaLimit) {
		return true
	}
	switch reason {
	case "", "agent_error", string(taskfailure.ReasonAgentUnknown):
		return taskfailure.Classify(text) == taskfailure.ReasonAgentProviderQuotaLimit
	default:
		return false
	}
}

func isCapacityFailure(reason, text string) bool {
	if reason == string(taskfailure.ReasonAgentProviderCapacityOrRateLimit) {
		return true
	}
	switch reason {
	case "", "agent_error", string(taskfailure.ReasonAgentUnknown):
		return taskfailure.Classify(text) == taskfailure.ReasonAgentProviderCapacityOrRateLimit
	default:
		return false
	}
}

// IsManualRecovery reports a breaker kind that the timed sweep must not
// close. Only an explicit re-enable ends it.
func IsManualRecovery(kind Kind) bool {
	return kind == KindBalanceExhausted
}

var paymentCodeRe = regexp.MustCompile(`(^|[^0-9])402([^0-9]|$)`)

// balanceWorded separates "the account is out of money" from a usage window.
// Both classify as provider_quota_limit; only the first needs a person.
// Window wording ("weekly", "resets in") wins so a Claude weekly limit that
// happens to mention credits still recovers on its own.
func balanceWorded(text string) bool {
	lower := strings.ToLower(text)
	if weeklyWorded(lower) || resetsInRe.MatchString(text) {
		return false
	}
	if paymentCodeRe.MatchString(lower) {
		return true
	}
	for _, phrase := range []string{
		"payment required",
		"insufficient_balance",
		"insufficient balance",
		"balance exhausted",
		"balance is too low",
		"usage balance",
		"credit balance",
		"credits exhausted",
		"out of credits",
		"billing",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func kindOf(errorText string, binding Binding) Kind {
	lower := strings.ToLower(errorText)
	if weeklyWorded(lower) {
		return KindWeeklyAgent
	}
	model := strings.ToLower(strings.TrimSpace(binding.Model))
	if binding.OwnModel && model != "" && modelWorded(lower, model) {
		return KindSpecializedModel
	}
	return KindWeeklyAgent
}

func weeklyWorded(lower string) bool {
	return strings.Contains(lower, "weekly") ||
		strings.Contains(lower, "this week") ||
		strings.Contains(lower, "per week")
}

func modelWorded(lower, model string) bool {
	if strings.Contains(lower, "model quota") ||
		strings.Contains(lower, "model usage") ||
		strings.Contains(lower, "model limit") {
		return true
	}
	return strings.Contains(lower, model)
}

var (
	resetsInRe = regexp.MustCompile(`(?i)resets\s+in\s+((?:\d+d)?(?:\d+h)?(?:\d+m)?(?:\d+s)?)`)
	durationRe = regexp.MustCompile(`(\d+)([dhms])`)
)

func parseResetsIn(text string, now time.Time) (time.Time, bool) {
	if now.IsZero() {
		return time.Time{}, false
	}
	match := resetsInRe.FindStringSubmatch(text)
	if len(match) < 2 || match[1] == "" {
		return time.Time{}, false
	}
	var total time.Duration
	for _, part := range durationRe.FindAllStringSubmatch(match[1], -1) {
		n, err := strconv.Atoi(part[1])
		if err != nil || n < 0 {
			continue
		}
		switch part[2] {
		case "d", "D":
			total += time.Duration(n) * 24 * time.Hour
		case "h", "H":
			total += time.Duration(n) * time.Hour
		case "m", "M":
			total += time.Duration(n) * time.Minute
		case "s", "S":
			total += time.Duration(n) * time.Second
		}
	}
	if total <= 0 {
		return time.Time{}, false
	}
	return now.Add(total), true
}

// nextWeeklyReset is the following Monday 00:00 UTC. A hit on Monday still
// waits until the next Monday: the quota that just failed is this week's.
func nextWeeklyReset(now time.Time) time.Time {
	utc := now.UTC()
	days := (8 - int(utc.Weekday())) % 7
	if days == 0 {
		days = 7
	}
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	return midnight.AddDate(0, 0, days)
}

// Seat is one routable agent the relay may hand work to.
// Provider is the ladder family (openai, anthropic, …). Empty means the
// name is not on the ladder, so a cross-house pick cannot treat it as a
// different house.
//
// AvoidHouse is set only on the failed seat. When it is non-empty, the
// same tier may only offer a different provider. One tier down ignores it:
// dropping a rung is how a tier that has only the same house still gets
// the work done.
type Seat struct {
	ID         string
	Name       string
	Tier       string
	Direction  string
	Provider   string
	Eligible   bool
	AvoidHouse string
	// StrictHouse keeps AvoidHouse on the one-tier-down pick too. A seat
	// whose account ran out of money shares that account with its house;
	// dropping a rung inside the same house lands on the same empty wallet.
	StrictHouse bool
	// Exclude lists seat ids that must not be picked for this handoff, such
	// as the issue's own acceptance seat.
	Exclude []string
}

// Choice is the replacement seat. SteppedDown is true when the same tier
// had nobody eligible and the seat is exactly one rung weaker.
type Choice struct {
	Seat        Seat
	SteppedDown bool
}

// Pick chooses an eligible seat on the failed seat's tier, or exactly one
// tier down the ladder. It does not walk further. Ladder keys are strongest
// first. An untagged failed seat cannot be replaced.
//
// When the failed seat sets AvoidHouse, the same tier skips that provider
// and prefers an Anthropic seat over any other different house. The one
// tier down uses the ordinary pick, including the avoided house: the point
// of stepping down is to keep the issue moving.
func Pick(failed Seat, roster []Seat, ladder []string) (Choice, bool) {
	if failed.Tier == "" {
		return Choice{}, false
	}
	index := -1
	for i, key := range ladder {
		if key == failed.Tier {
			index = i
			break
		}
	}
	if index < 0 {
		return Choice{}, false
	}
	if seat, ok := bestOnTier(roster, failed, failed.Tier); ok {
		return Choice{Seat: seat}, true
	}
	if index+1 >= len(ladder) {
		return Choice{}, false
	}
	down := failed
	if !failed.StrictHouse {
		down.AvoidHouse = ""
	}
	seat, ok := bestOnTier(roster, down, ladder[index+1])
	if !ok {
		return Choice{}, false
	}
	return Choice{Seat: seat, SteppedDown: true}, true
}

func bestOnTier(roster []Seat, failed Seat, tier string) (Seat, bool) {
	var sameDir, other []Seat
	for _, seat := range roster {
		if !seat.Eligible || seat.ID == failed.ID || seat.Tier != tier || excluded(failed.Exclude, seat.ID) {
			continue
		}
		if !admitsHouse(seat, failed.AvoidHouse) {
			continue
		}
		if failed.Direction != "" && seat.Direction == failed.Direction {
			sameDir = append(sameDir, seat)
			continue
		}
		other = append(other, seat)
	}
	var pool []Seat
	switch {
	case failed.AvoidHouse != "":
		// Cross-house outranks direction: a Claude seat on another
		// direction is the handoff this failure asked for.
		pool = append(sameDir, other...)
	case len(sameDir) > 0:
		pool = sameDir
	default:
		pool = other
	}
	if len(pool) == 0 {
		return Seat{}, false
	}
	best := pool[0]
	for _, seat := range pool[1:] {
		if betterSeat(seat, best, failed) {
			best = seat
		}
	}
	return best, true
}

func excluded(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func admitsHouse(seat Seat, avoid string) bool {
	if avoid == "" {
		return true
	}
	return seat.Provider != "" && seat.Provider != avoid
}

func betterSeat(seat, best, failed Seat) bool {
	if failed.AvoidHouse != "" {
		seatClaude := seat.Provider == AnthropicHouse
		bestClaude := best.Provider == AnthropicHouse
		if seatClaude != bestClaude {
			return seatClaude
		}
	}
	if failed.AvoidHouse != "" && failed.Direction != "" {
		seatDir := seat.Direction == failed.Direction
		bestDir := best.Direction == failed.Direction
		if seatDir != bestDir {
			return seatDir
		}
	}
	return seat.Name < best.Name || (seat.Name == best.Name && seat.ID < best.ID)
}
