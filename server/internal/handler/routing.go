package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// routeTimeout bounds one detached routing pass. Generous enough for a small
// JSON completion, short enough that a hung model cannot accumulate goroutines
// across a busy workspace.
const routeTimeout = 45 * time.Second

const routingModelListTimeout = 20 * time.Second

// RouteIssueAsync is the hook. Both call sites — issue creation and status
// change — call this one function; adding routing behaviour for another status
// is a row in the routing state table, never a third hook.
//
// Detached from the request on purpose. The request's context is cancelled the
// moment the response is written, and routing may have to wait on a model;
// running it inline would put an outbound LLM call on the latency path of
// every issue write in the workspace.
//
// Both halves of this are load-bearing for the create path in particular:
// creation and the status change that follows it can land here at nearly the
// same moment, which is exactly the race the conditional writes and the
// one-comment-per-kind index exist to absorb.
func (h *Handler) RouteIssueAsync(r *http.Request, workspaceID, issueID string) {
	if h.Routing == nil || workspaceID == "" || issueID == "" {
		return
	}
	attrs := logger.RequestAttrs(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), routeTimeout)
		defer cancel()
		outcome, err := h.Routing.Route(ctx, workspaceID, issueID)
		if err != nil {
			slog.Warn("routing pass failed",
				append(attrs, "workspace_id", workspaceID, "issue_id", issueID, "error", err)...)
			return
		}
		if outcome.Action == routing.ActionSkipped || outcome.Action == routing.ActionNoop {
			return
		}
		slog.Info("routing pass",
			append(attrs,
				"workspace_id", workspaceID,
				"issue_id", issueID,
				"state", string(outcome.State),
				"action", string(outcome.Action),
				"mentioned", outcome.Mentioned)...)
	}()
}

