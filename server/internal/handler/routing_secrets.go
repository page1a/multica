package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// The routing gateway key is the first credential a workspace types into
// Multica's own settings rather than into a runtime's config file, and it
// lives in `workspace.settings` — a JSONB column the workspace read endpoint
// serialises wholesale to every member.
//
// Three rules keep that from being a leak, and all three are enforced here
// rather than at the call sites:
//
//  1. It is sealed before it is stored, so a database dump without the
//     deployment secret carries nothing usable.
//  2. The sealed value is stripped on the way out, so no client ever receives
//     it — not even the ciphertext, and not in masked form.
//  3. Because the client never receives it, the client also cannot send it
//     back. A settings write therefore CARRIES THE STORED VALUE FORWARD
//     instead of taking the client's word for it. Without that, the first
//     unrelated settings save — a GitHub toggle, a display preference — would
//     silently wipe the key and routing would stop with nothing to point at.
//
// Clearing is explicit: `api_key: ""`. Absence means "leave it alone", which
// is the only reading that survives rule 3.

// routingSecretPlainKey is the write-only field a client sends a new key in.
// It is never read back out of storage, because it is never stored: the write
// path replaces it with routingSecretSealedKey.
const routingSecretPlainKey = "api_key"

// routingSecretSealedKey is the stored, sealed field. It matches the
// `api_key_enc` tag on routing.Settings; named here too because this file
// manipulates the settings JSON as a map, before it has a type.
const routingSecretSealedKey = "api_key_enc"

// NewRoutingSecretBox derives the routing secretbox from a deployment secret.
//
// Derived rather than configured separately so that handing routing to a team
// needs no new deployment step: a self-hosted instance already has JWT_SECRET,
// and requiring a second env var would mean the feature silently cannot store
// a key until somebody SSHes into the box — which is the exact friction that
// made workspace-level configuration necessary in the first place.
//
// The HMAC domain string keeps this key distinct from every other use of the
// same deployment secret, so a routing ciphertext can never be opened by, or
// confused with, a token from another subsystem.
func NewRoutingSecretBox(deploymentSecret string) (*secretbox.Box, error) {
	trimmed := strings.TrimSpace(deploymentSecret)
	if trimmed == "" {
		return nil, secretbox.ErrInvalidKey
	}
	sum := sha256.Sum256([]byte("multica/routing-gateway-key/v1:" + trimmed))
	return secretbox.New(sum[:])
}

// sealRoutingKey returns the base64 ciphertext for a plaintext key.
func (h *Handler) sealRoutingKey(plain string) (string, bool) {
	if h.RoutingSecrets == nil || strings.TrimSpace(plain) == "" {
		return "", false
	}
	sealed, err := h.RoutingSecrets.Seal([]byte(strings.TrimSpace(plain)))
	if err != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(sealed), true
}

// openRoutingKey reverses sealRoutingKey. Every failure — no box, bad base64,
// wrong deployment secret, tampered row — yields the empty string rather than
// an error, and the empty string means "this workspace has no usable key", so
// routing falls back to the deployment gateway instead of dialling with
// garbage. The settings section reports the key as unset, which is the honest
// thing to show for a key nothing can open.
func (h *Handler) openRoutingKey(sealed string) string {
	if h.RoutingSecrets == nil || strings.TrimSpace(sealed) == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		return ""
	}
	plain, err := h.RoutingSecrets.Open(raw)
	if err != nil {
		return ""
	}
	return string(plain)
}

// redactRoutingSettings strips the sealed key from a settings payload on its
// way to a client.
//
// It mutates nothing the caller owns: the routing block is copied before the
// key is dropped, so redacting a response cannot alter the settings anything
// else in the same request is still reading.
func redactRoutingSettings(settings any) any {
	root, ok := settings.(map[string]any)
	if !ok {
		return settings
	}
	block, ok := root[routing.SettingsKey].(map[string]any)
	if !ok {
		return settings
	}
	analysis, hasAnalysis := block[routingAnalysisKey].(map[string]any)
	_, topSealed := block[routingSecretSealedKey]
	_, analysisSealed := analysis[routingSecretSealedKey]
	if !topSealed && !analysisSealed {
		return settings
	}
	cleanBlock := withoutKeyFields(block)
	if hasAnalysis {
		// The analysis role carries its own key (DENE-923), under the same
		// two field names and the same three rules.
		cleanBlock[routingAnalysisKey] = withoutKeyFields(analysis)
	}
	cleanRoot := make(map[string]any, len(root))
	for k, v := range root {
		cleanRoot[k] = v
	}
	cleanRoot[routing.SettingsKey] = cleanBlock
	return cleanRoot
}

