package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A fake provider gateway.
//
// The save-time health check is two real HTTP round trips, so the tests that
// exercise it — and every pre-existing upsert test, because a save now probes
// — need something on the other end. The gateway below is that something, and
// it is reached through a rewriting transport rather than through rewritten
// baseURLs: a fixture can keep saying `https://api.example.invalid/provider/v1`
// and still get a real request/response cycle.

// dshFakeGateway answers the two endpoints a probe calls and records what it
// was asked for.
type dshFakeGateway struct {
	mu sync.Mutex

	// catalog is what GET {base}/models answers with.
	catalog []map[string]any

	// chatStatus and chatBody shape the minimal completion. Step two is where
	// the failures this ticket is about live, so the listing can succeed while
	// the completion does not.
	chatStatus int
	chatBody   string

	// modelsStatus, when non-zero, rejects the listing instead — the gateway
	// that publishes no catalog.
	modelsStatus int

	modelCalls int
	chatCalls  int

	lastModelsAuth    string
	lastModelsKey     string
	lastModelsVersion string
	lastChatAuth      string
	lastChatKey       string
	lastChatPath      string
	lastChatBody      map[string]any
}

func dshDefaultFakeGateway() *dshFakeGateway {
	return &dshFakeGateway{
		catalog: []map[string]any{},
		// 200 with an OpenAI-shaped body; step two only looks at the status.
		chatStatus: http.StatusOK,
		chatBody:   `{"id":"probe","choices":[{"message":{"role":"assistant","content":"pong"}}]}`,
	}
}

// serve adds model ids to the catalog the gateway answers with. A test that
// configures a preset with an id of its own choosing is asserting something
// about the write, not about the probe, and a gateway that did not list the id
// would fail the save for a reason the test is not about.
func (g *dshFakeGateway) serve(ids ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		if id == "" || g.hasModel(id) {
			continue
		}
		g.catalog = append(g.catalog, map[string]any{
			"id":                  id,
			"name":                id,
			"context_length":      200000,
			"supported_endpoints": []string{providerEndpointChatCompletions},
		})
	}
}

// serveMessagesModel adds a model the catalog reports as speaking the
// anthropic protocol instead.
func (g *dshFakeGateway) serveMessagesModel(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hasModel(id) {
		return
	}
	g.catalog = append(g.catalog, map[string]any{
		"id":                  id,
		"name":                id,
		"context_length":      200000,
		"supported_endpoints": []string{providerEndpointMessages},
	})
}

// serveResponsesModel adds a model the catalog reports as speaking the OpenAI
// Responses protocol instead.
func (g *dshFakeGateway) serveResponsesModel(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hasModel(id) {
		return
	}
	g.catalog = append(g.catalog, map[string]any{
		"id":                  id,
		"name":                id,
		"context_length":      200000,
		"supported_endpoints": []string{providerEndpointResponses},
	})
}

// failChat makes step two answer with a gateway rejection while step one keeps
// succeeding — the "the key authenticates but cannot be used" case.
func (g *dshFakeGateway) failChat(status int, body string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.chatStatus = status
	g.chatBody = body
}

// failModels rejects the listing, which is what a gateway with no /models
// looks like from here.
func (g *dshFakeGateway) failModels(status int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.modelsStatus = status
}

func (g *dshFakeGateway) hasModel(id string) bool {
	for _, entry := range g.catalog {
		if entry["id"] == id {
			return true
		}
	}
	return false
}

func (g *dshFakeGateway) calls() (models, chats int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.modelCalls, g.chatCalls
}

// catalogSize is the byte length of the listing's answer. A test that is about
// a real-sized catalog has to prove its fixture is one, or it passes with the
// truncation bug still in place.
func (g *dshFakeGateway) catalogSize() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	body, err := json.Marshal(map[string]any{"object": "list", "data": g.catalog})
	if err != nil {
		return 0
	}
	return len(body)
}

func (g *dshFakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
		g.modelCalls++
		g.lastModelsAuth = r.Header.Get("Authorization")
		g.lastModelsKey = r.Header.Get("x-api-key")
		g.lastModelsVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		if g.modelsStatus != 0 {
			w.WriteHeader(g.modelsStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"no model listing"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": g.catalog})
	case r.Method == http.MethodPost &&
		(strings.HasSuffix(r.URL.Path, providerEndpointChatCompletions) ||
			strings.HasSuffix(r.URL.Path, providerEndpointMessages) ||
			strings.HasSuffix(r.URL.Path, providerEndpointResponses)):
		g.chatCalls++
		g.lastChatAuth = r.Header.Get("Authorization")
		g.lastChatKey = r.Header.Get("x-api-key")
		g.lastChatPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.lastChatBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.chatStatus)
		if g.chatBody == "" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(g.chatBody))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no such endpoint"}}`))
	}
}

// dshInstalledGateway is the gateway the current sequential test installed.
// This package's provider tests never call t.Parallel, and Go resumes parallel
// tests only after every sequential test in the package has finished, so a
// package-level handle cannot be observed mid-test by another goroutine.
var dshInstalledGateway *dshFakeGateway

// dshInstallFakeGateway points every provider probe at a local gateway and
// returns it. A test that wants a specific failure installs its own; the
// default one accepts whatever the preset lists and answers the completion.
func dshInstallFakeGateway(t *testing.T) *dshFakeGateway {
	t.Helper()

	gateway := dshDefaultFakeGateway()
	dshInstalledGateway = gateway

	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse fake gateway URL: %v", err)
	}
	previous := providerProbeClient
	providerProbeClient = &http.Client{
		Timeout:   providerProbeTimeout,
		Transport: dshRewritingTransport{target: target, inner: server.Client().Transport},
	}
	t.Cleanup(func() {
		providerProbeClient = previous
		dshInstalledGateway = nil
	})
	return gateway
}

