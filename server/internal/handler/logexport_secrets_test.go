// The log-export git token rules. Nothing here touches the database: every
// rule under test is a pure transformation of a settings payload, which is
// what makes them cheap enough to pin exhaustively.
package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func logExportBoxedHandler(t *testing.T) *Handler {
	t.Helper()
	box, err := NewLogExportSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewLogExportSecretBox: %v", err)
	}
	return &Handler{LogExportSecrets: box}
}

func settingsWithLogExport(repo map[string]any) map[string]any {
	return map[string]any{
		"github_enabled": true,
		"logExport":      map[string]any{"gitRepo": repo},
	}
}

func logExportRepoBlock(t *testing.T, settings any) map[string]any {
	t.Helper()
	root, ok := settings.(map[string]any)
	if !ok {
		t.Fatalf("settings is not an object: %#v", settings)
	}
	block, ok := root["logExport"].(map[string]any)
	if !ok {
		t.Fatalf("logExport block missing: %#v", root)
	}
	repo, ok := block["gitRepo"].(map[string]any)
	if !ok {
		t.Fatalf("logExport.gitRepo missing: %#v", block)
	}
	return repo
}

// TestLogExportTokenRoundTrips is the base case: what goes in comes back out,
// and the stored form is not the plaintext.
func TestLogExportTokenRoundTrips(t *testing.T) {
	h := logExportBoxedHandler(t)
	sealed, ok := h.sealLogExportToken("ghp_live_abc123")
	if !ok {
		t.Fatal("sealLogExportToken refused a token on a boxed deployment")
	}
	if strings.Contains(sealed, "ghp_live_abc123") {
		t.Fatalf("sealed value contains the plaintext: %q", sealed)
	}
	if got := h.openLogExportToken(sealed); got != "ghp_live_abc123" {
		t.Fatalf("openLogExportToken = %q, want the original token", got)
	}
}

// TestLogExportTokenSealedUnderAnotherDeploymentOpensEmpty covers a restored
// database or a rotated secret. The token must read as absent, which the push
// handler turns into a configuration error rather than an anonymous push.
func TestLogExportTokenSealedUnderAnotherDeploymentOpensEmpty(t *testing.T) {
	sealed, ok := logExportBoxedHandler(t).sealLogExportToken("ghp_live_abc123")
	if !ok {
		t.Fatal("seal failed")
	}
	other, err := NewLogExportSecretBox("a-completely-different-secret")
	if err != nil {
		t.Fatalf("NewLogExportSecretBox: %v", err)
	}
	if got := (&Handler{LogExportSecrets: other}).openLogExportToken(sealed); got != "" {
		t.Fatalf("openLogExportToken under a foreign secret = %q, want empty", got)
	}
	if got := (&Handler{}).openLogExportToken(sealed); got != "" {
		t.Fatalf("openLogExportToken with no box = %q, want empty", got)
	}
	if got := (&Handler{LogExportSecrets: other}).openLogExportToken("not base64 at all !!"); got != "" {
		t.Fatalf("openLogExportToken on junk = %q, want empty", got)
	}
}

// TestLogExportRedactionStripsToken is the rule that keeps the credential off
// the wire. It also asserts the rest of the nested payload survives and that
// the caller's own maps are untouched — redaction happens while other code in
// the same request may still be reading the settings.
func TestLogExportRedactionStripsToken(t *testing.T) {
	in := settingsWithLogExport(map[string]any{
		"enabled":   true,
		"url":       "https://github.com/org/repo.git",
		"branch":    "kun",
		"token":     "ghp_plaintext",
		"token_enc": "c2VhbGVk",
	})
	out := redactLogExportSettings(in)
	repo := logExportRepoBlock(t, out)
	if _, present := repo["token"]; present {
		t.Fatalf("plaintext token survived redaction: %#v", repo)
	}
	if _, present := repo["token_enc"]; present {
		t.Fatalf("sealed token survived redaction: %#v", repo)
	}
	if repo["enabled"] != true || repo["url"] != "https://github.com/org/repo.git" || repo["branch"] != "kun" {
		t.Fatalf("redaction dropped non-secret fields: %#v", repo)
	}
	if root, _ := out.(map[string]any); root["github_enabled"] != true {
		t.Fatalf("redaction touched an unrelated settings key: %#v", out)
	}
	// The caller's own maps must be unchanged.
	original := in["logExport"].(map[string]any)["gitRepo"].(map[string]any)
	if _, present := original["token_enc"]; !present {
		t.Fatal("redaction mutated the input settings")
	}
	if _, present := original["token"]; !present {
		t.Fatal("redaction mutated the input settings")
	}
}

// TestLogExportUnrelatedSettingsWritePreservesToken is the failure this file
// exists to prevent. The client was never sent the token, so it cannot send it
// back; a write that took the payload literally would silently delete it.
func TestLogExportUnrelatedSettingsWritePreservesToken(t *testing.T) {
	h := logExportBoxedHandler(t)
	sealed, _ := h.sealLogExportToken("ghp_old")
	stored, _ := json.Marshal(settingsWithLogExport(map[string]any{
		"enabled": true, "url": "https://github.com/o/r.git", "token_enc": sealed,
	}))

	incoming := settingsWithLogExport(map[string]any{
		"enabled": true, "url": "https://github.com/o/r.git", "branch": "kun",
	})
	merged, ok := h.applyLogExportToken(incoming, stored)
	if !ok {
		t.Fatal("applyLogExportToken refused a write it should have accepted")
	}
	if got := logExportRepoBlock(t, merged)["token_enc"]; got != sealed {
		t.Fatalf("token_enc = %v, want the stored ciphertext carried forward", got)
	}
}

