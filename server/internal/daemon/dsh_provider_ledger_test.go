package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dshUpsertBody is one ordinary save: an endpoint, a key and a DeepSeek model,
// which is the shape the Command Code line uses and therefore the one whose
// compat switch has to survive every path below.
func dshUpsertBody(id string, modelIDs ...string) map[string]any {
	models := make([]map[string]any, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, map[string]any{"id": modelID})
	}
	return map[string]any{
		"id":       id,
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"api_key":  "sk-test-" + id,
		"models":   models,
	}
}

// dshProviderEntry reads one provider section out of settings.yaml, or nil when
// the file has none.
func dshProviderEntry(t *testing.T, home, id string) map[string]any {
	t.Helper()
	entry, _ := dshProviderSections(t, home)[id].(map[string]any)
	return entry
}

// dshProviderSections returns every provider section, tolerating a file that
// has none — which is exactly the state these tests set up on purpose, so it
// cannot go through dshMapPath's fatal path lookup.
func dshProviderSections(t *testing.T, home string) map[string]any {
	t.Helper()
	root, _ := dshSettingsOrEmpty(t, home)[dshProviderRootKey].(map[string]any)
	providers, _ := root[dshProvidersKey].(map[string]any)
	return providers
}

// dshResetSettings is a DSH installation whose configuration was reset: the
// file DSH ships after a reinstall, with none of the user's providers in it.
// The credentials file is left alone, which is the realistic case — the two are
// separate files and only one of them is DSH's to rewrite.
func dshResetSettings(t *testing.T, home string) {
	t.Helper()
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), "ui-chat:\n  transcriptView: compact\n")
}

func dshThinkingFormat(entry map[string]any) string {
	compat, _ := entry["compat"].(map[string]any)
	format, _ := compat["thinkingFormat"].(string)
	return format
}

// TestDshProviderReplayRestoresResetSettings is the failure this ledger exists
// for: DSH's configuration is reset, the seat is still configured for the
// preset's model, and without a replay the next run fails with "model is not
// supported on this endpoint" hours later.
func TestDshProviderReplayRestoresResetSettings(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))
	dshApplyOK(t, providerActionActivate, map[string]any{"id": "command-code"})

	dshResetSettings(t, home)
	if entry := dshProviderEntry(t, home, "command-code"); entry != nil {
		t.Fatalf("reset left the provider behind: %#v", entry)
	}

	snapshot := dshApplyOK(t, providerActionReplay, nil)

	entry := dshProviderEntry(t, home, "command-code")
	if entry == nil {
		t.Fatal("replay did not restore the provider section")
	}
	if got := entry["baseURL"]; got != "https://api.example.invalid/provider/v1" {
		t.Errorf("baseURL = %v", got)
	}
	if got := entry["api"]; got != providerAPIOpenAICompletions {
		t.Errorf("api = %v", got)
	}
	if got := dshThinkingFormat(entry); got != providerThinkingFormatDeepSeek {
		t.Errorf("compat.thinkingFormat = %q, want %q — a DeepSeek route without it answers blank and reports no error",
			got, providerThinkingFormatDeepSeek)
	}
	if got := dshMapPath(t, dshSettingsOrEmpty(t, home), dshActiveModelKey, "model"); got != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("agent-default-model.model = %v", got)
	}

	if len(snapshot.Providers) != 1 {
		t.Fatalf("snapshot lists %d providers, want 1", len(snapshot.Providers))
	}
	// The credentials file survived the reset, so the restored preset is
	// immediately usable rather than restored-but-unauthenticated.
	if !snapshot.Providers[0].HasKey {
		t.Error("restored preset reports no key, but .credentials.yaml still holds it")
	}
	if len(snapshot.Providers[0].Models) != 1 || snapshot.Providers[0].Models[0].ID != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("restored models = %#v", snapshot.Providers[0].Models)
	}
}

