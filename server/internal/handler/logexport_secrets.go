package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/multica-ai/multica/server/internal/logexport"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// The log-export git token is the second credential a workspace types into
// Multica's own settings, and it follows the same three rules as the routing
// gateway key (see routing_secrets.go) for the same reason: it lives in
// `workspace.settings`, a JSONB column the workspace read endpoint serialises
// wholesale to every member.
//
//  1. It is sealed before it is stored, so a database dump without the
//     deployment secret carries nothing usable.
//  2. The sealed value is stripped on the way out, so no client ever receives
//     it — not even the ciphertext, and not in masked form.
//  3. Because the client never receives it, the client also cannot send it
//     back. A settings write therefore CARRIES THE STORED VALUE FORWARD
//     instead of taking the client's word for it. Without that, the first
//     unrelated settings save — a routing toggle, a display preference —
//     would silently wipe the token and pushes would start failing with
//     nothing on screen to explain why.
//
// Clearing is explicit: `token: ""`. Absence means "leave it alone", which is
// the only reading that survives rule 3.
//
// The token is nested one level deeper than the routing key because it belongs
// to one repository, not to the whole log-export block. Both write paths run
// on the same settings payload, so `applyRoutingSecret` and
// `applyLogExportToken` are chained by UpdateWorkspace rather than merged into
// one function that would have to know both shapes.

// logExportTokenPlainKey is the write-only field a client sends a new token in.
// It is never stored: the write path replaces it with logExportTokenSealedKey.
const logExportTokenPlainKey = "token"

// logExportTokenSealedKey is the stored, sealed field. It matches the
// `token_enc` name the settings JSON uses; named here too because this file
// manipulates the settings JSON as a map, before it has a type.
const logExportTokenSealedKey = "token_enc"

// logExportGitRepoKey is the nested block the token belongs to.
const logExportGitRepoKey = "gitRepo"

// NewLogExportSecretBox derives the log-export secretbox from a deployment
// secret.
//
// Derived rather than configured separately so that using a private repo needs
// no new deployment step: a self-hosted instance already has JWT_SECRET, and
// requiring a second env var would mean the feature silently cannot store a
// token until somebody SSHes into the box.
//
// The HMAC domain string keeps this key distinct from every other use of the
// same deployment secret, including the routing key derived from the same
// JWT_SECRET, so a log-export ciphertext can never be opened by, or confused
// with, a token from another subsystem.
func NewLogExportSecretBox(deploymentSecret string) (*secretbox.Box, error) {
	trimmed := strings.TrimSpace(deploymentSecret)
	if trimmed == "" {
		return nil, secretbox.ErrInvalidKey
	}
	sum := sha256.Sum256([]byte("multica/log-export-git-token/v1:" + trimmed))
	return secretbox.New(sum[:])
}

// sealLogExportToken returns the base64 ciphertext for a plaintext token.
func (h *Handler) sealLogExportToken(plain string) (string, bool) {
	if h.LogExportSecrets == nil || strings.TrimSpace(plain) == "" {
		return "", false
	}
	sealed, err := h.LogExportSecrets.Seal([]byte(strings.TrimSpace(plain)))
	if err != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(sealed), true
}

// openLogExportToken reverses sealLogExportToken. Every failure — no box, bad
// base64, wrong deployment secret, tampered row — yields the empty string
// rather than an error, and the empty string means "no usable token". The push
// handler reads that as an anonymous push when no token was configured, and as
// a deployment misconfiguration when one was; the caller makes that call, not
// this function.
func (h *Handler) openLogExportToken(sealed string) string {
	if h.LogExportSecrets == nil || strings.TrimSpace(sealed) == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		return ""
	}
	plain, err := h.LogExportSecrets.Open(raw)
	if err != nil {
		return ""
	}
	return string(plain)
}

