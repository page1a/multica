// Package routing decides who should be holding an issue right now.
//
// It is an if function. Every time an issue is created or changes status, the
// server calls Route once, and Route answers one question: given this status,
// who should hold this ticket. That framing fixes three properties the rest of
// this package depends on:
//
//   - An if function has no side effects of its own. The model is asked for a
//     branch and a confidence, never for prose, an action, or control flow.
//     Every write is done by deterministic code outside the model call.
//   - An if function can always fall through to else. Disabled, unconfigured,
//     model unreachable, breaker open, confidence below the threshold — all of
//     them take the same else branch, which is exactly the behaviour this
//     deployment had before the package existed.
//   - An if function is more reliable with fewer branches. Anything already
//     known is resolved deterministically before the model is asked, so the
//     model only ever answers the part nobody knows.
//
// Route never advances status. Status is a fact about the work, and only the
// seat doing the work knows it; a model that could write status would move
// tickets to "in review" with the work undone, and that mistake is invisible
// on the board.
package routing

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// SettingsKey is the key under which routing configuration lives inside the
// workspace `settings` JSONB column. The column is shared with other product
// settings, so everything this package owns is nested under one key.
const SettingsKey = "routing"

// DefaultConfidenceThreshold is the threshold applied when settings carry no
// explicit one. It no longer decides whether a slot gets filled — routing
// always dispatches — only whether the judge's own pick is used or the
// ladder's fallback rung is. Measured verdicts on real tickets cluster in the
// 0.5-0.7 band, so a floor above that band sent every ticket to the fallback
// and threw the judge's answer away.
const DefaultConfidenceThreshold = 0.60

// DefaultStaleReviewHours is how long a ticket may sit awaiting acceptance
// before the stale sweep looks at it, when settings carry no explicit value.
//
// A day rather than an hour on purpose. The sweep exists for tickets nobody
// will move again, and a reviewer seat that is simply queued behind other work
// is not one of them; a short floor would wake seats that were about to run
// anyway and turn a safety net into a second dispatcher.
const DefaultStaleReviewHours = 24

