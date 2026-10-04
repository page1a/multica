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
	// KindProviderCapacity is kept only to read breaker rows written before
	// DENE-1093. A full model or rate limit no longer opens a breaker: the
	// issue stays on its seat and the platform retries it in place with
	// backoff (service.providerCapacityRetrySchedule). PlanFor never returns
	// this kind.
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
	ConditionSessionWindow    = "provider session window"
	sessionQuotaWindow        = 5 * time.Hour
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

// ShouldInspect reports whether a failed task should enter the relay. Only
// quota exhaustion does: weekly windows, a specialisation's own model window,
// and an empty balance. A capacity or rate-limit failure never does
// (DENE-1093) — it is retried in place and leaves the seat open.
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
// quota exhaustion. Network errors stay out, and so does a capacity or
// rate-limit failure, even when its text also mentions quota: a full model
// clears in minutes, so the seat stays open and the issue is retried in
// place (DENE-1093).
func PlanFor(failureReason, errorText string, binding Binding, now time.Time) (Plan, bool) {
	if isCapacityFailure(failureReason, errorText) {
		return Plan{}, false
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
	if reset, ok := parseResetsAt(errorText, now); ok {
		plan.RecoverAt = reset
		plan.Condition = ConditionParsedReset
		return plan, true
	}
	if kind == KindWeeklyAgent && sessionWorded(strings.ToLower(errorText)) && !now.IsZero() {
		// A session window is hours, not a week. Waiting for Monday would
		// keep a seat shut for days after its account was usable again.
		plan.RecoverAt = now.Add(sessionQuotaWindow)
		plan.Condition = ConditionSessionWindow
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

// sessionWorded is Claude Code's short window: "You've hit your session
// limit · resets 11:10pm (Asia/Taipei)" (DENE-1159).
func sessionWorded(lower string) bool {
	return strings.Contains(lower, "session limit")
}

// resetsAtRe matches a wall-clock reset right after "resets", with the zone
// Claude Code prints in parentheses: "resets 11:10pm (Asia/Taipei)",
// "resets 3pm". A date in front ("resets Oct 6, 9am") does not match; those
// are weekly windows and keep the weekly default.
var resetsAtRe = regexp.MustCompile(`(?i)resets\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b(?:\s*\(([^)]+)\))?`)

// parseResetsAt turns a wall-clock reset into the next such instant after
// now. Without a zone, or with one Go cannot load, it gives up rather than
// guess: the session or weekly default then applies.
func parseResetsAt(text string, now time.Time) (time.Time, bool) {
	if now.IsZero() {
		return time.Time{}, false
	}
	m := resetsAtRe.FindStringSubmatch(text)
	if m == nil || strings.TrimSpace(m[4]) == "" {
		return time.Time{}, false
	}
	loc, err := time.LoadLocation(strings.TrimSpace(m[4]))
	if err != nil {
		return time.Time{}, false
	}
	hour, err := strconv.Atoi(m[1])
	if err != nil || hour < 1 || hour > 12 {
		return time.Time{}, false
	}
	minute := 0
	if m[2] != "" {
		if minute, err = strconv.Atoi(m[2]); err != nil || minute > 59 {
			return time.Time{}, false
		}
	}
	hour %= 12
	if strings.EqualFold(m[3], "pm") {
		hour += 12
	}
	local := now.In(loc)
	reset := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !reset.After(now) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset, true
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
	// Demoted marks a seat that keeps running out of quota; UsageRank is the
	// headroom a person tagged it with, 0 = ample, 2 = tight, all zero when
	// the workspace switched 「用量优先」 off. Both only order seats that
	// already qualify — the same order routing uses inside a rung (DENE-922).
	Demoted   bool
	UsageRank int
	// Running is how many unfinished runs the seat holds right now. Routing's
	// 负载 rule fills it (DENE-1203); the relay leaves it zero, which keeps
	// its order unchanged. Fewer goes first, after demotion.
	Running int
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

// BestOnTier is the same-tier choice Pick makes before it steps down, open
// to other callers so a rung is ordered one way everywhere. failed is the
// seat being replaced, or a seat with an empty ID when nobody is: its
// Direction, AvoidHouse and Exclude still shape the pool.
func BestOnTier(roster []Seat, failed Seat, tier string) (Seat, bool) {
	return bestOnTier(roster, failed, tier)
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
	if seat.Demoted != best.Demoted {
		return !seat.Demoted
	}
	if seat.Running != best.Running {
		return seat.Running < best.Running
	}
	if seat.UsageRank != best.UsageRank {
		return seat.UsageRank < best.UsageRank
	}
	return seat.Name < best.Name || (seat.Name == best.Name && seat.ID < best.ID)
}
