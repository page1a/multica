package agent

import (
	"context"
	"strings"
	"testing"
)

func TestEveryRuntimeDeclaresModelDiscovery(t *testing.T) {
	ids := append([]string{}, SupportedTypes...)
	for _, runtime := range BuiltinRuntimes {
		ids = append(ids, runtime.ID)
	}
	if missing := undeclaredModelDiscovery(ids); len(missing) != 0 {
		t.Fatalf("runtimes with no discovery strategy: %v", missing)
	}

	decls := snapshotModelDiscovery()
	for _, runtime := range BuiltinRuntimes {
		decl, ok := decls[runtime.ID]
		if !ok {
			t.Fatalf("%s missing from the discovery table", runtime.ID)
		}
		if runtime.ModelDiscovery == nil && decl.Kind == modelDiscoveryDedicated && decl.Discover == nil {
			t.Errorf("%s claims a dedicated discoverer but neither the declaration nor BuiltinRuntime.ModelDiscovery has one", runtime.ID)
		}
	}
	for id, decl := range decls {
		switch decl.Kind {
		case modelDiscoveryManual:
			if strings.TrimSpace(decl.Reason) == "" {
				t.Errorf("%s is manual-only without a reason", id)
			}
			if ModelSelectionSupported(id) {
				t.Errorf("%s is manual-only but the picker would still offer a model list", id)
			}
		case modelDiscoveryChain:
			if decl.Endpoint == nil && !listCommandRegistered(decl.ListCommand) {
				t.Errorf("%s declares the fallback chain but registers neither an endpoint nor a readonly list command", id)
			}
		case modelDiscoveryDedicated:
			if decl.Discover != nil {
				continue
			}
			runtime, ok := BuiltinRuntimeByID(id)
			if !ok || runtime.ModelDiscovery == nil {
				t.Errorf("%s claims a dedicated discoverer but none is wired", id)
			}
		default:
			t.Errorf("%s has an unknown discovery kind %d", id, decl.Kind)
		}
	}

	// The guard itself must fail closed: a runtime that was never declared
	// is reported, which is what CI uses to catch the next one.
	missing := undeclaredModelDiscovery([]string{"brand-new-runtime"})
	if len(missing) != 1 || missing[0] != "brand-new-runtime" {
		t.Fatalf("undeclared runtime = %v, want [brand-new-runtime]", missing)
	}
}

func TestListModelsUnknownProviderIsUnavailable(t *testing.T) {
	_, err := ListModels(context.Background(), "brand-new-runtime", Command{})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("unknown provider error = %v", err)
	}
}

func TestParseOpenAIModelListAnthropicDisplayName(t *testing.T) {
	models, empty, err := parseOpenAIModelList([]byte(`{"data":[{"id":"claude-sonnet","display_name":"Claude Sonnet","type":"model"}]}`), "")
	if err != nil || empty {
		t.Fatalf("parse anthropic list: empty=%v err=%v", empty, err)
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet" || models[0].Label != "Claude Sonnet" {
		t.Fatalf("models = %+v", models)
	}
}
