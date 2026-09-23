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
)

const (
	ScopeAgent = "agent"
	ScopeModel = "model"

	ConditionWeekly      = "next weekly reset"
	ConditionParsedReset = "provider reset hint"
	ConditionModelWindow = "specialized model quota window"
	modelQuotaWindow     = time.Hour

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

// ShouldInspect reports whether a failed task should enter the quota relay.
// Transient provider failures, including capacity and rate limits, do not.
func ShouldInspect(failureReason, errorText string) bool {
	_, ok := PlanFor(failureReason, errorText, Binding{}, time.Time{})
	return ok
}

// PlanFor classifies one failure. ok is false for anything that is not a
// quota exhaustion, including a retryable capacity or network error.
func PlanFor(failureReason, errorText string, binding Binding, now time.Time) (Plan, bool) {
	if !isQuotaFailure(failureReason, errorText) {
		return Plan{}, false
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
type Seat struct {
	ID        string
	Name      string
	Tier      string
	Direction string
	Eligible  bool
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
	seat, ok := bestOnTier(roster, failed, ladder[index+1])
	if !ok {
		return Choice{}, false
	}
	return Choice{Seat: seat, SteppedDown: true}, true
}

func bestOnTier(roster []Seat, failed Seat, tier string) (Seat, bool) {
	var sameDir, other []Seat
	for _, seat := range roster {
		if !seat.Eligible || seat.ID == failed.ID || seat.Tier != tier {
			continue
		}
		if failed.Direction != "" && seat.Direction == failed.Direction {
			sameDir = append(sameDir, seat)
			continue
		}
		other = append(other, seat)
	}
	pool := sameDir
	if len(pool) == 0 {
		pool = other
	}
	if len(pool) == 0 {
		return Seat{}, false
	}
	best := pool[0]
	for _, seat := range pool[1:] {
		if seat.Name < best.Name || (seat.Name == best.Name && seat.ID < best.ID) {
			best = seat
		}
	}
	return best, true
}
