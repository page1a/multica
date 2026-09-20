package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DSH keeps the provider presets a user picks a default model from in two
// files under its own home:
//
//	settings.yaml       llm-pi-ai.providers.<id> — one entry per preset,
//	                    agent-default-model      — the pair that is active
//	.credentials.yaml   refs[<apiKeyEnv>]        — the key each entry names
//
// so "configure a provider" is a write to the user's own DSH installation, and
// this file is the only place that does it.
//
// Everything round-trips through yaml.Node rather than a typed struct. The
// same files hold ui-chat, permission, llm-deepseek, per-provider compat and
// per-model reasoningEfforts — keys this feature has no opinion about — and a
// struct that only knows the fields it cares about deletes all of them the
// moment it marshals. Since a lost key is a silently changed agent
// configuration, the read-modify-write has to preserve what it does not
// understand.
const (
	dshSettingsFileName    = "settings.yaml"
	dshCredentialsFileName = ".credentials.yaml"

	dshActiveModelKey  = "agent-default-model"
	dshProviderRootKey = "llm-pi-ai"
	dshProvidersKey    = "providers"
	dshRefsKey         = "refs"

	// dshProviderBackupKeep is the backup rotation depth. DSH writes its own
	// backups beside the file; matching a short window keeps an edit session
	// undoable without letting the directory grow without bound.
	dshProviderBackupKeep = 10

	// dshMaskedKeyValue is what the daemon reports for a key too short to mask
	// partially — reporting any part of it would be most of the key.
	dshMaskedKeyValue = "••••"
)

// Provider-preset actions. The wire values are the daemon's action names and
// the server's request actions; both sides spell them the same way.
const (
	providerActionList     = "list"
	providerActionModels   = "models"
	providerActionUpsert   = "upsert"
	providerActionDelete   = "delete"
	providerActionActivate = "activate"
)

// providerPresetModel is one entry of a preset's model list, as reported to the
// server. ContextWindow is informational; DSH reads it from the preset.
type providerPresetModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// providerPresetEntry is one provider preset. It deliberately has no field for
// the key itself — only whether one exists and a mask short enough to identify
// it in a list.
type providerPresetEntry struct {
	ID        string                `json:"id"`
	API       string                `json:"api,omitempty"`
	BaseURL   string                `json:"base_url,omitempty"`
	APIKeyEnv string                `json:"api_key_env,omitempty"`
	KeyMask   string                `json:"key_mask,omitempty"`
	HasKey    bool                  `json:"has_key"`
	Active    bool                  `json:"active,omitempty"`
	Models    []providerPresetModel `json:"models"`
}

// providerPresetActive is the agent-default-model pair.
type providerPresetActive struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// providerConfigSnapshot is the reply to every action: the refreshed preset
// list plus what is active now, so a client can redraw its list from one round
// trip instead of following up with a separate list.
type providerConfigSnapshot struct {
	Providers     []providerPresetEntry `json:"providers"`
	Active        *providerPresetActive `json:"active,omitempty"`
	ClearedActive bool                  `json:"cleared_active,omitempty"`
	// Models is what the endpoint says it serves, filled only by the models
	// action. The provider's ids travel verbatim — escaping them for Multica's
	// provider/model string is the caller's concern, not this one's.
	Models []providerPresetModel `json:"models,omitempty"`
}

// dshProviderUpsertPayload is the upsert body. APIKey is write-only: it is
// accepted, written to the credentials file and never echoed back.
type dshProviderUpsertPayload struct {
	ID        string                `json:"id"`
	API       string                `json:"api"`
	BaseURL   string                `json:"base_url"`
	APIKeyEnv string                `json:"api_key_env"`
	Models    []providerPresetModel `json:"models"`
	APIKey    string                `json:"api_key"`
	// VerifyModel names the model the save-time health check runs against.
	// Empty means the preset's first model, which is also what activate picks
	// when it is not told.
	VerifyModel string `json:"verify_model"`
}

