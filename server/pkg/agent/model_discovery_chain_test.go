package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeChainScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func TestWalkChainEndpointHitSkipsListCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'should-not-win\\n'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, modelDiscoveryDecl{
		Kind: modelDiscoveryChain,
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "sk-test", "nexus-coder", nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "nexus-coder" || !got.Models[0].Default {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("endpoint hit still ran the list command")
	}
}

func TestWalkChainConfirmedEmptyStops(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, modelDiscoveryDecl{
		Kind: modelDiscoveryChain,
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "", "", nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("confirmed empty should not be an error: %v", err)
	}
	if len(got.Models) != 0 {
		t.Fatalf("models = %+v, want none", got.Models)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("confirmed empty list still ran the list command")
	}
}

func TestWalkChainListCommandRunsOnlyWhenRegistered(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, modelDiscoveryDecl{
		Kind:        modelDiscoveryChain,
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("registered command: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-cli" {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("registered list command did not run")
	}

	unregistered := filepath.Join(t.TempDir(), "unregistered")
	other := writeChainScript(t, "#!/bin/sh\ntouch '"+unregistered+"'\nprintf 'nope\\n'\n")
	_, err = walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: other}, modelDiscoveryDecl{})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("unregistered chain error = %v", err)
	}
	if _, statErr := os.Stat(unregistered); statErr == nil {
		t.Fatal("unregistered provider ran a list command")
	}
}

func TestWalkChainEndpointFailureFallsThroughToListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	bin := writeChainScript(t, "#!/bin/sh\nprintf 'from-cli\\n'\n")

	got, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, modelDiscoveryDecl{
		Kind: modelDiscoveryChain,
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "sk-test", "", nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err != nil {
		t.Fatalf("list command should cover an endpoint miss: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-cli" {
		t.Fatalf("models = %+v", got.Models)
	}
}

func TestWalkChainBothStepsFailKeepsEndpointReason(t *testing.T) {
	const secret = "sk-chain-do-not-leak"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, secret, http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	bin := writeChainScript(t, "#!/bin/sh\nprintf '"+secret+"\\n' >&2\nexit 1\n")

	_, err := walkModelDiscoveryChain(context.Background(), "chain-probe", Command{Path: bin}, modelDiscoveryDecl{
		Kind: modelDiscoveryChain,
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", secret, "", nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v, want the endpoint status", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}
}

func TestListModelsUnknownProviderDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'nope\\n'\n")
	_, err := ListModels(context.Background(), "brand-new-runtime", Command{Path: bin})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("unknown provider error = %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("unknown provider spawned the runtime CLI")
	}
}

func installModelDiscovery(t *testing.T, id string, decl modelDiscoveryDecl) {
	t.Helper()
	modelDiscoveryMu.Lock()
	prev, had := modelDiscoveryByProvider[id]
	modelDiscoveryByProvider[id] = decl
	modelDiscoveryMu.Unlock()
	t.Cleanup(func() {
		modelDiscoveryMu.Lock()
		if had {
			modelDiscoveryByProvider[id] = prev
		} else {
			delete(modelDiscoveryByProvider, id)
		}
		modelDiscoveryMu.Unlock()
		modelCacheMu.Lock()
		for key := range modelCache {
			if key == id || strings.HasPrefix(key, id+":") {
				delete(modelCache, key)
			}
		}
		modelCacheMu.Unlock()
	})
}

func TestListModelsDedicatedFailureUsesDeclaredEndpoint(t *testing.T) {
	const id = "dene792-dedicated-fail"
	var dedicatedCalls atomic.Int32
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"from-endpoint"}]}`))
	}))
	t.Cleanup(srv.Close)

	installModelDiscovery(t, id, modelDiscoveryDecl{
		Kind: modelDiscoveryDedicated,
		Discover: func(context.Context, Command) (Catalog, error) {
			dedicatedCalls.Add(1)
			return Catalog{}, errModelsListUnavailable("专用发现器连不上")
		},
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "sk-test", "", nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})

	got, err := ListModels(context.Background(), id, Command{Path: bin})
	if err != nil {
		t.Fatalf("endpoint should cover a dedicated failure: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-endpoint" {
		t.Fatalf("models = %+v", got.Models)
	}
	if dedicatedCalls.Load() != 1 {
		t.Fatalf("dedicated discoverer calls = %d, want 1", dedicatedCalls.Load())
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("endpoint hit still ran the list command")
	}
}

func TestListModelsDedicatedEmptyUsesRegisteredCommand(t *testing.T) {
	const id = "dene792-dedicated-empty"
	var dedicatedCalls atomic.Int32
	marker := filepath.Join(t.TempDir(), "invoked")
	bin := writeChainScript(t, "#!/bin/sh\ntouch '"+marker+"'\nprintf 'from-cli\\n'\n")
	installModelDiscovery(t, id, modelDiscoveryDecl{
		Kind: modelDiscoveryDedicated,
		Discover: func(context.Context, Command) (Catalog, error) {
			dedicatedCalls.Add(1)
			return Catalog{}, nil
		},
		ListCommand: modelListCommand{Args: []string{"models"}, Parse: parseChainIDLines},
	})

	got, err := ListModels(context.Background(), id, Command{Path: bin})
	if err != nil {
		t.Fatalf("list command should cover an empty dedicated catalog: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-cli" {
		t.Fatalf("models = %+v", got.Models)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatal("registered list command did not run")
	}

	_, err = ListModels(context.Background(), id, Command{Path: bin})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if dedicatedCalls.Load() != 2 {
		t.Fatalf("dedicated calls = %d, want 2 — an empty catalog was cached", dedicatedCalls.Load())
	}
}

func TestListModelsCopilotFailureUsesProbeEndpoint(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"from-endpoint"}]}`))
	}))
	t.Cleanup(srv.Close)

	ctx := withModelDiscoveryProbe(context.Background(), modelDiscoveryProbe{
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", "", "", nil
		},
	})
	got, err := ListModels(ctx, "copilot", Command{Path: missingAgentExecutable(t, "copilot")})
	if err != nil {
		t.Fatalf("copilot fallback should continue to the declared endpoint: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "from-endpoint" {
		t.Fatalf("models = %+v, want the endpoint list rather than the static copilot catalog", got.Models)
	}
	if hits.Load() != 1 {
		t.Fatalf("endpoint calls = %d, want 1", hits.Load())
	}
}