// dshRewritingTransport sends every probe request to the fake gateway,
// whatever host the preset named.
type dshRewritingTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (t dshRewritingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.inner.RoundTrip(clone)
}

// dshBodyModelIDs reads the model ids out of an upsert body.
func dshBodyModelIDs(body map[string]any) []string {
	raw, ok := body["models"].([]map[string]any)
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(raw))
	for _, entry := range raw {
		if id, ok := entry["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// dshUpsertVerify runs an upsert without the "the gateway serves what the
// preset lists" convenience, so the health check sees exactly the catalog the
// test configured.
// dshSettingsAfter reads settings.yaml the way a refused action leaves it.
func dshSettingsAfter(t *testing.T, home string) map[string]any {
	t.Helper()
	return dshSettingsOrEmpty(t, home)
}

func dshUpsertVerify(t *testing.T, body map[string]any) error {
	t.Helper()
	_, err := applyProviderConfig(context.Background(), "dsh", providerActionUpsert, dshJSON(t, body))
	return err
}

func dshFailure(t *testing.T, err error) *providerConfigFailure {
	t.Helper()
	if err == nil {
		t.Fatal("expected the action to fail")
	}
	var failure *providerConfigFailure
	if !errors.As(err, &failure) {
		t.Fatalf("error carries no classified failure: %v", err)
	}
	return failure
}

// ---------------------------------------------------------------------------
// Model discovery
// ---------------------------------------------------------------------------

// TestDshProviderModelsReturnsProviderIDsVerbatim pins that the listing is the
// endpoint's own answer: an id with a slash is a real model id, and escaping
// it is the surface's job when it builds a seat's provider/model string.
func TestDshProviderModelsReturnsProviderIDsVerbatim(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("deepseek/deepseek-v4.1-flash")
	gateway.serveMessagesModel("claude-sonnet-5")

	snapshot := dshApplyOK(t, providerActionModels, map[string]any{
		"base_url": "https://api.example.invalid/provider/v1",
		"api_key":  "sk-test-xxxx",
	})

	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %+v", snapshot.Models)
	}
	if snapshot.Models[0].ID != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("model id = %q, want it verbatim", snapshot.Models[0].ID)
	}
	if snapshot.Models[0].ContextWindow != 200000 {
		t.Errorf("context window = %d", snapshot.Models[0].ContextWindow)
	}
	if got := gateway.lastModelsAuth; got != "Bearer sk-test-xxxx" {
		t.Errorf("listing auth = %q", got)
	}
	// Discovery writes nothing: settings.yaml is still whatever it was.
	if settings := dshSettingsAfter(t, home); len(settings) != 0 {
		t.Errorf("the models action wrote settings.yaml: %#v", settings)
	}
}

// TestDshProviderModelsReusesStoredCredential pins that editing an existing
// preset without retyping the key can still fetch a list.
func TestDshProviderModelsReusesStoredCredential(t *testing.T) {
	home := dshTestHome(t)
	seedDshHome(t, home)
	dshInstalledGateway.serve("deepseek/deepseek-v4.1-flash")

	snapshot := dshApplyOK(t, providerActionModels, map[string]any{"id": "command-code"})

	if len(snapshot.Models) == 0 {
		t.Fatal("no models returned for a preset with a stored credential")
	}
	if got := dshInstalledGateway.lastModelsAuth; got != "Bearer sk-test-existing" {
		t.Errorf("listing auth = %q, want the stored credential", got)
	}
}

// TestDshProviderModelsReadsACatalogLargerThanTheDisplayLimit is the regression
// for a read bound applied in the wrong place: the 400-byte limit exists to
// truncate a gateway's error prose, and sharing it with the success path cut a
// real catalog in half, so every listing and every save failed with
// "unexpected end of JSON input" — a failure that reads as the gateway's fault.
func TestDshProviderModelsReadsACatalogLargerThanTheDisplayLimit(t *testing.T) {
	dshTestHome(t)
	gateway := dshInstalledGateway
	ids := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		ids = append(ids, fmt.Sprintf("deepseek/deepseek-v4.1-flash-%02d", i))
	}
	gateway.serve(ids...)

	// The fixture has to be bigger than the bound that caused the bug, or this
	// test would have passed while it was live.
	if size := gateway.catalogSize(); size <= providerProbeDetailLimit {
		t.Fatalf("fixture catalog is %d bytes, must exceed the %d-byte display limit", size, providerProbeDetailLimit)
	}

	snapshot := dshApplyOK(t, providerActionModels, map[string]any{
		"base_url": "https://api.example.invalid/provider/v1",
		"api_key":  "sk-test-xxxx",
	})
	if len(snapshot.Models) != len(ids) {
		t.Fatalf("models = %d, want all %d entries of the catalog", len(snapshot.Models), len(ids))
	}
	if snapshot.Models[len(ids)-1].ID != ids[len(ids)-1] {
		t.Errorf("last model = %q, want %q", snapshot.Models[len(ids)-1].ID, ids[len(ids)-1])
	}

	// And the save that consumes the same listing has to survive it too.
	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "big-catalog",
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": ids[11]}},
		"api_key":  "sk-test-xxxx",
	})
}

