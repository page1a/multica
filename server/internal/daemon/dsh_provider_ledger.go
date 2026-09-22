package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Multica's own record of the provider presets it wrote into DSH.
//
// settings.yaml is DSH's file, not ours: a DSH upgrade, a reinstall, or a user
// resetting the file takes the provider section with it. Nothing else in
// Multica remembers what was there — the server deliberately keeps no durable
// copy of a preset, because the upsert payload carries the API key and that key
// has exactly one destination (see runtime_provider_presets.go). So when the
// provider section disappears, a seat that is still configured for
// `command-code/deepseek%2F…` fails at the next run with "model is not
// supported on this endpoint", hours later, on a machine nobody is watching.
//
// This ledger is the replay source for that case. It lives beside the daemon's
// own state rather than inside the DSH home, so resetting DSH's configuration
// does not reset the record of what belongs in it, and it holds every field the
// upsert writes EXCEPT the key:
//
//	settings.yaml  llm-pi-ai.providers.<id>  → recorded here, replayable
//	.credentials.yaml  refs[<apiKeyEnv>]     → never recorded, never replayed
//
// A replay therefore restores the shape and reuses whatever credential is still
// in DSH's credentials file. When that file went too, the restored preset comes
// back with `has_key: false`, which is the honest answer: the key is the one
// thing only the user can supply again.
const (
	dshLedgerFileName = "dsh-provider-presets.yaml"
	dshLedgerVersion  = 1
	dshLedgerFileMode = os.FileMode(0o600)
)

// dshLedgerModel is one recorded model. It mirrors the fields dshModelNode
// writes; per-model keys this feature does not model (reasoningEfforts) are
// DSH's and are not ours to restore.
type dshLedgerModel struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name,omitempty"`
	ContextWindow int64  `yaml:"contextWindow,omitempty"`
}

// dshLedgerPreset is one recorded preset. ThinkingFormat is stored flat rather
// than under a nested compat map because it is the only compat key this feature
// owns — and it is the one whose loss is silent: without it a DeepSeek route
// answers with an empty message and no error anywhere.
type dshLedgerPreset struct {
	API            string           `yaml:"api,omitempty"`
	BaseURL        string           `yaml:"baseURL,omitempty"`
	APIKeyEnv      string           `yaml:"apiKeyEnv,omitempty"`
	ThinkingFormat string           `yaml:"thinkingFormat,omitempty"`
	Models         []dshLedgerModel `yaml:"models,omitempty"`
}

// dshLedgerHome is everything recorded for one DSH home. The ledger is keyed by
// home path because DSH_HOME is per-profile here (see dsh_profile.go): two
// homes hold two unrelated sets of presets, and replaying one into the other
// would write a preset the user never configured there.
type dshLedgerHome struct {
	Active    *providerPresetActive      `yaml:"active,omitempty"`
	Providers map[string]dshLedgerPreset `yaml:"providers,omitempty"`
}

type dshLedger struct {
	Version int                       `yaml:"version"`
	Homes   map[string]*dshLedgerHome `yaml:"homes,omitempty"`
}

// dshLedgerPathFn resolves the ledger file. It is a variable so a test writes
// into its own temp directory instead of the developer's real ~/.multica.
var dshLedgerPathFn = defaultDshLedgerPath

func defaultDshLedgerPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".multica", dshLedgerFileName), nil
}

// loadDshLedger reads the ledger. A missing file is an empty ledger — the first
// save creates it — but a file that exists and does not parse is an error: it
// is a record of the user's configuration, and overwriting it would discard
// whatever is left of that record.
func loadDshLedger() (*dshLedger, string, error) {
	path, err := dshLedgerPathFn()
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &dshLedger{Version: dshLedgerVersion, Homes: map[string]*dshLedgerHome{}}, path, nil
	case err != nil:
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	ledger := &dshLedger{}
	if err := yaml.Unmarshal(raw, ledger); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if ledger.Homes == nil {
		ledger.Homes = map[string]*dshLedgerHome{}
	}
	ledger.Version = dshLedgerVersion
	return ledger, path, nil
}