// dshProviderModelsPayload asks the endpoint for its own catalog. The key may
// be typed into the form (api_key) or already stored for an existing preset
// (id), because editing an endpoint without retyping the key is the ordinary
// path. API travels with it: it decides whether the key is sent as a Bearer
// token or as an x-api-key, and getting that wrong reads as a rejected key.
type dshProviderModelsPayload struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url"`
	API     string `json:"api"`
	APIKey  string `json:"api_key"`
}

// dshProviderIDPayload covers the actions that only name a preset.
type dshProviderIDPayload struct {
	ID string `json:"id"`
}

// dshProviderActivatePayload names the preset to activate and, optionally, the
// model; an empty model means the preset's first model.
type dshProviderActivatePayload struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// providerConfigDriver implements the preset actions for one agent CLI. The
// entry point is the provider dimension, not the action dimension, because
// "where does this provider keep its configuration" is the only thing that
// differs between them.
//
// DSH is the only implementation this change ships. agy and claude are the
// reserved extension slots: whichever lands next registers a driver here. An
// id with no driver is rejected as unsupported rather than guessed at, so an
// older daemon answers a newer server's request honestly instead of writing
// something plausible into the wrong file.
type providerConfigDriver interface {
	Apply(ctx context.Context, action string, payload json.RawMessage) (*providerConfigSnapshot, error)
}

var providerConfigDrivers = map[string]providerConfigDriver{
	"dsh": dshProviderDriver{},
}

// applyProviderConfig routes one action to its provider driver.
func applyProviderConfig(ctx context.Context, provider, action string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	driver, ok := providerConfigDrivers[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	return driver.Apply(ctx, action, payload)
}

// dshProviderDriver reads and writes DSH's own configuration files. The
// directory is resolved per call rather than held on the driver so a test (or
// a user with a relocated DSH_HOME) can point it elsewhere.
type dshProviderDriver struct{}

func (dshProviderDriver) Apply(ctx context.Context, action string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	dshHome, err := dshHomePath()
	if err != nil {
		return nil, err
	}
	switch action {
	case providerActionList:
		settings, credentials, err := loadDshProviderDocuments(dshHome)
		if err != nil {
			return nil, err
		}
		snapshot := dshSnapshot(settings, credentials)
		return &snapshot, nil
	case providerActionModels:
		return dshListProviderModels(ctx, dshHome, payload)
	case providerActionUpsert:
		return dshUpsertProvider(ctx, dshHome, payload)
	case providerActionDelete:
		return dshDeleteProvider(dshHome, payload)
	case providerActionActivate:
		return dshActivateProvider(dshHome, payload)
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

// dshHomePath resolves DSH's home exactly as the rest of the daemon does:
// DSH_HOME when set, ~/.dsh otherwise.
func dshHomePath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("DSH_HOME")); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".dsh"), nil
}

// dshDefaultAPIKeyEnv names the environment variable a preset's key lives
// under when the caller did not pick one. DSH addresses the key by env name,
// so the derived name only has to be stable and unique per preset.
func dshDefaultAPIKeyEnv(id string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(id) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return "MULTICA_" + b.String() + "_API_KEY"
}

// dshKeyMask renders a key as its first three and last four characters. The
// mask is produced here, on the machine that holds the file, so the value
// never has to travel for the UI to show something identifying.
func dshKeyMask(key string) string {
	if key == "" {
		return ""
	}
	runes := []rune(key)
	if len(runes) < 8 {
		return dshMaskedKeyValue
	}
	return string(runes[:3]) + "…" + string(runes[len(runes)-4:])
}

// ---------------------------------------------------------------------------
// YAML documents
// ---------------------------------------------------------------------------

// dshYAMLDocument is a parsed configuration file. root is always a mapping, so
// callers never deal with the document node wrapper.
type dshYAMLDocument struct {
	path   string
	root   *yaml.Node
	exists bool
}

