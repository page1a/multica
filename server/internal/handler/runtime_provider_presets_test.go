package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/testutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The provider-preset flow carries an API key from a browser to the daemon
// through the server. These tests pin the one property that makes that
// acceptable: the key is delivered once and kept nowhere.

const providerPresetTestKey = "sk-test-xxxx"

func providerPresetTestPayload(t *testing.T, apiKey string) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id":       "test-provider",
		"api":      "openai-completions",
		"base_url": "https://example.invalid/v1",
		"models":   []map[string]any{{"id": "test-model", "name": "Test Model"}},
		"api_key":  apiKey,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return payload
}

func providerPresetPayloadHasKey(t *testing.T, payload json.RawMessage) bool {
	t.Helper()
	if len(payload) == 0 {
		return false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return false
	}
	_, present := body["api_key"]
	return present
}

func TestRedactProviderPresetPayload(t *testing.T) {
	t.Run("strips the key and keeps everything else", func(t *testing.T) {
		got := redactProviderPresetPayload(json.RawMessage(`{"id":"p1","api_key":"sk-test-xxxx","base_url":"https://x.invalid"}`))
		if strings.Contains(string(got), "api_key") || strings.Contains(string(got), providerPresetTestKey) {
			t.Fatalf("key survived redaction: %s", got)
		}
		var body map[string]any
		if err := json.Unmarshal(got, &body); err != nil {
			t.Fatalf("redacted payload is not valid JSON: %v", err)
		}
		if body["id"] != "p1" || body["base_url"] != "https://x.invalid" {
			t.Fatalf("redaction dropped unrelated fields: %s", got)
		}
	})

	t.Run("leaves a payload with no key alone", func(t *testing.T) {
		in := json.RawMessage(`{"id":"p1"}`)
		if got := redactProviderPresetPayload(in); string(got) != string(in) {
			t.Fatalf("payload changed: %s", got)
		}
	})

	t.Run("drops a body it cannot rewrite", func(t *testing.T) {
		for _, in := range []json.RawMessage{nil, {}, json.RawMessage(`"sk-test-xxxx"`), json.RawMessage(`not json`)} {
			if got := redactProviderPresetPayload(in); got != nil {
				t.Errorf("payload %q survived as %s", in, got)
			}
		}
	})
}

func TestInMemoryProviderPresetStore_KeyIsDeliveredOnceAndKeptNowhere(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryProviderPresetStore()

	req, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionUpsert, providerPresetTestPayload(t, providerPresetTestKey))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A poll must not hand the key back, and must not blank the record a later
	// heartbeat still has to deliver.
	polled, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if providerPresetPayloadHasKey(t, polled.Payload) {
		t.Fatalf("the poll response carried the key: %s", polled.Payload)
	}
	if polled.Payload == nil {
		t.Fatalf("the poll response lost the payload entirely: %+v", polled)
	}

	// The claim is the delivery: it hands over the key...
	claimed, err := store.PopPending(ctx, "runtime-1")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if claimed == nil || claimed.ID != req.ID {
		t.Fatalf("claim = %+v, want %s", claimed, req.ID)
	}
	if !providerPresetPayloadHasKey(t, claimed.Payload) {
		t.Fatalf("the claim did not carry the key, so the daemon can never receive it: %s", claimed.Payload)
	}

	// ...and the stored record is left without it.
	stored, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after claim: %v", err)
	}
	if providerPresetPayloadHasKey(t, stored.Payload) {
		t.Fatalf("the request store kept the key after the claim: %s", stored.Payload)
	}
	if stored.Status != ProviderPresetRunning {
		t.Fatalf("status after claim = %s", stored.Status)
	}

	if err := store.Complete(ctx, req.ID, ProviderPresetResult{
		Providers: []ProviderPresetEntry{{ID: "test-provider"}},
		Active:    &ProviderPresetActive{Provider: "test-provider", Model: "test-model"},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	completed, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after complete: %v", err)
	}
	if completed.Status != ProviderPresetCompleted || len(completed.Providers) != 1 {
		t.Fatalf("completion not stored: %+v", completed)
	}
	if completed.Active == nil || completed.Active.Model != "test-model" {
		t.Fatalf("active model not stored: %+v", completed.Active)
	}
	if providerPresetPayloadHasKey(t, completed.Payload) {
		t.Fatalf("the key came back on the completed record: %s", completed.Payload)
	}
}

