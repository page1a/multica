package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// dshTestHome points DSH_HOME at a fresh directory so a test never reads or
// writes the developer's real ~/.dsh, and installs the fake gateway every
// save-time health check reaches. A test that needs a specific gateway answer
// installs its own afterwards.
func dshTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	dshTestLedger(t)
	dshInstallFakeGateway(t)
	return home
}

// dshTestLedger points the replay ledger at a directory of the test's own.
// Unlike DSH_HOME it is not read from the environment, so without this every
// save in this package would append to the developer's real
// ~/.multica/dsh-provider-presets.yaml — and a later replay there would write
// a test's fixture into their own DSH installation.
func dshTestLedger(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), dshLedgerFileName)
	previous := dshLedgerPathFn
	dshLedgerPathFn = func() (string, error) { return path, nil }
	t.Cleanup(func() { dshLedgerPathFn = previous })
	return path
}

func dshWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func dshReadTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// dshSettingsOrEmpty parses DSH's settings file, treating "the action was
// refused before it wrote anything" as an empty document rather than a
// failure — which is exactly what several tests are asserting.
func dshSettingsOrEmpty(t *testing.T, home string) map[string]any {
	t.Helper()
	path := filepath.Join(home, dshSettingsFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return dshYAMLMap(t, string(data))
}

func dshJSON(t *testing.T, body map[string]any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return data
}

func dshApply(t *testing.T, action string, body map[string]any) error {
	t.Helper()
	// A preset is only savable once the endpoint has both authenticated it and
	// run a completion on it, so an upsert test needs a gateway that serves the
	// models the test just listed. Tests that are about the probe itself call
	// dshUpsertVerify instead, which leaves the catalog alone.
	if action == providerActionUpsert && dshInstalledGateway != nil {
		dshInstalledGateway.serve(dshBodyModelIDs(body)...)
	}
	_, err := applyProviderConfig(context.Background(), "dsh", action, dshJSON(t, body))
	return err
}

// dshApplyOK fails the test on error and returns the snapshot.
func dshApplyOK(t *testing.T, action string, body map[string]any) *providerConfigSnapshot {
	t.Helper()
	if action == providerActionUpsert && dshInstalledGateway != nil {
		dshInstalledGateway.serve(dshBodyModelIDs(body)...)
	}
	snapshot, err := applyProviderConfig(context.Background(), "dsh", action, dshJSON(t, body))
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return snapshot
}

// The settings fixture carries every shape a real DSH settings.yaml has that
// this feature does not model: unrelated top-level sections, a provider-level
// compat block, and a per-model reasoningEfforts map. All of them must survive
// an edit to a neighbouring field.
const dshSettingsFixture = `agent-default-model:
  provider: command-code
  model: deepseek/deepseek-v4.1-flash
llm-deepseek:
  baseURL: https://example.invalid/zen/go/v1
ui-chat:
  transcriptView: compact
permission:
  defaultPreset: danger-full-access
llm-pi-ai:
  providers:
    command-code:
      apiKeyEnv: COMMAND_CODE_API_KEY
      api: openai-completions
      baseURL: https://api.example.invalid/provider/v1
      compat:
        thinkingFormat: deepseek
      models:
        - id: deepseek/deepseek-v4.1-flash
          name: DeepSeek V4.1 Flash
          contextWindow: 1000000
          reasoningEfforts:
            off:
            low: low
            high: high
            max: max
`

const dshCredentialsFixture = `version: 3
refs:
  COMMAND_CODE_API_KEY: sk-test-existing
records:
  keep: me
`

func seedDshHome(t *testing.T, home string) {
	t.Helper()
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), dshSettingsFixture)
	dshWriteTestFile(t, filepath.Join(home, dshCredentialsFileName), dshCredentialsFixture)
}

func dshYAMLMap(t *testing.T, content string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("parse yaml: %v\n%s", err, content)
	}
	return out
}

func dshMapPath(t *testing.T, root map[string]any, path ...any) any {
	t.Helper()
	var current any = root
	for _, key := range path {
		switch typed := key.(type) {
		case string:
			mapping, ok := current.(map[string]any)
			if !ok {
				t.Fatalf("path %v: %q is not inside a mapping", path, typed)
			}
			current = mapping[typed]
		case int:
			items, ok := current.([]any)
			if !ok || typed >= len(items) {
				t.Fatalf("path %v: index %d is not inside a sequence", path, typed)
			}
			current = items[typed]
		default:
			t.Fatalf("path %v: unsupported key %T", path, key)
		}
	}
	return current
}