// TestDshProviderReplayRunsTwiceWithoutChangingAnything pins that replay is
// safe to run at any time, including against a file that never lost anything.
func TestDshProviderReplayRunsTwiceWithoutChangingAnything(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))
	dshResetSettings(t, home)

	dshApplyOK(t, providerActionReplay, nil)
	first := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))
	dshApplyOK(t, providerActionReplay, nil)
	second := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))

	if first != second {
		t.Errorf("a second replay rewrote the file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if providers := dshProviderSections(t, home); len(providers) != 1 {
		t.Errorf("settings.yaml holds %d provider sections, want 1", len(providers))
	}
}

// TestDshProviderUpsertTwiceLeavesOneSection is the other half of the same
// promise, on the save path rather than the replay path.
func TestDshProviderUpsertTwiceLeavesOneSection(t *testing.T) {
	home := dshTestHome(t)
	body := dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash")
	dshApplyOK(t, providerActionUpsert, body)
	first := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))
	dshApplyOK(t, providerActionUpsert, body)
	second := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))

	if first != second {
		t.Errorf("saving the same preset twice changed the file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if providers := dshProviderSections(t, home); len(providers) != 1 {
		t.Errorf("settings.yaml holds %d provider sections, want 1", len(providers))
	}
	if got := dshThinkingFormat(dshProviderEntry(t, home, "command-code")); got != providerThinkingFormatDeepSeek {
		t.Errorf("compat.thinkingFormat = %q after the second save", got)
	}
}

// TestDshProviderReplayKeepsHandEditedValues pins the rule that makes replay
// safe to offer as a button: it fills gaps, it does not correct the user.
func TestDshProviderReplayKeepsHandEditedValues(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))

	// The user moved the endpoint by hand and dropped the compat switch with
	// it — the exact mix replay has to treat differently field by field.
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), `llm-pi-ai:
  providers:
    command-code:
      apiKeyEnv: MULTICA_COMMAND_CODE_API_KEY
      api: openai-completions
      baseURL: https://moved.example.invalid/v1
      models:
        - id: deepseek/deepseek-v4.1-flash
`)
	dshApplyOK(t, providerActionReplay, nil)

	entry := dshProviderEntry(t, home, "command-code")
	if got := entry["baseURL"]; got != "https://moved.example.invalid/v1" {
		t.Errorf("replay overwrote a hand-edited baseURL: %v", got)
	}
	if got := dshThinkingFormat(entry); got != providerThinkingFormatDeepSeek {
		t.Errorf("compat.thinkingFormat = %q, want it restored alongside the hand-edited endpoint", got)
	}
}

// TestDshProviderReplayDoesNotResurrectDeletedPreset is the outcome that would
// make replay worse than no replay.
func TestDshProviderReplayDoesNotResurrectDeletedPreset(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("keeper", "deepseek/deepseek-v4.1-flash"))
	dshApplyOK(t, providerActionDelete, map[string]any{"id": "command-code"})
	dshResetSettings(t, home)

	dshApplyOK(t, providerActionReplay, nil)

	if entry := dshProviderEntry(t, home, "command-code"); entry != nil {
		t.Errorf("replay brought back a deleted preset: %#v", entry)
	}
	if entry := dshProviderEntry(t, home, "keeper"); entry == nil {
		t.Error("replay dropped the preset that was not deleted")
	}
}

// TestDshProviderReplayReportsMissingCredential covers the reinstall that took
// the credentials file too. The shape comes back so the seat's model id
// resolves; the key does not, and the snapshot says so rather than presenting a
// preset that will fail at the next run.
func TestDshProviderReplayReportsMissingCredential(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))
	dshResetSettings(t, home)
	if err := os.Remove(filepath.Join(home, dshCredentialsFileName)); err != nil {
		t.Fatalf("remove credentials: %v", err)
	}

	snapshot := dshApplyOK(t, providerActionReplay, nil)

	if len(snapshot.Providers) != 1 {
		t.Fatalf("snapshot lists %d providers, want 1", len(snapshot.Providers))
	}
	if snapshot.Providers[0].HasKey {
		t.Error("snapshot claims a key the credentials file no longer holds")
	}
	if dshProviderEntry(t, home, "command-code") == nil {
		t.Error("replay wrote nothing, so the seat's model id still will not resolve")
	}
}