// routingAnalysisKey is the nested block the analysis role's settings live
// in. It matches the `analysis` tag on routing.Settings.
const routingAnalysisKey = "analysis"

// withoutKeyFields copies a block minus both key fields.
func withoutKeyFields(block map[string]any) map[string]any {
	clean := make(map[string]any, len(block))
	for k, v := range block {
		if k == routingSecretSealedKey || k == routingSecretPlainKey {
			continue
		}
		clean[k] = v
	}
	return clean
}

// applyRoutingSecret resolves the routing key for an incoming settings write,
// given the settings currently stored.
//
// The three cases, in the order they are checked:
//
//   - a non-empty `api_key` — a new key was typed. Seal it and store the
//     ciphertext. If this deployment has no secretbox the write is REFUSED
//     (ok=false) rather than quietly dropped: a key that looks saved but is
//     not is worse than an error message.
//   - an empty `api_key` — an explicit clear. Both fields end up absent.
//   - no `api_key` at all — every other settings write there is. The stored
//     ciphertext is carried forward untouched.
//
// The plaintext field never survives into the returned payload in any branch.
func (h *Handler) applyRoutingSecret(incoming any, stored []byte) (any, bool) {
	root, ok := incoming.(map[string]any)
	if !ok {
		return incoming, true
	}
	block, hasBlock := root[routing.SettingsKey].(map[string]any)
	if !hasBlock {
		// No routing block in this write at all. UpdateWorkspace replaces the
		// whole column, so the stored block — key included — is already being
		// dropped by the caller's own semantics; nothing to preserve here that
		// the client did not itself decide to discard.
		return incoming, true
	}

	stored0, storedAnalysis := storedRoutingSealedKeys(stored)
	next, ok := h.resolveKeyFields(block, stored0)
	if !ok {
		return nil, false
	}
	if analysis, isBlock := block[routingAnalysisKey].(map[string]any); isBlock {
		nested, ok := h.resolveKeyFields(analysis, storedAnalysis)
		if !ok {
			return nil, false
		}
		next[routingAnalysisKey] = nested
	}

	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = v
	}
	out[routing.SettingsKey] = next
	return out, true
}

// resolveKeyFields applies the three cases to one block — the routing block
// itself, or the analysis block nested in it — given the ciphertext stored
// for that block.
func (h *Handler) resolveKeyFields(block map[string]any, storedSealed string) (map[string]any, bool) {
	next := withoutKeyFields(block)
	plain, typed := block[routingSecretPlainKey].(string)
	switch {
	case typed && strings.TrimSpace(plain) != "":
		sealed, sealedOK := h.sealRoutingKey(plain)
		if !sealedOK {
			return nil, false
		}
		next[routingSecretSealedKey] = sealed
	case typed:
		// Explicit clear: leave both fields absent.
	default:
		if storedSealed != "" {
			next[routingSecretSealedKey] = storedSealed
		}
	}
	return next, true
}

// storedRoutingSealedKey reads the judge's sealed key out of a stored
// settings column.
func storedRoutingSealedKey(stored []byte) string {
	judge, _ := storedRoutingSealedKeys(stored)
	return judge
}

// storedRoutingSealedKeys reads both sealed keys — the judge's, then the
// analysis role's — out of a stored settings column. Anything unreadable
// yields the empty string: a settings blob this code cannot parse must not
// be able to smuggle a value into the next write.
func storedRoutingSealedKeys(stored []byte) (string, string) {
	if len(stored) == 0 {
		return "", ""
	}
	var envelope struct {
		Routing struct {
			APIKeyEnc string `json:"api_key_enc"`
			Analysis  struct {
				APIKeyEnc string `json:"api_key_enc"`
			} `json:"analysis"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(stored, &envelope); err != nil {
		return "", ""
	}
	return envelope.Routing.APIKeyEnc, envelope.Routing.Analysis.APIKeyEnc
}