func TestDshProviderUpsertPreservesUnknownKeys(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	// Edit the preset that already exists: the untouched keys inside it, and
	// in the rest of the file, are the point of the test.
	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "command-code",
		"api":      "openai-completions",
		"base_url": "https://api.example.invalid/provider/v2",
		"models": []map[string]any{
			{"id": "deepseek/deepseek-v4.1-flash", "name": "DeepSeek V4.1 Flash", "context_window": 1000000},
		},
	})

	settings := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)))

	// Unrelated sections, untouched.
	for _, key := range []string{"ui-chat", "permission", "llm-deepseek"} {
		before := dshMapPath(t, dshYAMLMap(t, dshSettingsFixture), key)
		if got := dshMapPath(t, settings, key); !yamlEqual(got, before) {
			t.Errorf("top-level %q changed: got %#v, want %#v", key, got, before)
		}
	}
	// Provider-level compat and model-level reasoningEfforts, both unknown to
	// the driver and both inside the entry it rewrote.
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code", "compat", "thinkingFormat"); got != "deepseek" {
		t.Errorf("provider compat lost: got %#v", got)
	}
	efforts := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code", "models", 0, "reasoningEfforts")
	effortsMap, ok := efforts.(map[string]any)
	if !ok || len(effortsMap) != 4 {
		t.Fatalf("model reasoningEfforts lost or truncated: %#v", efforts)
	}
	for _, level := range []string{"off", "low", "high", "max"} {
		if _, present := effortsMap[level]; !present {
			t.Errorf("reasoningEfforts missing level %q: %#v", level, effortsMap)
		}
	}
	// And the edit landed.
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code", "baseURL"); got != "https://api.example.invalid/provider/v2" {
		t.Errorf("baseURL not updated: got %#v", got)
	}
}

// yamlEqual compares two decoded YAML values for equality.
func yamlEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func TestDshProviderUpsertPreservesUnrelatedProvider(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "second-provider",
		"api":      "openai-completions",
		"base_url": "https://second.example.invalid/v1",
		"models":   []map[string]any{{"id": "m-1", "name": "M One"}},
		"api_key":  "sk-test-xxxx",
	})

	settings := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)))
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code", "baseURL"); got != "https://api.example.invalid/provider/v1" {
		t.Errorf("existing provider changed by a sibling upsert: %#v", got)
	}
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code", "compat", "thinkingFormat"); got != "deepseek" {
		t.Errorf("existing provider compat lost: %#v", got)
	}
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "second-provider", "api"); got != "openai-completions" {
		t.Errorf("new provider not written: %#v", got)
	}
}

func TestDshProviderUpsertDerivesAPIKeyEnvAndWritesKey(t *testing.T) {
	home := dshTestHome(t)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "my-provider-2",
		"api":      "openai-completions",
		"base_url": "https://example.invalid/v1",
		"models":   []map[string]any{{"id": "m-1"}},
		"api_key":  "sk-test-xxxx",
	})

	settings := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)))
	env := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "my-provider-2", "apiKeyEnv")
	if env != "MULTICA_MY_PROVIDER_2_API_KEY" {
		t.Fatalf("derived apiKeyEnv = %#v", env)
	}
	credentials := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshCredentialsFileName)))
	if got := dshMapPath(t, credentials, dshRefsKey, "MULTICA_MY_PROVIDER_2_API_KEY"); got != "sk-test-xxxx" {
		t.Fatalf("key not written to refs: %#v", got)
	}
	// A file this feature created has to carry the version DSH expects.
	if got := dshMapPath(t, credentials, "version"); got != 1 {
		t.Errorf("credentials version = %#v, want 1", got)
	}
}

func TestDshProviderUpsertKeepsExistingKeyWhenAPIKeyOmitted(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":          "command-code",
		"api":         "openai-completions",
		"base_url":    "https://api.example.invalid/provider/v3",
		"api_key_env": "COMMAND_CODE_API_KEY",
		"models":      []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}},
	})

	credentials := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshCredentialsFileName)))
	if got := dshMapPath(t, credentials, dshRefsKey, "COMMAND_CODE_API_KEY"); got != "sk-test-existing" {
		t.Fatalf("an empty api_key must not clear the stored key: %#v", got)
	}
}