// Settings is the whole routing configuration: an on/off switch, the model the
// judge runs on, the confidence threshold, and — optionally — the endpoint and
// key that model lives behind.
//
// The gateway pair started out absent on purpose: the judge borrowed the
// deployment's MULTICA_LLM_* configuration, so a workspace had nothing to
// configure. That holds for a deployment with one workspace and one operator.
// It stops holding the moment routing is handed to a team on a shared
// self-hosted instance: "ask whoever runs the server to edit an env var and
// restart" is not a setting, and it is the same answer for every workspace on
// the box. Both fields stay OPTIONAL — empty means the deployment gateway, so
// every workspace configured before they existed keeps working unchanged.
//
// The JSON field names are the cross-surface contract. The desktop settings
// section writes exactly these names, and changing one is a breaking change
// for any workspace already configured.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Model is a model identifier passed through to the server-internal LLM
	// layer. Empty while the switch is on is the "incomplete" state: somebody
	// flipped the toggle and stopped, and the product must say so rather than
	// look enabled.
	Model string `json:"model"`
	// ConfidenceThreshold is the floor a verdict must clear before its answer
	// is written to a slot. Zero or out of range means "unset" and yields
	// DefaultConfidenceThreshold; it is never treated as "accept everything".
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	// StaleReviewHours is the stall threshold for the in-review sweep, in
	// hours. Zero or out of range means "unset" and yields
	// DefaultStaleReviewHours; it is deliberately not a second on/off switch —
	// the sweep runs only while Enabled is true, exactly like every other row.
	StaleReviewHours float64 `json:"stale_review_hours"`
	// BaseURL is this workspace's own OpenAI-compatible endpoint for the
	// judge. Empty means "use the deployment's MULTICA_LLM_BASE_URL", which is
	// the only thing that existed before a workspace could bring its own.
	//
	// It is NOT a secret and is deliberately stored in the clear: a reader who
	// cannot see which endpoint their tickets are described to cannot consent
	// to it. The key that goes with it is a different matter — see below.
	BaseURL string `json:"base_url"`
	// APIKeyEnc is the workspace key, sealed with the deployment's routing
	// secretbox and base64-encoded. It is the ONLY field here that a client
	// never receives: the workspace read path strips it, and the settings
	// write path carries the stored value forward rather than accepting one.
	//
	// Ciphertext rather than plaintext because `workspace.settings` is a
	// shared JSONB column that lands in every database dump; sealed, a dump
	// without the deployment secret carries nothing usable.
	APIKeyEnc string `json:"api_key_enc,omitempty"`
	// APIKey is the opened form of APIKeyEnc. It is populated by the store
	// after decryption and is never serialised — `json:"-"` is load-bearing,
	// because this struct is marshalled back into the settings column.
	APIKey string `json:"-"`
	// Projects is this workspace's project -> direction table: project name
	// (exact, or `prefix*`) to one of the ladder's directions, or "通用" for a
	// project that is deliberately general-purpose. Rows here are laid over
	// the shipped defaults in ladder.json and win, so classifying a project is
	// a settings write and never a release.
	Projects map[string]string `json:"projects,omitempty"`
	// Domains is the workspace's domain list (DENE-1451), filled by the store
	// from its own table and never serialised: it is the ladder's direction
	// list, maintained in one place.
	Domains []string `json:"-"`
	// PolicyPrompt is the workspace's own tier-preference wording. Empty
	// means DefaultPolicyPrompt. It constrains which tier the judge picks;
	// it is not allowed to change status or take an action, and the judge
	// prompts say so alongside it.
	PolicyPrompt string `json:"policy_prompt,omitempty"`
	// WatchedProviders names whose quota summaries are attached to a judge
	// request. Empty means DefaultWatchedProviders (claude, codex, grok).
	// Replacing one key does not change the request shape.
	WatchedProviders []string `json:"watched_providers,omitempty"`
	// UsagePriority is 「用量优先」: inside a rung, a seat tagged ample goes
	// before a tight one. A pointer because the default is ON and a workspace
	// saved before the field existed must read as on (DENE-922).
	UsagePriority *bool `json:"usage_priority,omitempty"`
	// AllowUpshift is 「允许上调一档」: when every seat on the judged rung is
	// tight, the rung above's ample seat takes the work. Off by default.
	AllowUpshift bool `json:"allow_upshift,omitempty"`
	// PreferContinuation is 「接着做」 (DENE-1202): a ticket continuing earlier
	// work goes back to that work's executor when it can take it. Off by
	// default, which is shadow mode — the ladder's pick is written and the
	// assignment comment says who the rule would have picked.
	PreferContinuation bool `json:"prefer_continuation,omitempty"`
	// PreferIdle is 「负载分流」 (DENE-1203): inside the ladder's rung and
	// direction, a seat with fewer unfinished runs goes before a busy one.
	// Off by default, which is shadow mode, like PreferContinuation. With
	// both on, 接着做 wins.
	PreferIdle bool `json:"prefer_idle,omitempty"`
	// LearnFromOutcomes is 「从结果里学」 (DENE-1722): a class of tickets
	// whose recent members were judged too low gets its next ticket one rung
	// higher. Off by default, which is shadow mode, like PreferContinuation.
	// LearnLowRate and LearnWindowDays tune it; zero or out of range means
	// the defaults in learn.go.
	LearnFromOutcomes bool    `json:"learn_from_outcomes,omitempty"`
	LearnLowRate      float64 `json:"learn_low_rate,omitempty"`
	LearnWindowDays   float64 `json:"learn_window_days,omitempty"`
	// JudgedReview is 「按判断配验收」 (DENE-1252): the reviewer slot gets a
	// seat only when the judge asks for a check with confidence (or asks for
	// a person). An unsure or "none" answer writes 不需要验收 instead of the
	// fallback seat, and the executor merges and closes. Off by default,
	// which keeps the fallback seat.
	JudgedReview bool `json:"judged_review,omitempty"`

	// JudgeEnabled switches the judge role — Model, BaseURL and the key above
	// are that role's fields, because the judge is the only model routing had
	// before the roles were split (DENE-923). Nil is a block written before
	// the split: see Mode for how it reads.
	JudgeEnabled *bool `json:"judge_enabled,omitempty"`
	// Analysis is the second role: a general model that reads the whole
	// ticket and reduces it to a handful of facts. Nil and a switched-off
	// block mean the same thing.
	Analysis *AnalysisSettings `json:"analysis,omitempty"`
}

