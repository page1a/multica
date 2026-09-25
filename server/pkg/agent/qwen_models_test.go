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

func withQwenHome(t *testing.T, dir string) {
	t.Helper()
	prev := qwenConfigHome
	qwenConfigHome = func(map[string]string) string { return dir }
	t.Cleanup(func() { qwenConfigHome = prev })
}

func writeQwenFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestResolveQwenEndpointEnvFile(t *testing.T) {
	dir := t.TempDir()
	writeQwenFile(t, dir, ".env", ""+
		"# comment\n"+
		"export OPENAI_API_KEY=\"sk-test-secret\"\n"+
		"OPENAI_BASE_URL=http://127.0.0.1:8090/v1\n"+
		"OPENAI_MODEL=nexus-coder\n")
	writeQwenFile(t, dir, "settings.json", `{"$version":4,"security":{"auth":{"selectedType":"openai"}}}`)

	got, err := resolveQwenEndpoint(dir, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.BaseURL != "http://127.0.0.1:8090/v1" || got.APIKey != "sk-test-secret" || got.Model != "nexus-coder" {
		t.Fatalf("resolve = %+v", got)
	}
	if strings.Contains(got.BaseURL, "sk-test-secret") {
		t.Fatal("base URL must not carry the key")
	}
}

func TestResolveQwenEndpointOverlayWins(t *testing.T) {
	dir := t.TempDir()
	writeQwenFile(t, dir, ".env", "OPENAI_BASE_URL=http://127.0.0.1:8090/v1\nOPENAI_API_KEY=file-key\nOPENAI_MODEL=nexus-coder\n")

	got, err := resolveQwenEndpoint(dir, map[string]string{
		"OPENAI_BASE_URL": "http://127.0.0.1:9000/v1",
		"OPENAI_API_KEY":  "overlay-key",
		"OPENAI_MODEL":    "other-model",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.BaseURL != "http://127.0.0.1:9000/v1" || got.APIKey != "overlay-key" || got.Model != "other-model" {
		t.Fatalf("overlay should win, got %+v", got)
	}
}

func TestResolveQwenEndpointProviderFillsGapsOnly(t *testing.T) {
	dir := t.TempDir()
	writeQwenFile(t, dir, "settings.json", `{
		"security": {"auth": {"selectedType": "openai"}},
		"env": {"DASHSCOPE_API_KEY": "dash-key"},
		"modelProviders": {"openai": [{
			"id": "qwen3-coder-plus",
			"baseUrl": "https://dashscope.example/v1",
			"envKey": "DASHSCOPE_API_KEY"
		}]}
	}`)

	got, err := resolveQwenEndpoint(dir, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.BaseURL != "https://dashscope.example/v1" || got.APIKey != "dash-key" || got.Model != "qwen3-coder-plus" {
		t.Fatalf("provider row should fill the gap, got %+v", got)
	}

	writeQwenFile(t, dir, ".env", "OPENAI_BASE_URL=http://127.0.0.1:8090/v1\nOPENAI_API_KEY=file-key\nOPENAI_MODEL=nexus-coder\n")
	got, err = resolveQwenEndpoint(dir, nil)
	if err != nil {
		t.Fatalf("resolve with .env: %v", err)
	}
	if got.BaseURL != "http://127.0.0.1:8090/v1" || got.APIKey != "file-key" || got.Model != "nexus-coder" {
		t.Fatalf(".env should beat modelProviders, got %+v", got)
	}
}

func TestResolveQwenEndpointMissingIsUnavailable(t *testing.T) {
	const secret = "sk-test-do-not-leak"
	dir := t.TempDir()
	writeQwenFile(t, dir, "settings.json", `{"env": {"OPENAI_API_KEY": "`+secret+`"} not json`)

	_, err := resolveQwenEndpoint(dir, nil)
	if err == nil {
		t.Fatal("expected an error when no endpoint can be read")
	}
	if !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("error = %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}
}

func TestDiscoverQwenModelsListsAndDoesNotCacheMisses(t *testing.T) {
	const secret = "sk-test-do-not-leak"
	var hits atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if strings.Contains(r.URL.String(), secret) {
			t.Errorf("request URL contains the key: %s", r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("Authorization = %q", got)
		}
		if fail.Load() {
			http.Error(w, secret, http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"other"},{"id":"nexus-coder","name":"Nexus"},{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	writeQwenFile(t, dir, ".env", "OPENAI_BASE_URL="+srv.URL+"/v1\nOPENAI_API_KEY="+secret+"\nOPENAI_MODEL=nexus-coder\n")
	withQwenHome(t, dir)

	ctx := context.Background()
	_, err := ListModels(ctx, "qwen", Command{})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("down endpoint error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}

	fail.Store(false)
	got, err := ListModels(ctx, "qwen", Command{})
	if err != nil {
		t.Fatalf("up endpoint: %v", err)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models = %+v, want other and nexus-coder once", got.Models)
	}
	if !got.Models[1].Default || got.Models[1].ID != "nexus-coder" || got.Models[1].Label != "Nexus" {
		t.Fatalf("default model = %+v", got.Models[1])
	}
	if got.Models[0].Default {
		t.Fatal("non-default model was badged")
	}

	fail.Store(true)
	if _, err := ListModels(ctx, "qwen", Command{}); err == nil {
		t.Fatal("a later outage was served from a cached success")
	}
	if hits.Load() != 3 {
		t.Fatalf("probe calls = %d, want 3 (failure, success, failure) — a miss or a hit was cached", hits.Load())
	}
}

func TestListModelsQwenCustomEnvOverlayWins(t *testing.T) {
	const secret = "sk-overlay-do-not-leak"
	var fileHits atomic.Int32
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileHits.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"from-file"}]}`))
	}))
	t.Cleanup(fileSrv.Close)

	var overlayHits atomic.Int32
	var overlayDown atomic.Bool
	overlaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overlayHits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("Authorization = %q", got)
		}
		if strings.Contains(r.URL.String(), secret) {
			t.Errorf("request URL contains the key: %s", r.URL.String())
		}
		if overlayDown.Load() {
			http.Error(w, secret, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"nexus-coder"}]}`))
	}))
	t.Cleanup(overlaySrv.Close)

	dir := t.TempDir()
	writeQwenFile(t, dir, ".env", "OPENAI_BASE_URL="+fileSrv.URL+"/v1\nOPENAI_API_KEY=file-key\nOPENAI_MODEL=from-file\n")
	withQwenHome(t, dir)

	ctx := WithModelEnvOverlay(context.Background(), map[string]string{
		"OPENAI_BASE_URL": overlaySrv.URL + "/v1",
		"OPENAI_API_KEY":  secret,
		"OPENAI_MODEL":    "nexus-coder",
	})
	got, err := ListModels(ctx, "qwen", Command{})
	if err != nil {
		t.Fatalf("overlay endpoint: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "nexus-coder" || !got.Models[0].Default {
		t.Fatalf("models = %+v, want the overlay endpoint", got.Models)
	}
	if overlayHits.Load() != 1 {
		t.Fatalf("overlay probe calls = %d, want 1", overlayHits.Load())
	}
	if fileHits.Load() != 0 {
		t.Fatalf("machine config was probed %d times; the agent overlay should win", fileHits.Load())
	}

	overlayDown.Store(true)
	_, err = ListModels(ctx, "qwen", Command{})
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("overlay outage error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}
	if fileHits.Load() != 0 {
		t.Fatal("an overlay outage fell through to the machine endpoint")
	}
	if overlayHits.Load() != 2 {
		t.Fatalf("overlay probe calls = %d, want 2 — a success was cached across the outage", overlayHits.Load())
	}
}

func TestDiscoverOpenAICompatibleModelsConfirmedEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	models, err := discoverOpenAICompatibleModels(context.Background(), srv.URL+"/v1", "sk-test", "nexus-coder")
	if err != nil {
		t.Fatalf("confirmed empty list: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want none", models)
	}
}

func TestDiscoverOpenAICompatibleModelsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := discoverOpenAICompatibleModels(context.Background(), url+"/v1", "sk-closed-secret", "nexus-coder")
	if err == nil || !strings.Contains(err.Error(), "暂时无法获取") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "sk-closed-secret") {
		t.Fatalf("error leaked the key: %q", err)
	}
}