func TestDshProviderListNeverReturnsTheKeyValue(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	snapshot := dshApplyOK(t, providerActionList, nil)

	if len(snapshot.Providers) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(snapshot.Providers))
	}
	preset := snapshot.Providers[0]
	if preset.ID != "command-code" || !preset.HasKey {
		t.Fatalf("preset = %+v", preset)
	}
	// The mask names the key without being usable: first three and last four
	// of "sk-test-existing".
	if preset.KeyMask != "sk-…ting" {
		t.Errorf("key_mask = %q", preset.KeyMask)
	}
	if snapshot.Active == nil || snapshot.Active.Provider != "command-code" {
		t.Fatalf("active = %+v", snapshot.Active)
	}
	if !preset.Active {
		t.Error("the active preset is not flagged")
	}
	if len(preset.Models) != 1 || preset.Models[0].Name != "DeepSeek V4.1 Flash" || preset.Models[0].ContextWindow != 1000000 {
		t.Errorf("models = %+v", preset.Models)
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-existing") {
		t.Fatalf("snapshot leaked the key: %s", encoded)
	}
}

func TestDshKeyMaskShortKeyIsFullyHidden(t *testing.T) {
	for _, key := range []string{"short", "1234567"} {
		if got := dshKeyMask(key); got != dshMaskedKeyValue {
			t.Errorf("mask(%q) = %q, want %q", key, got, dshMaskedKeyValue)
		}
	}
	if got := dshKeyMask(""); got != "" {
		t.Errorf("mask of an absent key = %q", got)
	}
	if got := dshKeyMask("sk-1234567890abcd"); got != "sk-…abcd" {
		t.Errorf("mask = %q", got)
	}
}

func TestDshProviderWriteRefusesCorruptSettings(t *testing.T) {
	home := dshTestHome(t)
	const corrupt = "ui-chat: [unclosed\n"
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), corrupt)
	dshWriteTestFile(t, filepath.Join(home, dshCredentialsFileName), dshCredentialsFixture)

	err := dshApply(t, providerActionUpsert, map[string]any{
		"id": "x", "api": "openai-completions", "base_url": "https://example.invalid",
		"models": []map[string]any{{"id": "m"}}, "api_key": "sk-test-xxxx",
	})
	if err == nil {
		t.Fatal("upsert must refuse to overwrite an unparseable settings.yaml")
	}
	if !strings.Contains(err.Error(), dshSettingsFileName) {
		t.Errorf("error does not name the file: %v", err)
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)); got != corrupt {
		t.Errorf("settings.yaml was rewritten:\n%s", got)
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshCredentialsFileName)); got != dshCredentialsFixture {
		t.Errorf("credentials were written despite the refusal:\n%s", got)
	}
	if backups := dshBackupNames(t, home, dshSettingsFileName); len(backups) != 0 {
		t.Errorf("a refused write left backups behind: %v", backups)
	}
}

func TestDshProviderWriteRefusesCorruptCredentials(t *testing.T) {
	home := dshTestHome(t)
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), dshSettingsFixture)
	const corrupt = "refs: {a: b\n"
	dshWriteTestFile(t, filepath.Join(home, dshCredentialsFileName), corrupt)

	err := dshApply(t, providerActionUpsert, map[string]any{
		"id": "x", "api": "openai-completions", "base_url": "https://example.invalid",
		"models": []map[string]any{{"id": "m"}}, "api_key": "sk-test-xxxx",
	})
	if err == nil {
		t.Fatal("upsert must refuse to overwrite an unparseable .credentials.yaml")
	}
	if !strings.Contains(err.Error(), dshCredentialsFileName) {
		t.Errorf("error does not name the file: %v", err)
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)); got != dshSettingsFixture {
		t.Errorf("settings.yaml was written despite the refusal:\n%s", got)
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshCredentialsFileName)); got != corrupt {
		t.Errorf("credentials were rewritten:\n%s", got)
	}
}

func TestDshProviderActivateUnknownProviderWritesNothing(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)
	before := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))

	err := dshApply(t, providerActionActivate, map[string]any{"id": "nope"})
	if err == nil {
		t.Fatal("activating an unconfigured provider must fail")
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)); got != before {
		t.Errorf("file written for a failed activation:\n%s", got)
	}
	if backups := dshBackupNames(t, home, dshSettingsFileName); len(backups) != 0 {
		t.Errorf("a failed activation left backups behind: %v", backups)
	}
}