// TestDshProviderModelsSendsTheCredentialTheRouteExpects pins that step one
// obeys the same header rule as step two. A Bearer token sent to an
// anthropic-messages gateway is answered with 401, which reads as a rejected
// key and blocks every save behind the listing.
func TestDshProviderModelsSendsTheCredentialTheRouteExpects(t *testing.T) {
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serveMessagesModel("claude-sonnet-5")

	dshApplyOK(t, providerActionModels, map[string]any{
		"base_url": "https://api.example.invalid/provider/v1",
		"api":      providerAPIAnthropicMessages,
		"api_key":  "sk-ant-typed",
	})
	if got := gateway.lastModelsKey; got != "sk-ant-typed" {
		t.Errorf("anthropic listing sent x-api-key = %q", got)
	}
	if got := gateway.lastModelsAuth; got != "" {
		t.Errorf("anthropic listing also sent Authorization = %q, want none", got)
	}
	if got := gateway.lastModelsVersion; got != providerAnthropicVersion {
		t.Errorf("anthropic listing sent anthropic-version = %q, want %q", got, providerAnthropicVersion)
	}
}

// TestDshProviderModelsTakesTheProtocolFromTheStoredPreset covers the other
// way a caller names the route: editing an existing preset without retyping
// anything. The header rule has to come off the entry, or a previously working
// anthropic preset stops listing its models after an unrelated edit.
func TestDshProviderModelsTakesTheProtocolFromTheStoredPreset(t *testing.T) {
	home := dshTestHome(t)
	dshWriteTestFile(t, filepath.Join(home, dshSettingsFileName), `llm-pi-ai:
  providers:
    anthropic-gw:
      apiKeyEnv: ANTHROPIC_GW_KEY
      api: anthropic-messages
      baseURL: https://api.example.invalid/provider/v1
      models:
        - id: claude-sonnet-5
`)
	dshWriteTestFile(t, filepath.Join(home, dshCredentialsFileName), `version: 3
refs:
  ANTHROPIC_GW_KEY: sk-ant-stored
`)
	gateway := dshInstalledGateway
	gateway.serveMessagesModel("claude-sonnet-5")

	snapshot := dshApplyOK(t, providerActionModels, map[string]any{"id": "anthropic-gw"})

	if len(snapshot.Models) == 0 {
		t.Fatal("no models returned for a stored anthropic preset")
	}
	if got := gateway.lastModelsKey; got != "sk-ant-stored" {
		t.Errorf("x-api-key = %q, want the stored credential", got)
	}
	if got := gateway.lastModelsAuth; got != "" {
		t.Errorf("Authorization = %q, want none for an anthropic preset", got)
	}
}

// TestDshProviderModelsWithoutCredentialFails pins the refusal rather than a
// request with an empty bearer token.
func TestDshProviderModelsWithoutCredentialFails(t *testing.T) {
	dshTestHome(t)

	err := dshApply(t, providerActionModels, map[string]any{"base_url": "https://example.invalid/v1"})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindMissingCredential {
		t.Errorf("kind = %q, want %q", failure.Kind, providerProbeKindMissingCredential)
	}
}

// TestDshProviderModelsReportsEndpointWithoutAList pins the one failure the
// manual-entry fallback exists for.
func TestDshProviderModelsReportsEndpointWithoutAList(t *testing.T) {
	dshTestHome(t)
	dshInstalledGateway.failModels(http.StatusNotFound)

	err := dshApply(t, providerActionModels, map[string]any{
		"base_url": "https://api.example.invalid/provider/v1",
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindModelsUnavailable {
		t.Fatalf("kind = %q, want %q", failure.Kind, providerProbeKindModelsUnavailable)
	}
	// The copy has to point at the fallback, not just report a status code.
	if !strings.Contains(failure.Message, "by hand") {
		t.Errorf("message does not offer manual entry: %s", failure.Message)
	}
}

// ---------------------------------------------------------------------------
// Save-time health check
// ---------------------------------------------------------------------------

// TestDshProviderUpsertRunsBothProbeSteps pins that a save is not satisfied by
// the cheap step: the catalog authenticates, the completion proves usability.
func TestDshProviderUpsertRunsBothProbeSteps(t *testing.T) {
	home := dshTestHome(t)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "command-code2",
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}},
		"api_key":  "sk-test-xxxx",
	})

	models, chats := dshInstalledGateway.calls()
	if models != 1 || chats != 1 {
		t.Fatalf("probe calls = %d listing / %d chat, want 1 and 1", models, chats)
	}
	body := dshInstalledGateway.lastChatBody
	if body["model"] != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("probe model = %#v", body["model"])
	}
	if body["max_tokens"] != float64(providerProbeMaxTokens) {
		t.Errorf("probe max_tokens = %#v, want the minimum", body["max_tokens"])
	}
	if dshInstalledGateway.lastChatPath != "/provider/v1"+providerEndpointChatCompletions {
		t.Errorf("probe path = %q", dshInstalledGateway.lastChatPath)
	}
	settings := dshSettingsAfter(t, home)
	if dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code2", "api") != providerAPIOpenAICompletions {
		t.Errorf("api not written: %#v", settings)
	}
}