func yamlMappingNode() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

func yamlScalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func yamlIntNode(value int64) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(value, 10)}
}

// loadDshYAMLDocument parses one file. A missing file is not an error — the
// first write creates it — but a file that exists and does not parse is, and
// the caller must not write over it: an unparseable file is the user's
// configuration, and replacing it would discard whatever they meant by it.
func loadDshYAMLDocument(path string) (*dshYAMLDocument, error) {
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return &dshYAMLDocument{path: path, root: yamlMappingNode()}, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	root := &doc
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return &dshYAMLDocument{path: path, root: yamlMappingNode(), exists: true}, nil
		}
		root = doc.Content[0]
	}
	// An empty file leaves the node zero-valued.
	if root.Kind == 0 {
		return &dshYAMLDocument{path: path, root: yamlMappingNode(), exists: true}, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: top level is not a mapping", path)
	}
	return &dshYAMLDocument{path: path, root: root, exists: true}, nil
}

func loadDshProviderDocuments(dshHome string) (*dshYAMLDocument, *dshYAMLDocument, error) {
	settings, err := loadDshYAMLDocument(filepath.Join(dshHome, dshSettingsFileName))
	if err != nil {
		return nil, nil, err
	}
	credentials, err := loadDshYAMLDocument(filepath.Join(dshHome, dshCredentialsFileName))
	if err != nil {
		return nil, nil, err
	}
	return settings, credentials, nil
}

// yamlMapValue returns the value node for key, or nil when key is absent.
func yamlMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode || key == "" {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// yamlMapMapping returns the mapping under key, creating one when absent or
// when what is there is not a mapping.
func yamlMapMapping(mapping *yaml.Node, key string) *yaml.Node {
	if child := yamlMapValue(mapping, key); child != nil && child.Kind == yaml.MappingNode {
		return child
	}
	child := yamlMappingNode()
	yamlMapSet(mapping, key, child)
	return child
}

// yamlMapSet writes key=value, replacing an existing value in place so the
// file keeps the order the user already had.
func yamlMapSet(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content, yamlScalarNode(key), value)
}

func yamlMapDelete(mapping *yaml.Node, key string) bool {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return true
		}
	}
	return false
}