// AnalysisSettings is the analysis role's own endpoint and model. It has the
// same shape as the judge's fields for the same reasons: an empty endpoint
// means the deployment gateway, and the key is sealed at rest and never sent
// back to a client.
type AnalysisSettings struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model"`
	BaseURL string `json:"base_url"`
	// Source selects the transport used by the analysis role. api_gateway is
	// the historical OpenAI-compatible path; runtime_subscription asks the
	// selected local runtime to execute the read-only prompt.
	Source        string `json:"source,omitempty"`
	RuntimeID     string `json:"runtime_id,omitempty"`
	ThinkingLevel string `json:"thinking_level,omitempty"`
	APIKeyEnc     string `json:"api_key_enc,omitempty"`
	APIKey        string `json:"-"`
}

const (
	AnalysisSourceAPIGateway          = "api_gateway"
	AnalysisSourceRuntimeSubscription = "runtime_subscription"
)

// Mode is which of the two roles are switched on. Since DENE-1677 no mode
// lets a model pick the tier: the rule table (rules.json) does.
//
//   - ModeNone      — no model is called; every slot takes the ladder's
//     fallback rung.
//   - ModeAnalysis  — the analysis model answers the table's numbered
//     questions and the table picks the tier.
//   - ModeBoth      — as ModeAnalysis, and the judge, reading only the
//     answers, may raise the tier one rung with a reason.
//   - ModeJudge     — nobody answers the questions, so the table's unknown
//     row sets the tier, and the judge may raise it one rung.
type Mode string

const (
	ModeNone     Mode = "none"
	ModeAnalysis Mode = "analysis"
	ModeBoth     Mode = "analysis_judge"
	ModeJudge    Mode = "judge"
)

// AnalysisOn reports whether the analysis role is switched on.
func (s Settings) AnalysisOn() bool { return s.Analysis != nil && s.Analysis.Enabled }

// JudgeOn reports whether the judge role is switched on.
//
// An unset switch is a block written before the roles existed. Those blocks
// had only the judge, so they keep it — unless the same block already turns
// the analysis role on, which only a writer that knows about roles can do,
// and such a writer states the judge switch explicitly anyway.
func (s Settings) JudgeOn() bool {
	if s.JudgeEnabled != nil {
		return *s.JudgeEnabled
	}
	return !s.AnalysisOn()
}

// Mode derives the combination from the two switches.
func (s Settings) Mode() Mode {
	switch a, j := s.AnalysisOn(), s.JudgeOn(); {
	case a && j:
		return ModeBoth
	case a:
		return ModeAnalysis
	case j:
		return ModeJudge
	}
	return ModeNone
}

// AnalysisTarget resolves where this workspace's analysis calls go.
func (s Settings) AnalysisTarget() Target {
	if s.Analysis == nil {
		return Target{}
	}
	return Target{
		Model:         s.Analysis.Model,
		BaseURL:       strings.TrimSpace(s.Analysis.BaseURL),
		APIKey:        strings.TrimSpace(s.Analysis.APIKey),
		Source:        strings.TrimSpace(s.Analysis.Source),
		RuntimeID:     strings.TrimSpace(s.Analysis.RuntimeID),
		ThinkingLevel: strings.TrimSpace(s.Analysis.ThinkingLevel),
	}
}

// PrimaryTarget is the call the settings section reports on and the probe
// dials: the judge when it is on, since it has the last word, otherwise the
// analysis model. ok is false in ModeNone — there is nothing to call.
func (s Settings) PrimaryTarget() (Target, bool) {
	switch {
	case s.JudgeOn():
		return s.Target(), true
	case s.AnalysisOn():
		return s.AnalysisTarget(), true
	}
	return Target{}, false
}

// Target is where one judge call is sent: which model, on whose endpoint,
// with whose key. Route resolves it once from Settings and passes it down, so
// no layer below has to know whether the workspace brought its own gateway.
type Target struct {
	Model         string
	BaseURL       string
	APIKey        string
	Source        string
	RuntimeID     string
	ThinkingLevel string
}

func (t Target) UsesRuntime() bool {
	return t.Source == AnalysisSourceRuntimeSubscription && t.RuntimeID != ""
}

