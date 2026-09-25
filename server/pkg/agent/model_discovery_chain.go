package agent

import (
	"context"
	"strings"
	"time"
)

// modelListCommandTimeout bounds one registered readonly list command.
// 15s matches the pi and opencode discovery caps: long enough for a CLI that
// reads a local config, short enough that a hung command does not pin the
// picker.
const modelListCommandTimeout = 15 * time.Second

// modelEndpointSource reads the base URL, key, and default model id from a
// runtime's own config. The key is for the local probe only.
type modelEndpointSource func(ctx context.Context) (baseURL, apiKey, defaultModel string, err error)

// modelListCommand is a readonly catalog command a runtime has explicitly
// registered. The chain never invents args or a parser: a declaration with
// no args or no parser is not probed with `models` or `--list-models`.
type modelListCommand struct {
	Args  []string
	Parse func([]byte) ([]Model, error)
}

// modelDiscoveryProbe overlays steps onto the declaration ListModels already
// resolved. Production requests never set it. Tests use it to prove a real
// runtime walks the later steps, without rewriting the process-wide table
// while other tests are listing models for that same runtime.
type modelDiscoveryProbe struct {
	Discover    modelDiscoverer
	Endpoint    modelEndpointSource
	ListCommand *modelListCommand
}

type modelDiscoveryProbeKey struct{}

func withModelDiscoveryProbe(ctx context.Context, probe modelDiscoveryProbe) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, modelDiscoveryProbeKey{}, probe)
}

func applyModelDiscoveryProbe(ctx context.Context, decl modelDiscoveryDecl) modelDiscoveryDecl {
	if ctx == nil {
		return decl
	}
	probe, ok := ctx.Value(modelDiscoveryProbeKey{}).(modelDiscoveryProbe)
	if !ok {
		return decl
	}
	if probe.Discover != nil {
		decl.Discover = probe.Discover
	}
	if probe.Endpoint != nil {
		decl.Endpoint = probe.Endpoint
	}
	if probe.ListCommand != nil {
		decl.ListCommand = *probe.ListCommand
	}
	return decl
}

type modelEnvOverlayKey struct{}

// WithModelEnvOverlay attaches an agent's custom_env to a model-list request.
// Endpoint readers consult it and let those values win over the machine's
// own config files. The map is copied; the key stays in this process and is
// not part of the catalog result.
func WithModelEnvOverlay(ctx context.Context, overlay map[string]string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	copied := cloneModelEnvOverlay(overlay)
	if len(copied) == 0 {
		return ctx
	}
	return context.WithValue(ctx, modelEnvOverlayKey{}, copied)
}

// ModelEnvOverlay returns the agent custom_env attached to ctx, or nil when
// the request is for the machine config alone.
func ModelEnvOverlay(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	overlay, _ := ctx.Value(modelEnvOverlayKey{}).(map[string]string)
	return overlay
}

func cloneModelEnvOverlay(overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return nil
	}
	copied := make(map[string]string, len(overlay))
	for key, value := range overlay {
		copied[key] = value
	}
	return copied
}

// walkModelDiscoveryChain is the one model-list path. Every runtime uses it.
// The first step that obtains a list stops the walk:
//
//  1. Dedicated discoverer, when the declaration has one. A non-empty catalog
//     that is not a static fallback is the answer. A failure, an empty
//     catalog, or a fallback catalog continues.
//  2. GET {base}/models on the endpoint the runtime's own config names, when
//     the declaration registers a reader. A parsed list stops the walk,
//     including a confirmed empty one. A transport or HTTP failure continues.
//  3. The readonly list command on the declaration. A command that is not
//     registered is not run.
//  4. Manual entry, which stays in the picker. This step does not invent a
//     model id. It returns the dedicated discoverer's stand-in when that is
//     the only list anyone produced, otherwise the first failure, so a tunnel
//     that is down is not reported as an empty catalog.
//
// Only a definitive dedicated list is cached, and only inside that step.
// Endpoint and command results are not cached: the next refresh has to see
// the tunnel as it is now. An empty dedicated result is not cached either,
// so a later success is not hidden behind it.
func walkModelDiscoveryChain(
	ctx context.Context,
	providerType string,
	runtimeCmd Command,
	decl modelDiscoveryDecl,
) (Catalog, error) {
	var firstFail error
	var dedicated Catalog
	sawDedicated := false

	if decl.Discover != nil {
		sawDedicated = true
		catalog, err := cachedDiscovery(discoveryCacheKey(providerType, runtimeCmd), func() (Catalog, error) {
			return decl.Discover(ctx, runtimeCmd)
		})
		if err == nil && len(catalog.Models) > 0 && !catalog.Fallback {
			return catalog, nil
		}
		if err != nil {
			firstFail = err
		} else {
			dedicated = catalog
		}
	}

	if decl.Endpoint != nil {
		baseURL, apiKey, defaultModel, err := decl.Endpoint(ctx)
		if err != nil {
			if firstFail == nil {
				firstFail = err
			}
		} else {
			models, err := discoverCompatibleEndpointModels(ctx, baseURL, apiKey, defaultModel)
			if err != nil {
				if firstFail == nil {
					firstFail = err
				}
			} else {
				return Catalog{Models: models}, nil
			}
		}
	}

	if listCommandRegistered(decl.ListCommand) {
		models, err := runRegisteredModelListCommand(ctx, runtimeCmd, decl.ListCommand)
		if err != nil {
			if firstFail == nil {
				firstFail = err
			}
		} else {
			return Catalog{Models: models}, nil
		}
	}

	if sawDedicated && firstFail == nil {
		return dedicated, nil
	}
	// A static stand-in still beats a later probe that also failed. Runtimes
	// that already ship one keep it when they have no better answer.
	if dedicated.Fallback && len(dedicated.Models) > 0 {
		return dedicated, nil
	}
	if firstFail != nil {
		return Catalog{}, firstFail
	}
	return Catalog{}, errModelsListUnavailable("这个运行时没有登记发现方式")
}

func listCommandRegistered(cmd modelListCommand) bool {
	return len(cmd.Args) > 0 && cmd.Parse != nil
}

// runRegisteredModelListCommand runs a command the runtime registered as
// readonly. The exit status and the parser decide the outcome. Stdout is not
// copied into the error: a confused CLI can echo a config line, and that
// must not reach the picker.
func runRegisteredModelListCommand(ctx context.Context, runtimeCmd Command, cmd modelListCommand) ([]Model, error) {
	if strings.TrimSpace(runtimeCmd.Path) == "" {
		return nil, errModelsListUnavailable("没有可执行的列表命令")
	}
	runCtx, cancel := context.WithTimeout(ctx, modelListCommandTimeout)
	defer cancel()
	proc := runtimeCmd.exec(runCtx, cmd.Args...)
	hideAgentWindow(proc)
	stdout, err := outputOwned(proc, runtimeCmd.logger)
	if err != nil {
		return nil, errModelsListUnavailable("列表命令没有返回模型清单")
	}
	models, err := cmd.Parse(stdout)
	if err != nil {
		return nil, errModelsListUnavailable("列表命令没有返回模型清单")
	}
	return models, nil
}