func TestDshProviderActivateWithoutModelsWritesNothing(t *testing.T) {
	home := dshTestHome(t)
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), `llm-pi-ai:
  providers:
    empty:
      api: openai-completions
`)
	before := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))

	if err := dshApply(t, providerActionActivate, map[string]any{"id": "empty"}); err == nil {
		t.Fatal("activating a preset with no models must fail")
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)); got != before {
		t.Errorf("file written for a failed activation:\n%s", got)
	}
}

func TestDshProviderActivateDefaultsToFirstModel(t *testing.T) {
	dshTestHome(t)

	snapshot := dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "p1",
		"api":      "openai-completions",
		"base_url": "https://example.invalid/v1",
		"models":   []map[string]any{{"id": "m-first"}, {"id": "m-second"}},
		"api_key":  "sk-test-xxxx",
	})
	if snapshot.Active != nil {
		t.Fatalf("a fresh preset must not be active: %+v", snapshot.Active)
	}

	snapshot = dshApplyOK(t, providerActionActivate, map[string]any{"id": "p1"})
	if snapshot.Active == nil || snapshot.Active.Provider != "p1" || snapshot.Active.Model != "m-first" {
		t.Fatalf("active = %+v, want p1/m-first", snapshot.Active)
	}
	if !snapshot.Providers[0].Active {
		t.Error("the activated preset is not flagged in the refreshed list")
	}

	// An explicit model wins over the first entry.
	snapshot = dshApplyOK(t, providerActionActivate, map[string]any{"id": "p1", "model": "m-second"})
	if snapshot.Active == nil || snapshot.Active.Model != "m-second" {
		t.Fatalf("active = %+v, want m-second", snapshot.Active)
	}
}

func TestDshProviderDeleteClearsActiveAndKeepsKey(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	snapshot := dshApplyOK(t, providerActionDelete, map[string]any{"id": "command-code"})
	if !snapshot.ClearedActive {
		t.Fatal("deleting the active preset must report cleared_active")
	}
	if snapshot.Active != nil {
		t.Errorf("active survived the delete: %+v", snapshot.Active)
	}
	if len(snapshot.Providers) != 0 {
		t.Errorf("preset survived the delete: %+v", snapshot.Providers)
	}

	settings := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)))
	if _, present := settings[dshActiveModelKey]; present {
		t.Error("agent-default-model was not cleared")
	}
	// The ref belongs to the environment variable name, which another preset
	// may still use, so a delete must not take it.
	credentials := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshCredentialsFileName)))
	if got := dshMapPath(t, credentials, dshRefsKey, "COMMAND_CODE_API_KEY"); got != "sk-test-existing" {
		t.Fatalf("delete removed a shared credential: %#v", got)
	}
	if got := dshMapPath(t, credentials, "records", "keep"); got != "me" {
		t.Errorf("delete rewrote the credentials file: %#v", got)
	}
}

func TestDshProviderDeleteInactivePresetKeepsActive(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id": "other", "api": "openai-completions", "base_url": "https://other.invalid",
		"models": []map[string]any{{"id": "m"}}, "api_key": "sk-test-xxxx",
	})
	snapshot := dshApplyOK(t, providerActionDelete, map[string]any{"id": "other"})
	if snapshot.ClearedActive {
		t.Error("deleting an inactive preset must not clear the active model")
	}
	if snapshot.Active == nil || snapshot.Active.Provider != "command-code" {
		t.Fatalf("active = %+v, want command-code", snapshot.Active)
	}
}

func TestDshProviderDeleteUnknownPresetFails(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)
	before := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName))

	if err := dshApply(t, providerActionDelete, map[string]any{"id": "nope"}); err == nil {
		t.Fatal("deleting an unconfigured provider must fail")
	}
	if got := dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)); got != before {
		t.Errorf("file written for a failed delete:\n%s", got)
	}
}