// TestLogExportNewTokenReplacesTheStoredOne — and the plaintext field never
// reaches storage in any form.
func TestLogExportNewTokenReplacesTheStoredOne(t *testing.T) {
	h := logExportBoxedHandler(t)
	oldSealed, _ := h.sealLogExportToken("ghp-old")
	stored, _ := json.Marshal(settingsWithLogExport(map[string]any{"token_enc": oldSealed}))

	merged, ok := h.applyLogExportToken(settingsWithLogExport(map[string]any{
		"enabled": true, "url": "u", "token": "ghp-new",
	}), stored)
	if !ok {
		t.Fatal("applyLogExportToken refused a new token")
	}
	repo := logExportRepoBlock(t, merged)
	if _, present := repo["token"]; present {
		t.Fatalf("plaintext token reached storage: %#v", repo)
	}
	sealed, _ := repo["token_enc"].(string)
	if sealed == "" || sealed == oldSealed {
		t.Fatalf("token_enc was not replaced: %q", sealed)
	}
	if got := h.openLogExportToken(sealed); got != "ghp-new" {
		t.Fatalf("stored token opens to %q, want ghp-new", got)
	}
	encoded, _ := json.Marshal(merged)
	if strings.Contains(string(encoded), "ghp-new") {
		t.Fatalf("serialised settings carried the plaintext: %s", encoded)
	}
}

// TestLogExportEmptyTokenClearsIt — the only way to remove a stored token, and
// it has to be distinguishable from "this write is about something else".
func TestLogExportEmptyTokenClearsIt(t *testing.T) {
	h := logExportBoxedHandler(t)
	sealed, _ := h.sealLogExportToken("ghp-old")
	stored, _ := json.Marshal(settingsWithLogExport(map[string]any{"token_enc": sealed}))

	merged, ok := h.applyLogExportToken(settingsWithLogExport(map[string]any{
		"enabled": true, "url": "u", "token": "",
	}), stored)
	if !ok {
		t.Fatal("applyLogExportToken refused a clear")
	}
	repo := logExportRepoBlock(t, merged)
	if _, present := repo["token_enc"]; present {
		t.Fatalf("an explicit clear left a token behind: %#v", repo)
	}
}

// TestLogExportDeploymentWithoutABoxRefusesToken. Accepting and dropping it
// would show a saved-looking form over a token that was never stored.
func TestLogExportDeploymentWithoutABoxRefusesToken(t *testing.T) {
	h := &Handler{}
	if _, ok := h.applyLogExportToken(settingsWithLogExport(map[string]any{"token": "ghp_live"}), nil); ok {
		t.Fatal("a deployment with no secretbox accepted a log-export token")
	}
	// A write that carries no token at all is still fine on such a deployment.
	if _, ok := h.applyLogExportToken(settingsWithLogExport(map[string]any{"enabled": true}), nil); !ok {
		t.Fatal("a deployment with no secretbox refused a token-free settings write")
	}
}

// TestLogExportGarbageStoredSettingsCannotSmuggleAValue — an unparseable
// stored column contributes nothing rather than an attacker-shaped string.
func TestLogExportGarbageStoredSettingsCannotSmuggleAValue(t *testing.T) {
	if got := storedLogExportSealedToken([]byte("{not json")); got != "" {
		t.Fatalf("storedLogExportSealedToken on junk = %q", got)
	}
	if got := storedLogExportSealedToken(nil); got != "" {
		t.Fatalf("storedLogExportSealedToken on nil = %q", got)
	}
}

// TestLogExportMergeChainsWithRouting is the UpdateWorkspace contract: both
// blocks live in one column, and carrying one forward must not discard the
// other.
func TestLogExportMergeChainsWithRouting(t *testing.T) {
	h := &Handler{}
	routingBox, err := NewRoutingSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	h.RoutingSecrets = routingBox
	h.LogExportSecrets, err = NewLogExportSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewLogExportSecretBox: %v", err)
	}

	routingSealed, _ := h.sealRoutingKey("sk-live")
	logExportSealed, _ := h.sealLogExportToken("ghp-live")
	stored, _ := json.Marshal(map[string]any{
		"routing":   map[string]any{"enabled": true, "api_key_enc": routingSealed},
		"logExport": map[string]any{"gitRepo": map[string]any{"enabled": true, "url": "u", "token_enc": logExportSealed}},
	})

	incoming := map[string]any{
		"routing":   map[string]any{"enabled": true},
		"logExport": map[string]any{"gitRepo": map[string]any{"enabled": true, "url": "u"}},
	}
	merged, ok := h.applyRoutingSecret(incoming, stored)
	if !ok {
		t.Fatal("applyRoutingSecret refused")
	}
	merged, ok = h.applyLogExportToken(merged, stored)
	if !ok {
		t.Fatal("applyLogExportToken refused")
	}
	root := merged.(map[string]any)
	if got := root["routing"].(map[string]any)["api_key_enc"]; got != routingSealed {
		t.Fatalf("routing key was dropped by the chain: %v", got)
	}
	if got := logExportRepoBlock(t, merged)["token_enc"]; got != logExportSealed {
		t.Fatalf("log-export token was dropped by the chain: %v", got)
	}
}