// TestDshProviderUpsertRefusesWhenOnlyTheModelListPasses is the ticket's
// central case: a key that authenticates and still cannot run anything. Every
// one of these gateways answers /models happily.
func TestDshProviderUpsertRefusesWhenOnlyTheModelListPasses(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		status      int
		body        string
		wantKind    string
		wantInError []string
	}{
		{
			name:   "plan does not include the model",
			model:  "claude-sonnet-5",
			status: http.StatusForbidden,
			body: `{"error":{"code":"MODEL_NOT_IN_PLAN","message":` +
				`"MODEL_NOT_IN_PLAN: claude-sonnet-5 is available in Pro and above plans"}}`,
			wantKind:    providerProbeKindModelNotInPlan,
			wantInError: []string{"claude-sonnet-5", "upgrade the plan"},
		},
		{
			name:   "key is bound to a cancelled billing cycle",
			status: http.StatusTooManyRequests,
			body: `{"error":{"code":"RATE_LIMITED","message":` +
				`"RATE_LIMITED: rate limit reached. Your limit resets at 2026-09-21T00:00:00Z"}}`,
			wantKind: providerProbeKindRateLimited,
			wantInError: []string{
				"regenerate the key in the provider dashboard",
				"cancelled billing cycle",
			},
		},
		{
			name:   "model id is not served on this endpoint",
			status: http.StatusBadRequest,
			body: `{"error":{"message":` +
				`"Model \"deepseek-v4.1-flash\" is not supported on this endpoint"}}`,
			wantKind:    providerProbeKindUnknownModel,
			wantInError: []string{"deepseek-v4.1-flash", "fetched model list"},
		},
		{
			name:     "request went to the wrong protocol",
			status:   http.StatusBadRequest,
			body:     `{"error":{"message":"Use /provider/v1/chat/completions for OpenAI and OSS models"}}`,
			wantKind: providerProbeKindEndpointMismatch,
			wantInError: []string{
				providerEndpointChatCompletions,
			},
		},
		{
			name:        "credential rejected",
			status:      http.StatusUnauthorized,
			body:        `{"error":{"message":"invalid api key"}}`,
			wantKind:    providerProbeKindInvalidCredential,
			wantInError: []string{"API key"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := dshTestHome(t)
			gateway := dshInstalledGateway
			gateway.serve("deepseek/deepseek-v4.1-flash", "claude-sonnet-5")
			gateway.failChat(testCase.status, testCase.body)

			model := testCase.model
			if model == "" {
				model = "deepseek/deepseek-v4.1-flash"
			}
			err := dshUpsertVerify(t, map[string]any{
				"id":       "command-code2",
				"api":      providerAPIOpenAICompletions,
				"base_url": "https://api.example.invalid/provider/v1",
				"models":   []map[string]any{{"id": model}},
				"api_key":  "sk-test-xxxx",
			})
			failure := dshFailure(t, err)
			if failure.Kind != testCase.wantKind {
				t.Errorf("kind = %q, want %q (message: %s)", failure.Kind, testCase.wantKind, failure.Message)
			}
			for _, want := range testCase.wantInError {
				if !strings.Contains(failure.Message, want) {
					t.Errorf("message %q does not mention %q", failure.Message, want)
				}
			}

			// Nothing landed: a refused save leaves the machine alone.
			if settings := dshSettingsAfter(t, home); len(settings) != 0 {
				t.Errorf("a refused save wrote settings.yaml: %#v", settings)
			}
			if backups := dshBackupNames(t, home, dshSettingsFileName); len(backups) != 0 {
				t.Errorf("a refused save left backups: %v", backups)
			}
		})
	}
}

// TestDshRateLimitedFailureNamesTheRegenerateAction pins the parameter a
// localized surface turns into a clickable "regenerate the key" step. Without
// it the user is told to wait for a reset that will never come.
func TestDshRateLimitedFailureNamesTheRegenerateAction(t *testing.T) {
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("deepseek/deepseek-v4.1-flash")
	gateway.failChat(http.StatusTooManyRequests,
		`{"error":{"code":"RATE_LIMITED","message":"Your limit resets at 2026-09-21T00:00:00Z"}}`)

	err := dshUpsertVerify(t, map[string]any{
		"id":       "command-code2",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Params["action"] != "regenerate_key" {
		t.Errorf("params = %#v, want action=regenerate_key", failure.Params)
	}
	if failure.Params["reset_at_local"] == "" {
		t.Errorf("params = %#v, want the reset instant in local time", failure.Params)
	}
	if failure.Params["model"] != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("params = %#v, want the model", failure.Params)
	}
}

// TestDshProviderUpsertRefusesModelMissingFromTheCatalog is the "lost the
// deepseek/ prefix" case: it looks like a normal id, and the only thing that
// knows it is wrong is the provider's own list.
func TestDshProviderUpsertRefusesModelMissingFromTheCatalog(t *testing.T) {
	home := dshTestHome(t)
	dshInstalledGateway.serve("deepseek/deepseek-v4.1-flash")

	err := dshUpsertVerify(t, map[string]any{
		"id":       "command-code2",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "deepseek-v4.1-flash"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindUnknownModel {
		t.Fatalf("kind = %q, want %q", failure.Kind, providerProbeKindUnknownModel)
	}
	if !strings.Contains(failure.Message, "deepseek-v4.1-flash") {
		t.Errorf("message does not name the model: %s", failure.Message)
	}
	if _, chats := dshInstalledGateway.calls(); chats != 0 {
		t.Errorf("chat probe ran %d times for an unknown model", chats)
	}
	if settings := dshSettingsAfter(t, home); len(settings) != 0 {
		t.Errorf("a refused save wrote settings.yaml: %#v", settings)
	}
	if backups := dshBackupNames(t, home, dshSettingsFileName); len(backups) != 0 {
		t.Errorf("a refused save left backups: %v", backups)
	}
}

// TestDshProviderUpsertVerifiesTheNamedModel pins that the caller can pick
// which model the completion runs against; the preset's first entry is only
// the default.
func TestDshProviderUpsertVerifiesTheNamedModel(t *testing.T) {
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("m-first", "m-second")

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":           "p1",
		"base_url":     "https://example.invalid/v1",
		"models":       []map[string]any{{"id": "m-first"}, {"id": "m-second"}},
		"verify_model": "m-second",
		"api_key":      "sk-test-xxxx",
	})

	if got := gateway.lastChatBody["model"]; got != "m-second" {
		t.Errorf("probe model = %#v, want m-second", got)
	}
}

// TestDshProviderUpsertRefusesWithoutCredential pins that an unverifiable save
// is refused rather than written: a preset whose apiKeyEnv resolves to nothing
// is already a route that fails at request time.
func TestDshProviderUpsertRefusesWithoutCredential(t *testing.T) {
	home := dshTestHome(t)

	err := dshApply(t, providerActionUpsert, map[string]any{
		"id":       "p1",
		"base_url": "https://example.invalid/v1",
		"models":   []map[string]any{{"id": "m-1"}},
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindMissingCredential {
		t.Errorf("kind = %q, want %q", failure.Kind, providerProbeKindMissingCredential)
	}
	if backups := dshBackupNames(t, home, dshSettingsFileName); len(backups) != 0 {
		t.Errorf("a refused save left backups: %v", backups)
	}
}

// ---------------------------------------------------------------------------
// Route inference
// ---------------------------------------------------------------------------

// TestDshProviderUpsertAcceptsAHandTypedModelWithoutACatalog pins the fallback
// the parent ticket keeps: the listing fills a form in, it is not a
// precondition for a save. A gateway with no /models still has to be
// configurable by hand — otherwise the change makes those gateways strictly
// harder to save than before it, and the manual path the error copy points at
// is a dead end.
func TestDshProviderUpsertAcceptsAHandTypedModelWithoutACatalog(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			home := dshTestHome(t)
			gateway := dshInstalledGateway
			gateway.failModels(status)

			dshApplyOK(t, providerActionUpsert, map[string]any{
				"id":       "no-catalog",
				"api":      providerAPIOpenAICompletions,
				"base_url": "https://api.example.invalid/provider/v1",
				"models":   []map[string]any{{"id": "hand/typed-model"}},
				"api_key":  "sk-test-xxxx",
			})

			// The completion is the step that proved it, and it ran on the
			// hand-typed id.
			if _, chats := gateway.calls(); chats != 1 {
				t.Fatalf("chat probes = %d, want the completion to still run", chats)
			}
			if got := gateway.lastChatBody["model"]; got != "hand/typed-model" {
				t.Errorf("probe model = %#v", got)
			}
			settings := dshSettingsAfter(t, home)
			// Nothing answered the protocol question, so the caller's declared
			// value is what the route records.
			if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "no-catalog", "api"); got != providerAPIOpenAICompletions {
				t.Errorf("api = %#v, want the declared protocol", got)
			}
			if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "no-catalog", "models", 0, "id"); got != "hand/typed-model" {
				t.Errorf("model id = %#v", got)
			}
		})
	}
}