func TestDshProviderBackupRotation(t *testing.T) {
	home := dshTestHome(t)
	settingsPath := filepath.Join(home, dshSettingsFileName)

	// One create plus eleven edits: eleven backups exist before the rotation
	// can drop one, and the boundary is what the test pins.
	for i := 1; i <= 12; i++ {
		dshApplyOK(t, providerActionUpsert, map[string]any{
			"id":       "p1",
			"api":      "openai-completions",
			"base_url": "http://example.test/v1-" + strconv.Itoa(i),
			"models":   []map[string]any{{"id": "m"}},
			"api_key":  "sk-test-xxxx",
		})
	}

	backups := dshBackupNames(t, home, dshSettingsFileName)
	if len(backups) != dshProviderBackupKeep {
		t.Fatalf("kept %d backups, want %d: %v", len(backups), dshProviderBackupKeep, backups)
	}

	today := time.Now().Format("20060102")
	// Nameless first, then a letter per further write in the day — DSH's own
	// sequence. Twelve writes produce eleven backups, so the rotation has
	// already taken the nameless one and the survivors start at "b".
	want := make([]string, 0, dshProviderBackupKeep)
	for i := 0; i < dshProviderBackupKeep; i++ {
		want = append(want, dshSettingsFileName+".bak-"+today+string(rune('b'+i)))
	}
	for i, name := range backups {
		if name != want[i] {
			t.Errorf("backup %d = %q, want %q", i, name, want[i])
		}
	}

	// The pruned file is the oldest state, and the newest backup is the state
	// the last write replaced.
	if strings.Contains(dshReadTestFile(t, filepath.Join(home, backups[0])), "v1-1\n") {
		t.Error("the oldest backup was kept after rotation")
	}
	if !strings.Contains(dshReadTestFile(t, filepath.Join(home, backups[0])), "v1-2\n") {
		t.Errorf("newest kept backups do not start at state 2: %s", dshReadTestFile(t, filepath.Join(home, backups[0])))
	}
	if !strings.Contains(dshReadTestFile(t, filepath.Join(home, backups[len(backups)-1])), "v1-11\n") {
		t.Error("the newest backup is not the state the last write replaced")
	}
	if !strings.Contains(dshReadTestFile(t, settingsPath), "v1-12\n") {
		t.Error("settings.yaml does not hold the last write")
	}
}

func TestDshCredentialFilesArePrivate(t *testing.T) {
	home := dshTestHome(t)
	credentialsPath := filepath.Join(home, dshCredentialsFileName)

	// A key written by a world-readable editor must land 0600, and so must the
	// backups of it.
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), dshSettingsFixture)
	dshWriteTestFile(t, credentialsPath, dshCredentialsFixture)
	if err := os.Chmod(credentialsPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	for i := 0; i < 2; i++ {
		dshApplyOK(t, providerActionUpsert, map[string]any{
			"id": "p1", "api": "openai-completions", "base_url": "https://example.invalid",
			"models": []map[string]any{{"id": "m"}}, "api_key": "sk-test-xxxx",
		})
	}

	assertMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", filepath.Base(path), got, want)
		}
	}
	assertMode(credentialsPath, dshCredentialsFileMode)
	backups := dshBackupNames(t, home, dshCredentialsFileName)
	if len(backups) == 0 {
		t.Fatal("no credentials backup was written")
	}
	for _, name := range backups {
		assertMode(filepath.Join(home, name), dshCredentialsFileMode)
	}
}

func TestDshProviderRejectsUnsupportedProviderAndAction(t *testing.T) {
	dshTestHome(t)

	if _, err := applyProviderConfig(context.Background(), "claude", providerActionList, nil); err == nil {
		t.Error("an unimplemented provider must be reported as unsupported")
	} else if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("claude: %v", err)
	}
	if _, err := applyProviderConfig(context.Background(), "agy", providerActionList, nil); err == nil {
		t.Error("agy must be reported as unsupported until it has a driver")
	}
	if err := dshApply(t, "explode", map[string]any{"id": "p1"}); err == nil {
		t.Error("an unknown action must be rejected")
	}
	if _, err := applyProviderConfig(context.Background(), "dsh", providerActionList, nil); err != nil {
		t.Errorf("list on an empty DSH home must succeed: %v", err)
	}
}