func yamlScalarValue(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func yamlSequenceItems(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

// dshSnapshot renders what the server sees: every preset, the mask of the key
// it references, and which preset is active. It never reads the key value out
// of the document beyond masking it.
func dshSnapshot(settings, credentials *dshYAMLDocument) providerConfigSnapshot {
	snapshot := providerConfigSnapshot{Providers: []providerPresetEntry{}}

	activeMapping := yamlMapValue(settings.root, dshActiveModelKey)
	activeProvider := yamlScalarValue(yamlMapValue(activeMapping, "provider"))
	activeModel := yamlScalarValue(yamlMapValue(activeMapping, "model"))
	if activeProvider != "" || activeModel != "" {
		snapshot.Active = &providerPresetActive{Provider: activeProvider, Model: activeModel}
	}

	refs := yamlMapValue(credentials.root, dshRefsKey)
	providers := yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey)
	if providers == nil || providers.Kind != yaml.MappingNode {
		return snapshot
	}
	for i := 0; i+1 < len(providers.Content); i += 2 {
		id := providers.Content[i].Value
		entry := providers.Content[i+1]
		if id == "" {
			continue
		}
		preset := providerPresetEntry{
			ID:        id,
			API:       yamlScalarValue(yamlMapValue(entry, "api")),
			BaseURL:   yamlScalarValue(yamlMapValue(entry, "baseURL")),
			APIKeyEnv: yamlScalarValue(yamlMapValue(entry, "apiKeyEnv")),
			Models:    dshPresetModels(entry),
			Active:    id == activeProvider,
		}
		if preset.Models == nil {
			preset.Models = []providerPresetModel{}
		}
		if preset.APIKeyEnv != "" {
			key := yamlScalarValue(yamlMapValue(refs, preset.APIKeyEnv))
			preset.HasKey = key != ""
			preset.KeyMask = dshKeyMask(key)
		}
		snapshot.Providers = append(snapshot.Providers, preset)
	}
	return snapshot
}

func dshPresetModels(entry *yaml.Node) []providerPresetModel {
	items := yamlSequenceItems(yamlMapValue(entry, "models"))
	models := make([]providerPresetModel, 0, len(items))
	for _, item := range items {
		id := yamlScalarValue(yamlMapValue(item, "id"))
		if id == "" {
			continue
		}
		model := providerPresetModel{ID: id, Name: yamlScalarValue(yamlMapValue(item, "name"))}
		if window := yamlScalarValue(yamlMapValue(item, "contextWindow")); window != "" {
			if parsed, err := strconv.ParseInt(strings.TrimSpace(window), 10, 64); err == nil {
				model.ContextWindow = parsed
			}
		}
		models = append(models, model)
	}
	return models
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// dshListProviderModels asks the endpoint what it serves. It writes nothing:
// the answer is candidate metadata the caller may adopt, and settings.yaml
// stays the only thing that decides what a route carries.
func dshListProviderModels(ctx context.Context, dshHome string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	var input dshProviderModelsPayload
	if err := decodeDshProviderPayload(payload, &input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	baseURL := strings.TrimSpace(input.BaseURL)
	apiKey := strings.TrimSpace(input.APIKey)
	api := strings.TrimSpace(input.API)

	settings, credentials, err := loadDshProviderDocuments(dshHome)
	if err != nil {
		return nil, err
	}
	// Editing an endpoint without retyping the credential is the ordinary
	// path, so a blank key falls back to the one the named preset already
	// references — never to some other preset's key. The protocol falls back
	// the same way, because it decides which header carries that key.
	if id != "" && (baseURL == "" || apiKey == "" || api == "") {
		entry := yamlMapValue(yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey), id)
		if entry == nil || entry.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("provider %q is not configured", id)
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(yamlScalarValue(yamlMapValue(entry, "baseURL")))
		}
		if apiKey == "" {
			apiKey = dshStoredAPIKey(credentials, yamlScalarValue(yamlMapValue(entry, "apiKeyEnv")))
		}
		if api == "" {
			api = strings.TrimSpace(yamlScalarValue(yamlMapValue(entry, "api")))
		}
	}
	if baseURL == "" {
		return nil, errors.New("base_url is required to fetch a model list")
	}
	if apiKey == "" {
		return nil, providerFailure(providerProbeKindMissingCredential,
			"No API key is available to fetch a model list. Enter the key and try again.", nil)
	}

	discovered, err := fetchProviderModels(ctx, baseURL, api, apiKey)
	if err != nil {
		return nil, err
	}
	snapshot := dshSnapshot(settings, credentials)
	snapshot.Models = make([]providerPresetModel, 0, len(discovered))
	for _, model := range discovered {
		snapshot.Models = append(snapshot.Models, providerPresetModel{
			ID:            model.ID,
			Name:          model.Name,
			ContextWindow: model.ContextWindow,
		})
	}
	return &snapshot, nil
}

// dshStoredAPIKey reads the credential a preset's apiKeyEnv reference points
// at. It is the one place a key is read back out of the file, and it exists so
// the health check can use the credential the save would leave behind.
func dshStoredAPIKey(credentials *dshYAMLDocument, apiKeyEnv string) string {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" {
		return ""
	}
	return strings.TrimSpace(yamlScalarValue(yamlMapValue(yamlMapValue(credentials.root, dshRefsKey), apiKeyEnv)))
}