// TestDshProviderUpsertWithoutACatalogStillRefusesAnUnusableModel pins that
// dropping the listing does not drop the check that matters: with no catalog to
// consult, the completion is the only gate left, and it has to stay shut.
func TestDshProviderUpsertWithoutACatalogStillRefusesAnUnusableModel(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.failModels(http.StatusNotFound)
	gateway.failChat(http.StatusForbidden, `{"error":{"code":"MODEL_NOT_IN_PLAN"}}`)

	err := dshUpsertVerify(t, map[string]any{
		"id":       "no-catalog",
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "hand/typed-model"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindModelNotInPlan {
		t.Fatalf("kind = %q, want %q", failure.Kind, providerProbeKindModelNotInPlan)
	}
	if settings := dshSettingsAfter(t, home); len(settings) != 0 {
		t.Errorf("a refused save wrote settings.yaml: %#v", settings)
	}
}

// TestDshProviderFailureKeepsTheGatewayBodyOutOfParams pins what error_params
// is: the daemon's own facts. The gateway's prose leaves in `error`, which goes
// through the credential filter; a raw second copy in params is how a key
// fragment a gateway echoed back travels to the server and into the UI under a
// name that promises it holds no provider text.
func TestDshProviderFailureKeepsTheGatewayBodyOutOfParams(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("m1")
	gateway.failChat(http.StatusBadRequest,
		fmt.Sprintf(`{"error":"invalid api key %s"}`, secret))

	err := dshUpsertVerify(t, map[string]any{
		"id":       "leaky",
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "m1"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	for key, value := range failure.Params {
		if strings.Contains(value, secret) {
			t.Errorf("error_params[%q] carries the gateway body: %q", key, value)
		}
		if strings.Contains(value, "invalid api key") {
			t.Errorf("error_params[%q] carries gateway prose: %q", key, value)
		}
	}
	// The message is where that prose belongs, and it is filtered there.
	if strings.Contains(failure.Message, secret) {
		t.Errorf("the gateway's echoed key survived into the message: %s", failure.Message)
	}
	if !strings.Contains(failure.Message, "[REDACTED API KEY]") {
		t.Errorf("message was not passed through the credential filter: %s", failure.Message)
	}
	if !strings.Contains(failure.Message, "invalid api key") {
		t.Errorf("message dropped the gateway's reason entirely: %s", failure.Message)
	}
}

// TestDshProviderFailureTextIsBounded pins the other half of the display limit:
// it still applies where it was meant to. A gateway that answers with a page of
// HTML must not put that page into a message.
func TestDshProviderFailureTextIsBounded(t *testing.T) {
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("m1")
	gateway.failChat(http.StatusBadRequest, strings.Repeat("<html>nope</html>", 200))

	err := dshUpsertVerify(t, map[string]any{
		"id":       "verbose",
		"api":      providerAPIOpenAICompletions,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "m1"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if len(failure.Message) > providerProbeDetailLimit+200 {
		t.Errorf("message is %d bytes, want the body truncated to ~%d", len(failure.Message), providerProbeDetailLimit)
	}
}

// TestDshProviderUpsertInfersDeepSeekThinkingFormat pins the compat switch
// whose absence is invisible: the reply renders blank and nothing errors.
func TestDshProviderUpsertInfersDeepSeekThinkingFormat(t *testing.T) {
	home := dshTestHome(t)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "command-code2",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}},
		"api_key":  "sk-test-xxxx",
	})

	settings := dshSettingsAfter(t, home)
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code2", "compat", "thinkingFormat"); got != providerThinkingFormatDeepSeek {
		t.Fatalf("thinkingFormat = %#v, want %q", got, providerThinkingFormatDeepSeek)
	}
}

// TestDshProviderUpsertLeavesUnrelatedModelsWithoutAThinkingFormat pins that
// the switch is inferred per family, not stamped on every route.
func TestDshProviderUpsertLeavesUnrelatedModelsWithoutAThinkingFormat(t *testing.T) {
	home := dshTestHome(t)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "p1",
		"base_url": "https://example.invalid/v1",
		"models":   []map[string]any{{"id": "glm-5.2"}},
		"api_key":  "sk-test-xxxx",
	})

	settings := dshSettingsAfter(t, home)
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "p1", "compat"); got != nil {
		t.Fatalf("compat written for a model that needs none: %#v", got)
	}
}

