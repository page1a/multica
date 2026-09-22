//go:build agentintegration

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDshProviderPresetBaseURLTakesEffect is the end-to-end proof that a
// preset written through the provider-preset actions is the configuration DSH
// actually runs with: it writes the files, then boots the real `dsh` CLI
// against a local stand-in for an openai-completions endpoint and asserts the
// request arrived there, with the model and the key from the preset.
//
// It executes a user-installed CLI, so it lives behind the agentintegration
// build tag and the MULTICA_RUN_REAL_AGENT_SMOKE opt-in, and it checks both
// before it looks for the executable. It never contacts a third-party endpoint
// and never uses a real credential: the baseURL is an httptest server on
// loopback and the key is a fake literal.
//
// Run it with:
//
//	(cd server && MULTICA_RUN_REAL_AGENT_SMOKE=1 go test -tags=agentintegration \
//	  ./internal/daemon -run TestDshProviderPresetBaseURLTakesEffect -count=1 -v)
func TestDshProviderPresetBaseURLTakesEffect(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run the dsh provider smoke test")
	}
	dshPath, err := exec.LookPath("dsh")
	if err != nil {
		t.Skipf("dsh is not installed: %v", err)
	}

	type hit struct {
		path          string
		authorization string
		model         string
	}
	var (
		mu   sync.Mutex
		hits []hit
	)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		hits = append(hits, hit{path: r.URL.Path, authorization: r.Header.Get("Authorization"), model: body.Model})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-smoke",
			"object":  "chat.completion",
			"created": 0,
			"model":   body.Model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer endpoint.Close()

	dshHome := dshTestHome(t)
	const (
		presetID = "dene346-smoke"
		modelID  = "smoke-model"
		apiKey   = "sk-test-smoke-not-a-real-key"
	)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       presetID,
		"api":      "openai-completions",
		"base_url": endpoint.URL + "/v1",
		"models":   []map[string]any{{"id": modelID, "name": "Smoke Model", "context_window": 128000}},
		"api_key":  apiKey,
	})
	snapshot := dshApplyOK(t, providerActionActivate, map[string]any{"id": presetID})
	if snapshot.Active == nil || snapshot.Active.Provider != presetID || snapshot.Active.Model != modelID {
		t.Fatalf("activation did not take: %+v", snapshot.Active)
	}
	t.Logf("wrote %s with baseURL %s and activated %s/%s",
		filepath.Join(dshHome, dshSettingsFileName), endpoint.URL+"/v1", presetID, modelID)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// --from-default-profile means the temporary DSH_HOME needs no profile of
	// its own: this test never reads or writes the developer's real ~/.dsh
	// beyond the CLI binary itself.
	cmd := exec.CommandContext(ctx, dshPath,
		"--profile", "multica-smoke", "--from-default-profile", "headless", "say hi")
	cmd.Env = append(os.Environ(), "DSH_HOME="+dshHome)
	output, runErr := cmd.CombinedOutput()
	// The CLI is expected to end with a transport complaint: the stand-in
	// answers one JSON body, not the SSE stream the client wants. What the
	// test asserts is where the request went, not that the turn completed.
	t.Logf("dsh exited with %v\n--- dsh output ---\n%s", runErr, truncateBytesForLog(output, 4000))

	mu.Lock()
	got := append([]hit(nil), hits...)
	mu.Unlock()

	if len(got) == 0 {
		t.Fatal("dsh never contacted the configured baseURL")
	}
	t.Logf("--- captured requests (%d) ---", len(got))
	for i, h := range got {
		t.Logf("%d. path=%s model=%s authorization=%s", i+1, h.path, h.model, h.authorization)
	}

	var sawConfiguredEndpoint bool
	for _, h := range got {
		if h.path != "/v1/chat/completions" {
			continue
		}
		if h.model != modelID {
			t.Errorf("request used model %q, want %q", h.model, modelID)
			continue
		}
		if h.authorization != "Bearer "+apiKey {
			t.Errorf("request authorization = %q, want the preset's key", h.authorization)
			continue
		}
		sawConfiguredEndpoint = true
	}
	if !sawConfiguredEndpoint {
		t.Fatalf("no request carried the configured model and key: %+v", got)
	}
}

// truncateBytesForLog bounds captured CLI output in the test log.
func truncateBytesForLog(raw []byte, limit int) string {
	text := strings.TrimSpace(string(raw))
	if len(text) <= limit {
		return text
	}
	return "…" + text[len(text)-limit:]
}