func TestDshProviderListOnEmptyHome(t *testing.T) {
	dshTestHome(t)

	snapshot := dshApplyOK(t, providerActionList, nil)
	if snapshot.Providers == nil || len(snapshot.Providers) != 0 {
		t.Fatalf("providers = %#v, want an empty list", snapshot.Providers)
	}
	if snapshot.Active != nil {
		t.Fatalf("active = %+v, want nil", snapshot.Active)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// `null` would make every client special-case the empty list.
	if !strings.Contains(string(encoded), `"providers":[]`) {
		t.Errorf("empty provider list is not an array: %s", encoded)
	}
}

func TestDshProviderUpsertDropsRemovedModelAndKeepsItsSibling(t *testing.T) {
	home := dshTestHome(t)
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), `llm-pi-ai:
  providers:
    p1:
      api: openai-completions
      models:
        - id: keep
          reasoningEfforts:
            high: high
        - id: drop
`)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id": "p1", "api": "openai-completions", "base_url": "https://example.invalid",
		"models":  []map[string]any{{"id": "keep", "name": "Keep", "context_window": 1000}},
		"api_key": "sk-test-xxxx",
	})

	settings := dshYAMLMap(t, dshReadTestFile(t, filepath.Join(home, dshSettingsFileName)))
	models, ok := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "p1", "models").([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v", models)
	}
	entry := models[0].(map[string]any)
	if entry["id"] != "keep" || entry["name"] != "Keep" {
		t.Errorf("kept model not updated: %#v", entry)
	}
	if _, present := entry["reasoningEfforts"]; !present {
		t.Errorf("kept model lost its unknown keys: %#v", entry)
	}
}

// itoa keeps call sites short.
func itoa(value int) string {
	return strconv.Itoa(value)
}

// TestHandleProviderConfigReportsFailureForUnsupportedProvider pins that an
// action the daemon cannot perform comes back as a report rather than silence:
// a request answered by nothing would sit "running" on the server until its
// timeout, which reads to the user as a hang.
func TestHandleProviderConfigReportsFailureForUnsupportedProvider(t *testing.T) {
	withFastLocalSkillReportBackoffs(t)

	var body map[string]any
	d, _ := localSkillReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	d.handleProviderConfig(context.Background(), Runtime{ID: "rt-1"}, PendingProviderConfig{
		ID:       "req-1",
		Provider: "claude",
		Action:   providerActionUpsert,
		Payload:  json.RawMessage(`{"id":"p1","api_key":"sk-test-xxxx"}`),
	})

	if body["status"] != "failed" {
		t.Fatalf("report = %#v", body)
	}
	// The refused request's payload carries a key the daemon must not echo.
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-xxxx") {
		t.Fatalf("the failure report echoed the key: %s", encoded)
	}
}

// TestHandleProviderConfigReportsRefreshedList pins that a successful action
// answers with the whole refreshed list, so a client needs one round trip.
func TestHandleProviderConfigReportsRefreshedList(t *testing.T) {
	withFastLocalSkillReportBackoffs(t)
	dshTestHome(t)
	dshInstalledGateway.serve("m")

	var body map[string]any
	d, _ := localSkillReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	d.handleProviderConfig(context.Background(), Runtime{ID: "rt-1"}, PendingProviderConfig{
		ID:       "req-1",
		Provider: "dsh",
		Action:   providerActionUpsert,
		Payload: dshJSON(t, map[string]any{
			"id": "p1", "api": "openai-completions", "base_url": "https://example.invalid",
			"models": []map[string]any{{"id": "m"}}, "api_key": "sk-test-xxxx",
		}),
	})

	if body["status"] != "completed" {
		t.Fatalf("report = %#v", body)
	}
	providers, ok := body["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("providers = %#v", body["providers"])
	}
	if body["active"] != nil {
		t.Errorf("a fresh preset must not report an active model: %#v", body["active"])
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-xxxx") {
		t.Fatalf("the report echoed the key: %s", encoded)
	}
}

func TestReportProviderConfigResultSendsCorrectPath(t *testing.T) {
	withFastLocalSkillReportBackoffs(t)

	var path string
	d, _ := localSkillReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	d.reportProviderConfigResult(context.Background(), Runtime{ID: "rt-a"}, "req-1", map[string]any{"status": "completed"})

	if !strings.HasSuffix(path, "/api/daemon/runtimes/rt-a/provider-presets/req-1/result") {
		t.Fatalf("provider preset report path = %q", path)
	}
}

// dshBackupNames returns the backup file names for one configuration file, in
// rotation order.
func dshBackupNames(t *testing.T, home, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read %s: %v", home, err)
	}
	prefix := name + ".bak-"
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			names = append(names, entry.Name())
		}
	}
	return names
}
