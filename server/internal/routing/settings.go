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
}

// Target is where one judge call is sent: which model, on whose endpoint,
// with whose key. Route resolves it once from Settings and passes it down, so
// no layer below has to know whether the workspace brought its own gateway.
type Target struct {
	Model   string
	BaseURL string
	APIKey  string
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
	// StateIncomplete — the switch is on but no model was chosen. Behaves
	// exactly like StateOff; it exists so the UI can say why nothing happens.
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
	if s.Model == "" {
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
