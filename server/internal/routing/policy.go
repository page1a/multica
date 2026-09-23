package routing

import "strings"

// DefaultPolicyPrompt is the tier preference the judge follows when a
// workspace has not saved its own wording.
//
// The settings page shows the same bytes, from
// packages/core/workspace/routing-policy-prompt.ts. A test compares the two
// so a copy edit on one side cannot leave the model and the person reading
// different instructions. An empty saved prompt means this text. It does not
// mean "use whatever the model would have done anyway".
const DefaultPolicyPrompt = `Tier preference:
- Default: medium or strong. Ordinary work lands on one of these.
- Simple, explicit, low-risk work: weak.
- Complex, vague, cross-module, or high-cost work: strong or strongest.

Do not send a clearly simple ticket to the strongest tier. Do not send ordinary work to the weakest tier. Step up to strong or strongest only when the work is complex or high-risk.

You only choose a tier. You do not change status, assignee, reviewer, or any other ticket field, and you do not take an action.

Seat availability and provider quota in this state are observed facts. unknown means the fact is missing: do not treat it as available, unavailable, zero, or exhausted. A seat whose availability is not available or unknown is not a candidate. Provider quota never decides whether a seat is alive.`

// DefaultRoutingPolicy is the structured half of the same preference. The
// prose can be edited per workspace; these three keys stay so a reader of the
// request can see the rule without parsing the paragraph.
var DefaultRoutingPolicy = RoutingPolicy{
	DefaultTier: "medium_or_strong",
	SimpleWork:  "weak",
	ComplexWork: "strong_or_strongest",
}

// RoutingPolicy is the auditable preference attached to every judge request.
type RoutingPolicy struct {
	DefaultTier string `json:"default_tier"`
	SimpleWork  string `json:"simple_work"`
	ComplexWork string `json:"complex_work"`
}

// DefaultWatchedProviders is the quota subscription the routing context
// starts with. A workspace replaces the list in settings; the judge payload
// stays a list of provider summaries either way.
var DefaultWatchedProviders = []string{"claude", "codex", "grok"}

// EffectivePolicyPrompt returns the saved wording, or the default when the
// workspace has not saved one. Blank is not a third state.
func (s Settings) EffectivePolicyPrompt() string {
	if text := strings.TrimSpace(s.PolicyPrompt); text != "" {
		return text
	}
	return DefaultPolicyPrompt
}

// ProviderKeys returns the configured provider keys, or the default three
// when the setting is absent. Keys are lower-case and de-duplicated. An empty
// list is the same as absent: the subscription exists so the request can say
// unknown, not so a blank setting silently drops the quota context.
func (s Settings) ProviderKeys() []string {
	if len(s.WatchedProviders) == 0 {
		return append([]string(nil), DefaultWatchedProviders...)
	}
	out := make([]string, 0, len(s.WatchedProviders))
	seen := make(map[string]bool, len(s.WatchedProviders))
	for _, raw := range s.WatchedProviders {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	if len(out) == 0 {
		return append([]string(nil), DefaultWatchedProviders...)
	}
	return out
}