// redactLogExportSettings strips the token fields from a settings payload on
// its way to a client.
//
// It mutates nothing the caller owns: the `logExport` block and the nested
// `gitRepo` block are both copied before the token is dropped, so redacting a
// response cannot alter the settings anything else in the same request is
// still reading.
func redactLogExportSettings(settings any) any {
	root, ok := settings.(map[string]any)
	if !ok {
		return settings
	}
	block, ok := root[logexport.SettingsKey].(map[string]any)
	if !ok {
		return settings
	}
	repo, ok := block[logExportGitRepoKey].(map[string]any)
	if !ok {
		return settings
	}
	_, hasPlain := repo[logExportTokenPlainKey]
	_, hasSealed := repo[logExportTokenSealedKey]
	if !hasPlain && !hasSealed {
		return settings
	}

	cleanRepo := make(map[string]any, len(repo))
	for k, v := range repo {
		if k == logExportTokenPlainKey || k == logExportTokenSealedKey {
			continue
		}
		cleanRepo[k] = v
	}
	cleanBlock := make(map[string]any, len(block))
	for k, v := range block {
		cleanBlock[k] = v
	}
	cleanBlock[logExportGitRepoKey] = cleanRepo
	cleanRoot := make(map[string]any, len(root))
	for k, v := range root {
		cleanRoot[k] = v
	}
	cleanRoot[logexport.SettingsKey] = cleanBlock
	return cleanRoot
}

// applyLogExportToken resolves the repository token for an incoming settings
// write, given the settings currently stored.
//
// The three cases, in the order they are checked:
//
//   - a non-empty `token` — a new token was typed. Seal it and store the
//     ciphertext. If this deployment has no secretbox the write is REFUSED
//     (ok=false) rather than quietly dropped: a token that looks saved but is
//     not is worse than an error message.
//   - an empty `token` — an explicit clear. Both fields end up absent.
//   - no `token` at all — every other settings write there is. The stored
//     ciphertext is carried forward untouched.
//
// The plaintext field never survives into the returned payload in any branch.
func (h *Handler) applyLogExportToken(incoming any, stored []byte) (any, bool) {
	root, ok := incoming.(map[string]any)
	if !ok {
		return incoming, true
	}
	block, hasBlock := root[logexport.SettingsKey].(map[string]any)
	if !hasBlock {
		// No log-export block in this write at all. UpdateWorkspace replaces
		// the whole column, so the stored block — token included — is already
		// being dropped by the caller's own semantics; nothing to preserve
		// here that the client did not itself decide to discard.
		return incoming, true
	}
	repo, hasRepo := block[logExportGitRepoKey].(map[string]any)
	if !hasRepo {
		// The write keeps the block but drops the repository. A token with no
		// repository to authenticate is dead weight, so it is not preserved.
		return incoming, true
	}

	nextRepo := make(map[string]any, len(repo)+1)
	for k, v := range repo {
		if k == logExportTokenPlainKey || k == logExportTokenSealedKey {
			continue
		}
		nextRepo[k] = v
	}

	plain, typed := repo[logExportTokenPlainKey].(string)
	switch {
	case typed && strings.TrimSpace(plain) != "":
		sealed, sealedOK := h.sealLogExportToken(plain)
		if !sealedOK {
			return nil, false
		}
		nextRepo[logExportTokenSealedKey] = sealed
	case typed:
		// Explicit clear: leave both fields absent.
	default:
		if carried := storedLogExportSealedToken(stored); carried != "" {
			nextRepo[logExportTokenSealedKey] = carried
		}
	}

	nextBlock := make(map[string]any, len(block))
	for k, v := range block {
		nextBlock[k] = v
	}
	nextBlock[logExportGitRepoKey] = nextRepo
	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = v
	}
	out[logexport.SettingsKey] = nextBlock
	return out, true
}

// storedLogExportSealedToken reads the sealed token out of a stored settings
// column. Anything unreadable yields the empty string: a settings blob this
// code cannot parse must not be able to smuggle a value into the next write.
func storedLogExportSealedToken(stored []byte) string {
	if len(stored) == 0 {
		return ""
	}
	var envelope struct {
		LogExport struct {
			GitRepo struct {
				TokenEnc string `json:"token_enc"`
			} `json:"gitRepo"`
		} `json:"logExport"`
	}
	if err := json.Unmarshal(stored, &envelope); err != nil {
		return ""
	}
	return envelope.LogExport.GitRepo.TokenEnc
}