// TestDshProviderReplayWithNothingRecordedFails: a machine that never saved a
// preset and has none configured now has nothing to put back, and saying so
// beats writing an empty provider section and reporting success. (A machine
// that does have presets is never in this state — adoption records them.)
func TestDshProviderReplayWithNothingRecordedFails(t *testing.T) {
	home := dshTestHome(t)
	dshResetSettings(t, home)

	err := dshApply(t, providerActionReplay, nil)
	if err == nil {
		t.Fatal("replay with an empty ledger succeeded")
	}
	if !strings.Contains(err.Error(), "nothing to replay") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestDshProviderLedgerNeverHoldsTheKey. The ledger is a second file on the
// user's disk describing their providers; the credential is the one field it
// must never learn, or a reset of .credentials.yaml would be repaired from a
// copy nobody agreed to keep.
func TestDshProviderLedgerNeverHoldsTheKey(t *testing.T) {
	dshTestHome(t)
	path := dshTestLedger(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))

	recorded := dshReadTestFile(t, path)
	if strings.Contains(recorded, "sk-test-") {
		t.Errorf("the replay ledger recorded an API key:\n%s", recorded)
	}
	if !strings.Contains(recorded, "MULTICA_COMMAND_CODE_API_KEY") {
		t.Errorf("the ledger did not record the env name the key is addressed by:\n%s", recorded)
	}
}

// TestDshProviderReplayIsScopedToItsDshHome. DSH_HOME is per-profile here, so
// two homes hold two unrelated sets of presets and replaying one into the other
// would write a provider the user never configured there.
func TestDshProviderReplayIsScopedToItsDshHome(t *testing.T) {
	dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))

	other := t.TempDir()
	t.Setenv("DSH_HOME", other)
	if err := dshApply(t, providerActionReplay, nil); err == nil {
		t.Fatal("replay wrote another DSH home's presets into this one")
	}
	if entry := dshProviderEntry(t, other, "command-code"); entry != nil {
		t.Errorf("a foreign preset landed in this home: %#v", entry)
	}
}

// TestDshProviderUpsertWritesThinkingFormatForAnyDeepSeekModel pins that the
// compat switch is a property of the preset, not of whichever model the health
// check happened to run against. It is derived from the whole model list
// because the seat may be pointed at a model the probe never touched — and the
// loss is silent: the route answers and the reply renders blank.
func TestDshProviderUpsertWritesThinkingFormatForAnyDeepSeekModel(t *testing.T) {
	home := dshTestHome(t)
	body := dshUpsertBody("command-code", "kimi-k2", "deepseek/deepseek-v4.1-flash")
	body["verify_model"] = "kimi-k2"
	dshApplyOK(t, providerActionUpsert, body)

	if got := dshThinkingFormat(dshProviderEntry(t, home, "command-code")); got != providerThinkingFormatDeepSeek {
		t.Errorf("compat.thinkingFormat = %q, want %q — the preset carries a DeepSeek model even though the probe verified another one",
			got, providerThinkingFormatDeepSeek)
	}
}