// TestDshProviderUpsertRoutesBySupportedEndpoints pins that the endpoint's own
// answer decides the protocol — including which path the completion probe uses.
func TestDshProviderUpsertRoutesBySupportedEndpoints(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serveMessagesModel("claude-sonnet-5")

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "anthropic-route",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "claude-sonnet-5"}},
		"api_key":  "sk-test-xxxx",
	})

	if got := gateway.lastChatPath; got != "/provider/v1"+providerEndpointMessages {
		t.Errorf("probe path = %q, want the anthropic endpoint", got)
	}
	if got := gateway.lastChatKey; got != "sk-test-xxxx" {
		t.Errorf("anthropic probe sent x-api-key = %q", got)
	}
	settings := dshSettingsAfter(t, home)
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "anthropic-route", "api"); got != providerAPIAnthropicMessages {
		t.Fatalf("api = %#v, want %q", got, providerAPIAnthropicMessages)
	}
}

// TestDshProviderUpsertRefusesAPresetWhoseModelsNeedTwoProtocols pins the
// guard behind the protocol inference: one preset carries one protocol, so a
// mixed list would send half its models down the wrong endpoint at run time.
func TestDshProviderUpsertRefusesAPresetWhoseModelsNeedTwoProtocols(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("deepseek/deepseek-v4.1-flash")
	gateway.serveMessagesModel("claude-sonnet-5")

	err := dshUpsertVerify(t, map[string]any{
		"id":       "mixed",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}, {"id": "claude-sonnet-5"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindMixedProtocols {
		t.Fatalf("kind = %q, want %q (message: %s)", failure.Kind, providerProbeKindMixedProtocols, failure.Message)
	}
	if !strings.Contains(failure.Message, "claude-sonnet-5") {
		t.Errorf("message does not name the disagreeing model: %s", failure.Message)
	}
	if settings := dshSettingsAfter(t, home); len(settings) != 0 {
		t.Errorf("a refused save wrote settings.yaml: %#v", settings)
	}
}

// TestDshProviderProtocolForModel pins the three-way rule directly: the
// endpoint answers when it can, a declared protocol survives an endpoint that
// says nothing, and a declared protocol this build cannot serve is refused
// rather than recorded as a different one.
func TestDshProviderProtocolForModel(t *testing.T) {
	cases := []struct {
		name     string
		model    providerDiscoveredModel
		declared string
		want     string
		wantErr  bool
	}{
		{
			name:  "chat completions route",
			model: providerDiscoveredModel{SupportedEndpoints: []string{providerEndpointChatCompletions}},
			want:  providerAPIOpenAICompletions,
		},
		{
			name:  "messages route",
			model: providerDiscoveredModel{SupportedEndpoints: []string{providerEndpointMessages}},
			want:  providerAPIAnthropicMessages,
		},
		{
			name:  "responses route",
			model: providerDiscoveredModel{SupportedEndpoints: []string{providerEndpointResponses}},
			want:  providerAPIOpenAIResponses,
		},
		{
			name:  "completions route wins over responses when a model lists both",
			model: providerDiscoveredModel{SupportedEndpoints: []string{providerEndpointResponses, providerEndpointChatCompletions}},
			want:  providerAPIOpenAICompletions,
		},
		{
			name:  "messages route wins over both",
			model: providerDiscoveredModel{SupportedEndpoints: []string{providerEndpointChatCompletions, providerEndpointMessages}},
			want:  providerAPIAnthropicMessages,
		},
		{
			name:     "silent endpoint keeps the declared anthropic protocol",
			model:    providerDiscoveredModel{},
			declared: providerAPIAnthropicMessages,
			want:     providerAPIAnthropicMessages,
		},
		{
			name:     "silent endpoint keeps the declared responses protocol",
			model:    providerDiscoveredModel{},
			declared: providerAPIOpenAIResponses,
			want:     providerAPIOpenAIResponses,
		},
		{
			name:     "silent endpoint keeps the declared completions protocol",
			model:    providerDiscoveredModel{},
			declared: providerAPIOpenAICompletions,
			want:     providerAPIOpenAICompletions,
		},
		{
			name:  "silent endpoint with nothing declared",
			model: providerDiscoveredModel{},
			want:  providerAPIOpenAICompletions,
		},
		{
			name:     "a protocol this build cannot serve is refused",
			model:    providerDiscoveredModel{},
			declared: "openai-embeddings",
			wantErr:  true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := providerProtocolForModel(testCase.model, testCase.declared)
			if testCase.wantErr {
				failure := dshFailure(t, err)
				if failure.Kind != providerProbeKindUnsupportedProtocol {
					t.Errorf("kind = %q, want %q", failure.Kind, providerProbeKindUnsupportedProtocol)
				}
				return
			}
			if err != nil {
				t.Fatalf("protocol: %v", err)
			}
			if got != testCase.want {
				t.Errorf("protocol = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestDshProviderUpsertHonoursTheDeclaredResponsesProtocol pins that the third
// protocol the form offers survives the save. It used to be normalized to
// openai-completions: the save reported success, settings.yaml recorded a
// protocol the user never chose, and nothing anywhere said so.
func TestDshProviderUpsertHonoursTheDeclaredResponsesProtocol(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	// A gateway with no model list is the case where the declaration is the
	// only source for the protocol, and therefore where it was ignored.
	gateway.failModels(http.StatusNotFound)

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "responses-route",
		"api":      providerAPIOpenAIResponses,
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "gpt-5.1"}},
		"api_key":  "sk-test-xxxx",
	})

	// The completion is what proved the route, and it has to run against the
	// endpoint the recorded protocol names — a probe that certified
	// /chat/completions while settings.yaml says openai-responses would
	// vouch for a request DSH will never make.
	if got := gateway.lastChatPath; got != "/provider/v1"+providerEndpointResponses {
		t.Errorf("probe path = %q, want the responses endpoint", got)
	}
	if got := gateway.lastChatBody["input"]; got != providerProbePrompt {
		t.Errorf("responses probe body input = %#v, want the probe prompt", got)
	}
	if got := gateway.lastChatBody["max_output_tokens"]; got != float64(providerProbeResponsesMinTokens) {
		t.Errorf("responses probe max_output_tokens = %#v, want %d", got, providerProbeResponsesMinTokens)
	}
	settings := dshSettingsAfter(t, home)
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "responses-route", "api"); got != providerAPIOpenAIResponses {
		t.Fatalf("api = %#v, want the declared %q", got, providerAPIOpenAIResponses)
	}
}

// TestDshProviderUpsertRoutesByAdvertisedResponsesEndpoint pins the other half
// of the same rule: when the gateway does describe its endpoints, the one it
// names decides the protocol — and it is the one the probe runs against.
func TestDshProviderUpsertRoutesByAdvertisedResponsesEndpoint(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serveResponsesModel("gpt-5.1")

	dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":       "responses-route",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "gpt-5.1"}},
		"api_key":  "sk-test-xxxx",
	})

	if got := gateway.lastChatPath; got != "/provider/v1"+providerEndpointResponses {
		t.Errorf("probe path = %q, want the responses endpoint", got)
	}
	settings := dshSettingsAfter(t, home)
	if got := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "responses-route", "api"); got != providerAPIOpenAIResponses {
		t.Fatalf("api = %#v, want %q", got, providerAPIOpenAIResponses)
	}
}