// dshUpsertProvider writes one preset, but only after the route has answered
// both probes. A save that cannot be verified does not land: "the form said it
// was fine" is the failure this action exists to remove, and a preset that
// only fails at the next agent run fails hours later on a machine nobody is
// watching.
func dshUpsertProvider(ctx context.Context, dshHome string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	var input dshProviderUpsertPayload
	if err := decodeDshProviderPayload(payload, &input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return nil, errors.New("provider id is required")
	}
	if len(input.Models) == 0 {
		return nil, errors.New("at least one model is required")
	}
	for i, model := range input.Models {
		if strings.TrimSpace(model.ID) == "" {
			return nil, fmt.Errorf("model %d has no id", i+1)
		}
	}

	// Both files are parsed before either is written, so a corrupt file is
	// refused without a partial edit landing beside it — and, since the health
	// check follows, without a network round trip either.
	settings, credentials, err := loadDshProviderDocuments(dshHome)
	if err != nil {
		return nil, err
	}

	providers := yamlMapMapping(yamlMapMapping(settings.root, dshProviderRootKey), dshProvidersKey)
	entry := yamlMapMapping(providers, id)
	// A blank api_key_env names no change: reusing the reference the preset
	// already has is what keeps "edit the endpoint, leave the credential
	// alone" from silently re-pointing the preset at a different variable.
	apiKeyEnv := strings.TrimSpace(input.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = strings.TrimSpace(yamlScalarValue(yamlMapValue(entry, "apiKeyEnv")))
	}
	if apiKeyEnv == "" {
		apiKeyEnv = dshDefaultAPIKeyEnv(id)
	}

	apiKey := strings.TrimSpace(input.APIKey)
	if apiKey == "" {
		apiKey = dshStoredAPIKey(credentials, apiKeyEnv)
	}
	if apiKey == "" {
		return nil, providerFailure(providerProbeKindMissingCredential,
			"No API key is available to verify this provider. Enter the key and save again.", nil)
	}

	verifyModel := strings.TrimSpace(input.VerifyModel)
	if verifyModel == "" {
		verifyModel = strings.TrimSpace(input.Models[0].ID)
	}
	modelIDs := make([]string, 0, len(input.Models))
	for _, model := range input.Models {
		modelIDs = append(modelIDs, strings.TrimSpace(model.ID))
	}
	route, err := dshVerifyProviderRoute(ctx, dshVerifyRequest{
		BaseURL:  strings.TrimSpace(input.BaseURL),
		APIKey:   apiKey,
		ModelID:  verifyModel,
		ModelIDs: modelIDs,
		API:      input.API,
	})
	if err != nil {
		return nil, err
	}

	yamlMapSet(entry, "apiKeyEnv", yamlScalarNode(apiKeyEnv))
	// The protocol is the one the endpoint's own answer implies; the caller's
	// choice stands only for a gateway that does not describe its endpoints.
	yamlMapSet(entry, "api", yamlScalarNode(route.API))
	yamlMapSet(entry, "baseURL", yamlScalarNode(input.BaseURL))
	// Set, never delete: a compat switch the user set by hand is not this
	// feature's to remove, and a family that needs one gets it without the
	// user knowing the key exists.
	if route.ThinkingFormat != "" {
		yamlMapSet(yamlMapMapping(entry, "compat"), "thinkingFormat", yamlScalarNode(route.ThinkingFormat))
	}
	yamlMapSet(entry, "models", dshModelsNode(input.Models, yamlMapValue(entry, "models")))

	// The key is written first: a credentials write that fails leaves settings
	// untouched, while the reverse order could publish a preset whose key
	// never landed.
	if strings.TrimSpace(input.APIKey) != "" {
		if !credentials.exists {
			// Match the versioned shape DSH itself writes, so a file this
			// feature created is not one DSH has to migrate.
			yamlMapSet(credentials.root, "version", yamlIntNode(1))
		}
		yamlMapSet(yamlMapMapping(credentials.root, dshRefsKey), apiKeyEnv, yamlScalarNode(input.APIKey))
		if err := writeDshYAMLFile(credentials.path, credentials, dshCredentialsFileMode); err != nil {
			return nil, err
		}
	}

	if err := writeDshYAMLFile(settings.path, settings, dshExistingFileMode(settings.path, 0o600)); err != nil {
		return nil, err
	}

	snapshot := dshSnapshot(settings, credentials)
	return &snapshot, nil
}