// TestDshProviderListAdoptsPresetConfiguredBeforeTheLedger is the case the
// ledger would otherwise miss entirely: every preset on a machine that was
// working before this feature shipped was configured without passing through
// save, activate or delete, so nothing recorded it. Reading the provider list —
// which is what opening the section does — has to claim them, or the replay
// button on that very screen answers "nothing to replay" on the one machine it
// was built for.
func TestDshProviderListAdoptsPresetConfiguredBeforeTheLedger(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	dshApplyOK(t, providerActionList, nil)

	dshResetSettings(t, home)
	snapshot := dshApplyOK(t, providerActionReplay, nil)

	entry := dshProviderEntry(t, home, "command-code")
	if entry == nil {
		t.Fatal("replay restored nothing: the preset was never adopted into the ledger")
	}
	if got := entry["baseURL"]; got != "https://api.example.invalid/provider/v1" {
		t.Errorf("baseURL = %v", got)
	}
	if got := entry["apiKeyEnv"]; got != "COMMAND_CODE_API_KEY" {
		t.Errorf("apiKeyEnv = %v — the restored preset no longer names the key the credentials file holds", got)
	}
	if got := dshThinkingFormat(entry); got != providerThinkingFormatDeepSeek {
		t.Errorf("compat.thinkingFormat = %q, want %q", got, providerThinkingFormatDeepSeek)
	}
	if got := dshMapPath(t, dshSettingsOrEmpty(t, home), dshActiveModelKey, "model"); got != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("agent-default-model.model = %v", got)
	}
	if len(snapshot.Providers) != 1 || !snapshot.Providers[0].HasKey {
		t.Errorf("restored snapshot = %#v, want one preset still holding its key", snapshot.Providers)
	}
}

// TestDshProviderListDoesNotRerecordOverTheLedger. Adoption fills the gap and
// stops there: the ledger is the record of what Multica wrote, and a
// settings.yaml the user has since edited must not quietly become the record —
// otherwise a reset that happens to be read before it is noticed would replace
// the saved configuration with whatever state the file was left in.
func TestDshProviderListDoesNotRerecordOverTheLedger(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))

	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), `llm-pi-ai:
  providers:
    command-code:
      apiKeyEnv: MULTICA_COMMAND_CODE_API_KEY
      api: openai-completions
      baseURL: https://moved.example.invalid/v1
      models:
        - id: deepseek/deepseek-v4.1-flash
`)
	dshApplyOK(t, providerActionList, nil)
	dshResetSettings(t, home)
	dshApplyOK(t, providerActionReplay, nil)

	if got := dshProviderEntry(t, home, "command-code")["baseURL"]; got != "https://api.example.invalid/provider/v1" {
		t.Errorf("baseURL = %v, want the endpoint Multica saved", got)
	}
}

// TestDshProviderListDoesNotAdoptDeletedPreset. Delete removes the preset from
// settings.yaml as well as from the ledger, so a later list has nothing to
// adopt — pinned because adoption is the one path that could undo a delete.
func TestDshProviderListDoesNotAdoptDeletedPreset(t *testing.T) {
	home := dshTestHome(t)
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("command-code", "deepseek/deepseek-v4.1-flash"))
	dshApplyOK(t, providerActionUpsert, dshUpsertBody("keeper", "deepseek/deepseek-v4.1-flash"))
	dshApplyOK(t, providerActionDelete, map[string]any{"id": "command-code"})

	dshApplyOK(t, providerActionList, nil)
	dshResetSettings(t, home)
	dshApplyOK(t, providerActionReplay, nil)

	if entry := dshProviderEntry(t, home, "command-code"); entry != nil {
		t.Errorf("a deleted preset came back through adoption: %#v", entry)
	}
	if dshProviderEntry(t, home, "keeper") == nil {
		t.Error("replay dropped the preset that was not deleted")
	}
}

// TestDshProviderAdoptionNeverRecordsTheKey. Adoption reads a file the user
// wrote by hand, so it meets shapes the upsert path never produces; the
// credential must stay out of the ledger on this path too.
func TestDshProviderAdoptionNeverRecordsTheKey(t *testing.T) {
	home := dshTestHome(t)
	path := dshTestLedger(t)
	seedDshHome(t, home)

	dshApplyOK(t, providerActionList, nil)

	recorded := dshReadTestFile(t, path)
	if strings.Contains(recorded, "sk-test-existing") {
		t.Errorf("adoption copied an API key into the ledger:\n%s", recorded)
	}
}
