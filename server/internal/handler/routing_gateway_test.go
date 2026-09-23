package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// routingHealthReportForTest is a minimal healthy report: these tests are
// about the deployment half of the payload, not about the breaker.
func routingHealthReportForTest() routing.HealthReport {
	return routing.HealthReport{State: routing.StateEnabled, Usable: true, Model: "gpt-5.6-luna"}
}

// gatewayHost reduces the deployment's LLM base URL to something the settings
// section can show. The cases that matter are the ones where the raw value
// carries more than a host: a path, a port, or — in a URL somebody pasted
// straight out of a provider dashboard — userinfo credentials. None of those
// may reach the client, and a value that cannot be parsed at all must produce
// nothing rather than be echoed back.
func TestGatewayHostShowsTheHostAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"plain base url", "https://api.openai.com/v1", "api.openai.com"},
		{"with a port", "https://llm.internal.example:8443/v1", "llm.internal.example"},
		{"surrounding whitespace", "  https://api.openai.com/v1  ", "api.openai.com"},
		{"userinfo is dropped", "https://sk-secret:x@gateway.example/v1", "gateway.example"},
		{"empty", "", ""},
		{"not a url", "sk-this-is-a-key-not-a-url", ""},
		{"scheme only", "https://", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayHost(tc.in); got != tc.want {
				t.Fatalf("gatewayHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The payload is assembled from deployment config, so the one thing worth
// pinning is that the API key never appears in it in any form.
func TestRoutingHealthPayloadCarriesNoAPIKey(t *testing.T) {
	h := &Handler{cfg: Config{
		LLMAPIKey:       "sk-super-secret-value",
		LLMBaseURL:      "https://gateway.example/v1",
		LLMDefaultModel: "gpt-5.6-mini",
	}}
	got := h.routingHealthPayload(context.Background(), "", routingHealthReportForTest())
	if got.GatewayHost != "gateway.example" {
		t.Fatalf("GatewayHost = %q", got.GatewayHost)
	}
	if !got.GatewayConfigured {
		t.Fatal("GatewayConfigured = false, want true with both key and base URL set")
	}
	for _, field := range []string{got.GatewayHost, got.GatewayDefaultModel, got.Reason, got.Model} {
		if field == h.cfg.LLMAPIKey {
			t.Fatalf("payload field carries the API key: %q", field)
		}
	}
}

func TestRoutingHealthPayloadReportsAnUnconfiguredDeployment(t *testing.T) {
	// A key with no base URL is still unconfigured: pkg/llm needs both.
	h := &Handler{cfg: Config{LLMAPIKey: "sk-only"}}
	if h.routingHealthPayload(context.Background(), "", routingHealthReportForTest()).GatewayConfigured {
		t.Fatal("GatewayConfigured = true with no base URL")
	}
}

// TestWorkspaceGatewayWinsInThePayload. When the workspace brought its own
// endpoint, the settings section must name THAT host and say whose it is —
// showing the deployment's is how somebody debugs against the wrong server.
func TestWorkspaceGatewayWinsInThePayload(t *testing.T) {
	h := &Handler{cfg: Config{
		LLMAPIKey:  "sk-deployment",
		LLMBaseURL: "https://deployment.example/v1",
	}}
	rep := routingHealthReportForTest()
	rep.BaseURL = "https://sk-pasted:x@workspace.example/v1"
	rep.KeySet = true
	rep.UsesWorkspaceGateway = true

	got := h.routingHealthPayload(context.Background(), "", rep)
	if got.GatewayHost != "workspace.example" {
		t.Fatalf("GatewayHost = %q, want the workspace host", got.GatewayHost)
	}
	if got.GatewayScope != gatewayScopeWorkspace {
		t.Fatalf("GatewayScope = %q, want %q", got.GatewayScope, gatewayScopeWorkspace)
	}
	if !got.GatewayKeySet {
		t.Fatal("GatewayKeySet = false with a workspace key stored")
	}
	// Userinfo in the pasted URL must not survive into the client payload.
	if strings.Contains(got.GatewayHost, "sk-pasted") {
		t.Fatalf("GatewayHost leaked the raw url: %q", got.GatewayHost)
	}
}

// TestWorkspaceGatewayMakesAnUnconfiguredDeploymentConfigured is the state
// that used to read as a dead end: no deployment LLM, and a workspace that
// just supplied its own. GatewayConfigured=false there tells the reader there
// is nothing they can do, at the exact moment they have already done it.
func TestWorkspaceGatewayMakesAnUnconfiguredDeploymentConfigured(t *testing.T) {
	h := &Handler{cfg: Config{}}
	rep := routingHealthReportForTest()
	rep.BaseURL = "https://workspace.example/v1"
	rep.KeySet = true
	rep.UsesWorkspaceGateway = true
	if !h.routingHealthPayload(context.Background(), "", rep).GatewayConfigured {
		t.Fatal("GatewayConfigured = false for a workspace running on its own gateway")
	}
}

// TestKeyStorabilityIsReported so the section can disable the key field up
// front rather than accepting a credential it will then refuse.
func TestKeyStorabilityIsReported(t *testing.T) {
	if (&Handler{}).routingHealthPayload(context.Background(), "", routingHealthReportForTest()).WorkspaceKeyStorable {
		t.Fatal("WorkspaceKeyStorable = true with no secretbox")
	}
	box, err := NewRoutingSecretBox("deployment-jwt-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	if !(&Handler{RoutingSecrets: box}).routingHealthPayload(context.Background(), "", routingHealthReportForTest()).WorkspaceKeyStorable {
		t.Fatal("WorkspaceKeyStorable = false with a secretbox wired")
	}
}

func TestListRoutingModelsUsesWorkspaceTargetAndReturnsIDsOnly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"model-z","object":"model","created":0,"owned_by":"test"},{"id":"model-a","object":"model","created":0,"owned_by":"test"},{"id":"model-a","object":"model","created":0,"owned_by":"test"}]}`)
	}))
	t.Cleanup(upstream.Close)

	previousSettings := []byte{}
	if err := testPool.QueryRow(context.Background(), `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previousSettings); err != nil {
		t.Fatalf("read workspace settings: %v", err)
	}
	previousSecrets := testHandler.RoutingSecrets
	box, err := NewRoutingSecretBox("routing-model-list-test-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	testHandler.RoutingSecrets = box
	sealed, ok := testHandler.sealRoutingKey("workspace-model-key")
	if !ok {
		t.Fatal("sealRoutingKey refused the test key")
	}
	settings, _ := json.Marshal(map[string]any{
		"routing": map[string]any{
			"enabled": true, "model": "", "base_url": upstream.URL, "api_key_enc": sealed,
		},
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, settings, testWorkspaceID); err != nil {
		t.Fatalf("write workspace settings: %v", err)
	}
	t.Cleanup(func() {
		restore := previousSettings
		if len(restore) == 0 {
			restore = []byte(`{}`)
		}
		_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, restore, testWorkspaceID)
		testHandler.RoutingSecrets = previousSecrets
	})

	req := withURLParam(newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/routing/models", nil), "id", testWorkspaceID)
	resp := httptest.NewRecorder()
	testHandler.ListRoutingModels(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("ListRoutingModels status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if gotAuthorization != "Bearer workspace-model-key" {
		t.Fatalf("upstream authorization = %q, want workspace key", gotAuthorization)
	}
	if strings.Contains(resp.Body.String(), "workspace-model-key") {
		t.Fatalf("response leaked the workspace key: %s", resp.Body.String())
	}
	var got routingModelListResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, want := strings.Join(got.Models, ","), "model-a,model-z"; got != want {
		t.Fatalf("models = %q, want %q", got, want)
	}
}

func containsAny(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && strings.Contains(s, sub)
}

// TestWorkspaceResponseNeverCarriesTheSealedKey pins the redaction to the
// path a client actually reads from.
//
// The rule "the key never reaches a client" was covered only as a pure
// transformation of a settings map, which passes just as happily with the
// call removed from workspaceToResponse — the one place that makes the rule
// true. Reading it back through GetWorkspace is what turns a helper somebody
// could delete during a refactor into a guard that fails loudly.
func TestWorkspaceResponseNeverCarriesTheSealedKey(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	previousSettings := []byte{}
	if err := testPool.QueryRow(context.Background(), `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&previousSettings); err != nil {
		t.Fatalf("read workspace settings: %v", err)
	}
	previousSecrets := testHandler.RoutingSecrets
	box, err := NewRoutingSecretBox("workspace-response-redaction-secret")
	if err != nil {
		t.Fatalf("NewRoutingSecretBox: %v", err)
	}
	testHandler.RoutingSecrets = box
	sealed, ok := testHandler.sealRoutingKey("sk-live-must-not-leak")
	if !ok {
		t.Fatal("sealRoutingKey refused the test key")
	}
	settings, _ := json.Marshal(map[string]any{
		"github_enabled": true,
		"routing": map[string]any{
			"enabled": true, "model": "gpt-5.6-luna",
			"base_url": "https://gw.example/v1", "api_key_enc": sealed,
		},
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, settings, testWorkspaceID); err != nil {
		t.Fatalf("write workspace settings: %v", err)
	}
	t.Cleanup(func() {
		restore := previousSettings
		if len(restore) == 0 {
			restore = []byte(`{}`)
		}
		_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET settings = $1 WHERE id = $2`, restore, testWorkspaceID)
		testHandler.RoutingSecrets = previousSecrets
	})

	req := withURLParam(newRequest("GET", "/api/workspaces/"+testWorkspaceID, nil), "id", testWorkspaceID)
	body := testutil.Call(t, testHandler.GetWorkspace, req).Want(http.StatusOK).Text()
	if strings.Contains(body, sealed) || strings.Contains(body, "api_key_enc") {
		t.Fatalf("workspace response carried the sealed routing key: %s", body)
	}
	// The rest of the block has to survive, or every read renders a workspace
	// that looks unconfigured and somebody retypes a key that was already set.
	if !strings.Contains(body, "gpt-5.6-luna") || !strings.Contains(body, "gw.example") {
		t.Fatalf("redaction dropped non-secret routing fields: %s", body)
	}
}