// dshModelsNode rebuilds a preset's model list from the request. An entry that
// already exists is edited in place, which is what keeps the per-model keys
// this feature does not model — reasoningEfforts — attached to their model.
// Ids the request dropped are dropped from the file.
func dshModelsNode(models []providerPresetModel, existing *yaml.Node) *yaml.Node {
	byID := map[string]*yaml.Node{}
	for _, item := range yamlSequenceItems(existing) {
		if id := yamlScalarValue(yamlMapValue(item, "id")); id != "" {
			byID[id] = item
		}
	}
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, model := range models {
		node.Content = append(node.Content, dshModelNode(model, byID[model.ID]))
	}
	return node
}

func dshModelNode(model providerPresetModel, existing *yaml.Node) *yaml.Node {
	entry := existing
	if entry == nil || entry.Kind != yaml.MappingNode {
		entry = yamlMappingNode()
	}
	yamlMapSet(entry, "id", yamlScalarNode(model.ID))
	if model.Name == "" {
		yamlMapDelete(entry, "name")
	} else {
		yamlMapSet(entry, "name", yamlScalarNode(model.Name))
	}
	if model.ContextWindow <= 0 {
		yamlMapDelete(entry, "contextWindow")
	} else {
		yamlMapSet(entry, "contextWindow", yamlIntNode(model.ContextWindow))
	}
	return entry
}

func dshDeleteProvider(dshHome string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	var input dshProviderIDPayload
	if err := decodeDshProviderPayload(payload, &input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return nil, errors.New("provider id is required")
	}

	settings, credentials, err := loadDshProviderDocuments(dshHome)
	if err != nil {
		return nil, err
	}
	providers := yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey)
	if yamlMapValue(providers, id) == nil {
		return nil, fmt.Errorf("provider %q is not configured", id)
	}
	yamlMapDelete(providers, id)

	// refs is deliberately left alone: the variable name is not owned by the
	// preset, and another provider may reference the same one.
	clearedActive := false
	if active := yamlMapValue(settings.root, dshActiveModelKey); active != nil {
		if yamlScalarValue(yamlMapValue(active, "provider")) == id {
			yamlMapDelete(settings.root, dshActiveModelKey)
			clearedActive = true
		}
	}

	if err := writeDshYAMLFile(settings.path, settings, dshExistingFileMode(settings.path, 0o600)); err != nil {
		return nil, err
	}
	snapshot := dshSnapshot(settings, credentials)
	snapshot.ClearedActive = clearedActive
	return &snapshot, nil
}

func dshActivateProvider(dshHome string, payload json.RawMessage) (*providerConfigSnapshot, error) {
	var input dshProviderActivatePayload
	if err := decodeDshProviderPayload(payload, &input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return nil, errors.New("provider id is required")
	}

	settings, credentials, err := loadDshProviderDocuments(dshHome)
	if err != nil {
		return nil, err
	}
	entry := yamlMapValue(yamlMapValue(yamlMapValue(settings.root, dshProviderRootKey), dshProvidersKey), id)
	if entry == nil || entry.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("provider %q is not configured", id)
	}

	model := strings.TrimSpace(input.Model)
	if model == "" {
		for _, item := range yamlSequenceItems(yamlMapValue(entry, "models")) {
			if candidate := yamlScalarValue(yamlMapValue(item, "id")); candidate != "" {
				model = candidate
				break
			}
		}
	}
	if model == "" {
		return nil, fmt.Errorf("provider %q has no model to activate", id)
	}

	active := yamlMapMapping(settings.root, dshActiveModelKey)
	yamlMapSet(active, "provider", yamlScalarNode(id))
	yamlMapSet(active, "model", yamlScalarNode(model))

	if err := writeDshYAMLFile(settings.path, settings, dshExistingFileMode(settings.path, 0o600)); err != nil {
		return nil, err
	}
	snapshot := dshSnapshot(settings, credentials)
	return &snapshot, nil
}