// Override reports whether this target names a workspace-owned gateway rather
// than the deployment's.
//
// Both halves are required. A base URL with no key would silently fall back to
// the deployment key against somebody else's endpoint — which is how a
// deployment credential gets sent to a host the operator never chose — and a
// key with no base URL would send a workspace credential to the deployment
// endpoint. Neither is ever what the person filling in the form meant, so a
// half-filled pair uses the deployment gateway and the settings section says
// so.
func (t Target) Override() bool {
	return strings.TrimSpace(t.BaseURL) != "" && strings.TrimSpace(t.APIKey) != ""
}

// Target resolves where this workspace's judge calls go.
func (s Settings) Target() Target {
	return Target{
		Model:   s.Model,
		BaseURL: strings.TrimSpace(s.BaseURL),
		APIKey:  strings.TrimSpace(s.APIKey),
	}
}

// State is what the settings section shows and what Route branches on. It is
// derived, never stored: a stored copy would drift from the fields above.
type State string

const (
	// StateOff — the switch is off. This is the default for every workspace
	// that has never touched the section.
	StateOff State = "off"
	// StateIncomplete — the switch is on and a role is switched on without a
	// model. Behaves exactly like StateOff; it exists so the UI can say why
	// nothing happens. Both roles off is NOT incomplete: that is ModeNone,
	// which routes on the fallback rung without calling anything.
	StateIncomplete State = "incomplete"
	// StateEnabled — switch on, model chosen. Routing does its work.
	StateEnabled State = "enabled"
	// StateIneffective — configured, but the model is currently unusable
	// (rejected, unreachable, deleted, or the breaker is cooling down).
	// Behaves exactly like StateOff, and the reason is shown in settings only.
	// It is never derived from the stored fields alone; Route supplies it.
	StateIneffective State = "ineffective"
)

// Active reports whether routing should do anything at all. Only StateEnabled
// is active: the other three states are the pre-existing code path.
func (s State) Active() bool { return s == StateEnabled }

// ParseSettings reads the routing block out of a workspace `settings` payload.
// A missing block, a malformed one, or a null yields the zero Settings, which
// is StateOff — a workspace whose settings JSON cannot be parsed must not
// start routing tickets.
func ParseSettings(raw []byte) Settings {
	if len(raw) == 0 {
		return Settings{}
	}
	var envelope struct {
		Routing *Settings `json:"routing"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Routing == nil {
		return Settings{}
	}
	return *envelope.Routing
}

// State classifies the stored fields. It can only return StateOff,
// StateIncomplete, or StateEnabled: StateIneffective depends on live model
// health, which the stored fields cannot know.
func (s Settings) State() State {
	if !s.Enabled {
		return StateOff
	}
	if s.JudgeOn() && strings.TrimSpace(s.Model) == "" {
		return StateIncomplete
	}
	if s.AnalysisOn() && (strings.TrimSpace(s.Analysis.Model) == "" || (s.Analysis.Source == AnalysisSourceRuntimeSubscription && strings.TrimSpace(s.Analysis.RuntimeID) == "")) {
		return StateIncomplete
	}
	return StateEnabled
}

// Threshold returns the effective confidence floor. An unset, negative,
// non-finite, or above-1 value falls back to the default rather than being
// clamped silently to something that would accept every verdict.
func (s Settings) Threshold() float64 {
	t := s.ConfidenceThreshold
	if math.IsNaN(t) || math.IsInf(t, 0) || t <= 0 || t > 1 {
		return DefaultConfidenceThreshold
	}
	return t
}

// maxStaleReviewHours caps the stored value. A year is already far past the
// point where the sweep would ever fire, and the cap is what keeps a typo or a
// hostile settings write from producing a duration that overflows into the
// past.
const maxStaleReviewHours = 24 * 365

// StaleAfter returns how long a ticket must have been quiet in the in-review
// category before the stale sweep may act on it. Out-of-range values fall back
// to the default rather than being clamped to zero, because zero here would
// mean "sweep every ticket the moment it enters review" — the one reading that
// turns the safety net into a loop.
func (s Settings) StaleAfter() time.Duration {
	h := s.StaleReviewHours
	if math.IsNaN(h) || math.IsInf(h, 0) || h <= 0 || h > maxStaleReviewHours {
		return DefaultStaleReviewHours * time.Hour
	}
	return time.Duration(h * float64(time.Hour))
}