func saveDshLedger(ledger *dshLedger, path string) error {
	data, err := yaml.Marshal(ledger)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	// Same atomic replace as the DSH files: a reader sees the whole old ledger
	// or the whole new one, never a truncated one.
	temp, err := os.CreateTemp(filepath.Dir(path), "."+dshLedgerFileName+".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(dshLedgerFileMode); err != nil {
		temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	tempName = ""
	return nil
}

func (l *dshLedger) home(dshHome string) *dshLedgerHome {
	entry := l.Homes[dshHome]
	if entry == nil {
		entry = &dshLedgerHome{}
		l.Homes[dshHome] = entry
	}
	if entry.Providers == nil {
		entry.Providers = map[string]dshLedgerPreset{}
	}
	return entry
}

// recordDshLedgerPreset records one saved preset.
//
// Called BEFORE settings.yaml is written, for the same reason the credentials
// file is: a failure here leaves DSH's own files untouched and the user retries
// a save that has not half-landed. The reverse case — a ledger entry whose
// settings write then failed — is precisely what replay exists to repair, so it
// costs nothing.
func recordDshLedgerPreset(dshHome, id string, preset dshLedgerPreset) error {
	ledger, path, err := loadDshLedger()
	if err != nil {
		return err
	}
	ledger.home(dshHome).Providers[id] = preset
	return saveDshLedger(ledger, path)
}

// recordDshLedgerActive records the agent-default-model pair, so a replay can
// put back the default model the user picked and not only the presets it came
// from.
func recordDshLedgerActive(dshHome, provider, model string) error {
	ledger, path, err := loadDshLedger()
	if err != nil {
		return err
	}
	ledger.home(dshHome).Active = &providerPresetActive{Provider: provider, Model: model}
	return saveDshLedger(ledger, path)
}

// forgetDshLedgerPreset drops a deleted preset. Without it a replay would
// resurrect the preset the user just removed, which is the one outcome that
// would make replay worse than nothing.
func forgetDshLedgerPreset(dshHome, id string) error {
	ledger, path, err := loadDshLedger()
	if err != nil {
		return err
	}
	entry := ledger.home(dshHome)
	delete(entry.Providers, id)
	if entry.Active != nil && entry.Active.Provider == id {
		entry.Active = nil
	}
	return saveDshLedger(ledger, path)
}

// dshLedgerPresetFromEntry reads back what was just written into settings.yaml,
// rather than recording what the request asked for. The two differ in the cases
// that matter: `api` is the protocol the gateway's own answer implied, and
// `compat.thinkingFormat` may be a value the user set by hand that the upsert
// deliberately left alone.
func dshLedgerPresetFromEntry(entry *yaml.Node) dshLedgerPreset {
	preset := dshLedgerPreset{
		API:            yamlScalarValue(yamlMapValue(entry, "api")),
		BaseURL:        yamlScalarValue(yamlMapValue(entry, "baseURL")),
		APIKeyEnv:      yamlScalarValue(yamlMapValue(entry, "apiKeyEnv")),
		ThinkingFormat: yamlScalarValue(yamlMapValue(yamlMapValue(entry, "compat"), "thinkingFormat")),
	}
	for _, model := range dshPresetModels(entry) {
		preset.Models = append(preset.Models, dshLedgerModel{
			ID:            model.ID,
			Name:          model.Name,
			ContextWindow: model.ContextWindow,
		})
	}
	return preset
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// dshReplayProviders writes the ledger back into a DSH installation whose
// provider configuration was reset, upgraded away or never received the write.
//
// It fills gaps and never overwrites. A preset the file still has keeps every
// value it has — a baseURL the user retyped by hand is theirs, not a stale copy
// to be corrected — and only the keys that are actually absent are restored.
// That is what makes replay safe to run at any time, including against a file
// that never lost anything: the second run is a no-op.
//
// It runs no health check. The route was verified when it was saved, the key
// may no longer be on this machine, and a probe would turn "put the
// configuration back" into a network operation that fails for reasons that have
// nothing to do with the file. A restored preset with no credential is reported
// as `has_key: false` in the snapshot rather than written and called fine.
func dshReplayProviders(dshHome string) (*providerConfigSnapshot, error) {
	settings, credentials, err := loadDshProviderDocuments(dshHome)
	if err != nil {
		return nil, err
	}
	// Whatever survives in the file is worth recording before it is read back:
	// a partial reset leaves presets the ledger may never have seen, and they
	// are the ones the NEXT reset would lose for good.
	if err := adoptDshLedgerPresets(dshHome, settings); err != nil {
		return nil, err
	}

	ledger, _, err := loadDshLedger()
	if err != nil {
		return nil, err
	}
	recorded := ledger.Homes[dshHome]
	if recorded == nil || len(recorded.Providers) == 0 {
		return nil, fmt.Errorf("no provider preset was ever saved for %s, so there is nothing to replay", dshHome)
	}

	ids := make([]string, 0, len(recorded.Providers))
	for id := range recorded.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	changed := false
	for _, id := range ids {
		if replayDshPreset(settings, id, recorded.Providers[id]) {
			changed = true
		}
	}
	if replayDshActive(settings, recorded.Active) {
		changed = true
	}

	if changed {
		if err := writeDshYAMLFile(settings.path, settings, dshExistingFileMode(settings.path, 0o600)); err != nil {
			return nil, err
		}
	}
	snapshot := dshSnapshot(settings, credentials)
	return &snapshot, nil
}

// replayDshPreset restores the keys one preset is missing and reports whether
// it wrote anything.
func replayDshPreset(settings *dshYAMLDocument, id string, preset dshLedgerPreset) bool {
	providers := yamlMapMapping(yamlMapMapping(settings.root, dshProviderRootKey), dshProvidersKey)
	existing := yamlMapValue(providers, id)
	fresh := existing == nil || existing.Kind != yaml.MappingNode
	entry := yamlMapMapping(providers, id)

	changed := fresh
	for key, value := range map[string]string{
		"apiKeyEnv": preset.APIKeyEnv,
		"api":       preset.API,
		"baseURL":   preset.BaseURL,
	} {
		if value == "" {
			continue
		}
		if strings.TrimSpace(yamlScalarValue(yamlMapValue(entry, key))) != "" {
			continue
		}
		yamlMapSet(entry, key, yamlScalarNode(value))
		changed = true
	}

	// The compat switch is restored whenever it is absent, including on a
	// preset that otherwise survived intact: losing this one key is invisible —
	// the route answers, the reply renders blank, and nothing reports an error.
	if preset.ThinkingFormat != "" {
		compat := yamlMapValue(entry, "compat")
		if strings.TrimSpace(yamlScalarValue(yamlMapValue(compat, "thinkingFormat"))) == "" {
			yamlMapSet(yamlMapMapping(entry, "compat"), "thinkingFormat", yamlScalarNode(preset.ThinkingFormat))
			changed = true
		}
	}

	// Models are restored only when the preset carries none. A list the user
	// has since edited is their model list, and half-merging a recorded one
	// into it would produce a set neither side chose.
	if len(preset.Models) > 0 && len(dshPresetModels(entry)) == 0 {
		models := make([]providerPresetModel, 0, len(preset.Models))
		for _, model := range preset.Models {
			models = append(models, providerPresetModel{
				ID:            model.ID,
				Name:          model.Name,
				ContextWindow: model.ContextWindow,
			})
		}
		yamlMapSet(entry, "models", dshModelsNode(models, yamlMapValue(entry, "models")))
		changed = true
	}
	return changed
}

// replayDshActive restores the default model pair when the file names none, or
// names a provider that is no longer configured. A valid pair the user set
// since is left alone.
func replayDshActive(settings *dshYAMLDocument, active *providerPresetActive) bool {
	if active == nil || active.Provider == "" || active.Model == "" {
		return false
	}
	current := yamlMapValue(settings.root, dshActiveModelKey)
	provider := strings.TrimSpace(yamlScalarValue(yamlMapValue(current, "provider")))
	model := strings.TrimSpace(yamlScalarValue(yamlMapValue(current, "model")))
	if provider != "" && model != "" {
		configured := yamlMapValue(yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey), provider)
		if configured != nil && configured.Kind == yaml.MappingNode {
			return false
		}
	}
	entry := yamlMapMapping(settings.root, dshActiveModelKey)
	yamlMapSet(entry, "provider", yamlScalarNode(active.Provider))
	yamlMapSet(entry, "model", yamlScalarNode(active.Model))
	return true
}

// ---------------------------------------------------------------------------
// Adoption
// ---------------------------------------------------------------------------

// adoptDshLedgerPresets claims presets that settings.yaml already has and the
// ledger does not.
//
// Recording only on save, activate and delete would protect nothing that exists
// today: every preset configured before this feature shipped — which on a
// working machine is all of them — has never passed through those three
// actions, so it is absent from the ledger and a replay for that home fails
// with "nothing to replay". The user would have to re-save each preset by hand,
// and no screen tells them to.
//
// So a read of the provider list writes the gap back. What is already recorded
// is left exactly as it is: the ledger is the record of what Multica wrote, and
// a settings.yaml that has since been edited must not quietly become the new
// truth. A preset the user deleted cannot return this way either — delete
// removes it from settings.yaml too, so there is nothing here to adopt.
func adoptDshLedgerPresets(dshHome string, settings *dshYAMLDocument) error {
	providers := yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey)
	activeMapping := yamlMapValue(settings.root, dshActiveModelKey)
	activeProvider := strings.TrimSpace(yamlScalarValue(yamlMapValue(activeMapping, "provider")))
	activeModel := strings.TrimSpace(yamlScalarValue(yamlMapValue(activeMapping, "model")))

	hasProviders := providers != nil && providers.Kind == yaml.MappingNode
	if !hasProviders && (activeProvider == "" || activeModel == "") {
		return nil
	}

	ledger, path, err := loadDshLedger()
	if err != nil {
		return err
	}
	// Reading the entry for a home that has none would create an empty bucket;
	// nothing to adopt should leave the file untouched, including uncreated.
	recorded := ledger.Homes[dshHome]
	changed := false

	if hasProviders {
		for i := 0; i+1 < len(providers.Content); i += 2 {
			id := providers.Content[i].Value
			entry := providers.Content[i+1]
			if id == "" || entry == nil || entry.Kind != yaml.MappingNode {
				continue
			}
			if recorded != nil {
				if _, ok := recorded.Providers[id]; ok {
					continue
				}
			}
			ledger.home(dshHome).Providers[id] = dshLedgerPresetFromEntry(entry)
			recorded = ledger.Homes[dshHome]
			changed = true
		}
	}

	if activeProvider != "" && activeModel != "" && (recorded == nil || recorded.Active == nil) {
		ledger.home(dshHome).Active = &providerPresetActive{Provider: activeProvider, Model: activeModel}
		recorded = ledger.Homes[dshHome]
		changed = true
	}

	if !changed {
		return nil
	}
	return saveDshLedger(ledger, path)
}