func TestInMemoryProviderPresetStore_FailKeepsTheKeyOut(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryProviderPresetStore()

	req, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionUpsert, providerPresetTestPayload(t, providerPresetTestKey))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.PopPending(ctx, "runtime-1"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if err := store.Fail(ctx, req.ID, ProviderPresetFailure{Kind: "provider_error", Message: "daemon said no"}); err != nil {
		t.Fatalf("fail: %v", err)
	}
	failed, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if failed.Status != ProviderPresetFailed || failed.Error != "daemon said no" {
		t.Fatalf("failure not stored: %+v", failed)
	}
	if providerPresetPayloadHasKey(t, failed.Payload) {
		t.Fatalf("the key survived a failed request: %s", failed.Payload)
	}
}

func TestInMemoryProviderPresetStore_TerminalRecordIgnoresSecondReport(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryProviderPresetStore()

	req, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionList, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Complete(ctx, req.ID, ProviderPresetResult{}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.Fail(ctx, req.ID, ProviderPresetFailure{Message: "late report"}); err != nil {
		t.Fatalf("fail: %v", err)
	}
	// The handler checks terminality before calling Fail; the store itself is
	// last-writer-wins, which is why the check exists. This test documents the
	// division so a future reader does not "simplify" the handler check away.
	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != ProviderPresetFailed {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestInMemoryProviderPresetStore_OnlyClaimsItsOwnRuntime(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryProviderPresetStore()

	if _, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionList, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := store.PopPending(ctx, "runtime-2")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if claimed != nil {
		t.Fatalf("claimed another runtime's request: %+v", claimed)
	}
	pending, err := store.HasPending(ctx, "runtime-1")
	if err != nil || !pending {
		t.Fatalf("runtime-1 pending = %v (%v)", pending, err)
	}
}

func TestApplyProviderPresetTimeout(t *testing.T) {
	now := time.Now()
	pending := &ProviderPresetRequest{Status: ProviderPresetPending, CreatedAt: now.Add(-time.Minute)}
	if !applyProviderPresetTimeout(pending, now) || pending.Status != ProviderPresetTimeout {
		t.Fatalf("pending timeout did not fire: %+v", pending)
	}
	started := now.Add(-2 * time.Minute)
	running := &ProviderPresetRequest{Status: ProviderPresetRunning, RunStartedAt: &started}
	if !applyProviderPresetTimeout(running, now) || running.Status != ProviderPresetTimeout {
		t.Fatalf("running timeout did not fire: %+v", running)
	}
	fresh := &ProviderPresetRequest{Status: ProviderPresetPending, CreatedAt: now}
	if applyProviderPresetTimeout(fresh, now) {
		t.Fatal("a fresh request must not time out")
	}
	terminal := &ProviderPresetRequest{Status: ProviderPresetCompleted, CreatedAt: now.Add(-time.Hour)}
	if applyProviderPresetTimeout(terminal, now) {
		t.Fatal("a terminal request must not transition again")
	}
}

func TestValidProviderPresetAction(t *testing.T) {
	for _, action := range []string{"list", "models", "upsert", "delete", "activate", "replay"} {
		if !validProviderPresetAction(action) {
			t.Errorf("%q should be accepted", action)
		}
	}
	// Replay rewrites the owner's settings.yaml from the daemon's own record,
	// so it is a write however little the caller supplies: the request body is
	// empty, but the effect is the same class as an upsert's.
	if !providerPresetActionWrites(ProviderPresetActionReplay) {
		t.Error("replay must be owner-only — it writes the owner's configuration file")
	}
	// `refresh` wrote the endpoint's whole catalog into the preset, which both
	// overwrote the user's own selection and could mix two wire protocols in
	// one route. `models` + `upsert` replace it.
	for _, action := range []string{"", "List", "remove", "refresh"} {
		if validProviderPresetAction(action) {
			t.Errorf("%q should be rejected", action)
		}
	}
}

// providerPresetTestRuntime gives the flow an online runtime owned by the test
// user, which is what the read-access check requires.
func providerPresetTestRuntime(t *testing.T) string {
	t.Helper()
	return dbfx.Runtime(t, "provider-preset-"+t.Name(), testutil.Cols{"provider": "dsh"})
}

// ---------------------------------------------------------------------------
// End-to-end, over the real HTTP handlers
// ---------------------------------------------------------------------------

func TestProviderPresetFlow_EndToEnd(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := providerPresetTestRuntime(t)

	// 1. The browser enqueues an action. The response names the request and
	//    nothing else — the body the caller sent may hold a key.
	w := httptest.NewRecorder()
	testHandler.InitiateProviderPresetAction(w, withURLParams(
		newRequestAsUser(testUserID, http.MethodPost, "/api/runtimes/"+runtimeID+"/provider-presets", map[string]any{
			"provider": "dsh",
			"action":   ProviderPresetActionUpsert,
			"payload": map[string]any{
				"id":       "e2e-provider",
				"api":      "openai-completions",
				"base_url": "https://example.invalid/v1",
				"models":   []map[string]any{{"id": "e2e-model"}},
				"api_key":  providerPresetTestKey,
			},
		}),
		"runtimeId", runtimeID,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("InitiateProviderPresetAction: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), providerPresetTestKey) {
		t.Fatalf("the POST response echoed the key: %s", w.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if created.ID == "" || created.Status != string(ProviderPresetPending) {
		t.Fatalf("created = %+v", created)
	}

	// 2. The daemon's next heartbeat claims it, and that frame is the one
	//    place the key is supposed to be.
	w = httptest.NewRecorder()
	testHandler.DaemonHeartbeat(w, newDaemonTokenRequest(http.MethodPost, "/api/daemon/heartbeat", map[string]any{
		"runtime_id": runtimeID,
	}, testWorkspaceID, "provider-preset-e2e-daemon"))
	if w.Code != http.StatusOK {
		t.Fatalf("DaemonHeartbeat: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var heartbeat map[string]any
	if err := json.NewDecoder(w.Body).Decode(&heartbeat); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	pending, ok := heartbeat["pending_provider_config"].(map[string]any)
	if !ok {
		t.Fatalf("heartbeat carried no provider config request: %v", heartbeat)
	}
	if pending["id"] != created.ID || pending["provider"] != "dsh" || pending["action"] != ProviderPresetActionUpsert {
		t.Fatalf("pending = %v", pending)
	}
	delivered, err := json.Marshal(pending["payload"])
	if err != nil {
		t.Fatalf("marshal delivered payload: %v", err)
	}
	if !strings.Contains(string(delivered), providerPresetTestKey) {
		t.Fatalf("the daemon never received the key: %s", delivered)
	}

	// 3. The stored record has already dropped it.
	stored, err := testHandler.ProviderPresetStore.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if stored == nil {
		t.Fatal("the request vanished from the store")
	}
	if providerPresetPayloadHasKey(t, stored.Payload) {
		t.Fatalf("the request store kept the key: %s", stored.Payload)
	}

	// 4. Polling shows the same redacted record.
	w = httptest.NewRecorder()
	testHandler.GetProviderPresetRequest(w, withURLParams(
		newRequestAsUser(testUserID, http.MethodGet, "/api/runtimes/"+runtimeID+"/provider-presets/"+created.ID, nil),
		"runtimeId", runtimeID,
		"requestId", created.ID,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("GetProviderPresetRequest: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), providerPresetTestKey) {
		t.Fatalf("the poll response carried the key: %s", w.Body.String())
	}

	// 5. The daemon reports the refreshed list; the poll then serves it.
	w = httptest.NewRecorder()
	testHandler.ReportProviderPresetResult(w, withURLParams(
		newDaemonTokenRequest(http.MethodPost, "/api/daemon/runtimes/"+runtimeID+"/provider-presets/"+created.ID+"/result", map[string]any{
			"status": "completed",
			"providers": []map[string]any{{
				"id":          "e2e-provider",
				"api":         "openai-completions",
				"base_url":    "https://example.invalid/v1",
				"api_key_env": "MULTICA_E2E_PROVIDER_API_KEY",
				"key_mask":    "sk-…xxxx",
				"has_key":     true,
				"models":      []map[string]any{{"id": "e2e-model"}},
			}},
			"active": map[string]any{"provider": "e2e-provider", "model": "e2e-model"},
		}, testWorkspaceID, "provider-preset-e2e-daemon"),
		"runtimeId", runtimeID,
		"requestId", created.ID,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("ReportProviderPresetResult: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	testHandler.GetProviderPresetRequest(w, withURLParams(
		newRequestAsUser(testUserID, http.MethodGet, "/api/runtimes/"+runtimeID+"/provider-presets/"+created.ID, nil),
		"runtimeId", runtimeID,
		"requestId", created.ID,
	))
	if w.Code != http.StatusOK {
		t.Fatalf("GetProviderPresetRequest after report: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var final ProviderPresetRequest
	if err := json.NewDecoder(w.Body).Decode(&final); err != nil {
		t.Fatalf("decode final: %v", err)
	}
	if final.Status != ProviderPresetCompleted {
		t.Fatalf("status = %s: %s", final.Status, w.Body.String())
	}
	if len(final.Providers) != 1 || final.Providers[0].ID != "e2e-provider" || !final.Providers[0].HasKey {
		t.Fatalf("providers = %+v", final.Providers)
	}
	if final.Active == nil || final.Active.Model != "e2e-model" {
		t.Fatalf("active = %+v", final.Active)
	}
	if providerPresetPayloadHasKey(t, final.Payload) {
		t.Fatalf("the key reappeared on the completed request: %s", final.Payload)
	}
}

func TestInitiateProviderPresetAction_RejectsUnknownAction(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := providerPresetTestRuntime(t)

	w := httptest.NewRecorder()
	testHandler.InitiateProviderPresetAction(w, withURLParams(
		newRequestAsUser(testUserID, http.MethodPost, "/api/runtimes/"+runtimeID+"/provider-presets", map[string]any{
			"provider": "dsh",
			"action":   "rm -rf",
			"payload":  map[string]any{"id": "p1"},
		}),
		"runtimeId", runtimeID,
	))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetProviderPresetRequest_HidesAnotherWorkspacesRequest(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID := providerPresetTestRuntime(t)

	// A record owned by a different runtime must not be readable through this
	// runtime's path, even within the same workspace.
	other, err := testHandler.ProviderPresetStore.Create(context.Background(), "00000000-0000-0000-0000-000000000000", "dsh", ProviderPresetActionList, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.GetProviderPresetRequest(w, withURLParams(
		newRequestAsUser(testUserID, http.MethodGet, "/api/runtimes/"+runtimeID+"/provider-presets/"+other.ID, nil),
		"runtimeId", runtimeID,
		"requestId", other.ID,
	))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestInitiateProviderPresetAction_WritesAreOwnerOnly pins the gate that
// separates reading this feature from writing it.
//
// A public runtime is readable by every workspace member, and `list` is a read:
// env var names, key masks, model ids — the same inventory local skills and the
// model list already expose. `upsert` is not. It writes into the owner's
// .credentials.yaml and can repoint an existing apiKeyEnv at another baseURL,
// which sends the owner's real key wherever the caller asked. So writes stay
// with the owner, with no workspace-admin override, exactly as local-skill
// import does.
func TestInitiateProviderPresetAction_WritesAreOwnerOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ownerID := dbfx.User(t, "preset-owner", "preset-owner@example.invalid")
	dbfx.Member(t, testWorkspaceID, ownerID, "member")
	runtimeID := dbfx.Runtime(t, "provider-preset-"+t.Name(), testutil.Cols{
		"provider":   "dsh",
		"visibility": "public",
		"owner_id":   ownerID,
	})

	post := func(userID, action string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.InitiateProviderPresetAction(w, withURLParams(
			newRequestAsUser(userID, http.MethodPost, "/api/runtimes/"+runtimeID+"/provider-presets", map[string]any{
				"provider": "dsh",
				"action":   action,
				"payload":  json.RawMessage(providerPresetTestPayload(t, providerPresetTestKey)),
			}),
			"runtimeId", runtimeID,
		))
		return w
	}

	// testUserID is a workspace member but not the runtime's owner.
	for _, action := range []string{
		ProviderPresetActionUpsert,
		ProviderPresetActionDelete,
		ProviderPresetActionActivate,
		// Listing models is a write for this purpose: it makes the owner's
		// daemon send a request to a caller-named host with the owner's stored
		// credential attached when the caller names a preset instead of typing
		// a key.
		ProviderPresetActionModels,
	} {
		if w := post(testUserID, action); w.Code != http.StatusForbidden {
			t.Fatalf("%s by a non-owner: expected 403, got %d: %s", action, w.Code, w.Body.String())
		}
	}

	// Reading the same public runtime stays open to members.
	if w := post(testUserID, ProviderPresetActionList); w.Code != http.StatusOK {
		t.Fatalf("list by a member: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The owner still writes.
	if w := post(ownerID, ProviderPresetActionUpsert); w.Code != http.StatusOK {
		t.Fatalf("upsert by the owner: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