// TestDshProviderUpsertRefusesAProtocolItCannotRoute pins the other side of
// the same defect: a declaration this build cannot serve is refused outright,
// never quietly recorded as something else.
func TestDshProviderUpsertRefusesAProtocolItCannotRoute(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.failModels(http.StatusNotFound)

	err := dshUpsertVerify(t, map[string]any{
		"id":       "unsupported-route",
		"api":      "openai-embeddings",
		"base_url": "https://api.example.invalid/provider/v1",
		"models":   []map[string]any{{"id": "m1"}},
		"api_key":  "sk-test-xxxx",
	})
	failure := dshFailure(t, err)
	if failure.Kind != providerProbeKindUnsupportedProtocol {
		t.Fatalf("kind = %q, want %q (message: %s)", failure.Kind, providerProbeKindUnsupportedProtocol, failure.Message)
	}
	if _, chats := gateway.calls(); chats != 0 {
		t.Errorf("chat probes = %d, want none for a protocol that cannot be probed", chats)
	}
	if settings := dshSettingsAfter(t, home); len(settings) != 0 {
		t.Errorf("a refused save wrote settings.yaml: %#v", settings)
	}
}

// TestParseProviderModelsAcceptsBothCapacitySpellings pins the sample shape
// from the ticket, including an id that carries a slash.
func TestParseProviderModelsAcceptsBothCapacitySpellings(t *testing.T) {
	models, err := parseProviderModels([]byte(`{"object":"list","data":[
		{"id":"deepseek/deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash",
		 "context_length":1000000,"supported_endpoints":["/chat/completions"]},
		{"id":"claude-sonnet-5","name":"Claude Sonnet 5","context_window":200000},
		{"id":"","name":"nameless"}
	]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want the nameless entry dropped", models)
	}
	if models[0].ID != "deepseek/deepseek-v4.1-flash" || models[0].ContextWindow != 1000000 {
		t.Errorf("first model = %+v", models[0])
	}
	if models[0].SupportedEndpoints[0] != providerEndpointChatCompletions {
		t.Errorf("supported endpoints = %#v", models[0].SupportedEndpoints)
	}
	if models[1].ContextWindow != 200000 {
		t.Errorf("context_window spelling ignored: %+v", models[1])
	}

	// `models` is the other envelope spelling the same listing comes in. It
	// was supported before this feature consolidated the fetch, and dropping
	// it would silently break a gateway that answers this way.
	renamed, err := parseProviderModels([]byte(`{"models":[{"id":"m2","name":"Second"}]}`))
	if err != nil {
		t.Fatalf("parse the models envelope: %v", err)
	}
	if len(renamed) != 1 || renamed[0].ID != "m2" {
		t.Errorf("models envelope = %+v", renamed)
	}
}

// ---------------------------------------------------------------------------
// Integration against the fake gateway
// ---------------------------------------------------------------------------

// TestDshProviderUpsertVerifyActivateRoundTrip runs the whole flow the UI
// drives and pins that the file it leaves behind is the shape a fixture
// already has.
func TestDshProviderUpsertVerifyActivateRoundTrip(t *testing.T) {
	home := dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("deepseek/deepseek-v4.1-flash")

	snapshot := dshApplyOK(t, providerActionUpsert, map[string]any{
		"id":          "command-code2",
		"api":         providerAPIOpenAICompletions,
		"base_url":    "https://api.example.invalid/provider/v1",
		"api_key_env": "MULTICA_COMMAND_CODE2_API_KEY",
		"models": []map[string]any{{
			"id": "deepseek/deepseek-v4.1-flash", "name": "DeepSeek V4.1 Flash", "context_window": 1000000,
		}},
		"api_key": "sk-test-xxxx",
	})
	if len(snapshot.Providers) != 1 || snapshot.Providers[0].ID != "command-code2" {
		t.Fatalf("providers = %+v", snapshot.Providers)
	}
	if !snapshot.Providers[0].HasKey {
		t.Error("the saved preset reports no key")
	}

	snapshot = dshApplyOK(t, providerActionActivate, map[string]any{"id": "command-code2"})
	if snapshot.Active == nil || snapshot.Active.Model != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("active = %+v", snapshot.Active)
	}

	settings := dshSettingsAfter(t, home)
	entry := dshMapPath(t, settings, dshProviderRootKey, dshProvidersKey, "command-code2")
	entryMap, ok := entry.(map[string]any)
	if !ok {
		t.Fatalf("preset = %#v", entry)
	}
	if entryMap["apiKeyEnv"] != "MULTICA_COMMAND_CODE2_API_KEY" {
		t.Errorf("apiKeyEnv = %#v", entryMap["apiKeyEnv"])
	}
	if entryMap["api"] != providerAPIOpenAICompletions {
		t.Errorf("api = %#v", entryMap["api"])
	}
	if entryMap["baseURL"] != "https://api.example.invalid/provider/v1" {
		t.Errorf("baseURL = %#v", entryMap["baseURL"])
	}
	compat, _ := entryMap["compat"].(map[string]any)
	if compat["thinkingFormat"] != providerThinkingFormatDeepSeek {
		t.Errorf("compat = %#v", entryMap["compat"])
	}
	models, ok := entryMap["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v", entryMap["models"])
	}
	model := models[0].(map[string]any)
	if model["id"] != "deepseek/deepseek-v4.1-flash" || model["name"] != "DeepSeek V4.1 Flash" {
		t.Errorf("model entry = %#v", model)
	}
	if model["contextWindow"] != 1000000 {
		t.Errorf("model contextWindow = %#v", model["contextWindow"])
	}
	active := dshMapPath(t, settings, dshActiveModelKey)
	activeMap, ok := active.(map[string]any)
	if !ok || activeMap["provider"] != "command-code2" || activeMap["model"] != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("agent-default-model = %#v", active)
	}
}

// TestHandleProviderConfigReportsProbeFailure pins that the classified failure
// reaches the server, which is the only place a surface can read it.
func TestHandleProviderConfigReportsProbeFailure(t *testing.T) {
	withFastLocalSkillReportBackoffs(t)
	dshTestHome(t)
	gateway := dshInstalledGateway
	gateway.serve("deepseek/deepseek-v4.1-flash")
	gateway.failChat(http.StatusTooManyRequests,
		`{"error":{"code":"RATE_LIMITED","message":"Your limit resets at 2026-09-21T00:00:00Z"}}`)

	var body map[string]any
	d, _ := localSkillReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	d.handleProviderConfig(context.Background(), Runtime{ID: "rt-1"}, PendingProviderConfig{
		ID:       "req-1",
		Provider: "dsh",
		Action:   providerActionUpsert,
		Payload: dshJSON(t, map[string]any{
			"id":       "command-code2",
			"base_url": "https://api.example.invalid/provider/v1",
			"models":   []map[string]any{{"id": "deepseek/deepseek-v4.1-flash"}},
			"api_key":  "sk-test-xxxx",
		}),
	})

	if body["status"] != "failed" {
		t.Fatalf("report = %#v", body)
	}
	if body["error_kind"] != providerProbeKindRateLimited {
		t.Errorf("error_kind = %#v, want %q", body["error_kind"], providerProbeKindRateLimited)
	}
	params, ok := body["error_params"].(map[string]any)
	if !ok || params["action"] != "regenerate_key" {
		t.Errorf("error_params = %#v", body["error_params"])
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-xxxx") {
		t.Fatalf("the failure report echoed the key: %s", encoded)
	}
}

// TestHandleProviderConfigReportsDiscoveredModels pins that the models action
// answers with the catalog, so the picker is one round trip.
func TestHandleProviderConfigReportsDiscoveredModels(t *testing.T) {
	withFastLocalSkillReportBackoffs(t)
	dshTestHome(t)
	dshInstalledGateway.serve("deepseek/deepseek-v4.1-flash")

	var body map[string]any
	d, _ := localSkillReportDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode report: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	d.handleProviderConfig(context.Background(), Runtime{ID: "rt-1"}, PendingProviderConfig{
		ID:       "req-1",
		Provider: "dsh",
		Action:   providerActionModels,
		Payload: dshJSON(t, map[string]any{
			"base_url": "https://api.example.invalid/provider/v1",
			"api_key":  "sk-test-xxxx",
		}),
	})

	models, ok := body["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %#v", body["models"])
	}
	entry := models[0].(map[string]any)
	if entry["id"] != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("model = %#v", entry)
	}
}