// decodeDshProviderPayload decodes an action body. The error never carries the
// body — a failed upsert body may hold the API key.
func decodeDshProviderPayload(payload json.RawMessage, target any) error {
	if len(payload) == 0 {
		return errors.New("missing payload")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// File writes
// ---------------------------------------------------------------------------

// dshCredentialsFileMode is unconditional for the credentials file and its
// backups: a key is the one secret in these files, so a write tightens a file
// an editor created world-readable instead of preserving that.
const dshCredentialsFileMode os.FileMode = 0o600

// dshExistingFileMode keeps an existing file's permissions and uses fallback
// for a file being created.
func dshExistingFileMode(path string, fallback os.FileMode) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
}

// writeDshYAMLFile replaces one configuration file: back the current file up
// beside itself, then write through a same-directory temp file and rename.
//
// The rename is what makes the edit atomic — a reader either sees the whole old
// file or the whole new one, never a truncated one. A cross-directory temp file
// would not give that, because rename is only atomic within a filesystem.
func writeDshYAMLFile(path string, doc *dshYAMLDocument, mode os.FileMode) error {
	// Marshal before touching the file, so a serialization failure cannot
	// leave a backup with no replacement behind it.
	data, err := yaml.Marshal(doc.root)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	if _, err := os.Stat(path); err == nil {
		if err := backupDshFile(path, mode); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tempName := temp.Name()
	defer func() {
		if tempName != "" {
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temp file for %s: %w", path, err)
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

// dshBackupStamp matches DSH's own backup naming: the date, then a letter once
// the day already has a backup. Because the stamp sorts lexicographically the
// same way it sorts in time, the rotation below can use it as the order.
var dshBackupStamp = regexp.MustCompile(`^\d{8}[a-z]?$`)

// backupDshFile copies the current file to the next free backup name beside it
// and prunes the rotation. Reusing DSH's naming matters: a user who opens the
// directory to undo an edit should not have to guess which writer made which
// file.
func backupDshFile(path string, mode os.FileMode) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s for backup: %w", path, err)
	}

	target, err := dshNextBackupPath(path)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create backup %s: %w", target, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(target)
		return fmt.Errorf("write backup %s: %w", target, err)
	}
	// O_CREATE applies the mode through the process umask, which is exactly
	// what must not happen to a credentials backup.
	if err := file.Chmod(mode); err != nil {
		file.Close()
		_ = os.Remove(target)
		return fmt.Errorf("chmod backup %s: %w", target, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("close backup %s: %w", target, err)
	}
	return pruneDshBackups(path)
}

func dshNextBackupPath(path string) (string, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path) + ".bak-" + time.Now().Format("20060102")
	candidates := make([]string, 0, 26)
	candidates = append(candidates, base)
	for letter := 'b'; letter <= 'z'; letter++ {
		candidates = append(candidates, fmt.Sprintf("%s%c", base, letter))
	}
	for _, candidate := range candidates {
		full := filepath.Join(dir, candidate)
		if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
			return full, nil
		}
	}
	return "", fmt.Errorf("no free backup name for %s", path)
}

func pruneDshBackups(path string) error {
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + ".bak-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list backups of %s: %w", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if !dshBackupStamp.MatchString(strings.TrimPrefix(entry.Name(), prefix)) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for len(names) > dshProviderBackupKeep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return fmt.Errorf("prune backup %s: %w", names[0], err)
		}
		names = names[1:]
	}
	return nil
}