func TestDiscoverCompatibleEndpointModelsRetriesAnthropicAuth(t *testing.T) {
	const secret = "sk-anthropic-do-not-leak"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.Contains(r.URL.String(), secret) {
			t.Errorf("request URL contains the key: %s", r.URL.String())
		}
		if r.Header.Get("x-api-key") == secret && r.Header.Get("anthropic-version") == "2023-06-01" && r.Header.Get("Authorization") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet","display_name":"Claude Sonnet","type":"model"}]}`))
			return
		}
		http.Error(w, secret, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	models, err := discoverCompatibleEndpointModels(context.Background(), srv.URL+"/v1", secret, "claude-sonnet")
	if err != nil {
		t.Fatalf("anthropic retry: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("probe calls = %d, want bearer then x-api-key", hits.Load())
	}
	if len(models) != 1 || models[0].ID != "claude-sonnet" || models[0].Label != "Claude Sonnet" || !models[0].Default {
		t.Fatalf("models = %+v", models)
	}
}

func TestDiscoverCompatibleEndpointModelsDoesNotRetryOutage(t *testing.T) {
	const secret = "sk-outage-do-not-leak"
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, secret, http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, err := discoverCompatibleEndpointModels(context.Background(), srv.URL+"/v1", secret, "nexus-coder")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("probe calls = %d, want 1 — an outage must not be retried as another auth scheme", hits.Load())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err)
	}
}

func TestOpenAIModelsEndpointJoinsModelsPath(t *testing.T) {
	got, host, err := openAIModelsEndpoint("http://user:sk-secret@127.0.0.1:8090/v1/")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	if got != "http://127.0.0.1:8090/v1/models" {
		t.Fatalf("endpoint = %s", got)
	}
	if host != "127.0.0.1:8090" {
		t.Fatalf("host = %s", host)
	}
	if strings.Contains(got, "sk-secret") || strings.Contains(host, "sk-secret") {
		t.Fatal("endpoint kept the key")
	}
	if _, _, err := openAIModelsEndpoint("ftp://127.0.0.1/v1"); err == nil {
		t.Fatal("non-http endpoint was accepted")
	}
}