func TestListModelsUnknownProviderDiscoversFromEndpoint(t *testing.T) {
	const id = "brand-new-runtime"
	const secret = "sk-unknown-do-not-leak"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("Authorization = %q", got)
		}
		if strings.Contains(r.URL.String(), secret) {
			t.Errorf("request URL contains the key: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	installModelDiscovery(t, id, modelDiscoveryDecl{
		Kind: modelDiscoveryChain,
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", secret, "nexus-coder", nil
		},
	})

	got, err := ListModels(context.Background(), id, Command{})
	if err != nil {
		t.Fatalf("unknown provider with a declared endpoint: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "nexus-coder" || !got.Models[0].Default {
		t.Fatalf("models = %+v", got.Models)
	}
	if hits.Load() != 1 {
		t.Fatalf("endpoint calls = %d, want 1", hits.Load())
	}
}

func TestListModelsDedicatedFailureRecoversWithoutCache(t *testing.T) {
	const id = "dene792-recover"
	const secret = "sk-recover-do-not-leak"
	var dedicatedCalls atomic.Int32
	var endpointCalls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		endpointCalls.Add(1)
		if fail.Load() {
			http.Error(w, secret, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	installModelDiscovery(t, id, modelDiscoveryDecl{
		Kind: modelDiscoveryDedicated,
		Discover: func(context.Context, Command) (Catalog, error) {
			dedicatedCalls.Add(1)
			return Catalog{}, errModelsListUnavailable("专用发现器连不上")
		},
		Endpoint: func(context.Context) (string, string, string, error) {
			return srv.URL + "/v1", secret, "", nil
		},
	})

	ctx := context.Background()
	_, err := ListModels(ctx, id, Command{})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("down endpoint error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}

	fail.Store(false)
	got, err := ListModels(ctx, id, Command{})
	if err != nil {
		t.Fatalf("recovered endpoint: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "nexus-coder" {
		t.Fatalf("models = %+v", got.Models)
	}

	fail.Store(true)
	if _, err := ListModels(ctx, id, Command{}); err == nil {
		t.Fatal("a later outage was served from a cached success")
	}
	if dedicatedCalls.Load() != 3 || endpointCalls.Load() != 3 {
		t.Fatalf("dedicated calls = %d, endpoint calls = %d, want 3 and 3 — a miss or a hit was cached", dedicatedCalls.Load(), endpointCalls.Load())
	}
}

func parseChainIDLines(stdout []byte) ([]Model, error) {
	var models []Model
	for _, line := range strings.Split(string(stdout), "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		models = append(models, Model{ID: id, Label: id})
	}
	return models, nil
}
