package agent

import (
	"context"
	"sync"
)

// modelDiscoveryKind is how a runtime says its model list is found.
// Every runtime in SupportedTypes, and every built-in runtime identity, has
// exactly one entry in modelDiscoveryByProvider. Adding a runtime without an
// entry fails TestEveryRuntimeDeclaresModelDiscovery.
type modelDiscoveryKind int

const (
	// modelDiscoveryDedicated means this runtime has its own discoverer.
	// That discoverer is step 1 of the shared chain. A real list stops the
	// walk. A failure, an empty catalog, or a static fallback continues to
	// the endpoint and the list command declared on the same entry.
	modelDiscoveryDedicated modelDiscoveryKind = iota
	// modelDiscoveryChain means there is no dedicated discoverer. The shared
	// chain starts at the endpoint, then the readonly list command.
	modelDiscoveryChain
	// modelDiscoveryManual means the runtime cannot be given a per-task model.
	// Reason is required. The picker shows "managed by the runtime" rather than
	// an empty catalog; ModelSelectionSupported must agree. The chain does not
	// run.
	modelDiscoveryManual
)

// modelDiscoverer is step 1 of the shared chain: the runtime's own discoverer.
type modelDiscoverer func(ctx context.Context, runtimeCmd Command) (Catalog, error)

// modelDiscoveryDecl is one runtime's discovery policy. ListModels reads this
// entry and walks it. Endpoint and ListCommand are optional steps; a nil
// endpoint or an unregistered command is skipped, never invented.
type modelDiscoveryDecl struct {
	Kind        modelDiscoveryKind
	Reason      string
	Discover    modelDiscoverer
	Endpoint    modelEndpointSource
	ListCommand modelListCommand
}

// modelDiscoveryMu guards modelDiscoveryByProvider. Tests install a temporary
// entry for a provider the build does not know; production reads take the
// read lock and copy the declaration out before doing any I/O.
var modelDiscoveryMu sync.RWMutex

// modelDiscoveryByProvider is the declaration table. ListModels walks the
// entry it finds here — dedicated discoverer, then the endpoint and the
// readonly list command on that same entry. A provider this table does not
// know walks the same chain with nothing registered, so the picker hears
// that the list is temporarily unavailable instead of "unknown agent type".
// A runtime added to SupportedTypes or BuiltinRuntimes without an entry fails
// TestEveryRuntimeDeclaresModelDiscovery instead of shipping as a silent
// empty catalog.
var modelDiscoveryByProvider = map[string]modelDiscoveryDecl{
	"claude":      {Kind: modelDiscoveryDedicated, Discover: catalogDiscoverer(discoverClaudeCatalog)},
	"codebuddy":   {Kind: modelDiscoveryDedicated, Discover: discoverCodebuddyModels},
	"codex":       {Kind: modelDiscoveryDedicated, Discover: modelValuesDiscoverer(discoverCodexModels)},
	"copilot":     {Kind: modelDiscoveryDedicated, Discover: discoverCopilotModels},
	"opencode":    {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverOpenCodeModels)},
	"codearts":    {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverCodeArtsModels)},
	"deveco":      {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverDevecoModels)},
	"openclaw":    {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverOpenclawAgents)},
	"hermes":      {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverHermesModels)},
	"pi":          {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverPiModels)},
	"cursor":      {Kind: modelDiscoveryDedicated, Discover: discoverCursorModels},
	"kimi":        {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverKimiModels)},
	"reasonix":    {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverReasonixModels)},
	"dsh":         {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverDshModels)},
	"kiro":        {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverKiroModels)},
	"antigravity": {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverAntigravityModels)},
	"qoder": {Kind: modelDiscoveryDedicated, Discover: func(ctx context.Context, cmd Command) (Catalog, error) {
		return discovered(discoverQoderModels(ctx, cmd, qoderDefaultBinary("qoder")))
	}},
	"qoderclicn": {Kind: modelDiscoveryDedicated, Discover: func(ctx context.Context, cmd Command) (Catalog, error) {
		return discovered(discoverQoderModels(ctx, cmd, qoderDefaultBinary("qoderclicn")))
	}},
	"traecli": {Kind: modelDiscoveryDedicated, Discover: modelsDiscoverer(discoverTraecliModels)},
	"grok":    {Kind: modelDiscoveryDedicated, Discover: discoverGrokModels},
	"dim":     {Kind: modelDiscoveryDedicated, Discover: discoverDimModels},
	// omp and devin keep their discoverer on the builtin descriptor. ListModels
	// copies it onto the declaration before the walk, so a descriptor without
	// ModelDiscovery cannot fall through to another family's command.
	"devin":    {Kind: modelDiscoveryDedicated},
	"omp":      {Kind: modelDiscoveryDedicated},
	"qwen":     {Kind: modelDiscoveryChain, Endpoint: qwenModelEndpoint},
	"qwenpaw":  {Kind: modelDiscoveryManual, Reason: "session/set_model writes the shared agent profile, not the task"},
	"mcode":    {Kind: modelDiscoveryManual, Reason: "MCode ACP exposes no session-scoped model selection"},
	"zeroclaw": {Kind: modelDiscoveryManual, Reason: "ZeroClaw has no session/set_model; the model comes from its agent profile"},
}

func catalogDiscoverer(fn func(context.Context, Command) Catalog) modelDiscoverer {
	return func(ctx context.Context, cmd Command) (Catalog, error) {
		return fn(ctx, cmd), nil
	}
}

func modelsDiscoverer(fn func(context.Context, Command) ([]Model, error)) modelDiscoverer {
	return func(ctx context.Context, cmd Command) (Catalog, error) {
		return discovered(fn(ctx, cmd))
	}
}

func modelValuesDiscoverer(fn func(context.Context, Command) []Model) modelDiscoverer {
	return func(ctx context.Context, cmd Command) (Catalog, error) {
		return Catalog{Models: fn(ctx, cmd)}, nil
	}
}

// snapshotModelDiscovery copies the table. The registry test ranges the copy
// so a test installing a temporary provider cannot change the slice under it.
func snapshotModelDiscovery() map[string]modelDiscoveryDecl {
	modelDiscoveryMu.RLock()
	defer modelDiscoveryMu.RUnlock()
	out := make(map[string]modelDiscoveryDecl, len(modelDiscoveryByProvider))
	for id, decl := range modelDiscoveryByProvider {
		out[id] = decl
	}
	return out
}

// lookupModelDiscovery copies one declaration out of the table.
func lookupModelDiscovery(provider string) (modelDiscoveryDecl, bool) {
	modelDiscoveryMu.RLock()
	defer modelDiscoveryMu.RUnlock()
	decl, ok := modelDiscoveryByProvider[provider]
	return decl, ok
}

// undeclaredModelDiscovery returns the ids that have no discovery declaration,
// in the order given. A new runtime added to SupportedTypes or BuiltinRuntimes
// shows up here until someone records how its models are found.
func undeclaredModelDiscovery(ids []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, ok := lookupModelDiscovery(id); !ok {
			missing = append(missing, id)
		}
	}
	return missing
}