// RouteIssue is the manual entry point behind POST /api/issues/{id}/route,
// which is what `multica issue route` calls.
//
// It runs the SAME Route as the hooks, synchronously, and reports what it did.
// There is no second implementation: a manual re-run that could disagree with
// the automatic one would be useless for exactly the case you reach for it.
func (h *Handler) RouteIssue(w http.ResponseWriter, r *http.Request) {
	// Resolved through the shared loader: the path segment may be a UUID or a
	// human-readable identifier, and every write below uses the resolved id.
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := util.UUIDToString(issue.WorkspaceID)
	outcome, err := h.Routing.Route(r.Context(), workspaceID, util.UUIDToString(issue.ID))
	if err != nil {
		slog.Warn("manual routing pass failed",
			append(logger.RequestAttrs(r), "issue_id", util.UUIDToString(issue.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "routing failed: "+err.Error())
		return
	}
	resp := map[string]any{
		"state":     string(outcome.State),
		"action":    string(outcome.Action),
		"reason":    outcome.Reason,
		"mentioned": outcome.Mentioned,
		"commented": outcome.Commented,
	}
	if outcome.ExecutorWritten != nil {
		resp["executor"] = outcome.ExecutorWritten.Name
	}
	if !outcome.ReviewerWritten.Empty() {
		resp["reviewer"] = outcome.ReviewerWritten.Label()
		resp["reviewer_type"] = string(outcome.ReviewerWritten.Kind)
		if outcome.ReviewerWritten.ID != "" {
			resp["reviewer_id"] = outcome.ReviewerWritten.ID
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// routingHealthResponse is the read-only health report the settings section
// renders. It is the ONLY surface on which a routing failure is visible to a
// person: the design keeps tickets quiet, so without this a broken model shows
// up nowhere but the server log (DENE-633 review, F2).
//
// It carries no credential and no model output — only the switch state, the
// model identifier the workspace itself typed in, and whether the last call
// worked.
type routingHealthResponse struct {
	State             string  `json:"state"`
	Usable            bool    `json:"usable"`
	Reason            string  `json:"reason"`
	RetryAfterSeconds int     `json:"retry_after_seconds"`
	LastSuccessAt     int64   `json:"last_success_at"`
	LastFailureAt     int64   `json:"last_failure_at"`
	Model             string  `json:"model"`
	Threshold         float64 `json:"threshold"`
	// Gateway* describe WHERE the model identifier above is sent. The box in
	// the settings section is a model id and nothing else, which left the
	// reader with no way to answer "which model is this, on whose endpoint?" —
	// the endpoint and the key are deployment configuration (MULTICA_LLM_*),
	// not workspace configuration, so they are not editable here and were
	// therefore not shown at all. Not showing them did not make the dependency
	// go away; it only made it invisible.
	//
	// GatewayHost is HOST ONLY, parsed out of MULTICA_LLM_BASE_URL. The raw
	// value may carry a path and, in a URL a person pasted, userinfo
	// credentials; taking the host drops both. GatewayDefaultModel is
	// MULTICA_LLM_DEFAULT_MODEL, which is what an empty model box would fall
	// back to elsewhere in the product. Neither is a secret, and the API key
	// itself is never represented here in any form, masked or otherwise.
	GatewayHost         string `json:"gateway_host"`
	GatewayDefaultModel string `json:"gateway_default_model"`
	// GatewayConfigured is false when there is no endpoint to call at all —
	// neither one this workspace supplied nor one the deployment did. It used
	// to mean only the second, which read as "nothing you can do here" at a
	// workspace that could in fact fix it from this very screen.
	GatewayConfigured bool `json:"gateway_configured"`
	// GatewayScope names WHOSE endpoint GatewayHost is: "workspace" when this
	// workspace supplied its own, "deployment" otherwise. The host alone does
	// not say, and the two answers lead to different next actions — edit the
	// field above, or go talk to whoever runs the server.
	GatewayScope string `json:"gateway_scope"`
	// GatewayKeySet reports that a workspace key is stored AND openable. It
	// is never the key, and it is false for a key sealed under a deployment
	// secret this server no longer has — which is a state that must read as
	// "type it again", not as a green chip.
	GatewayKeySet bool `json:"gateway_key_set"`
	// WorkspaceKeyStorable is false when this deployment has no secretbox, so
	// the section can disable the key field up front instead of letting
	// somebody type a credential into a form that will refuse it.
	WorkspaceKeyStorable bool `json:"workspace_key_storable"`
	// GatewayProtocol names the wire format the endpoint speaks: "systemone"
	// for a TypeSafe System One endpoint (Jev), "openai" for everything else.
	//
	// It is derived from the endpoint, never stored, and it exists because the
	// host alone does not answer the question a reader actually has. A
	// workspace that configured Jev and saw only a model id had no way to tell
	// whether its tickets were being judged by a System One model or by a chat
	// model being asked to imitate one.
	GatewayProtocol string `json:"gateway_protocol"`
	// DefaultPolicyPrompt is the wording used when the workspace has not
	// saved its own. PolicyPrompt is the wording the next judge call will
	// actually see. The settings page shows both so a restore can put the
	// default back without a second copy of the paragraph living in the UI.
	DefaultPolicyPrompt string                  `json:"default_policy_prompt"`
	PolicyPrompt        string                  `json:"policy_prompt"`
	ProviderQuotas      []routing.ProviderQuota `json:"provider_quotas"`
	Seats               []routing.SeatSnapshot  `json:"seats"`
}

// Gateway scope values. Named because the client switches on them.
const (
	gatewayScopeWorkspace  = "workspace"
	gatewayScopeDeployment = "deployment"
)

func (h *Handler) routingHealthPayload(ctx context.Context, workspaceID string, rep routing.HealthReport) routingHealthResponse {
	deploymentConfigured := h.cfg.LLMAPIKey != "" && h.cfg.LLMBaseURL != ""
	resp := routingHealthResponse{
		State:             string(rep.State),
		Usable:            rep.Usable,
		Reason:            rep.Reason,
		RetryAfterSeconds: rep.RetryAfterSeconds,
		LastSuccessAt:     rep.LastSuccessAt,
		LastFailureAt:     rep.LastFailureAt,
		Model:             rep.Model,
		Threshold:         rep.Threshold,
		// The deployment default is reported whichever gateway is in use: it
		// is what an empty model box falls back to, and that stays true for a
		// workspace running on its own endpoint.
		GatewayDefaultModel:  h.cfg.LLMDefaultModel,
		GatewayKeySet:        rep.KeySet,
		WorkspaceKeyStorable: h.RoutingSecrets != nil,
	}
	if rep.UsesWorkspaceGateway {
		resp.GatewayHost = gatewayHost(rep.BaseURL)
		resp.GatewayScope = gatewayScopeWorkspace
		resp.GatewayConfigured = true
		resp.GatewayProtocol = gatewayProtocol(rep.BaseURL)
		h.attachRoutingContext(ctx, workspaceID, &resp)
		return resp
	}
	resp.GatewayHost = gatewayHost(h.cfg.LLMBaseURL)
	resp.GatewayScope = gatewayScopeDeployment
	resp.GatewayConfigured = deploymentConfigured
	// The deployment gateway is an OpenAI-compatible contract by definition
	// (MULTICA_LLM_*), so it is never reported as System One even if somebody
	// pointed it at a host that looks like one.
	resp.GatewayProtocol = gatewayProtocolOpenAI
	h.attachRoutingContext(ctx, workspaceID, &resp)
	return resp
}

// attachRoutingContext adds the prompt and the quota/seat summary. A failure
// here leaves unknown rows rather than failing the health read: the settings
// page has to stay open when the summary query is briefly unavailable, and
// unknown is the honest thing to show.
func (h *Handler) attachRoutingContext(ctx context.Context, workspaceID string, resp *routingHealthResponse) {
	resp.DefaultPolicyPrompt = routing.DefaultPolicyPrompt
	resp.PolicyPrompt = routing.DefaultPolicyPrompt
	keys := append([]string(nil), routing.DefaultWatchedProviders...)
	resp.ProviderQuotas = routing.EnsureProviderQuotas(nil, keys)
	if h == nil || h.Routing == nil || workspaceID == "" {
		return
	}
	settings, err := h.RoutingStore().Settings(ctx, workspaceID)
	if err != nil {
		return
	}
	resp.PolicyPrompt = settings.EffectivePolicyPrompt()
	keys = settings.ProviderKeys()
	roster, err := h.RoutingStore().Roster(ctx, workspaceID)
	if err != nil {
		resp.ProviderQuotas = routing.EnsureProviderQuotas(nil, keys)
		return
	}
	seats := h.Routing.Ladder.WithProjects(settings.Projects).Candidates("", roster)
	ids := make([]string, 0, len(seats))
	for _, seat := range seats {
		ids = append(ids, seat.ID)
	}
	facts, err := h.RoutingStore().RoutingFacts(ctx, workspaceID, ids, keys)
	if err != nil {
		resp.ProviderQuotas = routing.EnsureProviderQuotas(nil, keys)
		return
	}
	resp.ProviderQuotas = routing.EnsureProviderQuotas(facts.Providers, keys)
	resp.Seats = routing.OrderSeatViews(seats, facts.Seats)
}

// Gateway protocol values. Named because the client switches on them.
const (
	gatewayProtocolOpenAI    = "openai"
	gatewayProtocolSystemOne = "systemone"
)

func gatewayProtocol(baseURL string) string {
	if routing.IsSystemOneEndpoint(baseURL) {
		return gatewayProtocolSystemOne
	}
	return gatewayProtocolOpenAI
}

// gatewayHost reduces MULTICA_LLM_BASE_URL to the host a reader recognises.
//
// A value that does not parse as a URL is reported as empty rather than echoed
// back: the point of this field is to name a host, and echoing an unparseable
// string is how a pasted credential would reach the client.
func gatewayHost(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// GetRoutingHealth backs GET /api/workspaces/{id}/routing/health.
//
// It reads breaker state; it never dials the model. An open settings tab
// polling this must not become an outbound request loop, and probing here
// would defeat the very cooldown it is reporting on.
func (h *Handler) GetRoutingHealth(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		// No router wired at all: the product is the pre-routing product, and
		// saying "off" is the honest answer rather than an error the settings
		// page would have to interpret.
		writeJSON(w, http.StatusOK, offlineRoutingHealth())
		return
	}
	rep, err := h.Routing.Health(r.Context(), workspaceID)
	if err != nil {
		slog.Warn("routing health failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read routing health")
		return
	}
	writeJSON(w, http.StatusOK, h.routingHealthPayload(r.Context(), workspaceID, rep))
}

// CheckRoutingHealth backs POST /api/workspaces/{id}/routing/health/check —
// the "re-check" button. It dials the model once on purpose, which is the only
// way out of a cooldown short of waiting it out.
func (h *Handler) CheckRoutingHealth(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	if h.Routing == nil {
		writeJSON(w, http.StatusOK, offlineRoutingHealth())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), routeTimeout)
	defer cancel()
	rep, err := h.Routing.Probe(ctx, workspaceID)
	if err != nil {
		slog.Warn("routing probe failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to check the routing model")
		return
	}
	writeJSON(w, http.StatusOK, h.routingHealthPayload(ctx, workspaceID, rep))
}

func offlineRoutingHealth() routingHealthResponse {
	return routingHealthResponse{
		State:               string(routing.StateOff),
		DefaultPolicyPrompt: routing.DefaultPolicyPrompt,
		PolicyPrompt:        routing.DefaultPolicyPrompt,
		ProviderQuotas:      routing.EnsureProviderQuotas(nil, routing.DefaultWatchedProviders),
	}
}

// routingModelListResponse is deliberately smaller than the OpenAI model
// object. The settings page needs an id to fill the routing model field; it
// must not become a second proxy that forwards provider metadata or a raw
// upstream response to every workspace member.
type routingModelListResponse struct {
	Models []string `json:"models"`
}

// ListRoutingModels backs POST /api/workspaces/{id}/routing/models.
//
// It uses the same target resolution as routing itself: a complete workspace
// URL/key pair wins, otherwise the deployment LLM settings are used. The key
// is read and used only inside the server, and the response contains model ids
// only. This is an explicit admin action because it makes an outbound request
// with a stored credential.
func (h *Handler) ListRoutingModels(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	settings, err := (routingStore{h: h}).Settings(r.Context(), workspaceID)
	if err != nil {
		slog.Warn("routing model discovery settings failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read routing settings")
		return
	}

	target := settings.Target()
	if !target.Override() {
		target.BaseURL = strings.TrimSpace(h.cfg.LLMBaseURL)
		target.APIKey = strings.TrimSpace(h.cfg.LLMAPIKey)
	}
	if strings.TrimSpace(target.BaseURL) == "" {
		writeError(w, http.StatusServiceUnavailable, "no routing endpoint is configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), routingModelListTimeout)
	defer cancel()
	// Two list formats, because there are two protocols. A System One endpoint
	// answers `{"models":[{"name":...}]}` rather than the OpenAI
	// `{"data":[{"id":...}]}` pkg/llm parses, and a workspace pointed at
	// TypeSafe used to get an empty list here with no way to tell that the
	// model it wanted was one field name away.
	var models []string
	if target.SystemOne() {
		models, err = routing.ListSystemOneModels(ctx, nil, target.BaseURL, target.APIKey)
	} else {
		models, err = llm.New(llm.Config{
			APIKey:     target.APIKey,
			BaseURL:    target.BaseURL,
			MaxRetries: h.cfg.LLMMaxRetries,
		}).ListModels(ctx)
	}
	if err != nil {
		slog.Warn("routing model discovery failed",
			append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		// Do not echo the upstream error: compatible clients sometimes include
		// the request URL or provider response, while the UI only needs a safe
		// retryable failure state.
		writeError(w, http.StatusBadGateway, "failed to list models from the routing endpoint")
		return
	}

	seen := make(map[string]struct{}, len(models))
	clean := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		clean = append(clean, model)
	}
	sort.Strings(clean)
	writeJSON(w, http.StatusOK, routingModelListResponse{Models: clean})
}
