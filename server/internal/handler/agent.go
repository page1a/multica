package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/coderesolve"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/runtimeapps"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

// Mirrors AGENT_DESCRIPTION_MAX_LENGTH in packages/core/agents/constants.ts
// and the agent_description_length CHECK constraint in migration 060. Counted
// in unicode code points (utf8.RuneCountInString), matching Postgres
// char_length and the front-end's String.prototype.length-with-counter UX.
const maxAgentDescriptionLength = 255

const (
	maxAgentConversationStarters      = 3
	maxAgentConversationStarterLabel  = 80
	maxAgentConversationStarterLength = 4000
)

type AgentConversationStarter struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

type AgentResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// RuntimeID is the empty string when the agent is unbound — it kept its
	// configuration and history when its runtime was deleted, and needs a new
	// runtime before it can run again (MUL-5559). The wire type stays a string
	// so installed clients keep parsing; RuntimeBound is the explicit signal.
	RuntimeID string `json:"runtime_id"`
	// RuntimeBound is false exactly when the agent has no runtime. UI should
	// branch on this rather than on RuntimeID being falsy, and must not confuse
	// it with a bound-but-offline runtime (a different user story: reconnect the
	// machine vs. pick a new one).
	RuntimeBound bool `json:"runtime_bound"`
	// RuntimeAvailability is the coarse liveness projection for a runtime that
	// may be hidden from the caller's runtime list. It deliberately carries no
	// timestamp, device, owner, configuration, or credential fields; clients
	// use it only when the full runtime row is unavailable.
	RuntimeAvailability string `json:"runtime_availability,omitempty"`
	Name                string `json:"name"`
	Description         string `json:"description"`
	// Instructions is what this agent's owner wrote. For a system agent it
	// holds only the workspace's own notes — the product half lives in
	// SystemInstructions and is never stored on the row.
	Instructions string `json:"instructions"`
	// ConversationStarters are optional, agent-specific first-turn suggestions. An
	// empty list tells clients to render their localized fallback prompts.
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	// SystemKey identifies a product-defined agent (e.g. "mika"). Empty for
	// every user- or template-created agent. The UI keys "this is maintained
	// by Multica" off this rather than off the display name, which owners may
	// change.
	SystemKey string `json:"system_key,omitempty"`
	// SystemInstructions is the read-only product half of a system agent's
	// prompt, filled from the server binary. Empty for ordinary agents.
	SystemInstructions string `json:"system_instructions,omitempty"`
	// ParentAgentID is empty for a base role and carries the base role's id for
	// a specialisation (DENE-301). The tree is at most two levels deep; the
	// server enforces that, so a non-empty value here is always a base role.
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	// RuntimeInherited marks this specialisation as following its base role's
	// runtime profile (DENE-505): runtime_id, runtime_mode, runtime_config,
	// model, thinking_level and service_tier stay equal to the base role's and
	// are re-copied whenever the base role's change. Always false for a base
	// role — the field reports the stored relationship, never "inherits
	// nothing".
	RuntimeInherited bool `json:"runtime_inherited"`
	// ParentAgentName is the display name of ParentAgentID, so a client can
	// name the parent without a second request. Populated on the agents list
	// and on agent detail. Empty for a base role.
	ParentAgentName string `json:"parent_agent_name,omitempty"`
	// InheritedInstructions is the parent's own `instructions`, verbatim. The
	// effective prompt of a specialisation is parent + "\n\n" + own, so the
	// detail surface can show which half came from the base role instead of
	// making the child's own text look self-contained. Empty for a base role.
	InheritedInstructions string `json:"inherited_instructions,omitempty"`
	// InheritedSkills is the parent's skill bindings, read-only for the child
	// (v1: a specialisation cannot drop an inherited skill). Only the detail
	// endpoint loads them; the list leaves the field nil.
	InheritedSkills []AgentSkillSummary `json:"inherited_skills,omitempty"`
	// ChildCount is how many ACTIVE specialisations hang off this agent. Only a
	// base role can have children. Archived specialisations are not counted:
	// they no longer block archiving the base role. Populated on the agents
	// list (0 for a specialisation); the detail response leaves it nil.
	ChildCount    *int    `json:"child_count,omitempty"`
	AvatarURL     *string `json:"avatar_url"`
	RuntimeMode   string  `json:"runtime_mode"`
	RuntimeConfig any     `json:"runtime_config"`
	// PlanLimits is this agent's own subscription windows, present only when
	// the daemon reported a snapshot for the CLI account this agent binds
	// (DENE-715). Absent or null means the client should fall back to the
	// runtime's plan_limits — the agent has no binding of its own, or the
	// daemon has not run it yet.
	PlanLimits *protocol.PlanLimitsSnapshot `json:"plan_limits,omitempty"`
	CustomArgs []string                     `json:"custom_args"`
	McpConfig  json.RawMessage              `json:"mcp_config"`
	// custom_env is intentionally NOT serialized on agent resources. The
	// agent_list/get/create/update/archive/restore responses and WS events
	// only expose coarse metadata (has_custom_env, custom_env_key_count) so
	// the UI can show "N variables configured" without dragging secrets
	// across the API surface. Reading values requires the dedicated, audited
	// `GET /api/agents/{id}/env` endpoint; writing requires `PUT` to the
	// same path. agent-actor tokens are denied there. See MUL-2600.
	HasCustomEnv      bool   `json:"has_custom_env"`
	CustomEnvKeyCount int    `json:"custom_env_key_count"`
	McpConfigRedacted bool   `json:"mcp_config_redacted"`
	Visibility        string `json:"visibility"`
	// PermissionMode is the invocation-permission mode (MUL-3963):
	// "private" (owner only) or "public_to" (allow-list in InvocationTargets).
	// Replaces Visibility as the authorization source; Visibility is kept as a
	// derived legacy field so old clients never see a permission widening.
	PermissionMode string `json:"permission_mode"`
	// InvocationTargets is the allow-list for a public_to agent. Empty for
	// private agents. Only populated on the detail / list / create / update
	// responses that load it; broadcast payloads leave it empty.
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             string                     `json:"status"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	// AutoRetryEnabled is the per-agent platform auto-retry switch
	// (DENE-217). Default true. When false, FailTask and
	// MaybeRetryFailedTask skip retryableReasons; manual rerun is
	// unaffected.
	AutoRetryEnabled bool `json:"auto_retry_enabled"`
	// WorkEnabled is the reversible seat gate (DENE-714). Default true.
	// When false the seat stays in the list, keeps its routing tag and
	// specialisations, and does not cancel running tasks — it is simply
	// not selected for automatic dispatch, not woken by assignment, and
	// does not claim new runs.
	WorkEnabled bool `json:"work_enabled"`
	// WorkPause says why the platform turned the seat off (DENE-870), taken
	// from its open quota breaker. Absent for a seat a person turned off.
	WorkPause *AgentWorkPause `json:"work_pause,omitempty"`
	// DoorbellEnabled (DENE-808): when true, a member who may not invoke this
	// agent rings a doorbell instead of being refused — the owner gets an
	// approval request in their inbox, and the agent is listed to members
	// so they can @ it. Default false: an owner opts in per agent, so the
	// private-agent guarantees (not listed, not enumerable) hold untouched
	// until they do. When false, the refusal stays a plain
	// invocation_not_allowed.
	DoorbellEnabled bool   `json:"doorbell_enabled"`
	Model           string `json:"model"`
	// ThinkingLevel is the runtime-native reasoning/effort token persisted
	// for this agent (empty = use runtime default). The picker is per-runtime
	// per-model; the API never normalizes across providers. See MUL-2339.
	ThinkingLevel string `json:"thinking_level"`
	// ServiceTier is the runtime-native Codex execution tier persisted for
	// this agent (empty = inherit local Codex configuration).
	ServiceTier string `json:"service_tier"`
	// RoutingTier is the seat's strength rung for automatic dispatch
	// (DENE-633): one of the ladder's tier keys, or empty for a seat that is
	// not on the ladder. It is tagged by a person rather than derived from
	// `model`, because the same model at another thinking_level is another
	// rung.
	RoutingTier string `json:"routing_tier"`
	// ComposioToolkitAllowlist is the subset of Composio toolkit slugs this
	// agent is allowed to mount as MCP at task dispatch — for ANY run that
	// passes the agent's invocation permission, using the agent OWNER's
	// Composio connection (MUL-3963; no longer gated on originator == owner).
	// NULL or empty = no overlay. Like mcp_config, this is
	// owner-only data: the slugs themselves are not secret, but the
	// "this is what {agent owner} is willing to surface" view is — surfacing
	// it cross-account is privacy-confusing UX and would let workspace
	// members infer another member's integration footprint. Redacted to
	// `nil` + `composio_toolkit_allowlist_redacted=true` for non-owners,
	// mirroring the existing mcp_config redaction contract.
	ComposioToolkitAllowlist         []string               `json:"composio_toolkit_allowlist,omitempty"`
	ComposioToolkitAllowlistRedacted bool                   `json:"composio_toolkit_allowlist_redacted,omitempty"`
	OwnerID                          *string                `json:"owner_id"`
	Skills                           []AgentSkillSummary    `json:"skills"`
	DisabledRuntimeSkills            []DisabledRuntimeSkill `json:"disabled_runtime_skills"`
	CreatedAt                        string                 `json:"created_at"`
	UpdatedAt                        string                 `json:"updated_at"`
	ArchivedAt                       *string                `json:"archived_at"`
	ArchivedBy                       *string                `json:"archived_by"`
}

// runtimeConfigGatewayTokenMask is the placeholder the API substitutes for
// any non-empty `runtime_config.gateway.token` (openclaw gateway mode, issue
// #3260). The token is a bearer credential; surfacing the real value through
// GET responses would let anyone with read access to the agent dump the
// gateway secret. The mask is a sentinel — when the UI later PATCHes the
// agent and submits the same mask verbatim under that field, the update
// handler restores the persisted token instead of overwriting it.
const runtimeConfigGatewayTokenMask = "***"

func (h *Handler) agentToResponse(a db.Agent) AgentResponse {
	var rc any
	if a.RuntimeConfig != nil {
		json.Unmarshal(a.RuntimeConfig, &rc)
	}
	if rc == nil {
		rc = map[string]any{}
	}
	maskGatewayToken(rc)

	// Compute env metadata WITHOUT exposing the values. We unmarshal here
	// only to count keys; the map never reaches the response. A coarse
	// has_custom_env / key_count is what the UI gets — to read the values
	// the caller must hit GET /api/agents/{id}/env (agent owner or
	// workspace owner/admin, audited).
	envKeyCount := 0
	if a.CustomEnv != nil {
		var customEnv map[string]string
		if err := json.Unmarshal(a.CustomEnv, &customEnv); err != nil {
			slog.Warn("failed to unmarshal agent custom_env", "agent_id", uuidToString(a.ID), "error", err)
		}
		envKeyCount = len(customEnv)
	}

	var customArgs []string
	if a.CustomArgs != nil {
		if err := json.Unmarshal(a.CustomArgs, &customArgs); err != nil {
			slog.Warn("failed to unmarshal agent custom_args", "agent_id", uuidToString(a.ID), "error", err)
		}
	}
	if customArgs == nil {
		customArgs = []string{}
	}

	var mcpConfig json.RawMessage
	if a.McpConfig != nil {
		mcpConfig = json.RawMessage(a.McpConfig)
	}

	conversationStarters := []AgentConversationStarter{}
	if len(a.ConversationStarters) > 0 {
		if err := json.Unmarshal(a.ConversationStarters, &conversationStarters); err != nil {
			slog.Warn("failed to unmarshal agent conversation_starters", "agent_id", uuidToString(a.ID), "error", err)
			conversationStarters = []AgentConversationStarter{}
		}
	}

	// plan_limits is the daemon's snapshot for THIS agent's own CLI account,
	// reported only for agents bound to a numbered account (DENE-715). NULL
	// means "nothing agent-specific to say", and readers then fall back to the
	// runtime row exactly as they did before. Like the runtime copy it is
	// credential-free: percentages, window lengths and reset times only.
	var planLimits *protocol.PlanLimitsSnapshot
	if len(a.PlanLimits) > 0 {
		var snapshot protocol.PlanLimitsSnapshot
		if err := json.Unmarshal(a.PlanLimits, &snapshot); err != nil {
			slog.Warn("failed to unmarshal agent plan_limits", "agent_id", uuidToString(a.ID), "error", err)
		} else {
			planLimits = &snapshot
		}
	}

	// composio_toolkit_allowlist: the column is stored as TEXT[] and arrives
	// here as a []string (sqlc). NULL and `{}` both serialize as nil through
	// the postgres driver — both correctly mean "no toolkits", but the API
	// surface keeps them distinguishable from "owner has not opened the
	// integration yet" only via the trio (slice nil / slice empty / slice
	// non-empty). We hand the slice through verbatim so the redaction +
	// owner-only gate below can decide.
	composioAllowlist := a.ComposioToolkitAllowlist

	return AgentResponse{
		ID:                       uuidToString(a.ID),
		WorkspaceID:              uuidToString(a.WorkspaceID),
		RuntimeID:                uuidToString(a.RuntimeID),
		RuntimeBound:             a.RuntimeID.Valid,
		Name:                     a.Name,
		Description:              a.Description,
		Instructions:             a.Instructions,
		ConversationStarters:     conversationStarters,
		SystemKey:                a.SystemKey.String,
		SystemInstructions:       systemInstructionsFor(a),
		ParentAgentID:            uuidToString(a.ParentAgentID),
		RuntimeInherited:         a.RuntimeInherited,
		AvatarURL:                h.resolveAvatarURLPtr(textToPtr(a.AvatarUrl)),
		RuntimeMode:              a.RuntimeMode,
		RuntimeConfig:            rc,
		CustomArgs:               customArgs,
		McpConfig:                mcpConfig,
		HasCustomEnv:             envKeyCount > 0,
		CustomEnvKeyCount:        envKeyCount,
		PlanLimits:               planLimits,
		Visibility:               a.Visibility,
		PermissionMode:           a.PermissionMode,
		InvocationTargets:        []AgentInvocationTargetDTO{},
		Status:                   a.Status,
		MaxConcurrentTasks:       a.MaxConcurrentTasks,
		AutoRetryEnabled:         a.AutoRetryEnabled,
		WorkEnabled:              a.WorkEnabled,
		DoorbellEnabled:          a.DoorbellEnabled,
		Model:                    a.Model.String,
		ThinkingLevel:            a.ThinkingLevel.String,
		ServiceTier:              a.ServiceTier.String,
		RoutingTier:              a.RoutingTier.String,
		ComposioToolkitAllowlist: composioAllowlist,
		OwnerID:                  uuidToPtr(a.OwnerID),
		Skills:                   []AgentSkillSummary{},
		DisabledRuntimeSkills:    decodeDisabledRuntimeSkills(a.DisabledRuntimeSkills),
		CreatedAt:                timestampToString(a.CreatedAt),
		UpdatedAt:                timestampToString(a.UpdatedAt),
		ArchivedAt:               timestampToPtr(a.ArchivedAt),
		ArchivedBy:               uuidToPtr(a.ArchivedBy),
	}
}

// ---------------------------------------------------------------------------
// Two-level agent specialisation (DENE-301)
//
// A base role has parent_agent_id IS NULL. A specialisation points at exactly
// one base role. A specialisation can never be a parent, so the tree is at most
// two levels deep and no cycle is expressible.
//
// What a specialisation inherits is only its prompt and its skills. model,
// runtime_id, max_concurrent_tasks and permissions stay independent per agent
// (see DENE-300 for why: per-field override on NOT NULL columns is expensive to
// explain and to build, and "same role, different model" is already satisfied
// when configuration is independent).
// ---------------------------------------------------------------------------

// agentInstructionSeparator joins a parent's prompt to its child's in the
// effective prompt. Two newlines keep the two blocks visually separate in the
// rendered prompt while staying a single string for the daemon's argv/stdin
// path.
const agentInstructionSeparator = "\n\n"

// composeAgentInstructions is the ONE place that defines what a specialisation
// actually runs with. Both the solidify endpoint (which bakes the result into
// the child's own instructions) and the daemon-side claim path use it, so the
// text a user sees frozen into the child is exactly the text the run used.
//
// Neither half is rewritten: an empty parent contributes nothing (not a stray
// separator), and an empty child leaves the parent's prompt alone.
func composeAgentInstructions(parent, child string) string {
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return parent + agentInstructionSeparator + child
}

// loadAgentDisplayNames returns id -> display name for every id, skipping
// unresolved ids. Used to name the parent of each specialisation in one read
// instead of one read per child.
func (h *Handler) loadAgentDisplayNames(ctx context.Context, ids []pgtype.UUID) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := h.Queries.GetAgentsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		names[uuidToString(row.ID)] = row.Name
	}
	return names, nil
}

// enrichAgentResponsesWithRelations fills the fields a specialisation's
// consumers need — the parent's name and, per base role, how many active
// specialisations hang off it — for a whole list in two reads. The agents list
// is a workspace-wide surface, so a per-agent lookup here would be the exact
// N+1 the response contract forbids.
func (h *Handler) enrichAgentResponsesWithRelations(ctx context.Context, resps []AgentResponse) error {
	parentIDs := make([]pgtype.UUID, 0, len(resps))
	ownIDs := make([]pgtype.UUID, 0, len(resps))
	seen := map[string]struct{}{}
	for _, resp := range resps {
		id, err := util.ParseUUID(resp.ID)
		if err != nil {
			continue
		}
		ownIDs = append(ownIDs, id)
		if resp.ParentAgentID == "" {
			continue
		}
		parentID, err := util.ParseUUID(resp.ParentAgentID)
		if err != nil {
			continue
		}
		if _, ok := seen[resp.ParentAgentID]; ok {
			continue
		}
		seen[resp.ParentAgentID] = struct{}{}
		parentIDs = append(parentIDs, parentID)
	}

	names, err := h.loadAgentDisplayNames(ctx, parentIDs)
	if err != nil {
		return err
	}

	counts := map[string]int{}
	if len(ownIDs) > 0 {
		rows, err := h.Queries.CountAgentChildrenByParentIDs(ctx, ownIDs)
		if err != nil {
			return err
		}
		for _, row := range rows {
			counts[uuidToString(row.ParentAgentID)] = int(row.ChildCount)
		}
	}

	for i := range resps {
		if resps[i].ParentAgentID != "" {
			resps[i].ParentAgentName = names[resps[i].ParentAgentID]
		}
		// Every agent carries child_count so a client can group without
		// branching on "is this a base role". A specialisation can never have
		// children, and the SQL agrees: it groups by parent_agent_id, which is
		// this agent's own id only for a base role.
		count := counts[resps[i].ID]
		resps[i].ChildCount = &count
	}
	return nil
}

// parentAgentForResponse loads the base role of a specialisation for the
// responses that carry the inherited half of the contract (agent detail).
// Returns false for a base role and for a parent this request cannot resolve.
func (h *Handler) parentAgentForResponse(ctx context.Context, resp *AgentResponse) (db.Agent, bool) {
	if resp.ParentAgentID == "" {
		return db.Agent{}, false
	}
	parentID, err := util.ParseUUID(resp.ParentAgentID)
	if err != nil {
		return db.Agent{}, false
	}
	wsID, err := util.ParseUUID(resp.WorkspaceID)
	if err != nil {
		return db.Agent{}, false
	}
	parent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parentID,
		WorkspaceID: wsID,
	})
	if err != nil {
		return db.Agent{}, false
	}
	return parent, true
}

// attachAgentInheritance fills the read-only half of a specialisation: the
// parent's prompt and the parent's skills. Both are exposed verbatim rather
// than pre-composed with the child's own values, because the client renders
// them as a separate, non-editable block — "this came from the base role" is
// the information, and only the parent's own rows carry it.
//
// The parent's prompt is subject to the SAME view gate as the parent itself.
// A base role may be private to another member, and a child that is visible
// must not become a window into it: the viewer still sees the relationship (the
// id is already on the response) but not the inherited text.
//
// A base role returns without touching the response, so a client can treat an
// empty inherited_instructions as "nothing is inherited" without also having to
// check parent_agent_id.
func (h *Handler) attachAgentInheritance(ctx context.Context, resp *AgentResponse, actorType, actorID string) error {
	parent, ok := h.parentAgentForResponse(ctx, resp)
	if !ok {
		return nil
	}
	if !h.canAccessPrivateAgent(ctx, parent, actorType, actorID, uuidToString(parent.WorkspaceID)) {
		return nil
	}
	resp.InheritedInstructions = parent.Instructions

	skills, err := h.Queries.ListAgentSkillSummaries(ctx, parent.ID)
	if err != nil {
		return err
	}
	inherited := make([]AgentSkillSummary, 0, len(skills))
	for _, s := range skills {
		inherited = append(inherited, AgentSkillSummary{
			ID:          uuidToString(s.ID),
			Name:        s.Name,
			Description: s.Description,
			Enabled:     s.Enabled,
		})
	}
	resp.InheritedSkills = inherited
	return nil
}

// syncInheritedAgentRuntimeProfiles copies a base role's runtime profile onto
// the specialisations that follow it (DENE-505) and returns the rows that
// actually changed. One statement, so the copy cannot land halfway.
//
// A failure is logged rather than returned: the caller's own write has already
// committed by the time this runs, so failing the request would tell the user
// their edit was rejected while the base role keeps it. The children stay on
// the previous profile until the next base-role edit or the child's own save
// re-runs this.
func (h *Handler) syncInheritedAgentRuntimeProfiles(ctx context.Context, parentAgentID pgtype.UUID, r *http.Request) []db.Agent {
	children, err := h.Queries.SyncInheritedAgentRuntimeProfiles(ctx, parentAgentID)
	if err != nil {
		slog.Warn("sync inherited agent runtime profiles failed",
			append(logger.RequestAttrs(r), "error", err, "parent_agent_id", uuidToString(parentAgentID))...)
		return nil
	}
	return children
}

// followsExecutionConfig reports whether a following specialisation also takes
// its base role's execution config — custom_env, custom_args, mcp_config
// (DENE-854). Only within one owner: env and MCP config carry the base role's
// credentials, and a member who specialises someone else's public base role
// must not receive them through their own env reveal. Mirrors the owner guard
// in SyncInheritedAgentRuntimeProfiles; a NULL owner never matches.
func followsExecutionConfig(childOwner, parentOwner pgtype.UUID) bool {
	return childOwner.Valid && parentOwner.Valid && childOwner == parentOwner
}

// executionConfigFields are the UpdateAgent fields that belong to the
// execution config a following specialisation takes from its base role.
// custom_env is not listed: it has its own endpoint (UpdateAgentEnv).
var executionConfigFields = []string{"custom_args", "mcp_config"}

// publishAgentUpdate fans one agent row out to the workspace as an
// agent:updated event. Used for the specialisations a base-role edit cascaded
// into: their rows changed while the request's actor was editing the base role,
// and a client that only saw the base role's event would keep painting the old
// runtime on every child row.
func (h *Handler) publishAgentUpdate(r *http.Request, agent db.Agent) {
	wsID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	resp := h.agentToResponse(agent)
	h.publish(protocol.EventAgentStatus, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
}

// validateAgentParent enforces the two-level rule for a requested parent.
//
// The parent must be a base role in the same workspace: a specialisation may
// not itself be specialised ("特化角色不能再派生"), which is what keeps the
// tree at two levels without a depth column or a recursive check. A parent that
// does not resolve is reported as not found, so a caller cannot probe for
// agents outside their workspace by watching which error comes back.
//
// The actor must also be able to VIEW the parent. Attaching is the moment the
// inheritance relationship is created, and every later read of the inherited
// prompt — attachAgentInheritance, SolidifyAgent, the daemon claim path — is
// downstream of a row written here. Gating only the reads leaves the write open:
// a member could point their own agent at another member's private base role and
// then solidify it to get the prompt text back in their own `instructions`. An
// unviewable parent gets the same "not found" wording as a missing one, so this
// endpoint does not become a probe for which private agents exist.
func (h *Handler) validateAgentParent(ctx context.Context, workspaceID string, parentAgentID string, childAgentID string, actorType, actorID string) (db.Agent, string) {
	parentUUID, err := util.ParseUUID(parentAgentID)
	if err != nil {
		return db.Agent{}, "parent_agent_id is not a valid id"
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Agent{}, "workspace id is not valid"
	}

	parent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          parentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		return db.Agent{}, "parent agent not found in this workspace"
	}
	if !h.canAccessPrivateAgent(ctx, parent, actorType, actorID, uuidToString(parent.WorkspaceID)) {
		return db.Agent{}, "parent agent not found in this workspace"
	}
	if childAgentID != "" && childAgentID == uuidToString(parent.ID) {
		return db.Agent{}, "an agent cannot be its own parent"
	}
	if parent.ArchivedAt.Valid {
		return db.Agent{}, "the parent agent is archived; restore it before making it a base role"
	}
	if parent.ParentAgentID.Valid {
		return db.Agent{}, "特化角色不能再派生：a specialisation cannot be a parent; attach this agent to a base role instead"
	}
	return parent, ""
}

// maskGatewayToken replaces runtime_config.gateway.token with the public
// mask sentinel when a non-empty value is present. No-op for any other
// shape so non-openclaw / non-gateway agents pass through untouched.
func maskGatewayToken(rc any) {
	root, ok := rc.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok == "" {
		return
	}
	gw["token"] = runtimeConfigGatewayTokenMask
}

// preserveMaskedGatewayToken substitutes the previously persisted gateway
// token back into an incoming runtime_config when the request submitted the
// public mask sentinel under `gateway.token`. Without this the next PATCH
// after a GET would round-trip the masked sentinel into the database and
// silently destroy the real secret. The previous value is taken from the
// agent row the handler has just loaded for ownership / scoping checks.
func preserveMaskedGatewayToken(incoming any, persistedRuntimeConfig []byte) {
	root, ok := incoming.(map[string]any)
	if !ok {
		return
	}
	gw, ok := root["gateway"].(map[string]any)
	if !ok {
		return
	}
	tok, _ := gw["token"].(string)
	if tok != runtimeConfigGatewayTokenMask {
		return
	}
	// The incoming token is the mask — fish the real one out of the row.
	var prev struct {
		Gateway struct {
			Token string `json:"token"`
		} `json:"gateway"`
	}
	if len(persistedRuntimeConfig) == 0 {
		// No prior token to keep; the field becomes effectively empty.
		delete(gw, "token")
		return
	}
	if err := json.Unmarshal(persistedRuntimeConfig, &prev); err != nil || prev.Gateway.Token == "" {
		delete(gw, "token")
		return
	}
	gw["token"] = prev.Gateway.Token
}

// RepoData holds repository information included in claim responses so the
// daemon can set up worktrees for each workspace repo.
type RepoData struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// ProjectResourceData is the wire shape for a project resource included in a
// claim response. The daemon reads this list and writes it into the agent's
// working directory so skills/agents can discover project-scoped context.
//
// resource_ref is type-specific JSON; the daemon doesn't interpret it beyond
// well-known fields like url for github_repo. New types can be added without
// changing this struct.
type ProjectResourceData struct {
	ID           string          `json:"id"`
	ResourceType string          `json:"resource_type"`
	ResourceRef  json.RawMessage `json:"resource_ref"`
	Label        string          `json:"label,omitempty"`
	// Access is "read-write" for the single directory this run writes in and
	// "read-only" for every other local_directory on the machine (DENE-619).
	// Empty for resource types that have no access dimension, such as a
	// repository, which is checked out rather than written in place. The
	// daemon carries it into .multica/project/resources.json and the brief, so
	// an agent can read across a project's directories while only one of them
	// is its workspace. Mirror field: internal/daemon/types.go, same JSON name.
	Access string `json:"access,omitempty"`
}

// TaskProjectContextData is one project attached to a daemon claim. The daemon
// renders every entry into the brief's Project Context section and writes them
// to .multica/project/resources.json, so an agent can work across several
// projects (and their repositories) in one run.
type TaskProjectContextData struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	Resources   []ProjectResourceData `json:"resources,omitempty"`
}

// ConnectedAppData keeps the daemon-claim wire field local to handler types
// while sharing the canonical JSON shape with the runtime app metadata package.
type ConnectedAppData = runtimeapps.ConnectedApp

// taskIssueStatusCap bounds the custom statuses a claim payload carries. A
// defensive ceiling, not a product limit: a real catalog holds a handful of
// entries, and the brief must not grow without bound on a workspace that
// scripted hundreds. Overflow is reported via IssueStatusesOmitted so the
// brief can disclose the truncation.
const taskIssueStatusCap = 30

// TaskIssueStatusData is one active CUSTOM workspace status on the claim wire
// (MUL-6460). Only the fields an agent needs to choose and write the status
// travel: key is the CLI argument, name is what users call it in instructions,
// category anchors the inherited platform behavior, and description is the
// admin's "when to use me" guidance — the disambiguator when a category holds
// more than one status. Color/position/id stay off the wire: they carry no
// behavioral meaning for an agent, and the server already emits entries in
// catalog order (category rank, then position, then key).
type TaskIssueStatusData struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
}

// TaskCancellationActor is the point-in-time actor snapshot attached to a
// cancelled run. Type stays open for forward compatibility; current producers
// emit member, agent, or system.
type TaskCancellationActor struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type AgentTaskResponse struct {
	CancelledByCommentChange bool                   `json:"cancelled_by_comment_change,omitempty"`
	CancelledBy              *TaskCancellationActor `json:"cancelled_by,omitempty"`

	ID                    string `json:"id"`
	AgentID               string `json:"agent_id"`
	RuntimeID             string `json:"runtime_id"`
	WorkThreadID          string `json:"work_thread_id,omitempty"`
	ContextGeneration     int32  `json:"context_generation,omitempty"`
	ContextMessageLimit   int32  `json:"context_message_limit,omitempty"`
	ContextTokenBudget    int32  `json:"context_token_budget,omitempty"`
	ContinuityBreakReason string `json:"continuity_break_reason,omitempty"`
	IssueID               string `json:"issue_id"`
	WorkspaceID           string `json:"workspace_id"`
	WorkspaceSlug         string `json:"workspace_slug,omitempty"`
	IssueIdentifier       string `json:"issue_identifier,omitempty"`
	// CanonicalBranch is the issue's canonical delivery branch (DENE-820).
	// A worktree-mode daemon continues it even when another seat created
	// it, so a rerun never opens a second delivery line by accident.
	CanonicalBranch      string                 `json:"canonical_branch,omitempty"`
	RemoteMCPConnections []remotemcp.Connection `json:"remote_mcp_connections,omitempty"`
	// PluginHookTools are the workspace's agent-trigger plugin hooks, which the
	// daemon renders as MCP tools for this task. Resolved at claim time so
	// disabling or uninstalling a plugin takes effect on the next task rather
	// than whenever a daemon happens to restart.
	PluginHookTools []service.PluginHookTool `json:"plugin_hook_tools,omitempty"`
	// RemoteMCPDaemonToken is a short-lived, workspace-and-daemon scoped
	// credential used only by the local daemon's write-only Remote MCP broker.
	// It is never injected into the agent process.
	RemoteMCPDaemonToken string `json:"remote_mcp_daemon_token,omitempty"`
	// WorkspaceContext is the workspace-level system prompt set in workspace
	// settings (`workspace.context` DB column). Injected into the agent brief
	// as `## Workspace Context` so every agent running in this workspace —
	// regardless of issue / chat / autopilot / quick-create — sees the same
	// shared context. Empty when the workspace owner hasn't set it.
	WorkspaceContext string `json:"workspace_context,omitempty"`
	// IssueStatuses is the workspace's ACTIVE CUSTOM status catalog (MUL-6460),
	// injected into the agent brief so agents can see and use statuses beyond
	// the seven built-ins. Built-ins are omitted: their keys, names, and
	// semantics are locked (is_system rows cannot be renamed or archived), so
	// the daemon already knows them. Empty for a workspace with no custom
	// statuses — the daemon then renders the brief exactly as before, keeping
	// existing deployments byte-identical. Capped at taskIssueStatusCap
	// entries; IssueStatusesOmitted carries the overflow count.
	IssueStatuses []TaskIssueStatusData `json:"issue_statuses,omitempty"`
	// IssueStatusesOmitted is how many active custom statuses were dropped by
	// the cap, so the brief can say the list is incomplete instead of
	// presenting a truncated catalog as the whole one.
	IssueStatusesOmitted int                   `json:"issue_statuses_omitted,omitempty"`
	IssueStateDeltaKnown bool                  `json:"issue_state_delta_known,omitempty"`
	IssueChangedFields   []string              `json:"issue_changed_fields,omitempty"`
	IssueStatus          string                `json:"issue_status,omitempty"`
	IssueAssigneeType    string                `json:"issue_assignee_type,omitempty"`
	IssueAssigneeID      string                `json:"issue_assignee_id,omitempty"`
	ThreadName           string                `json:"thread_name,omitempty"` // semantic title for provider-native session/thread history
	Status               string                `json:"status"`
	Priority             int32                 `json:"priority"`
	DispatchedAt         *string               `json:"dispatched_at"`
	StartedAt            *string               `json:"started_at"`
	CompletedAt          *string               `json:"completed_at"`
	Result               any                   `json:"result"`
	Error                *string               `json:"error"`
	FailureReason        string                `json:"failure_reason,omitempty"` // see TaskService.MaybeRetryFailedTask
	Attempt              int32                 `json:"attempt"`
	MaxAttempts          int32                 `json:"max_attempts"`
	ParentTaskID         *string               `json:"parent_task_id,omitempty"`
	IsLeaderTask         bool                  `json:"is_leader_task,omitempty"`
	LeaderRoleResolved   bool                  `json:"leader_role_resolved,omitempty"` // claim-only capability, always true here: IsLeaderTask/SquadID authoritatively answer "is this a leader run", so the daemon must not infer the role from briefing text. Servers predating it make no such promise — before #4951 they sent no is_leader_task at all, after it they sent the flag without guaranteeing a briefing — so a daemon seeing no capability keeps the legacy inference. Never rendered into a prompt; see daemon.taskIsSquadLeader (MUL-5811). Mirror field: internal/daemon/types.go, same JSON name
	Agent                *TaskAgentData        `json:"agent,omitempty"`
	ConnectedApps        []ConnectedAppData    `json:"connected_apps,omitempty"` // daemon-claim only: per-run app capabilities mounted through runtime MCP overlays
	Repos                []RepoData            `json:"repos,omitempty"`
	ProjectID            string                `json:"project_id,omitempty"`          // issue's project, when present
	ProjectTitle         string                `json:"project_title,omitempty"`       // for surfacing in agent context
	ProjectDescription   string                `json:"project_description,omitempty"` // durable project-level context injected into the brief
	ProjectResources     []ProjectResourceData `json:"project_resources,omitempty"`   // resources attached to the project
	// CodeDecision states which code this run uses, computed once on the
	// server by internal/coderesolve and shipped with the task so the daemon
	// and the desktop UI read one answer instead of each deriving it from the
	// resource list (DENE-619). Failure is a value here (kind=unresolvable
	// with a code), never a missing field: a daemon that reads no decision is
	// talking to a server that predates it, which is a different situation
	// from a run whose code source could not be resolved.
	CodeDecision *coderesolve.Decision `json:"code_decision,omitempty"`
	// Projects is the task's full project set in priority order (DENE-523): a
	// chat session can attach several projects, every other surface at most
	// one. The singular project_* fields above mirror the FIRST entry so a
	// daemon predating this field still renders the primary project. Mirror
	// field: internal/daemon/types.go, same JSON name.
	Projects       []TaskProjectContextData `json:"projects,omitempty"`
	CreatedAt      string                   `json:"created_at"`
	PriorSessionID string                   `json:"prior_session_id,omitempty"` // session ID from a previous task on same issue
	PriorWorkDir   string                   `json:"prior_work_dir,omitempty"`   // work_dir from a previous task on same issue
	// PriorSessionResumeUnavailable is set when a more recent Codex session was
	// withheld because its rollout was missing (MUL-5305); PriorSessionID (if
	// any) is then an older fallback, and the daemon surfaces the continuity gap
	// in the brief even when that older session resumes cleanly. It is also set
	// when an automatic retry continues in its parent's workdir under a fresh
	// session (MUL-7034). omitempty keeps it off the wire for the common
	// (no-gap) case and for old daemons.
	PriorSessionResumeUnavailable bool `json:"prior_session_resume_unavailable,omitempty"`
	// ContinueInterruptedSession is set on an automatic retry that inherited
	// a resume-safe parent session (DENE-727). The daemon resumes that session
	// and sends a short continue prompt instead of re-injecting the original
	// task. omitempty keeps it off the wire for every other claim and for old
	// daemons.
	ContinueInterruptedSession bool `json:"continue_interrupted_session,omitempty"`
	// ContinueAfterTimeLimit marks a continue-retry whose parent stopped on
	// the workspace time limit (DENE-857). The daemon's continue prompt then
	// tells the agent to close out finished work and split what remains.
	ContinueAfterTimeLimit bool   `json:"continue_after_time_limit,omitempty"`
	WorkDir                string `json:"work_dir,omitempty"` // local working directory pinned for this task; populated once the daemon reports it
	// RelativeWorkDir is a privacy-safe display form of WorkDir intended for
	// the UI. For standard tasks it strips the daemon's workspaces root while
	// preserving either the legacy or readable workspace/task segments; for local_directory
	// tasks the absolute path lives outside the envRoot layout, so we strip
	// recognised home-directory prefixes (`/Users/<name>/`, `/home/<name>/`,
	// `<drive>:/Users/<name>/`) and otherwise fall back to the basename so
	// the field never carries the user's home dir or account name. Empty
	// when WorkDir is empty, or when stripping leaves nothing. See
	// relativeWorkDir() for the full rules. Older clients can still read
	// WorkDir directly; newer UIs should prefer RelativeWorkDir.
	RelativeWorkDir string `json:"relative_work_dir,omitempty"`
	// DurableWorkDir is the daemon-confirmed directory that remains usable
	// after a disposable task worktree has been finalized and removed. It is a
	// point-in-time task snapshot and does not follow later resource edits.
	DurableWorkDir string `json:"durable_work_dir,omitempty"`
	// RelativeDurableWorkDir is the privacy-safe display form. The absolute
	// value is retained for explicit clipboard actions only.
	RelativeDurableWorkDir string `json:"relative_durable_work_dir,omitempty"`
	// BranchName is the git branch this run delivered its work on, set only by
	// worktree-mode local_directory tasks. Unlike WorkDir it is safe to show
	// verbatim: it is a ref inside the user's own repo, not a filesystem path.
	// Populated on both terminal paths — a failed run can still have committed
	// partial work, and that is when the pointer matters most.
	BranchName            string                 `json:"branch_name,omitempty"`
	TriggerCommentID      *string                `json:"trigger_comment_id,omitempty"`      // comment that triggered this task
	CoalescedCommentIDs   []string               `json:"coalesced_comment_ids,omitempty"`   // MUL-4195: earlier comments folded into this run when it had not yet started, so a single run still covers every deliberate comment; trigger_comment_id is the newest. Surfaced so the UI can show which comments a run covered. omitempty so old clients ignore it
	CoalescedComments     []CoalescedCommentData `json:"coalesced_comments,omitempty"`      // MUL-4195: full detail (thread_id/author/created_at/content) of the folded comments, so the daemon prompt can address each without assuming they share the triggering thread. omitempty so old clients ignore it
	DeliveredCommentIDs   []string               `json:"delivered_comment_ids"`             // always present: [] is an authoritative empty receipt, while field absence identifies responses from legacy servers
	TriggerThreadID       string                 `json:"trigger_thread_id,omitempty"`       // root comment ID for the triggering thread
	TriggerCommentContent string                 `json:"trigger_comment_content,omitempty"` // content of the triggering comment
	TriggerSummary        *string                `json:"trigger_summary,omitempty"`         // canonical short description snapshot — comment text / autopilot title — taken at task creation; survives source edits/deletes
	TriggerAuthorType     string                 `json:"trigger_author_type,omitempty"`     // "agent" or "member" — author kind of the triggering comment
	TriggerAuthorName     string                 `json:"trigger_author_name,omitempty"`     // display name of the triggering comment author
	NewCommentCount       int                    `json:"new_comment_count,omitempty"`       // ISSUE-WIDE comments since this agent's last run — every thread, not just the triggering one (CountNewCommentsSince); excludes the injected trigger and the agent's own comments; omitempty so old daemons ignore it
	NewCommentsSince      string                 `json:"new_comments_since,omitempty"`      // RFC3339 anchor (last run's started_at) the count is measured from; omitempty so old daemons ignore it. Suppressed with the count when the delta is zero — NewCommentsDeltaKnown, not this field, is what says the server looked
	// NewCommentsDeltaKnown reports that the issue-wide delta above was
	// actually COMPUTED this claim — both the anchor lookup and the count
	// query succeeded. Without it, NewCommentCount == 0 is ambiguous: a true
	// zero, a failed anchor read, a failed count read, a cold start with no
	// prior run, and an old server that never sends these fields all produce
	// the same zero. Only the first of those answers "has anything else been
	// said on this issue", so only the first may waive the workflow's comment
	// scan. Absent on old servers, which is the safe reading (MUL-6984).
	NewCommentsDeltaKnown bool   `json:"new_comments_delta_known,omitempty"`
	IssueTitle            string `json:"issue_title,omitempty"`
	IssueDescription      string `json:"issue_description,omitempty"`
	// CheckoutPaths is the issue's checkout_paths metadata: repo-relative
	// directories this task wants on disk. Empty (and absent on old servers)
	// checks out the whole repository.
	CheckoutPaths            string                `json:"checkout_paths,omitempty"`
	IssueCommentSummaries    []IssueContextComment `json:"issue_comment_summaries,omitempty"`
	IssueTriggerThread       []IssueContextComment `json:"issue_trigger_thread,omitempty"`
	IssueNewComments         []IssueContextComment `json:"issue_new_comments,omitempty"`
	IssueSubIssues           []SubIssueRef         `json:"issue_sub_issues,omitempty"` // the task issue's sub-issues; non-empty tells the run it holds a coordinator (DENE-812)
	IssueContextGeneratedAt  string                `json:"issue_context_generated_at,omitempty"`
	IssueContextTruncated    bool                  `json:"issue_context_truncated,omitempty"`
	ChatSessionID            string                `json:"chat_session_id,omitempty"`             // non-empty for chat tasks
	ChatChannelType          string                `json:"chat_channel_type,omitempty"`           // "slack" when the chat session is backed by an IM channel; empty for a web-only chat. Makes the agent channel-aware (read history from the channel, not Multica)
	ChatChannelDeliversFiles bool                  `json:"chat_channel_delivers_files,omitempty"` // server capability: THIS deployment can put a file the agent produced into THIS conversation — the adapter goes back for the bound attachment AND object storage exists to go back to. Absent/false on a server predating it, which is the safe reading: the agent is told to describe its file in words. Never inferred daemon-side from chat_channel_type; see handler.Handler.channelDeliversFiles
	ChatType                 string                `json:"chat_type,omitempty"`                   // channel_chat_session_binding.chat_type — "group" for a shared room, "p2p" for a 1:1 with the bot. Lets the per-turn prompt tell the agent who else can read its replies; empty for a web-only chat
	ChatInThread             bool                  `json:"chat_in_thread,omitempty"`              // true when the latest @mention was a thread reply; tells the agent to start with `multica chat thread` vs `multica chat history`
	ChatMessage              string                `json:"chat_message,omitempty"`                // user message for chat tasks
	ChatMessageAttachments   []ChatAttachmentMeta  `json:"chat_message_attachments,omitempty"`    // attachments on the user message — agent calls `multica attachment download <id>` per entry
	ChatIntro                bool                  `json:"chat_intro,omitempty"`                  // legacy compatibility for historical is_agent_intro sessions; new agent creation no longer creates these chats
	AutopilotRunID           string                `json:"autopilot_run_id,omitempty"`            // non-empty for autopilot-spawned tasks
	AutopilotID              string                `json:"autopilot_id,omitempty"`                // autopilot that spawned this task
	AutopilotTitle           string                `json:"autopilot_title,omitempty"`             // autopilot title used as task context
	AutopilotDescription     string                `json:"autopilot_description,omitempty"`       // autopilot description used as task prompt
	AutopilotSource          string                `json:"autopilot_source,omitempty"`            // manual, schedule, webhook, or api
	AutopilotTriggerPayload  json.RawMessage       `json:"autopilot_trigger_payload,omitempty"`   // optional trigger payload for webhook/api runs
	QuickCreatePrompt        string                `json:"quick_create_prompt,omitempty"`         // user's natural-language input for quick-create tasks
	QuickCreatePriority      string                `json:"quick_create_priority,omitempty"`       // explicit priority selected in quick-create
	QuickCreateDueDate       string                `json:"quick_create_due_date,omitempty"`       // explicit calendar due date selected in quick-create
	QuickCreateAttachmentIDs []string              `json:"quick_create_attachment_ids,omitempty"` // attachment ids uploaded in the quick-create prompt and bound on issue create
	QuickCreateSourceContext json.RawMessage       `json:"quick_create_source_context,omitempty"` // immutable historical context for source-context quick-create
	HandoffNote              string                `json:"handoff_note,omitempty"`                // legacy assignment handoff instruction retained for installed clients; rendered by the daemon only in the per-turn prompt
	SquadID                  string                `json:"squad_id,omitempty"`                    // for quick-create tasks where the picker was a squad; Agent is still the resolved leader
	SquadName                string                `json:"squad_name,omitempty"`                  // display name for the picker squad
	ParentIssueID            string                `json:"parent_issue_id,omitempty"`             // for quick-create tasks opened from "Add sub issue" — UUID of the parent issue the new issue should be filed under
	ParentIssueIdentifier    string                `json:"parent_issue_identifier,omitempty"`     // human-readable identifier (e.g. MUL-123) of the quick-create parent issue, resolved on claim for prompt context
	ProjectExplicitNone      bool                  `json:"project_explicit_none,omitempty"`       // user cleared the project; the daemon prompt must pass an empty --project
	// RequestingUserName + RequestingUserProfileDescription mirror the user
	// the agent is acting on behalf of (see daemon/types.go). v1 sources them
	// from the runtime owner so they're populated for daemon runtimes and
	// empty otherwise. The daemon emits both into the brief under
	// `## Requesting User`; the heading is skipped entirely when description
	// is empty.
	RequestingUserName               string `json:"requesting_user_name,omitempty"`
	RequestingUserProfileDescription string `json:"requesting_user_profile_description,omitempty"`
	// Initiator* identify the actor who triggered THIS task — the real
	// requester behind the current comment/mention or chat message — as
	// distinct from the runtime owner whose credentials the agent runs with.
	// Resolved at claim time: comment-triggered tasks use the triggering
	// comment's author; chat tasks use the chat session creator. Empty for
	// task kinds with no attributable human initiator (on-assign, autopilot,
	// quick-create). InitiatorEmail is set only for member initiators
	// ("member"); agent initiators ("agent") carry a name but no email. The
	// daemon emits these into the brief under `## Task Initiator` so a
	// workspace-visible, multi-user agent can attribute the request and apply
	// per-person privacy / access rules instead of seeing every requester as
	// the owner. The agent's effective Multica credentials stay owner-scoped —
	// this is an attested identity, not a credential. See MUL-2645.
	InitiatorType  string `json:"initiator_type,omitempty"`  // "member" or "agent"
	InitiatorID    string `json:"initiator_id,omitempty"`    // user UUID (member) or agent UUID
	InitiatorName  string `json:"initiator_name,omitempty"`  // display name of the initiator
	InitiatorEmail string `json:"initiator_email,omitempty"` // member email; empty for agent initiators
	Kind           string `json:"kind"`                      // source discriminator: "comment" | "autopilot" | "chat" | "quick_create" | "direct" — quick-create remains stable after its result issue is linked
	// Attribution is the resolved accountable-human provenance for this run
	// (MUL-4302 §9): the source label + precise flag, the initiator (accountable)
	// and originator refs, the evidence pointer, and lineage. Always present (the
	// pure taskToResponse builds the labels + raw ids); initiator/originator names
	// are hydrated from the global user table only on user-facing surfaces.
	Attribution *TaskAttribution `json:"attribution,omitempty"`
	// Usage is this run's own token consumption, one entry per (provider, model)
	// it used — the same grain `task_usage` stores and the same grain the client
	// prices at. Hydrated on issue execution logs and explicit agent-history
	// accounting requests; normal UI history and daemon claims leave it nil so
	// those payloads do not carry accounting they do not use.
	//
	// nil and [] are both "no usage recorded" and the UI renders an em dash for
	// them — a run that predates usage reporting, or one that died before any
	// model call, genuinely has no number, and showing 0 would assert it was
	// free. omitempty keeps both off the wire.
	Usage []TaskUsageData `json:"usage,omitempty"`
	// AuthToken is the task-scoped `mat_` token the daemon must inject as
	// MULTICA_TOKEN in the agent process environment. The server binds it to
	// this (agent_id, task_id) pair at claim time and treats any request
	// authenticated with it as actor=agent, regardless of headers — so the
	// agent process cannot use it to read another agent's secrets via the
	// env-management endpoint. Claim fails closed when the runtime has no
	// owning user; the daemon must not fall back to its own credential. See
	// MUL-3292.
	AuthToken string `json:"auth_token,omitempty"`
}

// TaskAttribution is the wire shape of a run's accountable-human provenance
// (MUL-4302 §9). Source/Precise/Evidence/lineage come straight from the row (pure);
// Initiator/Originator carry the raw user id always and the display name/email/avatar
// only after hydration on a user-facing surface.
type TaskAttribution struct {
	// Source is the waterfall level that resolved the accountable human:
	// direct_human | delegation | comment_source | rule_owner | owner_fallback |
	// backfill | unattributed. Never blank (a pre-migration NULL renders "unattributed").
	Source string `json:"source"`
	// Precise is false for degraded sources (owner_fallback / backfill / unattributed);
	// the UI marks these distinctly and they count against the coverage metric.
	Precise bool `json:"precise"`
	// Initiator is the accountable human (accountable_user_id). Nil when unattributed.
	Initiator *AttributionUser `json:"initiator,omitempty"`
	// Originator is the authorization human (originator_user_id); nil for autopilot
	// (rule_owner / owner_fallback), where no human authorized the run.
	Originator *AttributionUser `json:"originator,omitempty"`
	// Evidence points at the direct cause of the run so the UI can jump to it.
	Evidence            *TaskEvidence `json:"evidence,omitempty"`
	RuleVersionID       string        `json:"rule_version_id,omitempty"`
	DelegatedFromTaskID string        `json:"delegated_from_task_id,omitempty"`
	RetryOfTaskID       string        `json:"retry_of_task_id,omitempty"`
	RerunOfTaskID       string        `json:"rerun_of_task_id,omitempty"`
}

// AttributionUser is a departed-member-safe user ref, resolved from the global user
// table. Name/Email/AvatarURL are empty until hydrated.
type AttributionUser struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

// TaskEvidence is the kind-tagged handle to a run's direct cause (a comment,
// autopilot run, rule version, source task, ...).
type TaskEvidence struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
}

// taskAttributionBase builds the pure (no-DB) part of a run's attribution from the
// row: the source label + precise flag, evidence, lineage, and the raw initiator /
// originator user ids. Names are filled later by hydrateTaskAttributions.
func taskAttributionBase(t db.AgentTaskQueue) *TaskAttribution {
	src := attribution.Source(t.OriginatorSource.String)
	attr := &TaskAttribution{
		Source:              src.String(), // empty (pre-migration) → "unattributed"
		Precise:             src.Precise(),
		RuleVersionID:       uuidToString(t.RuleVersionID),
		DelegatedFromTaskID: uuidToString(t.DelegatedFromTaskID),
		RetryOfTaskID:       uuidToString(t.RetryOfTaskID),
		RerunOfTaskID:       uuidToString(t.RerunOfTaskID),
	}
	if t.AccountableUserID.Valid {
		attr.Initiator = &AttributionUser{ID: uuidToString(t.AccountableUserID)}
	}
	if t.OriginatorUserID.Valid {
		attr.Originator = &AttributionUser{ID: uuidToString(t.OriginatorUserID)}
	}
	if t.TriggerEvidenceKind.Valid && t.TriggerEvidenceKind.String != "" {
		attr.Evidence = &TaskEvidence{Kind: t.TriggerEvidenceKind.String, RefID: uuidToString(t.TriggerEvidenceRefID)}
	}
	return attr
}

// hydrateTaskAttributions fills the display name / email / avatar on the initiator
// and originator refs of the given attributions, resolving from the GLOBAL user
// table in one batch (departed-safe, N+1-free). Best-effort: on a lookup failure the
// raw user ids already present are left as-is — names are display sugar, not gating.
func (h *Handler) hydrateTaskAttributions(ctx context.Context, attrs []*TaskAttribution) {
	seen := make(map[string]struct{})
	var ids []pgtype.UUID
	add := func(ref *AttributionUser) {
		if ref == nil || ref.ID == "" {
			return
		}
		if _, ok := seen[ref.ID]; ok {
			return
		}
		if u, err := util.ParseUUID(ref.ID); err == nil {
			seen[ref.ID] = struct{}{}
			ids = append(ids, u)
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		add(a.Initiator)
		add(a.Originator)
	}
	if len(ids) == 0 {
		return
	}
	users, err := h.Queries.GetUsersByIDs(ctx, ids)
	if err != nil {
		return
	}
	byID := make(map[string]db.GetUsersByIDsRow, len(users))
	for _, u := range users {
		byID[uuidToString(u.ID)] = u
	}
	fill := func(ref *AttributionUser) {
		if ref == nil {
			return
		}
		if u, ok := byID[ref.ID]; ok {
			ref.Name = u.Name
			ref.Email = u.Email
			if u.AvatarUrl.Valid {
				ref.AvatarURL = h.resolveAvatarURL(u.AvatarUrl.String)
			}
		}
	}
	for _, a := range attrs {
		if a == nil {
			continue
		}
		fill(a.Initiator)
		fill(a.Originator)
	}
}

// attributionsOf collects the (non-nil) TaskAttribution pointers from a slice of
// task responses so a list endpoint can hydrate them all in one batch.
func attributionsOf(resps []AgentTaskResponse) []*TaskAttribution {
	out := make([]*TaskAttribution, 0, len(resps))
	for i := range resps {
		if resps[i].Attribution != nil {
			out = append(out, resps[i].Attribution)
		}
	}
	return out
}

// ChatAttachmentMeta is the structured attachment metadata embedded in
// claim responses for chat tasks. The agent uses these to run
// `multica attachment download <id>` rather than guessing from the
// markdown URL (which is signed and 30-min expiring on private CDN).
// The mirror struct on the daemon side lives in internal/daemon/types.go
// and uses the same JSON field names.
type ChatAttachmentMeta struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// CoalescedCommentData carries the full detail of a comment that was folded
// into a not-yet-started run (MUL-4195) so the daemon can embed it directly in
// the prompt. The earlier merge path only shipped comment IDs plus a
// "they are in the triggering thread" hint, which is WRONG when the folded
// comments span multiple threads (an issue's assignee can be triggered from
// different threads). Shipping thread_id / author / created_at / content lets
// the prompt address each folded comment without assuming a single thread or
// relying on a `--recent N` window that may not cover them all. The mirror
// struct on the daemon side lives in internal/daemon/types.go with the same
// JSON field names.
type CoalescedCommentData struct {
	ID         string `json:"id"`
	ThreadID   string `json:"thread_id,omitempty"`
	AuthorType string `json:"author_type,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Content    string `json:"content"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// SubIssueRef is one sub-issue of the task issue, as the claim
// snapshot carries it: enough for a coordinator to see who holds what and
// which stage is live, without a read per child.
type SubIssueRef struct {
	ID           string `json:"id"`
	Identifier   string `json:"identifier,omitempty"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Stage        int32  `json:"stage,omitempty"`
	AssigneeType string `json:"assignee_type,omitempty"`
	AssigneeName string `json:"assignee_name,omitempty"`
}

// maxClaimSubIssues bounds the list a claim carries; a larger tree is one
// `multica issue children` away and the snapshot says it was truncated.
const maxClaimSubIssues = 40

// claimSubIssues renders a parent's children for the claim snapshot. Names
// are resolved best-effort: an unresolvable holder still shows its type, and
// an unassigned child — the thing a coordinator most needs to see — shows
// none.
func (h *Handler) claimSubIssues(ctx context.Context, prefix string, children []db.Issue) []SubIssueRef {
	if len(children) == 0 {
		return nil
	}
	out := make([]SubIssueRef, 0, min(len(children), maxClaimSubIssues))
	names := map[string]string{}
	for _, child := range children {
		if len(out) == maxClaimSubIssues {
			break
		}
		item := SubIssueRef{
			ID:     uuidToString(child.ID),
			Title:  child.Title,
			Status: child.Status,
		}
		if prefix != "" {
			item.Identifier = service.IssueIdentifier(prefix, child.Number)
		}
		if child.Stage.Valid {
			item.Stage = child.Stage.Int32
		}
		if child.AssigneeType.Valid && child.AssigneeID.Valid {
			item.AssigneeType = child.AssigneeType.String
			key := item.AssigneeType + ":" + uuidToString(child.AssigneeID)
			name, seen := names[key]
			if !seen {
				switch item.AssigneeType {
				case "agent":
					if a, err := h.Queries.GetAgent(ctx, child.AssigneeID); err == nil {
						name = a.Name
					}
				case "member":
					if u, err := h.Queries.GetUser(ctx, child.AssigneeID); err == nil {
						name = u.Name
					}
				}
				names[key] = name
			}
			item.AssigneeName = name
		}
		out = append(out, item)
	}
	return out
}

// IssueContextComment is a bounded comment snapshot included in a daemon claim.
type IssueContextComment struct {
	ID             string `json:"id"`
	ThreadID       string `json:"thread_id,omitempty"`
	AuthorType     string `json:"author_type,omitempty"`
	Content        string `json:"content"`
	CreatedAt      string `json:"created_at,omitempty"`
	ReplyCount     int    `json:"reply_count,omitempty"`
	LastActivityAt string `json:"last_activity_at,omitempty"`
}

// TaskUsageData is one (provider, model) slice of a single run's token usage.
// Field names match the runtime/dashboard usage rows exactly so the client can
// feed it to the same `estimateCost` / `estimateCacheSavings` helpers without
// an adapter.
//
// CostUsdTicks is the provider's own price for these tokens (1e-10 USD) and is
// nil when the provider reported none — the client then estimates that slice
// from its rate table. A pointer, not a zero value: 0 ticks is a real answer
// ("the provider says this was free") and must stay distinguishable from
// "the provider said nothing".
type TaskUsageData struct {
	Provider             string `json:"provider,omitempty"`
	Model                string `json:"model"`
	InputTokens          int64  `json:"input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	CacheReadTokens      int64  `json:"cache_read_tokens"`
	CacheWriteTokens     int64  `json:"cache_write_tokens"`
	CostUsdTicks         *int64 `json:"cost_usd_ticks,omitempty"`
	NumTurns             int32  `json:"num_turns,omitempty"`
	Resumed              bool   `json:"resumed,omitempty"`
	SessionID            string `json:"session_id,omitempty"`
	LastContextTokens    *int64 `json:"last_context_tokens,omitempty"`
	QueueToClaimMS       *int64 `json:"queue_to_claim_ms,omitempty"`
	PrepareMS            *int64 `json:"prepare_ms,omitempty"`
	SpawnToFirstOutputMS *int64 `json:"spawn_to_first_output_ms,omitempty"`
	TotalMS              *int64 `json:"total_ms,omitempty"`
	AttributionSource    string `json:"attribution_source,omitempty"`
	TriggerEvidenceKind  string `json:"trigger_evidence_kind,omitempty"`
}

// TaskAgentData holds agent info included in claim responses so the daemon
// can set up the execution environment (branch naming, skill files, instructions).
type TaskAgentData struct {
	ID                    string                      `json:"id"`
	Name                  string                      `json:"name"`
	Instructions          string                      `json:"instructions"`
	Skills                []service.AgentSkillData    `json:"skills,omitempty"`
	SkillRefs             []service.AgentSkillRefData `json:"skill_refs,omitempty"`
	CustomEnv             map[string]string           `json:"custom_env,omitempty"`
	CustomArgs            []string                    `json:"custom_args,omitempty"`
	McpConfig             json.RawMessage             `json:"mcp_config,omitempty"`
	Model                 string                      `json:"model,omitempty"`
	ThinkingLevel         string                      `json:"thinking_level,omitempty"`
	ServiceTier           string                      `json:"service_tier,omitempty"`
	DisabledRuntimeSkills []DisabledRuntimeSkill      `json:"disabled_runtime_skills,omitempty"`
	// RuntimeConfig is the agent's saved runtime_config JSON as-is. The
	// daemon decodes it per-provider — e.g. the openclaw backend reads
	// `mode` + `gateway.*` to choose between embedded and gateway routing
	// (issue #3260). Other providers ignore the payload entirely. Sent
	// raw so the daemon can evolve its schema without a server roundtrip.
	RuntimeConfig json.RawMessage `json:"runtime_config,omitempty"`
}

// visibleTaskHistory omits unused assignee fallbacks created by older versions.
// Dispatch only begins preparation, so a fallback cancelled before StartTask
// is still unused. Keep started fallbacks and ordinary cancellations visible,
// and retain the underlying scheduling records for audit.
func visibleTaskHistory(tasks []db.AgentTaskQueue) []db.AgentTaskQueue {
	return slices.DeleteFunc(tasks, func(task db.AgentTaskQueue) bool {
		return task.EscalationForTaskID.Valid &&
			!task.StartedAt.Valid &&
			(task.Status == "deferred" || task.Status == "cancelled")
	})
}

// taskToResponse maps a queue row to its wire shape. workspaceID is threaded
// in because the row itself doesn't carry one (workspace lives on the agent
// / issue / chat session) — we ask the caller to resolve it once and pass it
// down. It populates WorkspaceID and powers the privacy-safe RelativeWorkDir
// derivation; pass "" only on daemon-facing paths that genuinely don't have
// it, in which case RelativeWorkDir falls back to the existing WorkDir.
func taskToResponse(t db.AgentTaskQueue, workspaceID string) AgentTaskResponse {
	var cancellation struct {
		TaskID string `json:"comment_change_cancelled_task_id"`
	}
	_ = json.Unmarshal(t.Context, &cancellation)
	var result any
	if t.Result != nil {
		json.Unmarshal(t.Result, &result)
	}
	failureReason := ""
	if t.FailureReason.Valid {
		failureReason = t.FailureReason.String
	}
	workDir := ""
	if t.WorkDir.Valid {
		workDir = t.WorkDir.String
	}
	durableWorkDir := ""
	if t.DurableWorkDir.Valid {
		durableWorkDir = t.DurableWorkDir.String
	}
	branchName := ""
	if t.BranchName.Valid {
		branchName = t.BranchName.String
	}
	handoffNote := ""
	if t.HandoffNote.Valid {
		handoffNote = t.HandoffNote.String
	}
	return AgentTaskResponse{
		// Task-scoped provenance must not transfer through copied retry context.
		CancelledByCommentChange: t.Status == "cancelled" && cancellation.TaskID != "" && cancellation.TaskID == uuidToString(t.ID),
		CancelledBy:              taskCancellationActorToResponse(t),

		ID:                     uuidToString(t.ID),
		AgentID:                uuidToString(t.AgentID),
		RuntimeID:              uuidToString(t.RuntimeID),
		WorkThreadID:           uuidToString(t.WorkThreadID),
		ContextGeneration:      t.ContextGeneration,
		ContextMessageLimit:    t.ContextMessageLimit,
		ContextTokenBudget:     t.ContextTokenBudget,
		ContinuityBreakReason:  textToString(t.ContinuityBreakReason),
		IssueID:                uuidToString(t.IssueID),
		WorkspaceID:            workspaceID,
		Status:                 t.Status,
		Priority:               t.Priority,
		DispatchedAt:           timestampToPtr(t.DispatchedAt),
		StartedAt:              timestampToPtr(t.StartedAt),
		CompletedAt:            timestampToPtr(t.CompletedAt),
		Result:                 result,
		Error:                  textToPtr(t.Error),
		FailureReason:          failureReason,
		BranchName:             branchName,
		CodeDecision:           codeDecisionFromRow(t.CodeDecision),
		Attempt:                t.Attempt,
		MaxAttempts:            t.MaxAttempts,
		ParentTaskID:           uuidToPtr(t.ParentTaskID),
		IsLeaderTask:           t.IsLeaderTask,
		CreatedAt:              timestampToString(t.CreatedAt),
		TriggerCommentID:       uuidToPtr(t.TriggerCommentID),
		CoalescedCommentIDs:    uuidsToStrings(t.CoalescedCommentIds),
		DeliveredCommentIDs:    uuidStringsOrEmpty(t.DeliveredCommentIds),
		TriggerSummary:         textToPtr(t.TriggerSummary),
		HandoffNote:            handoffNote,
		WorkDir:                workDir,
		RelativeWorkDir:        relativeWorkDir(workDir, workspaceID, uuidToString(t.ID)),
		DurableWorkDir:         durableWorkDir,
		RelativeDurableWorkDir: relativeWorkDir(durableWorkDir, "", ""),
		// Surface the stable task source. A successful quick-create gains an
		// issue link for navigation but retains its quick_create kind.
		ChatSessionID:  uuidToString(t.ChatSessionID),
		AutopilotRunID: uuidToString(t.AutopilotRunID),
		Kind:           computeTaskKind(t),
		// Attribution labels + evidence + lineage + raw user ids (pure). Names are
		// hydrated separately on user-facing surfaces (MUL-4302 §9).
		Attribution: taskAttributionBase(t),
	}
}

func taskCancellationActorToResponse(t db.AgentTaskQueue) *TaskCancellationActor {
	if t.Status != "cancelled" || !t.CancelledByType.Valid || t.CancelledByType.String == "" {
		return nil
	}
	actor := &TaskCancellationActor{
		Type: t.CancelledByType.String,
		ID:   uuidToString(t.CancelledByID),
	}
	if t.CancelledByName.Valid {
		actor.Name = t.CancelledByName.String
	}
	return actor
}

// relativeWorkDir produces a privacy-safe display form of the daemon-reported
// absolute work_dir. The contract: the returned string must never contain
// the user's home directory prefix or their account name. The chip is
// rendered in transcripts that frequently end up in screen shares,
// screenshots, and recordings, so this function is the only guard.
//
//   - For standard tasks, it validates the adjacent workspace/task segments
//     by their stable ID suffixes, then strips everything before them. This
//     accepts both legacy `<wsUUID>/<taskShort>` roots and readable
//     `<workspaceSlug>-<wsShort>/<issueKey>-<taskShort>` roots without treating
//     the labels as identity.
//   - For local_directory tasks the absolute path lives outside the envRoot
//     layout. We try to recognise common home-directory prefixes
//     (`/Users/<name>/`, `/home/<name>/`, `<drive>:/Users/<name>/`) and strip
//     them, returning the remainder (e.g. `repos/foo`). When the prefix
//     can't be recognised — unusual home layouts, network mounts, paths
//     under `/opt`, `/srv`, etc. — we fall back to the basename so we never
//     accidentally render a path component that happens to be a username.
//
// Returns empty when work_dir is empty, or when stripping leaves nothing
// (i.e. work_dir was exactly the user's home — rendering nothing is
// preferable to a chip that says `<name>`). taskDirSegment() must stay in
// lock-step with server/internal/daemon/execenv/git.go:taskKey — both
// consume the same task UUID. legacyTaskDirSegment keeps privacy-safe display
// working for pre-#7347 roots and roots created by an earlier build of this PR.
func relativeWorkDir(workDir, workspaceID, taskID string) string {
	if workDir == "" {
		return ""
	}
	// Normalize Windows separators so the rest of the function only
	// reasons about forward slashes.
	normalized := strings.ReplaceAll(workDir, "\\", "/")

	if workspaceID != "" && taskID != "" {
		parts := strings.Split(normalized, "/")
		for i := 0; i+1 < len(parts); i++ {
			if matchesWorkspacePathSegment(parts[i], workspaceID) &&
				matchesTaskPathSegment(parts[i+1], taskID) {
				return strings.Join(parts[i:], "/")
			}
		}
	}

	if stripped, ok := stripHomePrefix(normalized); ok {
		return stripped
	}

	return basename(normalized)
}

func matchesWorkspacePathSegment(segment, workspaceID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(workspaceID)
	current := strings.ToLower(taskDirSegment(workspaceID))
	return strings.EqualFold(segment, workspaceID) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

func matchesTaskPathSegment(segment, taskID string) bool {
	lower := strings.ToLower(segment)
	legacy := legacyTaskDirSegment(taskID)
	current := strings.ToLower(taskDirSegment(taskID))
	return strings.EqualFold(segment, legacy) || strings.EqualFold(segment, current) ||
		strings.HasSuffix(lower, "-"+legacy) || strings.HasSuffix(lower, "-"+current)
}

// taskDirSegmentLen and taskDirSegment mirror execenv.taskKeyLen /
// execenv.taskKey — the LAST 12 hex chars of the task id. Kept inline here so
// the agent handler has zero imports from the daemon package (which would
// create an unwanted cycle between handler and daemon).
//
// It must keep taking the TAIL. A UUIDv7's leading hex chars are timestamp
// bits shared by every task created within ~65.5s (#7326); the daemon stopped
// reading from that end, and this reconstruction only matches while both sides
// agree.
const taskDirSegmentLen = 12

func taskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > taskDirSegmentLen {
		return s[len(s)-taskDirSegmentLen:]
	}
	return s
}

// legacyTaskDirSegment mirrors the historical shortID layout: the first eight
// dash-free characters. It is display compatibility only, never new identity.
func legacyTaskDirSegment(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	if len(s) > 8 {
		return strings.ToLower(s[:8])
	}
	return strings.ToLower(s)
}

// homeDirPattern matches the well-known per-user home layouts on macOS,
// Linux, and Windows after backslash normalization:
//
//	/Users/<name>[/<rest>]
//	/home/<name>[/<rest>]
//	<drive>:/Users/<name>[/<rest>]
//
// Case-insensitive because macOS and Windows are case-insensitive at the
// filesystem layer; matching `/users/...` the same as `/Users/...` keeps
// the strip robust against unusual casings seen on shared drives.
// Capture group 1 is the optional remainder after the username segment.
var homeDirPattern = regexp.MustCompile(`(?i)^(?:[A-Za-z]:)?/(?:Users|home)/[^/]+(?:/(.*))?$`)

// stripHomePrefix recognises common home-directory layouts and returns
// the path remainder after the username segment. Returns (remainder, true)
// when a known home prefix matched. The remainder may be the empty string
// (work_dir was exactly the home directory) — the caller treats that as
// "nothing safe to display".
func stripHomePrefix(p string) (string, bool) {
	m := homeDirPattern.FindStringSubmatch(p)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// basename returns the last non-empty segment of a forward-slash path.
// Used as the ultimate privacy-safe fallback when we can't otherwise
// recognise the path: a single segment can never expose the home prefix,
// and the leaf is almost always the most useful piece of context anyway
// (typically the repo directory name for local_directory tasks).
func basename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// computeTaskKind picks the stable source-discriminator string task UIs use.
// Chat and autopilot have dedicated FKs; quick-create must inspect its context
// because completion links the newly created issue back onto the task. The
// remaining issue tasks split into comment-triggered and direct runs.
func computeTaskKind(t db.AgentTaskQueue) string {
	if uuidToString(t.ChatSessionID) != "" {
		return "chat"
	}
	if uuidToString(t.AutopilotRunID) != "" {
		return "autopilot"
	}
	var contextKind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(t.Context, &contextKind) == nil && contextKind.Type == service.QuickCreateContextType {
		return "quick_create"
	}
	// Preserve the historical classification for issue-less rows from before
	// quick-create stored a typed context.
	if uuidToString(t.IssueID) == "" {
		return "quick_create"
	}
	if uuidToString(t.TriggerCommentID) != "" {
		return "comment"
	}
	return "direct"
}

// loadAgentRuntimeAvailability returns only a coarse liveness bucket for
// agent presence. Runtime rows are loaded internally even when the caller is
// not allowed to list or inspect the private runtime; no runtime fields are
// copied onto an agent response. The existing runtime-list visibility contract
// is mirrored so the bucket is only copied when the full row is hidden.
func (h *Handler) loadAgentRuntimeAvailability(ctx context.Context, agents []db.Agent, workspaceID, viewerID, viewerRole string, now time.Time) (map[string]string, error) {
	// Owner/admin runtime lists already contain every row, so their normal
	// client-side derivation is authoritative and no projection query is needed.
	if roleAllowed(viewerRole, "owner", "admin") {
		return map[string]string{}, nil
	}

	runtimeIDs := make([]pgtype.UUID, 0, len(agents))
	for _, agent := range agents {
		// Archived presence always resolves to "archived", so its runtime state
		// is neither user-visible nor a reason for clients to keep polling.
		if !agent.ArchivedAt.Valid && agent.RuntimeID.Valid {
			runtimeIDs = append(runtimeIDs, agent.RuntimeID)
		}
	}
	result := make(map[string]string, len(runtimeIDs))
	if len(runtimeIDs) == 0 {
		return result, nil
	}

	// Read directly rather than through RuntimeLookup: this resolves rows for a
	// list of agents instead of resolving a runtime a caller asked for, so it
	// has no honest source label on multica_agent_runtime_lookup_total yet. See
	// the exception noted on service.RuntimeLookup (MUL-6884).
	runtimes, err := h.Queries.GetAgentRuntimes(ctx, runtimeIDs)
	if err != nil {
		return nil, err
	}
	for _, runtime := range runtimes {
		// Agent/runtime workspace consistency is normally enforced at bind time;
		// keep the projection fail-closed if a legacy row violates it.
		if uuidToString(runtime.WorkspaceID) != workspaceID {
			continue
		}
		// ListAgentRuntimes exposes every row to workspace owner/admin and only
		// owner/public rows to regular members. Keep the coarse bridge for the
		// rows that the viewer's runtime list cannot carry.
		if runtime.Visibility == "public" ||
			(runtime.OwnerID.Valid && uuidToString(runtime.OwnerID) == viewerID) {
			continue
		}
		result[uuidToString(runtime.ID)] = deriveAgentRuntimeAvailability(runtime, now)
	}
	return result, nil
}

func deriveAgentRuntimeAvailability(runtime db.AgentRuntime, now time.Time) string {
	status := pgtype.Text{String: runtime.Status, Valid: runtime.Status != ""}
	return deriveRuntimeAvailability(status, runtime.LastSeenAt, now)
}

func (h *Handler) ListAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	userID := requestUserID(r)

	var agents []db.Agent
	var err error
	if r.URL.Query().Get("include_archived") == "true" {
		agents, err = h.Queries.ListAllAgents(r.Context(), parseUUID(workspaceID))
	} else {
		agents, err = h.Queries.ListAgents(r.Context(), parseUUID(workspaceID))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents")
		return
	}
	runtimeAvailabilityByID, err := h.loadAgentRuntimeAvailability(r.Context(), agents, workspaceID, userID, member.Role, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent runtime availability")
		return
	}

	// Batch-load skills for all agents to avoid N+1.
	skillRows, err := h.Queries.ListAgentSkillsByWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	skillMap := map[string][]AgentSkillSummary{}
	for _, row := range skillRows {
		agentID := uuidToString(row.AgentID)
		skillMap[agentID] = append(skillMap[agentID], AgentSkillSummary{
			ID:          uuidToString(row.ID),
			Name:        row.Name,
			Description: row.Description,
			Enabled:     row.Enabled,
		})
	}

	// mcp_config still uses the workspace-level always-redact setting and
	// the per-row owner/admin gate — secrets in MCP server configs follow
	// the same exposure rules as custom_env used to. custom_env itself is
	// never serialized on agent resources anymore (MUL-2600); see the
	// AgentResponse comment.
	ws, err := h.Queries.GetWorkspace(r.Context(), parseUUID(workspaceID))
	if err != nil {
		slog.Warn("GetWorkspace failed for redact check", "workspace_id", workspaceID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	alwaysRedact := workspaceAlwaysRedactSecrets(ws.Settings)

	// Resolve the request actor once. Agents bypass the view gate to preserve
	// A2A collaboration; members see a private agent only when they own it or
	// are workspace owner/admin, and a public_to agent only when on its
	// invocation allow-list. Targets are batch-loaded to avoid an N+1 and
	// reused to enrich each response's invocation_targets.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	targetsByAgent, ok := h.loadInvocationTargetsByAgent(r.Context(), agents)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	var passAgents map[string]struct{}
	if actorType == "member" {
		passAgents = h.activePassAgentIDs(r.Context(), workspaceID, actorID)
	}
	pauses := h.workPauses(r.Context(), parseUUID(workspaceID))
	visible := make([]AgentResponse, 0, len(agents))
	for _, a := range agents {
		targets := targetsByAgent[uuidToString(a.ID)]
		if actorType == "member" {
			if !memberAllowedToViewAgentWithPasses(a, targets, actorID, member.Role, passAgents) {
				continue
			}
		}
		resp := h.agentToResponse(a)
		applyWorkPause(&resp, a, pauses)
		// The map is keyed by runtime, and active + archived agents may share one.
		// Keep the archived guard here as well as in the loader so an active sibling
		// cannot leak its projection onto an archived response.
		if availability, ok := runtimeAvailabilityByID[resp.RuntimeID]; ok && !a.ArchivedAt.Valid {
			resp.RuntimeAvailability = availability
		}
		applyInvocationTargetsToResponse(&resp, targets)
		if skills, ok := skillMap[resp.ID]; ok {
			resp.Skills = skills
		}
		// Agent actors NEVER see mcp_config secrets, even when their host's
		// PAT would normally satisfy the owner/admin role gate. Otherwise an
		// agent running under an owner's daemon could read other agents'
		// MCP configs (which routinely embed third-party API tokens) — the
		// same lateral-movement vector MUL-2600 closed for custom_env.
		if actorType == "agent" || alwaysRedact || !canViewAgentSecrets(a, userID, member.Role) {
			redactMcpConfig(&resp)
		}
		// composio_toolkit_allowlist is owner-only — not because the slugs
		// are secret but because surfacing "what {owner} has opted into"
		// across the workspace leaks the owner's integration footprint and
		// confuses non-owners who cannot actually edit it. Workspace
		// owner/admin do NOT bypass this gate (unlike mcp_config): the overlay
		// uses the OWNER's connection and follows invocation permission
		// (MUL-3963), so surfacing the slugs to admins gives them nothing
		// actionable. Agent actors are also redacted (same A2A
		// lateral-movement reasoning as mcp_config).
		if !h.composioMCPAppsEnabled(r.Context()) {
			suppressComposioToolkitAllowlist(&resp)
		} else if actorType == "agent" || uuidToString(a.OwnerID) != userID {
			redactComposioToolkitAllowlist(&resp)
		}
		visible = append(visible, resp)
	}

	// Nested grouping data for the specialisation tree (DENE-301): the base
	// role behind each child, plus how many active children each base role has.
	// Two batch reads for the whole list — the front end must not have to make a
	// request per row to build the tree.
	if err := h.enrichAgentResponsesWithRelations(r.Context(), visible); err != nil {
		slog.Warn("list agents: load parent relations failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
	}

	writeJSON(w, http.StatusOK, visible)
}

func (h *Handler) GetAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// Private-agent gate: members must be in allowed_principals to view
	// (and therefore navigate to) a private agent. The 403 lets the front-end
	// render an explicit "no access" placeholder instead of a 404 — see
	// agent-detail-page.tsx.
	workspaceID := uuidToString(agent.WorkspaceID)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	resp := h.agentToResponse(agent)
	if !agent.WorkEnabled {
		applyWorkPause(&resp, agent, h.workPauses(r.Context(), agent.WorkspaceID))
	}
	// resp is a slice, not a pointer, so the enrichment must write through the
	// slice element it is handed: taking the address of the local variable here
	// would fill a copy and serve an empty parent_agent_name.
	resps := []AgentResponse{resp}
	if err := h.enrichAgentResponsesWithRelations(r.Context(), resps); err != nil {
		// Non-fatal: the id is already on the response, and a viewer who can see
		// the child but not resolve the parent still gets a usable payload.
		slog.Warn("get agent: load parent relation failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
	}
	resp = resps[0]
	if err := h.attachAgentInheritance(r.Context(), &resp, actorType, actorID); err != nil {
		slog.Warn("get agent: load inherited prompt/skills failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(agent.ID))...)
	}
	viewerRole := ""
	if member, ok := ctxMember(r.Context()); ok {
		viewerRole = member.Role
	}
	runtimeAvailability, err := h.loadAgentRuntimeAvailability(r.Context(), []db.Agent{agent}, workspaceID, userID, viewerRole, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent runtime availability")
		return
	}
	if availability, ok := runtimeAvailability[resp.RuntimeID]; ok {
		resp.RuntimeAvailability = availability
	}
	if !h.enrichAgentResponseWithTargetsHTTP(w, r, &resp, agent.ID) {
		return
	}
	// Use the summary query (no `content` column) — the embedded
	// AgentSkillSummary only needs id/name/description, and reading large
	// SKILL.md bodies just to discard them is the exact regression we fixed
	// in #2174.
	if err := h.attachAgentSkills(r.Context(), &resp, agent.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}

	// mcp_config redaction (custom_env was removed from this response shape
	// in MUL-2600; secrets are now fetched via GET /api/agents/{id}/env).
	ws, err := h.Queries.GetWorkspace(r.Context(), agent.WorkspaceID)
	if err != nil {
		slog.Warn("GetWorkspace failed for redact check", "workspace_id", uuidToString(agent.WorkspaceID), "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	alwaysRedact := workspaceAlwaysRedactSecrets(ws.Settings)
	// Agent actors NEVER see mcp_config (see ListAgents for the rationale).
	if actorType == "agent" || alwaysRedact {
		redactMcpConfig(&resp)
	} else if member, ok := ctxMember(r.Context()); ok {
		if !canViewAgentSecrets(agent, userID, member.Role) {
			redactMcpConfig(&resp)
		}
	}
	// composio_toolkit_allowlist visibility is strictly owner-only (see
	// ListAgents for the rationale). No workspace owner/admin bypass.
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if actorType == "agent" || uuidToString(agent.OwnerID) != userID {
		redactComposioToolkitAllowlist(&resp)
	}

	writeJSON(w, http.StatusOK, resp)
}

type CreateAgentRequest struct {
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	Instructions         string                     `json:"instructions"`
	ConversationStarters []AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                    `json:"avatar_url"`
	RuntimeID            string                     `json:"runtime_id"`
	RuntimeConfig        any                        `json:"runtime_config"`
	CustomEnv            map[string]string          `json:"custom_env"`
	CustomArgs           []string                   `json:"custom_args"`
	McpConfig            json.RawMessage            `json:"mcp_config"`
	Visibility           string                     `json:"visibility"`
	// PermissionMode + InvocationTargets are the new invocation-permission
	// inputs (MUL-3963). When permission_mode is present it is authoritative
	// and Visibility is ignored; when absent, legacy Visibility is mapped
	// (private -> private, workspace -> public_to+workspace target). On create
	// only the caller can be the owner, so targets are accepted unconditionally.
	PermissionMode     *string                    `json:"permission_mode"`
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	Model              string                     `json:"model"`
	ThinkingLevel      string                     `json:"thinking_level"`
	ServiceTier        string                     `json:"service_tier"`
	// RoutingTier is the seat's rung on the dispatch ladder (DENE-633).
	// Empty on a specialisation inherits the base role's rung: a direction
	// seat is the same strength as the seat it specialises.
	RoutingTier string `json:"routing_tier"`
	// ComposioToolkitAllowlist seeds the per-task overlay gate (MUL-3869). On
	// create only the calling user can be the owner, so we accept the field
	// unconditionally here; the cross-owner permission gate lives on PUT.
	// Nil = leave column NULL (no overlay). Empty slice = explicit `{}` (no
	// overlay either, but the column reads as "configured" — distinct from
	// "owner has never opened the integration").
	ComposioToolkitAllowlist []string `json:"composio_toolkit_allowlist"`
	// Template records the creation-source attribution used by the
	// `agent_created` analytics event (for example, "agent_builder"). Empty
	// identifies a manually authored agent.
	Template string `json:"template"`
	// SkillIDs are attached inside the same transaction as the agent row so a
	// create never becomes visible in a partially configured state.
	SkillIDs []string `json:"skill_ids"`
	// ParentAgentID makes this agent a specialisation of an existing base role
	// (DENE-301). Empty creates a base role. The target must be a base role in
	// the same workspace; a specialisation cannot be specialised in turn.
	ParentAgentID string `json:"parent_agent_id"`
	// RuntimeInherited makes the new specialisation follow its base role's
	// runtime profile (DENE-505). Omitted with a parent means "follow": that is
	// the default for a specialisation, so runtime_id may then be omitted
	// entirely and any runtime fields sent alongside are not used — the child
	// takes the base role's. Explicit false opts out, and then this endpoint
	// behaves exactly as it did before DENE-505 (runtime_id required, model /
	// thinking_level / runtime_config as given). Setting it true without a
	// parent_agent_id is a 400.
	RuntimeInherited *bool `json:"runtime_inherited"`
}

func decodeJSONBodyWithRawFields(body io.Reader, dst any) (map[string]json.RawMessage, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(payload, dst); err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}

	return raw, nil
}

func normaliseAgentConversationStarters(starters []AgentConversationStarter) ([]AgentConversationStarter, error) {
	if len(starters) > maxAgentConversationStarters {
		return nil, fmt.Errorf("conversation_starters must contain at most %d items", maxAgentConversationStarters)
	}

	normalised := make([]AgentConversationStarter, 0, len(starters))
	for i, item := range starters {
		item.Label = strings.TrimSpace(item.Label)
		item.Prompt = strings.TrimSpace(item.Prompt)
		if item.Label == "" {
			return nil, fmt.Errorf("conversation_starters[%d].label is required", i)
		}
		if item.Prompt == "" {
			return nil, fmt.Errorf("conversation_starters[%d].prompt is required", i)
		}
		if utf8.RuneCountInString(item.Label) > maxAgentConversationStarterLabel {
			return nil, fmt.Errorf("conversation_starters[%d].label must be %d characters or fewer", i, maxAgentConversationStarterLabel)
		}
		if utf8.RuneCountInString(item.Prompt) > maxAgentConversationStarterLength {
			return nil, fmt.Errorf("conversation_starters[%d].prompt must be %d characters or fewer", i, maxAgentConversationStarterLength)
		}
		normalised = append(normalised, item)
	}
	return normalised, nil
}

func (h *Handler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)

	var req CreateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ownerID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(req.Description) > maxAgentDescriptionLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
		return
	}
	// A base role must name its runtime. A specialisation may omit it and
	// follow its base role's runtime instead (DENE-505); whether that is what
	// this request means is decided below, once the base role resolves.
	if req.RuntimeID == "" && req.ParentAgentID == "" {
		writeError(w, http.StatusBadRequest, "runtime_id is required")
		return
	}
	conversationStarters, err := normaliseAgentConversationStarters(req.ConversationStarters)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if err := defaultAndValidateAgentMaxConcurrentTasks(rawFields, &req.MaxConcurrentTasks); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// Resolve invocation permission (MUL-3963). permission_mode is
	// authoritative when present; otherwise the legacy visibility value is
	// mapped. On create the caller is always the owner, so targets are
	// accepted unconditionally.
	_, hasTargets := rawFields["invocation_targets"]
	legacyVis := req.Visibility
	perm, _, permErr := parsePermissionInput(wsUUID, req.PermissionMode, req.InvocationTargets, req.PermissionMode != nil, hasTargets, &legacyVis)
	if permErr != nil {
		writeError(w, http.StatusBadRequest, permErr.Error())
		return
	}

	// Two-level specialisation (DENE-301). An empty/absent parent_agent_id
	// creates a base role; a non-empty one must resolve to a base role in this
	// workspace, which is what stops a specialisation from being specialised in
	// turn. A create has no children yet, so the "parent cannot itself become a
	// child" arm of the rule cannot fire here.
	//
	// Resolved BEFORE the runtime (DENE-505) because inheriting the base role's
	// runtime is defined in terms of this row — there is no runtime_id to look
	// up when the caller asked to follow.
	var parentAgent db.Agent
	var parentAgentUUID pgtype.UUID
	if req.ParentAgentID != "" {
		parentActorType, parentActorID := h.resolveActor(r, ownerID, workspaceID)
		parent, reject := h.validateAgentParent(r.Context(), workspaceID, req.ParentAgentID, "", parentActorType, parentActorID)
		if reject != "" {
			writeError(w, http.StatusBadRequest, reject)
			return
		}
		parentAgent = parent
		parentAgentUUID = parent.ID
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	// Runtime inheritance (DENE-505): a specialisation follows its base role's
	// runtime profile by default. `runtime_inherited: false` is the explicit
	// opt-out and the only thing that makes the request's own runtime fields
	// authoritative; a base role can never inherit, so asking for it there is a
	// 400 rather than a silently ignored field.
	inheritRuntime := parentAgentUUID.Valid
	if req.RuntimeInherited != nil {
		if *req.RuntimeInherited && !parentAgentUUID.Valid {
			writeError(w, http.StatusBadRequest, "runtime_inherited needs a parent_agent_id: only a specialisation can follow a base role's runtime")
			return
		}
		inheritRuntime = *req.RuntimeInherited
	}

	// A specialisation may only follow its base role onto a runtime this member
	// can actually use. Dispatch refuses a private runtime whose owner is not
	// the agent's owner (see the daemon claim), so inheriting onto one would
	// create an agent that can never run. The caller's own runtime_id is then
	// the honest fallback — the pre-DENE-505 behaviour for attaching to another
	// member's base role — and without one there is nothing to bind to.
	inheritedRuntimeStatus := ""
	inheritedRuntimeProvider := ""
	if inheritRuntime && parentAgent.RuntimeID.Valid {
		if parentRuntime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          parentAgent.RuntimeID,
			WorkspaceID: wsUUID,
		}); err == nil {
			if !canUseRuntimeForAgent(member, parentRuntime) {
				if req.RuntimeID == "" {
					writeError(w, http.StatusForbidden, "this base role runs on a runtime that is private to its owner; pass runtime_id with runtime_inherited=false to give this agent its own runtime")
					return
				}
				slog.Info("create agent: base role runtime is not usable by the caller; creating with the requested runtime instead",
					append(logger.RequestAttrs(r), "parent_agent_id", req.ParentAgentID)...)
				inheritRuntime = false
			} else {
				inheritedRuntimeStatus = parentRuntime.Status
				inheritedRuntimeProvider = parentRuntime.Provider
			}
		}
	}

	// The resolved runtime of an agent that OWNS its configuration. Left zero
	// for an inherited specialisation: its profile comes from the base role.
	var runtime db.AgentRuntime
	if !inheritRuntime {
		if req.RuntimeID == "" {
			writeError(w, http.StatusBadRequest, "runtime_id is required unless the agent inherits its base role's runtime")
			return
		}
		runtimeUUID, ok := parseUUIDOrBadRequest(w, req.RuntimeID, "runtime_id")
		if !ok {
			return
		}
		runtime, err = h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid runtime_id")
			return
		}
		if !canUseRuntimeForAgent(member, runtime) {
			writeError(w, http.StatusForbidden, "this runtime is private; only its owner can create agents on it")
			return
		}

		// thinking_level validation: fixed-enum providers reject unknown literals;
		// dynamic-catalog providers (Codex/OpenCode) reject malformed tokens here.
		// Pi has a fixed token universe and a daemon-discovered per-model subset.
		// Per-model gaps are enforced by the daemon at execution time (MUL-2339):
		// combination-invalid values are logged and omitted from the invocation.
		//
		// Shared with the issue-draft carrier create (DENE-514) rather than
		// repeated: an alignment carrier that runs at the default effort while
		// its picker says "Extra high" is the same lie as an agent whose saved
		// level was silently dropped, and two copies of these three steps is
		// how the two answers drift apart.
		if !h.thinkingLevelAcceptedForRuntime(w, r, runtime, req.ThinkingLevel, req.Model) {
			return
		}
		if !agent.IsKnownServiceTier(runtime.Provider, req.ServiceTier) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("service_tier %q is not a recognised value for runtime %q", req.ServiceTier, runtime.Provider))
			return
		}
	}

	// Probe workspace agent count BEFORE the insert so the funnel has a
	// clean "first agent ever in this workspace" signal — Step 4 of
	// onboarding always lands in this branch. A non-fatal read: if the
	// list fails we fall through with isFirstAgent=false rather than
	// blocking creation, since the primary DB operation is the insert.
	isFirstAgent := false
	if existing, listErr := h.Queries.ListAgents(r.Context(), wsUUID); listErr == nil {
		isFirstAgent = len(existing) == 0
	}

	// A create has no prior token to restore, so if the caller submitted the
	// public mask sentinel as gateway.token (e.g. replayed a masked GET body)
	// drop it rather than persisting a literal "***" as a real bearer token.
	preserveMaskedGatewayToken(req.RuntimeConfig, nil)
	rc, _ := json.Marshal(req.RuntimeConfig)
	if req.RuntimeConfig == nil {
		rc = []byte("{}")
	}

	ce, _ := json.Marshal(req.CustomEnv)
	if req.CustomEnv == nil {
		ce = []byte("{}")
	}

	ca, _ := json.Marshal(req.CustomArgs)
	if req.CustomArgs == nil {
		ca = []byte("[]")
	}

	sp, _ := json.Marshal(conversationStarters)

	var mc []byte
	if rawMcpConfig, ok := rawFields["mcp_config"]; ok && !bytes.Equal(bytes.TrimSpace(rawMcpConfig), []byte("null")) {
		mc = append([]byte(nil), rawMcpConfig...)
	}

	// composio_toolkit_allowlist: the JSON field is a list-of-slugs that gets
	// stored as TEXT[]. We normalise here (lowercase + trim + dedupe) so the
	// dispatch path can compare against per-user connection rows with a
	// straight equality. A nil slice (or absent JSON key) maps to a NULL
	// column on insert. An explicitly empty list (`[]`) is preserved as an
	// empty TEXT[] (the dispatch path treats NULL and `{}` identically).
	allowlist := normaliseComposioToolkitAllowlist(req.ComposioToolkitAllowlist)
	if !h.composioMCPAppsEnabled(r.Context()) {
		allowlist = nil
	}

	skillUUIDs, ok := parseUUIDSliceOrBadRequest(w, req.SkillIDs, "skill_ids")
	if !ok {
		return
	}
	for _, skillID := range skillUUIDs {
		if _, err := h.Queries.GetSkillInWorkspace(r.Context(), db.GetSkillInWorkspaceParams{
			ID:          skillID,
			WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "skill does not belong to this workspace")
			return
		}
	}

	avatarURL, ok := h.newAgentAvatar(w, r, req.AvatarURL)
	if !ok {
		return
	}

	// The profile this row is born with. An inherited specialisation takes every
	// runtime-scoped value from its base role instead of the request: the
	// caller asked to follow, so a runtime_id / model / thinking_level /
	// runtime_config in the same payload is not an override — `runtime_inherited:
	// false` is how a caller asks for those to be its own (DENE-505).
	createdRuntimeMode := runtime.RuntimeMode
	createdRuntimeID := runtime.ID
	createdRuntimeConfig := rc
	createdModel := pgtype.Text{String: req.Model, Valid: req.Model != ""}
	createdThinkingLevel := pgtype.Text{String: req.ThinkingLevel, Valid: req.ThinkingLevel != ""}
	createdServiceTier := pgtype.Text{String: req.ServiceTier, Valid: req.ServiceTier != ""}
	routingTierKey, tierOK := routing.DefaultLadder.NormalizeTier(req.RoutingTier)
	if !tierOK {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"routing_tier %q is not a known tier; expected one of %s",
			req.RoutingTier, strings.Join(routing.DefaultLadder.TierKeys(), ", ")))
		return
	}
	createdRoutingTier := pgtype.Text{String: routingTierKey, Valid: routingTierKey != ""}
	if parentAgent.ID.Valid && routingTierKey == "" {
		// A specialisation with no rung of its own sits on its base role's
		// rung. Strength follows the runtime profile it was cloned from, so
		// leaving the 16 direction seats untagged would take them all off the
		// ladder the moment tags become how rungs are decided.
		createdRoutingTier = parentAgent.RoutingTier
	}
	if inheritRuntime {
		createdRuntimeMode = parentAgent.RuntimeMode
		createdRuntimeID = parentAgent.RuntimeID
		createdRuntimeConfig = parentAgent.RuntimeConfig
		createdModel = parentAgent.Model
		createdThinkingLevel = parentAgent.ThinkingLevel
		createdServiceTier = parentAgent.ServiceTier
		if followsExecutionConfig(parseUUID(ownerID), parentAgent.OwnerID) {
			ce = parentAgent.CustomEnv
			ca = parentAgent.CustomArgs
			mc = parentAgent.McpConfig
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start agent create transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	created, err := qtx.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:              wsUUID,
		Name:                     req.Name,
		Description:              req.Description,
		Instructions:             req.Instructions,
		AvatarUrl:                avatarURL,
		RuntimeMode:              createdRuntimeMode,
		RuntimeConfig:            createdRuntimeConfig,
		RuntimeID:                createdRuntimeID,
		Visibility:               perm.legacyVisibility(),
		PermissionMode:           perm.mode,
		MaxConcurrentTasks:       req.MaxConcurrentTasks,
		OwnerID:                  parseUUID(ownerID),
		CustomEnv:                ce,
		CustomArgs:               ca,
		McpConfig:                mc,
		Model:                    createdModel,
		ThinkingLevel:            createdThinkingLevel,
		ServiceTier:              createdServiceTier,
		RoutingTier:              createdRoutingTier,
		ConversationStarters:     sp,
		ComposioToolkitAllowlist: allowlist,
		ParentAgentID:            parentAgentUUID,
		RuntimeInherited:         pgtype.Bool{Bool: inheritRuntime, Valid: true},
	})
	if err != nil {
		// Unique constraint on (workspace_id, name) — return a clear conflict error
		// so the UI can show the right message instead of a generic 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", req.Name))
			return
		}
		slog.Warn("create agent failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to create agent: "+err.Error())
		return
	}
	if err := replaceInvocationTargetsWithQueries(r.Context(), qtx, created.ID, parseUUID(ownerID), perm.targets); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save agent access")
		return
	}
	for _, skillID := range skillUUIDs {
		if err := qtx.AddAgentSkill(r.Context(), db.AddAgentSkillParams{
			AgentID: created.ID,
			SkillID: skillID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to attach agent skill")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit agent create")
		return
	}
	slog.Info("agent created", append(logger.RequestAttrs(r), "agent_id", uuidToString(created.ID), "name", created.Name, "workspace_id", workspaceID)...)

	// An inherited specialisation binds the base role's runtime, so its liveness
	// is that runtime's, not the (absent) request runtime's.
	createdRuntimeStatus := runtime.Status
	if inheritRuntime {
		createdRuntimeStatus = inheritedRuntimeStatus
	}
	if createdRuntimeStatus == "online" {
		h.TaskService.ReconcileAgentStatus(r.Context(), created.ID)
		created, _ = h.Queries.GetAgent(r.Context(), created.ID)
	}

	resp := h.agentToResponse(created)
	if err := h.attachAgentSkills(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create agent: load skills for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, created.ID); err != nil {
		slog.Warn("create agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	// A freshly created agent has no children yet, so this only fills
	// parent_agent_name + child_count=0 — the same shape ListAgents serves, so
	// a client that inserts the create response into its list cache sees no
	// difference between the two. Written through a slice for the same reason
	// as GetAgent: a slice element is addressable, a local variable's copy is not.
	resps := []AgentResponse{resp}
	if err := h.enrichAgentResponsesWithRelations(r.Context(), resps); err != nil {
		slog.Warn("create agent: load parent relation for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(created.ID))...)
	}
	resp = resps[0]
	actorType, actorID := h.resolveActor(r, ownerID, workspaceID)
	h.publish(protocol.EventAgentCreated, workspaceID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})

	// Reported for the profile the agent actually got: an inherited
	// specialisation has no request runtime of its own, and reporting the empty
	// zero value would make the funnel look like a runtime-less create.
	createdRuntimeProvider := runtime.Provider
	if inheritRuntime {
		createdRuntimeProvider = inheritedRuntimeProvider
	}
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.AgentCreated(
		ownerID,
		workspaceID,
		uuidToString(created.ID),
		createdRuntimeProvider,
		createdRuntimeMode,
		req.Template,
		isFirstAgent,
	))

	redactAgentResponseForActor(&resp, actorType)
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusCreated, resp)
}

type UpdateAgentRequest struct {
	Name                 *string                     `json:"name"`
	Description          *string                     `json:"description"`
	Instructions         *string                     `json:"instructions"`
	ConversationStarters *[]AgentConversationStarter `json:"conversation_starters"`
	AvatarURL            *string                     `json:"avatar_url"`
	RuntimeID            *string                     `json:"runtime_id"`
	RuntimeConfig        any                         `json:"runtime_config"`
	// custom_env is intentionally NOT updatable through this endpoint.
	// Use `PUT /api/agents/{id}/env` for env changes — that path admits
	// the agent owner or a workspace owner/admin, denies agent actors,
	// and writes a persisted audit log entry. A `PUT /api/agents/{id}`
	// body that carries
	// `custom_env` is rejected with 400 in the handler below so a
	// caller never believes they rotated a secret when the value is
	// actually unchanged, and so a client that round-tripped a
	// previously-returned masked map cannot silently overwrite real
	// secret values with literal `****`. See MUL-2600.
	CustomArgs *[]string        `json:"custom_args"`
	McpConfig  *json.RawMessage `json:"mcp_config"`
	Visibility *string          `json:"visibility"`
	// PermissionMode + InvocationTargets are the invocation-permission inputs
	// (MUL-3963). Owner-only writes (like composio_toolkit_allowlist): a
	// non-owner admin passing them is silently ignored, because the invoke
	// gate is owner/allow-list based and an admin-authored allow-list would
	// confuse the owner about who can run their agent. permission_mode is
	// authoritative when present; otherwise legacy visibility is mapped.
	PermissionMode     *string                     `json:"permission_mode"`
	InvocationTargets  *[]AgentInvocationTargetDTO `json:"invocation_targets"`
	Status             *string                     `json:"status"`
	MaxConcurrentTasks *int32                      `json:"max_concurrent_tasks"`
	Model              *string                     `json:"model"`
	// ThinkingLevel is treated as a tri-state per-MUL-2339:
	//   - field omitted → no change (leave existing value alone)
	//   - field present with "" → explicit clear (use runtime default)
	//   - field present with non-empty value → set (validated server-side)
	// Distinguishing those modes is why this is a pointer; the raw-fields
	// map captured at decode time tells us whether the key was sent.
	ThinkingLevel *string `json:"thinking_level"`
	// ServiceTier follows the same tri-state contract as ThinkingLevel:
	// omitted preserves, empty clears, and non-empty sets a Codex catalog ID.
	ServiceTier *string `json:"service_tier"`
	// RoutingTier follows the same tri-state contract: omitted preserves,
	// empty takes the seat off the routing ladder, and a tier key or its
	// Chinese label sets the rung (DENE-633).
	RoutingTier *string `json:"routing_tier"`
	// ComposioToolkitAllowlist is a tri-state, same pattern as
	// thinking_level, mcp_config:
	//   - field omitted → no change (column preserved as-is)
	//   - field present with null → explicit clear (ClearAgent... query)
	//   - field present with [] → store empty TEXT[] (configured, no toolkits)
	//   - field present with non-empty → store deduped lowercase slugs
	// The decode-time raw fields map disambiguates "omitted" from "explicit
	// null" (a *[]string can't, because a nil pointer is the same wire
	// representation as both). MUL-3869.
	ComposioToolkitAllowlist *[]string `json:"composio_toolkit_allowlist"`
	// AutoRetryEnabled is omitted-preserves / present-sets. Explicit false
	// is not NULL, so COALESCE in UpdateAgent can distinguish "not sent"
	// from "turned off".
	AutoRetryEnabled *bool `json:"auto_retry_enabled"`
	// WorkEnabled is omitted-preserves / present-sets, same contract as
	// AutoRetryEnabled (DENE-714).
	WorkEnabled *bool `json:"work_enabled"`
	// DoorbellEnabled (DENE-808): omitted-preserves / present-sets. Only the
	// agent owner may change it.
	DoorbellEnabled *bool `json:"doorbell_enabled"`
	// ParentAgentID re-parents this agent (DENE-301): a non-empty value attaches
	// it to a base role, and an explicitly empty string detaches it. The field
	// is a tri-state like thinking_level — omitted preserves, `""` clears, a
	// value sets — which is why it is a pointer and why the raw-fields map
	// captured at decode time is what decides whether the column is touched.
	// Setting it on an agent that already has children is refused: that would
	// make this row a middle level of a three-level tree.
	ParentAgentID *string `json:"parent_agent_id"`
	// RuntimeInherited switches a specialisation between following its base
	// role's runtime profile and owning one (DENE-505). Omitted preserves the
	// stored value. TRUE re-copies the base role's runtime_id / runtime_mode /
	// runtime_config / model / thinking_level / service_tier immediately, and
	// later base-role edits keep re-copying them; it is refused on a base role
	// and in the same request as a runtime field, because "follow" and "set my
	// own runtime" contradict each other. FALSE is the opt-out and is lossless:
	// the agent keeps the values it was already running with, exactly like a
	// solidified prompt.
	RuntimeInherited *bool `json:"runtime_inherited"`
}

// workspaceAlwaysRedactSecrets reports whether the workspace has opted
// into unconditional redaction of secret-bearing fields (currently
// `mcp_config`) on read responses, regardless of the caller's role.
//
// The legacy JSON key is still `always_redact_env` for backwards-
// compatibility with workspaces that flipped the setting before MUL-2600
// shipped. The setting no longer affects `custom_env` because that field
// is never serialized on agent resources anymore — secrets there are
// fetched exclusively through `GET /api/agents/{id}/env` with audit
// logging — so the flag now only governs `mcp_config` exposure.
func workspaceAlwaysRedactSecrets(settings []byte) bool {
	if len(settings) == 0 {
		return false
	}
	var s struct {
		AlwaysRedactEnv bool `json:"always_redact_env"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		return false
	}
	return s.AlwaysRedactEnv
}

// canViewAgentSecrets checks whether the requesting user is allowed to
// see the agent's secret-bearing fields (currently `mcp_config`). Only
// the agent owner or workspace owner/admin qualify; for everyone else
// the response is redacted. `custom_env` is no longer part of an agent
// resource response (see MUL-2600), so this predicate is shared only by
// the remaining mcp_config redaction path.
func canViewAgentSecrets(agent db.Agent, userID string, memberRole string) bool {
	if roleAllowed(memberRole, "owner", "admin") {
		return true
	}
	return uuidToString(agent.OwnerID) == userID
}

// broadcastAgentResponse strips secret-bearing fields from an
// AgentResponse before it goes onto the WebSocket bus. Mutation
// handlers call this when fanning out create/update/archive/restore
// events: subscribers (which include agent processes that have
// authenticated with their own task tokens) must not learn another
// agent's mcp_config via a WS push that bypassed the read-path
// redaction in ListAgents / GetAgent. The caller still receives the
// canonical form in the HTTP response; only the broadcast copy is
// redacted.
//
// composio_toolkit_allowlist follows the same fan-out rule: every
// workspace member subscribes to agent:created/updated/archived, so
// a non-redacted broadcast would leak the agent owner's per-toolkit
// allowlist to every member regardless of whether they would have
// been allowed to read it via GET. Redact unconditionally on the
// broadcast copy.
func broadcastAgentResponse(resp AgentResponse) AgentResponse {
	out := resp
	redactMcpConfig(&out)
	redactComposioToolkitAllowlist(&out)
	// Belt-and-suspenders: agentToResponse already masks gateway.token on
	// every read, so by the time a response reaches this broadcast helper
	// the field is already "***". Re-mask anyway so a future refactor that
	// bypasses agentToResponse (e.g. constructing AgentResponse from raw
	// db.Agent in a new handler) cannot silently leak the token to every
	// WebSocket subscriber on the workspace, agent processes included.
	maskGatewayToken(out.RuntimeConfig)
	return out
}

// redactMcpConfig removes the mcp_config value from the response when the caller is not
// authorised to view it. The field is set to null; McpConfigRedacted is set to true so
// callers know a config exists without seeing its contents (which may contain secrets).
func redactMcpConfig(resp *AgentResponse) {
	if resp.McpConfig != nil {
		resp.McpConfig = nil
		resp.McpConfigRedacted = true
	}
}

// redactComposioToolkitAllowlist removes the composio_toolkit_allowlist
// value from the response when the caller is not the agent owner. The slug
// list itself is not secret, but the "what {agent owner} has opted into"
// view leaks the owner's integration footprint across the workspace, which
// is the same privacy concern that gates mcp_config visibility behind
// owner-only canViewAgentSecrets. We surface a coarse `_redacted` flag so
// the front-end can render "Configured" without the contents (parity with
// mcp_config_redacted). The clearing matters: the JSON `omitempty` only
// drops nil slices, so reset to nil rather than `[]string{}`.
func redactComposioToolkitAllowlist(resp *AgentResponse) {
	if resp.ComposioToolkitAllowlist != nil {
		resp.ComposioToolkitAllowlist = nil
		resp.ComposioToolkitAllowlistRedacted = true
	}
}

func suppressComposioToolkitAllowlist(resp *AgentResponse) {
	resp.ComposioToolkitAllowlist = nil
	resp.ComposioToolkitAllowlistRedacted = false
}

// normaliseComposioToolkitAllowlist canonicalises an incoming allowlist
// payload before persisting. Each slug is trimmed + lowercased so the
// dispatch path (which compares against user_composio_connection.toolkit_slug,
// stored lowercased by the Composio service) does a flat string match
// without needing a CITEXT or per-query LOWER(). Empty / whitespace-only
// strings are dropped and duplicates collapsed so a sloppy UI payload
// can't waste DB row-length or surface twice in the response.
//
// Contract:
//   - nil in → nil out: "field absent / explicit null" preserved. Combined
//     with sqlc.narg('composio_toolkit_allowlist')::text[] in UpdateAgent,
//     this is what makes "omit field" mean "leave column alone".
//   - empty slice in → empty slice out: "owner cleared all toolkits".
//     Distinct from nil only at the column-NULL level; the dispatch path
//     treats both identically as "no overlay".
//   - non-empty in → trimmed, lowercased, deduped, stable order.
func normaliseComposioToolkitAllowlist(in []string) []string {
	if in == nil {
		return nil
	}
	if len(in) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// redactAgentResponseForActor strips secret-bearing fields from an agent
// resource HTTP response when the request actor is an agent. Read
// handlers already gate on actorType — mutation handlers
// (create/update/archive/restore) must apply the same rule, otherwise
// an agent with a host owner/admin token can do an unrelated mutation
// (e.g. flip max_concurrent_tasks) on a target agent and harvest the
// target's mcp_config from the mutation response. MUL-2600.
//
// composio_toolkit_allowlist is redacted under the same logic: an agent
// runs with its host owner's PAT, so a mutation against a sibling agent
// could otherwise return the sibling owner's allowlist in the response.
func redactAgentResponseForActor(resp *AgentResponse, actorType string) {
	if actorType == "agent" {
		redactMcpConfig(resp)
		redactComposioToolkitAllowlist(resp)
	}
}

// canManageAgent checks whether the current user can update or archive an agent.
// Only the agent owner or workspace owner/admin can manage any agent,
// regardless of whether it is public or private.
func (h *Handler) canManageAgent(w http.ResponseWriter, r *http.Request, agent db.Agent) bool {
	wsID := uuidToString(agent.WorkspaceID)
	member, ok := h.requireWorkspaceRole(w, r, wsID, "agent not found", "owner", "admin", "member")
	if !ok {
		return false
	}
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	isAgentOwner := uuidToString(agent.OwnerID) == requestUserID(r)
	if !isAdmin && !isAgentOwner {
		writeError(w, http.StatusForbidden, "only the agent owner can manage this agent")
		return false
	}
	return true
}

func (h *Handler) UpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, existing) {
		return
	}

	var req UpdateAgentRequest
	rawFields, err := decodeJSONBodyWithRawFields(r.Body, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Hard-reject any attempt to write custom_env through the generic
	// update endpoint. Silently dropping the field (which is what an
	// `omitempty` field would do) was the pre-PR behaviour and led to
	// users believing they had rotated a secret when the value was
	// actually unchanged. env values move only through `PUT
	// /api/agents/{id}/env` — that endpoint admits the agent owner or a
	// workspace owner/admin, denies agent actors, and writes a queryable
	// audit row.
	if _, ok := rawFields["custom_env"]; ok {
		writeError(w, http.StatusBadRequest, "custom_env is no longer accepted on this endpoint; use PUT /api/agents/{id}/env (or `multica agent env set`)")
		return
	}

	// Two-level specialisation (DENE-301). Both directions of the rule are
	// checked here, because an update is the only way to reach either mistake:
	// pointing this agent at a parent that is itself a child (three levels
	// down), or giving this agent a parent while it already has children of its
	// own (three levels up). An explicitly empty parent_agent_id detaches the
	// specialisation and needs neither check.
	//
	// sentParent is keyed on the RAW field map, not on the pointer: a JSON null
	// and an omitted key both decode to a nil pointer, and only the raw map can
	// tell them apart. null is treated as a clear, matching the tri-state
	// contract documented on UpdateAgentRequest. The raw value itself is not
	// re-parsed here — the decode already rejected anything that is not a
	// string or null, so the pointer is the only reading of it that can differ.
	sentParent := false
	var parentAgentID pgtype.UUID
	if _, ok := rawFields["parent_agent_id"]; ok {
		requested := ""
		if req.ParentAgentID != nil {
			requested = strings.TrimSpace(*req.ParentAgentID)
		}
		if requested != "" {
			parentActorType, parentActorID := h.resolveActor(r, requestUserID(r), uuidToString(existing.WorkspaceID))
			parent, reject := h.validateAgentParent(r.Context(), uuidToString(existing.WorkspaceID), requested, uuidToString(existing.ID), parentActorType, parentActorID)
			if reject != "" {
				writeError(w, http.StatusBadRequest, reject)
				return
			}
			childCount, err := h.Queries.CountAgentChildren(r.Context(), existing.ID)
			if err != nil {
				slog.Warn("update agent: count children failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
				writeError(w, http.StatusInternalServerError, "failed to update agent")
				return
			}
			if childCount > 0 {
				writeError(w, http.StatusBadRequest, fmt.Sprintf(
					"this agent already has %d specialisation(s); a base role cannot itself become a specialisation", childCount))
				return
			}
			parentAgentID = parent.ID
		}
		sentParent = true
	}

	params := db.UpdateAgentParams{
		ID: existing.ID,
	}

	// Runtime inheritance (DENE-505). Everything is decided before the first
	// write: what the flag becomes, whether the request contradicts itself, and
	// which row the copy is taken from afterwards.
	//
	// A request that touches a runtime field cannot also mean "follow the base
	// role" — the two say opposite things about the same columns — so it is a
	// 400 rather than a silent pick. That is deliberately strict: rewriting a
	// following child's runtime by accident is the drift this feature exists to
	// stop, and the message names the one field that resolves it.
	runtimeFieldsTouched := false
	for _, field := range []string{"runtime_id", "runtime_config", "model", "thinking_level", "service_tier"} {
		if _, ok := rawFields[field]; ok {
			runtimeFieldsTouched = true
			break
		}
	}
	// The parent this update leaves behind: the requested one when the request
	// touched parent_agent_id, the stored one otherwise.
	parentAfter := existing.ParentAgentID
	if sentParent {
		parentAfter = parentAgentID
	}
	inheritRuntime := existing.RuntimeInherited
	if req.RuntimeInherited != nil {
		if *req.RuntimeInherited && !parentAfter.Valid {
			writeError(w, http.StatusBadRequest, "runtime_inherited needs a parent_agent_id: only a specialisation can follow a base role's runtime")
			return
		}
		if *req.RuntimeInherited && runtimeFieldsTouched {
			writeError(w, http.StatusBadRequest, "runtime_inherited=true follows the base role's runtime; drop the runtime fields from this request, or pass runtime_inherited=false to configure this agent's own runtime")
			return
		}
		inheritRuntime = *req.RuntimeInherited
		params.RuntimeInherited = pgtype.Bool{Bool: *req.RuntimeInherited, Valid: true}
	}
	if runtimeFieldsTouched && inheritRuntime {
		writeError(w, http.StatusBadRequest, "this agent follows its base role's runtime; pass runtime_inherited=false to give it its own runtime configuration")
		return
	}
	// The execution config follows too, within one owner (DENE-854). Refused
	// the same way as a runtime field: the next base-role edit would overwrite
	// it, so accepting the write would only lose it later.
	executionConfigTouched := false
	for _, field := range executionConfigFields {
		if _, ok := rawFields[field]; ok {
			executionConfigTouched = true
			break
		}
	}
	if executionConfigTouched && inheritRuntime && parentAfter.Valid {
		parentRow, err := h.Queries.GetAgent(r.Context(), parentAfter)
		if err != nil {
			slog.Warn("update agent: load base role failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update agent")
			return
		}
		if followsExecutionConfig(existing.OwnerID, parentRow.OwnerID) {
			writeError(w, http.StatusBadRequest, "this agent follows its base role's custom_args and mcp_config; edit the base role, or pass runtime_inherited=false to give it its own configuration")
			return
		}
	}
	if !parentAfter.Valid && inheritRuntime {
		// Detaching. SetAgentParentAgent clears the flag with the parent, and the
		// materialised profile stays behind, so the agent keeps exactly what it
		// was running with.
		inheritRuntime = false
	}

	if req.Name != nil {
		params.Name = pgtype.Text{String: *req.Name, Valid: true}
	}
	if req.Description != nil {
		if utf8.RuneCountInString(*req.Description) > maxAgentDescriptionLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("description must be %d characters or fewer", maxAgentDescriptionLength))
			return
		}
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Instructions != nil {
		params.Instructions = pgtype.Text{String: *req.Instructions, Valid: true}
	}
	if req.ConversationStarters != nil {
		conversationStarters, err := normaliseAgentConversationStarters(*req.ConversationStarters)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, _ := json.Marshal(conversationStarters)
		params.ConversationStarters = encoded
	}
	if req.AutoRetryEnabled != nil {
		params.AutoRetryEnabled = pgtype.Bool{Bool: *req.AutoRetryEnabled, Valid: true}
	}
	if req.WorkEnabled != nil {
		params.WorkEnabled = pgtype.Bool{Bool: *req.WorkEnabled, Valid: true}
	}
	if req.DoorbellEnabled != nil {
		if uuidToString(existing.OwnerID) != requestUserID(r) && existing.DoorbellEnabled != *req.DoorbellEnabled {
			writeError(w, http.StatusForbidden, "only the agent owner can change the doorbell setting")
			return
		}
		params.DoorbellEnabled = pgtype.Bool{Bool: *req.DoorbellEnabled, Valid: true}
	}
	if req.AvatarURL != nil {
		avatarURL, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, existing.AvatarUrl.String)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: avatarURL, Valid: true}
	}
	if req.RuntimeConfig != nil {
		// Restore the persisted gateway token when the request submitted the
		// public mask sentinel. Without this, a UI that GETs the agent and
		// PATCHes the same payload back round-trips "***" into the database
		// and silently destroys the real secret (issue #3260).
		preserveMaskedGatewayToken(req.RuntimeConfig, existing.RuntimeConfig)
		rc, _ := json.Marshal(req.RuntimeConfig)
		params.RuntimeConfig = rc
	}
	if req.CustomArgs != nil {
		ca, _ := json.Marshal(*req.CustomArgs)
		params.CustomArgs = ca
	}
	rawMcpConfig, hasMcpConfig := rawFields["mcp_config"]
	shouldClearMcpConfig := hasMcpConfig && bytes.Equal(bytes.TrimSpace(rawMcpConfig), []byte("null"))
	if hasMcpConfig && !shouldClearMcpConfig {
		params.McpConfig = append([]byte(nil), rawMcpConfig...)
	}

	// Resolve the runtime that will be in force after this update so the
	// thinking_level validation hits the right provider enum. When the
	// request doesn't move the agent, we still need to load the *current*
	// runtime to validate a thinking_level change. Resolve once and reuse.
	targetRuntimeID := existing.RuntimeID
	targetProvider := ""
	if req.RuntimeID != nil {
		runtimeUUID, ok := parseUUIDOrBadRequest(w, *req.RuntimeID, "runtime_id")
		if !ok {
			return
		}
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeUUID,
			WorkspaceID: existing.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid runtime_id")
			return
		}
		// Same gate as CreateAgent — prevents UpdateAgent from being used to
		// re-bind an agent onto someone else's private runtime, which would
		// otherwise be a quiet end-run around the CreateAgent check.
		member, ok := h.workspaceMember(w, r, uuidToString(existing.WorkspaceID))
		if !ok {
			return
		}
		if !canUseRuntimeForAgent(member, runtime) {
			writeError(w, http.StatusForbidden, "this runtime is private; only its owner can move agents onto it")
			return
		}
		params.RuntimeID = runtime.ID
		params.RuntimeMode = pgtype.Text{String: runtime.RuntimeMode, Valid: true}
		targetRuntimeID = runtime.ID
		targetProvider = runtime.Provider
	}
	// Invocation permission (MUL-3963). OWNER-ONLY write: access is the one
	// agent property a workspace admin may NOT change (only the owner decides
	// who can run their agent — the overlay uses the owner's own Composio
	// connection, so admin-authored access would be confusing and unsafe).
	//
	// Non-owner behaviour: a *real* change is rejected with 403 so the contract
	// is explicit and matches the owner-only UI (the picker is read-only for
	// non-owners). A no-op resubmit — an admin editing OTHER fields via a
	// PATCH-as-PUT client that echoes the unchanged permission back — is
	// tolerated (dropped) so it doesn't break legitimate admin edits.
	_, hasPermissionMode := rawFields["permission_mode"]
	_, hasTargets := rawFields["invocation_targets"]
	permissionTouched := hasPermissionMode || hasTargets || req.Visibility != nil
	replacePermissionTargets := false
	var resolvedPerm resolvedPermission
	if permissionTouched {
		isAgentOwner := uuidToString(existing.OwnerID) == requestUserID(r)
		if !isAgentOwner {
			changed, permErr := h.permissionInputChangesAgent(r.Context(), existing, req, hasPermissionMode, hasTargets)
			if permErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to evaluate invocation permission change")
				return
			}
			if changed {
				writeError(w, http.StatusForbidden, "only the agent owner can change access (permission_mode / invocation_targets)")
				return
			}
			slog.Debug("update agent: non-owner permission fields matched current state; ignored",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else {
			var targetsDTO []AgentInvocationTargetDTO
			if req.InvocationTargets != nil {
				targetsDTO = *req.InvocationTargets
			}
			perm, _, permErr := parsePermissionInput(existing.WorkspaceID, req.PermissionMode, targetsDTO, hasPermissionMode, hasTargets, req.Visibility)
			if permErr != nil {
				writeError(w, http.StatusBadRequest, permErr.Error())
				return
			}
			resolvedPerm = perm
			replacePermissionTargets = true
			params.PermissionMode = pgtype.Text{String: perm.mode, Valid: true}
			params.Visibility = pgtype.Text{String: perm.legacyVisibility(), Valid: true}
		}
	}
	if req.Status != nil {
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}
	if req.MaxConcurrentTasks != nil {
		if err := validateAgentMaxConcurrentTasks(*req.MaxConcurrentTasks); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.MaxConcurrentTasks = pgtype.Int4{Int32: *req.MaxConcurrentTasks, Valid: true}
	}
	if req.Model != nil {
		params.Model = pgtype.Text{String: *req.Model, Valid: true}
	} else if req.RuntimeID != nil && existing.Model.Valid && agent.ModelKnownIncompatibleWithProvider(targetProvider, existing.Model.String) {
		// Model is runtime-native. When moving an agent across known provider
		// families and the caller did not choose a replacement model, clear the
		// old value so the new runtime falls back to its own default instead of
		// receiving an obvious foreign model ID (e.g. Claude Code -> Codex).
		// Unknown/custom model strings are preserved by the helper.
		params.Model = pgtype.Text{String: "", Valid: true}
	}

	// thinking_level handling (MUL-2339). Tri-state semantics:
	//   - field omitted  → leave column alone (COALESCE narg), but if a
	//     runtime change in this same request would make the *existing*
	//     value invalid for the new provider's fixed enum or token syntax,
	//     reject 400. Exact dynamic-catalog compatibility is daemon-owned.
	//   - field set to "" → explicit clear (run ClearAgentThinkingLevel post-update)
	//   - field set to value → validate against the target runtime's fixed enum
	//     or dynamic-token syntax; reject literal-invalid with 400. Per-model
	//     combination checks run in the daemon at execution time, not here.
	shouldClearThinkingLevel := false
	if req.ThinkingLevel != nil {
		value := *req.ThinkingLevel
		if value == "" {
			shouldClearThinkingLevel = true
		} else {
			// Need the target runtime's provider to validate. Re-fetch only when
			// we haven't already loaded it above (i.e. the request didn't change
			// runtime_id), to keep the no-change path one DB roundtrip.
			provider := targetProvider
			if provider == "" {
				var ok bool
				provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
				if !ok {
					writeError(w, http.StatusInternalServerError, "failed to resolve runtime for thinking_level validation")
					return
				}
			}
			if !agent.IsKnownThinkingValue(provider, value) {
				writeError(w, http.StatusBadRequest, thinkingLevelRejection(provider, value))
				return
			}
			switch h.acpThinkingDecision(r.Context(), provider, targetRuntimeID) {
			case acpEffortAbsent:
				writeError(w, http.StatusBadRequest, thinkingCapabilityRejection(provider))
				return
			case acpEffortUnknown:
				writeError(w, http.StatusBadRequest, thinkingCapabilityUnknownRejection(provider))
				return
			}
			params.ThinkingLevel = pgtype.Text{String: value, Valid: true}
		}
	} else if req.RuntimeID != nil && existing.ThinkingLevel.Valid && existing.ThinkingLevel.String != "" {
		// Runtime is changing but the caller didn't touch thinking_level.
		// If the existing value is not in the new provider's enum at all,
		// preserving it would smuggle a literal-invalid token to the daemon.
		// Hold the same line as the explicit-set path: always 400 on
		// literal-invalid, never silently coerce. The caller can either
		// pass `thinking_level: ""` to clear or pick a value valid for the
		// new runtime.
		provider := targetProvider
		if provider == "" {
			var ok bool
			provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to resolve runtime for thinking_level validation")
				return
			}
		}
		if !agent.IsKnownThinkingValue(provider, existing.ThinkingLevel.String) {
			writeError(w, http.StatusBadRequest, existingThinkingLevelRejection(provider, existing.ThinkingLevel.String))
			return
		}
		switch h.acpThinkingDecision(r.Context(), provider, targetRuntimeID) {
		case acpEffortAbsent:
			writeError(w, http.StatusBadRequest, existingThinkingCapabilityRejection(provider, existing.ThinkingLevel.String))
			return
		case acpEffortUnknown:
			writeError(w, http.StatusBadRequest, existingThinkingCapabilityUnknownRejection(provider, existing.ThinkingLevel.String))
			return
		}
	}

	// Same combination check as CreateAgent, but against the state the request
	// actually lands on: a cleared model with a carried-over effort, or a new
	// effort on an agent that never had a model, are both the invalid pair. The
	// caller can always recover by pinning a model or clearing the level, so
	// this cannot lock an agent out of editing (MUL-7412).
	if effectiveThinking := effectiveThinkingLevel(params, existing, shouldClearThinkingLevel); effectiveThinking != "" &&
		strings.TrimSpace(effectiveModelValue(params, existing)) == "" {
		provider := targetProvider
		if provider == "" {
			var ok bool
			provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to resolve runtime for thinking_level validation")
				return
			}
		}
		if agent.ThinkingLevelRejectedWithoutModel(provider) {
			writeError(w, http.StatusBadRequest, thinkingNeedsExplicitModelRejection(provider))
			return
		}
	}

	// routing_tier is workspace configuration rather than a runtime override:
	// it says how strong this seat is for automatic dispatch. A key or its
	// label is accepted, only the key is stored, and anything else is refused
	// rather than written — an unknown rung would silently take the seat off
	// the ladder with nothing on the agent page to show it.
	shouldClearRoutingTier := false
	if req.RoutingTier != nil {
		value := strings.TrimSpace(*req.RoutingTier)
		if value == "" {
			shouldClearRoutingTier = true
		} else {
			key, ok := routing.DefaultLadder.NormalizeTier(value)
			if !ok {
				writeError(w, http.StatusBadRequest, fmt.Sprintf(
					"routing_tier %q is not a known tier; expected one of %s",
					value, strings.Join(routing.DefaultLadder.TierKeys(), ", ")))
				return
			}
			params.RoutingTier = pgtype.Text{String: key, Valid: true}
		}
	}

	shouldClearServiceTier := false
	if req.ServiceTier != nil {
		value := *req.ServiceTier
		if value == "" {
			shouldClearServiceTier = true
		} else {
			provider := targetProvider
			if provider == "" {
				var ok bool
				provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
				if !ok {
					writeError(w, http.StatusInternalServerError, "failed to resolve runtime for service_tier validation")
					return
				}
			}
			if !agent.IsKnownServiceTier(provider, value) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("service_tier %q is not a recognised value for runtime %q", value, provider))
				return
			}
			params.ServiceTier = pgtype.Text{String: value, Valid: true}
		}
	} else if req.RuntimeID != nil && existing.ServiceTier.Valid && existing.ServiceTier.String != "" {
		provider := targetProvider
		if provider == "" {
			var ok bool
			provider, ok = h.resolveAgentProvider(r, existing.WorkspaceID, targetRuntimeID)
			if !ok {
				writeError(w, http.StatusInternalServerError, "failed to resolve runtime for service_tier validation")
				return
			}
		}
		if !agent.IsKnownServiceTier(provider, existing.ServiceTier.String) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"existing service_tier %q is not valid for runtime %q; pass service_tier=\"\" to clear or set a value valid for the new runtime",
				existing.ServiceTier.String, provider,
			))
			return
		}
	}

	// composio_toolkit_allowlist handling (MUL-3869). Tri-state semantics
	// mirror thinking_level (see above): omitted → no change, null →
	// ClearAgentComposioToolkitAllowlist, slice → wholesale replace.
	//
	// Owner-only WRITE. The caller is already past canManageAgent, which lets
	// workspace owner/admins through alongside the agent owner — but the
	// Composio overlay uses the agent OWNER's connection (MUL-3963), so an
	// admin editing someone else's allowlist would silently reshape what the
	// OWNER exposes through their own connected apps, confusing the owner
	// about what their agent surfaces. Keep it owner-only.
	// Drop the field with a debug log instead of erroring so an over-eager
	// UI that sends the whole agent payload back on every save (PATCH-as-PUT)
	// keeps working — same "silent ignore" stance the issue calls out, and
	// the same one mcp_config takes for the broader admin pattern.
	shouldClearComposioAllowlist := false
	if _, hasAllowlist := rawFields["composio_toolkit_allowlist"]; hasAllowlist {
		isAgentOwner := uuidToString(existing.OwnerID) == requestUserID(r)
		if !h.composioMCPAppsEnabled(r.Context()) {
			slog.Debug("update agent: composio_toolkit_allowlist write dropped because feature flag is disabled",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else if !isAgentOwner {
			slog.Debug("update agent: composio_toolkit_allowlist write by non-owner silently dropped",
				append(logger.RequestAttrs(r), "agent_id", id)...)
		} else if req.ComposioToolkitAllowlist == nil {
			// JSON null → explicit clear via the dedicated query.
			shouldClearComposioAllowlist = true
		} else {
			// Normalise (trim/lowercase/dedupe). Empty slice is preserved as
			// an empty TEXT[] so the persisted value distinguishes "owner
			// cleared every toolkit" from "owner has never opened the
			// integration" (the dispatch path treats both as "no overlay"
			// either way, but the column tells UX whether to show a primed
			// vs empty picker).
			params.ComposioToolkitAllowlist = normaliseComposioToolkitAllowlist(*req.ComposioToolkitAllowlist)
		}
	}

	updated, err := h.Queries.UpdateAgent(r.Context(), params)
	if err != nil {
		// Unique constraint on (workspace_id, name) — mirror CreateAgent and
		// return a clear conflict instead of a 500 that leaks the raw
		// constraint name. The name can still be held by an *archived* agent
		// (the constraint does not exclude archived rows), so this is the only
		// signal the caller gets that a rename collided rather than the server
		// faulting.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_workspace_name_unique" {
			name := ""
			if req.Name != nil {
				name = *req.Name
			}
			writeError(w, http.StatusConflict, fmt.Sprintf("an agent named %q already exists in this workspace", name))
			return
		}
		slog.Warn("update agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to update agent: "+err.Error())
		return
	}

	// A base role owns the availability of its direct specialisations: turning
	// it off or on sets every specialisation to the same value.
	var toggledSpecialisations []db.Agent
	if req.WorkEnabled != nil && !updated.ParentAgentID.Valid {
		toggledSpecialisations, err = h.Queries.SetAgentSpecialisationsWorkEnabled(r.Context(), db.SetAgentSpecialisationsWorkEnabledParams{
			WorkEnabled:   *req.WorkEnabled,
			ParentAgentID: updated.ID,
		})
		if err != nil {
			slog.Warn("sync agent specialisations work_enabled failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update agent specialisations")
			return
		}
	}
	// A balance breaker has no timer: a person turning the seat back on after
	// topping up is its recovery. Tickets it handed to other seats stay there.
	if req.WorkEnabled != nil && *req.WorkEnabled && !existing.WorkEnabled {
		for _, seatID := range append([]pgtype.UUID{updated.ID}, agentIDs(toggledSpecialisations)...) {
			if _, err := h.Queries.CloseManualQuotaBreakers(r.Context(), seatID); err != nil {
				slog.Warn("close balance breaker failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(seatID))...)
			}
		}
	}
	if req.WorkEnabled != nil && *req.WorkEnabled && !existing.WorkEnabled && h.TaskService != nil {
		if err := h.TaskService.ReclaimDesignatedReviews(r.Context(), updated.ID); err != nil {
			slog.Warn("reclaim designated reviewer failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		}
		for _, spec := range toggledSpecialisations {
			if err := h.TaskService.ReclaimDesignatedReviews(r.Context(), spec.ID); err != nil {
				slog.Warn("reclaim designated reviewer failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(spec.ID))...)
			}
		}
	}

	// Nullable runtime overrides: null/empty in the request means explicitly
	// clear the field. COALESCE in UpdateAgent cannot set a column to NULL, so
	// mcp_config, thinking_level, and service_tier use dedicated clear queries.
	if shouldClearMcpConfig {
		updated, err = h.Queries.ClearAgentMcpConfig(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent mcp_config failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear mcp_config: "+err.Error())
			return
		}
	}
	if shouldClearThinkingLevel {
		updated, err = h.Queries.ClearAgentThinkingLevel(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent thinking_level failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear thinking_level: "+err.Error())
			return
		}
	}
	if shouldClearServiceTier {
		updated, err = h.Queries.ClearAgentServiceTier(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent service_tier failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear service_tier: "+err.Error())
			return
		}
	}
	if shouldClearRoutingTier {
		updated, err = h.Queries.ClearAgentRoutingTier(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent routing_tier failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear routing_tier: "+err.Error())
			return
		}
	}
	if shouldClearComposioAllowlist {
		updated, err = h.Queries.ClearAgentComposioToolkitAllowlist(r.Context(), updated.ID)
		if err != nil {
			slog.Warn("clear agent composio_toolkit_allowlist failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to clear composio_toolkit_allowlist: "+err.Error())
			return
		}
	}

	// Parent binding has its own statement so that "clear" is expressible at
	// all (see SetAgentParentAgent). Applied after the metadata update so the
	// response reflects both in one payload.
	if sentParent {
		updated, err = h.Queries.SetAgentParentAgent(r.Context(), db.SetAgentParentAgentParams{
			ID:               updated.ID,
			ParentAgentID:    parentAgentID,
			RuntimeInherited: params.RuntimeInherited,
		})
		if err != nil {
			slog.Warn("update agent: set parent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update agent parent")
			return
		}
	}

	// Materialise the copy the follow flag promises (DENE-505): a specialisation
	// that follows re-reads its base role, a base role re-writes the
	// specialisations that follow it. Never both — the two branches are
	// exclusive by construction, because a row with a parent is not a base role.
	//
	// Run after the parent/flag writes so the copy is taken from the row this
	// request just committed. Only a touched runtime or execution-config field can
	// have moved a base role's profile, so an unrelated edit (a rename, a
	// prompt) does not walk the children.
	var syncedChildren []db.Agent
	switch {
	case updated.RuntimeInherited && updated.ParentAgentID.Valid:
		syncedChildren = h.syncInheritedAgentRuntimeProfiles(r.Context(), updated.ParentAgentID, r)
		// The agent being edited can be one of the rows rewritten above when it
		// just flipped to following (or was re-parented onto a base role); the
		// response must carry the copy, not the pre-copy row.
		for _, child := range syncedChildren {
			if child.ID == updated.ID {
				updated = child
			}
		}
	case !updated.ParentAgentID.Valid && (runtimeFieldsTouched || executionConfigTouched):
		syncedChildren = h.syncInheritedAgentRuntimeProfiles(r.Context(), updated.ID, r)
	}

	// Invocation targets (MUL-3963): replace wholesale when the owner touched
	// permission. Done after the row update so a permission_mode flip and its
	// targets land together.
	if replacePermissionTargets {
		if err := h.replaceInvocationTargets(r.Context(), updated.ID, parseUUID(requestUserID(r)), resolvedPerm.targets); err != nil {
			slog.Warn("update agent: persist invocation targets failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to update invocation targets: "+err.Error())
			return
		}
	}

	// Fan the cascaded rows out before the actor's own response: every one of
	// them is a committed change other clients have to see, and a client that
	// only receives the base role's event would keep painting the old runtime on
	// each child row until its next full load.
	for _, child := range syncedChildren {
		if child.ID == updated.ID {
			continue
		}
		h.publishAgentUpdate(r, child)
	}
	for _, child := range toggledSpecialisations {
		h.publishAgentUpdate(r, child)
	}

	resp := h.agentToResponse(updated)
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("update agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent invocation targets")
		return
	}
	// agentToResponse always initialises Skills as []; junction-table rows
	// are untouched by the SQL update, so we reload them here to keep the
	// response (and the broadcast that mirrors it) in sync with reality.
	// Without this, callers see "skills": [] after every metadata-only
	// update and assume their bindings were cleared — see #3459.
	if err := h.attachAgentSkills(r.Context(), &resp, updated.ID); err != nil {
		slog.Warn("load agent skills after update failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	// Last wins over the response built above so a response can never show a
	// stale parent_agent_name after a re-parent, and so child_count reflects
	// children created since this agent was loaded.
	updatedResps := []AgentResponse{resp}
	if err := h.enrichAgentResponsesWithRelations(r.Context(), updatedResps); err != nil {
		slog.Warn("update agent: load parent relation for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}
	resp = updatedResps[0]
	slog.Info("agent updated", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", uuidToString(updated.WorkspaceID))...)
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(updated.WorkspaceID))
	h.publish(protocol.EventAgentStatus, uuidToString(updated.WorkspaceID), actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, actorType)
	// Workspace admins / non-owner members pass canManageAgent for legitimate
	// admin actions (e.g. bulk reassigning agents off a leaving member's
	// runtime), but they must not learn the agent owner's composio allowlist
	// from the mutation response. See ListAgents/GetAgent for the same gate.
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if uuidToString(updated.OwnerID) != userID {
		redactComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// attachAgentSkills populates resp.Skills from the agent_skill junction
// table for the given agent. agentToResponse zeros the field; mutation
// handlers that don't refresh it would otherwise serve a misleading
// empty array on every successful response (#3459).
func (h *Handler) attachAgentSkills(ctx context.Context, resp *AgentResponse, agentID pgtype.UUID) error {
	skills, err := h.Queries.ListAgentSkillSummaries(ctx, agentID)
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		return nil
	}
	out := make([]AgentSkillSummary, len(skills))
	for i, s := range skills {
		out[i] = AgentSkillSummary{
			ID:          uuidToString(s.ID),
			Name:        s.Name,
			Description: s.Description,
			Enabled:     s.Enabled,
		}
	}
	resp.Skills = out
	return nil
}

// resolveAgentProvider returns the provider name for the runtime that
// will own this agent after the in-flight update applies. Used by the
// thinking_level validator so a runtime/model swap and a level swap
// validated in the same request both consult the same provider.
func (h *Handler) resolveAgentProvider(r *http.Request, workspaceID pgtype.UUID, runtimeID pgtype.UUID) (string, bool) {
	rt, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          runtimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return "", false
	}
	return rt.Provider, true
}

// thinkingNeedsExplicitModelRejection is the copy for a level that is valid for
// the runtime but has no model to run it on.
func thinkingNeedsExplicitModelRejection(provider string) string {
	return fmt.Sprintf(
		"runtime %q resolves its own default model, so a reasoning effort needs an explicit model; set model or pass thinking_level=\"\" to clear",
		provider,
	)
}

// effectiveModelValue is the model the update lands on: the requested value
// when this request sets one (including an explicit clear), otherwise what the
// agent already holds.
func effectiveModelValue(params db.UpdateAgentParams, existing db.Agent) string {
	if params.Model.Valid {
		return params.Model.String
	}
	return existing.Model.String
}

// effectiveThinkingLevel is the effort the update lands on. An explicit clear
// wins over everything; otherwise a value set by this request wins over the
// stored one, which is carried when the field was omitted.
func effectiveThinkingLevel(params db.UpdateAgentParams, existing db.Agent, cleared bool) string {
	if cleared {
		return ""
	}
	if params.ThinkingLevel.Valid {
		return params.ThinkingLevel.String
	}
	if existing.ThinkingLevel.Valid {
		return existing.ThinkingLevel.String
	}
	return ""
}

// thinkingLevelRejection explains why the target runtime will not take this
// thinking_level. Two different failures used to share one sentence: a token
// the runtime's catalog doesn't list, and a runtime with no reasoning control
// at all. The second one made "high" look like a spelling mistake and sent
// users hunting for a value that does not exist for that runtime (MUL-5770),
// so it now names the capability gap instead.
func thinkingLevelRejection(provider, value string) string {
	if !agent.ThinkingControlSupported(provider) {
		return thinkingCapabilityRejection(provider)
	}
	return fmt.Sprintf("thinking_level %q is not a recognised value for runtime %q", value, provider)
}

// existingThinkingCapabilityRejection is the carry-over path's capability
// sentence — same answer as thinkingCapabilityRejection, but it names the value
// already on the agent and the escape hatch that clears it.
func existingThinkingCapabilityRejection(provider, value string) string {
	return fmt.Sprintf(
		"runtime %q does not support a per-agent reasoning effort; pass thinking_level=\"\" to clear the existing %q",
		provider, value,
	)
}

// thinkingCapabilityUnknownRejection covers the ambiguous case: the provider
// name does not say which binary is installed and no catalog has been reported
// yet, so we can neither confirm nor deny the capability.
//
// It deliberately does NOT reuse the "does not support" sentence. That claim
// would be actively wrong for a jcode runtime — sending its owner off to look
// for a limitation that does not exist — whereas naming the missing evidence
// points at the thing that resolves it.
func thinkingCapabilityUnknownRejection(provider string) string {
	return fmt.Sprintf(
		"cannot confirm whether runtime %q supports a per-agent reasoning effort: it has not reported a model catalog yet. Open the model picker for this runtime to trigger discovery and retry, or leave thinking_level empty to use the runtime default",
		provider,
	)
}

// existingThinkingCapabilityUnknownRejection is the carry-over path's version of
// the same answer: it names the value already on the agent and the escape hatch.
func existingThinkingCapabilityUnknownRejection(provider, value string) string {
	return fmt.Sprintf(
		"cannot confirm whether runtime %q supports a per-agent reasoning effort: it has not reported a model catalog yet. Pass thinking_level=\"\" to clear the existing %q, or retry once the runtime has reported its models",
		provider, value,
	)
}

// thinkingCapabilityRejection is the "this runtime has no reasoning dial"
// sentence. Split out because the same answer can now be reached two ways: from
// the provider name alone, or — for ACP-catalog providers, where the provider
// name is not decisive — from a discovered catalog that advertises no effort.
func thinkingCapabilityRejection(provider string) string {
	return fmt.Sprintf(
		"runtime %q does not support a per-agent reasoning effort; leave thinking_level empty to use the runtime default",
		provider,
	)
}

// acpEffortEvidence is what the discovered model catalog says about a runtime's
// reasoning-effort support.
type acpEffortEvidence int

const (
	// acpEffortUnknown — no catalog has been discovered for this runtime.
	//
	// This is NOT a transient cold-start state. The catalog is written only by
	// ReportModelListResult, i.e. only after a client explicitly asks for a
	// model list, so a caller who never opens a model picker (pure CLI use) can
	// sit here indefinitely. Treating unknown as "supported" is therefore not a
	// brief window — it is a permanent hole for anyone who works this way.
	acpEffortUnknown acpEffortEvidence = iota
	// acpEffortAbsent — a catalog exists and no model in it advertises an effort.
	acpEffortAbsent
	// acpEffortPresent — a catalog exists and at least one model advertises one.
	acpEffortPresent
)

// ambiguousACPEffortProviders are providers whose name does not determine which
// binary is actually installed, so "not discovered yet" cannot be read as
// "supported".
//
// `hermes` is the only one: it covers jcode (advertises an effort and applies
// it) and Hermes Agent (advertises none). reasonix is deliberately absent — that
// provider means one binary, which does support an effort, so an undiscovered
// reasonix runtime is safely allowed rather than blocked before its first
// discovery.
var ambiguousACPEffortProviders = map[string]bool{
	"hermes": true,
}

// acpThinkingDecision answers whether this runtime may carry a thinking level,
// consulting the model catalog its daemon reported.
//
// acpEffortPresent means "allow". Providers outside the ACP-catalog set, and
// unambiguous ACP providers with no catalog yet, are reported as present — their
// capability is already settled by the provider name. Only an ambiguous provider
// turns an undiscovered catalog into a refusal, because for those the name
// genuinely does not answer the question and guessing "yes" is what let a Hermes
// Agent user persist a level the daemon would later drop.
func (h *Handler) acpThinkingDecision(ctx context.Context, provider string, runtimeID pgtype.UUID) acpEffortEvidence {
	if !agent.UsesACPCatalogThinking(provider) {
		return acpEffortPresent
	}
	snapshot := h.cachedModelCatalog(ctx, uuidToString(runtimeID))
	if snapshot == nil || len(snapshot.Models) == 0 {
		if ambiguousACPEffortProviders[provider] {
			return acpEffortUnknown
		}
		return acpEffortPresent
	}
	for _, m := range snapshot.Models {
		if m.Thinking != nil && len(m.Thinking.SupportedLevels) > 0 {
			return acpEffortPresent
		}
	}
	return acpEffortAbsent
}

// existingThinkingLevelRejection is thinkingLevelRejection for the carry-over
// path, where the caller changed runtime without touching a level the new
// runtime cannot take. Both branches point at the same escape hatch, so the
// user does not have to guess that clearing is allowed.
func existingThinkingLevelRejection(provider, value string) string {
	if !agent.ThinkingControlSupported(provider) {
		return existingThinkingCapabilityRejection(provider, value)
	}
	return fmt.Sprintf(
		"existing thinking_level %q is not valid for runtime %q; pass thinking_level=\"\" to clear or set a value valid for the new runtime",
		value, provider,
	)
}

func (h *Handler) ArchiveAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is already archived")
		return
	}

	// A system agent belongs to the product, not to the workspace, and the
	// workspace's whole entry point runs through it. Archiving it would hide
	// it from every list while leaving the row in place — which also strands
	// the bootstrap endpoint, since its lookup skips archived rows but the
	// unique index does not.
	if agent.SystemKey.Valid && agent.SystemKey.String != "" {
		writeError(w, http.StatusBadRequest, "this agent is built into Multica and cannot be archived")
		return
	}

	// A base role with active specialisations cannot be archived: the children
	// would keep pointing at a hidden parent, and the two-level rule has no
	// meaning once the base role is not in the tree (DENE-301). The refusal
	// carries the children so the client can list them and offer the explicit
	// "solidify & unbind" action — the caller must be able to see what is
	// blocking without a second request.
	children, err := h.Queries.ListAgentChildren(r.Context(), agent.ID)
	if err != nil {
		slog.Warn("list agent children failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to archive agent")
		return
	}
	if len(children) > 0 {
		childNames := make([]string, 0, len(children))
		for _, child := range children {
			childNames = append(childNames, child.Name)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf(
				"this base role still has %d specialisation(s): %s. Solidify and unbind them first, or archive them.",
				len(children), strings.Join(childNames, ", ")),
			"code":     "agent_has_children",
			"children": childNames,
		})
		return
	}

	userID := requestUserID(r)
	archived, err := h.Queries.ArchiveAgent(r.Context(), db.ArchiveAgentParams{
		ID:         agent.ID,
		ArchivedBy: parseUUID(userID),
	})
	if err != nil {
		slog.Warn("archive agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to archive agent")
		return
	}

	// Release every acceptance slot pointing at this agent. The reviewer pair
	// is a reference with no foreign key behind it, so nothing else would drop
	// it — and a ticket whose reviewer can no longer be dispatched would sit in
	// in_review forever. Clearing it puts the slot back to "undecided", which
	// is what lets routing pick a live seat the next time the ticket moves.
	// (DENE-633)
	if _, err := h.Queries.ClearIssueReviewer(r.Context(), db.ClearIssueReviewerParams{
		WorkspaceID:  archived.WorkspaceID,
		ReviewerType: "agent",
		ReviewerID:   archived.ID,
	}); err != nil {
		slog.Warn("clear issue reviewer on agent archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}

	// Cancel all pending/active tasks for this agent. The cancel and its
	// delegated-failure settlement commit together — a settlement issued after
	// the cancel committed could never be repaired. Chat tasks publish
	// task:cancelled after commit for chat lifecycle consumers; the aggregate
	// agent:archived event below remains unchanged.
	if _, err := h.TaskService.CancelTasksForArchivedAgent(r.Context(), agent.ID); err != nil {
		slog.Warn("cancel agent tasks on archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}

	wsID := uuidToString(archived.WorkspaceID)
	slog.Info("agent archived", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(archived)
	if err := h.attachAgentSkills(r.Context(), &resp, archived.ID); err != nil {
		slog.Warn("load agent skills after archive failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventAgentArchived, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, actorType)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RestoreAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	if !agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "agent is not archived")
		return
	}

	restored, err := h.Queries.RestoreAgent(r.Context(), agent.ID)
	if err != nil {
		slog.Warn("restore agent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to restore agent")
		return
	}

	// A restored specialisation that follows its base role rejoins the tree and
	// takes the profile the base role has NOW: archived children are skipped by
	// the cascade, so the copy it went to sleep with can be several base-role
	// edits old (DENE-505).
	if restored.RuntimeInherited && restored.ParentAgentID.Valid {
		for _, child := range h.syncInheritedAgentRuntimeProfiles(r.Context(), restored.ParentAgentID, r) {
			if child.ID == restored.ID {
				restored = child
			}
		}
	}

	wsID := uuidToString(restored.WorkspaceID)
	slog.Info("agent restored", append(logger.RequestAttrs(r), "agent_id", id, "workspace_id", wsID)...)
	resp := h.agentToResponse(restored)
	if err := h.attachAgentSkills(r.Context(), &resp, restored.ID); err != nil {
		slog.Warn("load agent skills after restore failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load agent skills")
		return
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventAgentRestored, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, actorType)
	writeJSON(w, http.StatusOK, resp)
}

// SolidifyAgent bakes a specialisation's inherited prompt into the child and
// detaches it from its base role — the documented escape hatch for "this base
// role is going away" (DENE-301).
//
// The result is exactly what the child ran with: composeAgentInstructions is
// the same function the claim path uses, so a solidified child's own
// `instructions` equals the effective prompt it had one moment earlier. The
// child's skills are untouched; only the prompt was ever inherited as text.
//
// Refusals are all 409, not 400: nothing about the request is malformed, the
// child is simply not in a state where solidifying means anything (it has no
// parent, or its parent has no prompt to fold in).
func (h *Handler) SolidifyAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	child, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, child) {
		return
	}
	if !child.ParentAgentID.Valid {
		writeError(w, http.StatusConflict, "this agent is a base role; there is nothing to solidify")
		return
	}

	parent, parentFound := h.parentAgentForResponse(r.Context(), &AgentResponse{
		WorkspaceID:   uuidToString(child.WorkspaceID),
		ParentAgentID: uuidToString(child.ParentAgentID),
	})
	if !parentFound {
		writeError(w, http.StatusConflict, "the parent agent no longer exists; detach this agent instead")
		return
	}
	userID := requestUserID(r)
	actorType, actorID := h.resolveActor(r, userID, uuidToString(child.WorkspaceID))
	// Solidifying copies the parent's prompt into a row this actor owns and can
	// read back, so it needs the parent's VIEW permission on top of the child's
	// manage permission. validateAgentParent refuses to create such a pair in
	// the first place; this covers the pair that went one-sided afterwards —
	// the base role's owner flipped it to private, or the child changed hands.
	// Detaching without folding the text in stays available via
	// PUT /api/agents/{id} with parent_agent_id: "".
	if !h.canAccessPrivateAgent(r.Context(), parent, actorType, actorID, uuidToString(parent.WorkspaceID)) {
		writeError(w, http.StatusForbidden, "you cannot read this agent's base role; detach it instead of solidifying")
		return
	}
	if parent.Instructions == "" {
		writeError(w, http.StatusConflict, "the parent agent has no instructions to inherit")
		return
	}

	effective := composeAgentInstructions(parent.Instructions, child.Instructions)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start solidify transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// Read the child again inside the transaction and FOR UPDATE: two
	// concurrent solidify calls would otherwise both read the pre-solidify
	// instructions, and the second write would fold the parent's prompt in
	// twice. Locking also keeps a concurrent update's instruction edit from
	// landing between the read and the write.
	locked, err := qtx.GetAgentForUpdate(r.Context(), child.ID)
	if err != nil {
		writeError(w, http.StatusConflict, "this agent changed while solidifying; retry")
		return
	}
	if !locked.ParentAgentID.Valid {
		writeError(w, http.StatusConflict, "this agent is a base role; there is nothing to solidify")
		return
	}
	effective = composeAgentInstructions(parent.Instructions, locked.Instructions)

	solidified, err := qtx.UpdateAgent(r.Context(), db.UpdateAgentParams{
		ID:           child.ID,
		Instructions: pgtype.Text{String: effective, Valid: true},
	})
	if err != nil {
		slog.Warn("solidify agent: write instructions failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to solidify agent")
		return
	}
	if _, err := qtx.SetAgentParentAgent(r.Context(), db.SetAgentParentAgentParams{
		ID:            child.ID,
		ParentAgentID: pgtype.UUID{},
	}); err != nil {
		slog.Warn("solidify agent: detach parent failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to solidify agent")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit solidify")
		return
	}
	// Re-read after commit so the response carries the committed row rather
	// than the values this request happened to compute.
	solidified, err = h.Queries.GetAgent(r.Context(), child.ID)
	if err != nil {
		slog.Warn("solidify agent: reload failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to load solidified agent")
		return
	}

	wsID := uuidToString(solidified.WorkspaceID)
	slog.Info("agent solidified", append(logger.RequestAttrs(r), "agent_id", id, "parent_agent_id", uuidToString(child.ParentAgentID), "workspace_id", wsID)...)

	resp := h.agentToResponse(solidified)
	if err := h.attachAgentSkills(r.Context(), &resp, solidified.ID); err != nil {
		slog.Warn("load agent skills after solidify failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}
	if err := h.enrichAgentResponseWithTargets(r.Context(), &resp, solidified.ID); err != nil {
		slog.Warn("solidify agent: load invocation targets for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}
	solidifiedResps := []AgentResponse{resp}
	if err := h.enrichAgentResponsesWithRelations(r.Context(), solidifiedResps); err != nil {
		slog.Warn("solidify agent: load parent relation for response failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
	}
	resp = solidifiedResps[0]
	// A solidified child is a new base role, so the parent's child_count moved.
	// The parent is not the response body here; the update event is what tells a
	// client to re-read the list, and publishing the parent keeps a client that
	// patches from its event stream from showing a stale count.
	parentResps := []AgentResponse{h.agentToResponse(parent)}
	if err := h.enrichAgentResponsesWithRelations(r.Context(), parentResps); err != nil {
		slog.Warn("solidify agent: load parent relation failed", append(logger.RequestAttrs(r), "error", err, "agent_id", uuidToString(parent.ID))...)
	}
	parentActorType, parentActorID := h.resolveActor(r, userID, wsID)
	h.publish(protocol.EventAgentStatus, wsID, parentActorType, parentActorID, map[string]any{"agent": broadcastAgentResponse(parentResps[0])})

	h.publish(protocol.EventAgentStatus, wsID, actorType, actorID, map[string]any{"agent": broadcastAgentResponse(resp)})
	redactAgentResponseForActor(&resp, actorType)
	if !h.composioMCPAppsEnabled(r.Context()) {
		suppressComposioToolkitAllowlist(&resp)
	} else if uuidToString(solidified.OwnerID) != userID {
		redactComposioToolkitAllowlist(&resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// CancelAgentTasks bulk-cancels every active task (queued/dispatched/running)
// belonging to an agent. Powers the agents-list "Cancel all tasks" row
// action. Same permission gate as archive (canManageAgent — owner or
// workspace admin/owner). Each cancelled row triggers a task:cancelled WS
// event so connected clients clear their live cards immediately.
//
// Note: a `running` task on the daemon side won't actually halt for up to
// ~5 seconds (daemon polls GetTaskStatus on that interval). The DB row is
// marked cancelled instantly, but the child process keeps going briefly;
// see daemon/daemon.go:919-942 for the polling loop. Surface this in the
// confirm-dialog copy so users aren't surprised by trailing transcript
// lines.
type cancelAgentTasksResponse struct {
	Cancelled int `json:"cancelled"`
}

func (h *Handler) CancelAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}

	cancelled, err := h.TaskService.CancelTasksForAgent(r.Context(), parseUUID(id))
	if err != nil {
		slog.Warn("cancel agent tasks failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to cancel tasks")
		return
	}

	slog.Info("agent tasks cancelled",
		append(logger.RequestAttrs(r), "agent_id", id, "count", len(cancelled))...)
	writeJSON(w, http.StatusOK, cancelAgentTasksResponse{Cancelled: len(cancelled)})
}

func (h *Handler) ListAgentTasks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	agent, ok := h.loadAgentForUser(w, r, id)
	if !ok {
		return
	}
	// Run history is part of the private-agent gate ("查看历史会话"). Same
	// 403 semantics as GetAgent.
	workspaceID := uuidToString(agent.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	includeUsage := false
	switch raw := strings.TrimSpace(r.URL.Query().Get("include_usage")); raw {
	case "", "false":
	case "true":
		includeUsage = true
	default:
		writeError(w, http.StatusBadRequest, "include_usage must be true or false")
		return
	}

	tasks, err := h.Queries.ListAgentTasks(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent tasks")
		return
	}

	tasks = visibleTaskHistory(tasks)
	resp := make([]AgentTaskResponse, len(tasks))
	var taskIDs []pgtype.UUID
	if includeUsage {
		taskIDs = make([]pgtype.UUID, len(tasks))
	}
	for i, t := range tasks {
		resp[i] = taskToResponse(t, workspaceID)
		if includeUsage {
			taskIDs[i] = t.ID
		}
	}
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))
	if includeUsage {
		if err := h.hydrateAgentTaskUsage(r.Context(), agent.ID, taskIDs, resp); err != nil {
			slog.Warn("list agent task usage failed", append(logger.RequestAttrs(r), "error", err, "agent_id", id)...)
			writeError(w, http.StatusInternalServerError, "failed to list agent task usage")
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// AgentActivityBucket is one day-bucketed throughput sample for the
// Agents-list ACTIVITY sparkline. bucket_at is midnight UTC of the day.
type AgentActivityBucket struct {
	AgentID        string `json:"agent_id"`
	BucketAt       string `json:"bucket_at"`
	TaskCount      int32  `json:"task_count"`
	FailedCount    int32  `json:"failed_count"`
	CompletedCount int32  `json:"completed_count"`
	CancelledCount int32  `json:"cancelled_count"`
}

// AgentRunCount is the trailing-30-day total task run count per agent,
// powering the Agents-list RUNS column.
type AgentRunCount struct {
	AgentID  string `json:"agent_id"`
	RunCount int32  `json:"run_count"`
}

// WorkspaceWorkingAgent is the privacy-safe agent summary returned by the
// workspace working-agents endpoint. It deliberately carries only display
// information plus the current running-task count and referenced issue ids;
// full AgentResponse fields include runtime and integration configuration that
// this chip does not need.
type WorkspaceWorkingAgent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	AvatarURL        *string  `json:"avatar_url"`
	RunningTaskCount int32    `json:"running_task_count"`
	IssueIDs         []string `json:"issue_ids"`
}

// ListWorkspaceWorkingAgents returns currently working user-authored agents in
// the workspace, independent of issue filters or Table pagination. The
// optional type query selects issue, autopilot, or chat work; omitting it keeps
// the all-sources projection. scope=mine narrows issue work to the authenticated
// member's selected My Issues relation. Access filtering mirrors the other
// workspace-wide agent aggregations so a private/non-allow-listed agent is
// never exposed by its name, avatar, count, or even presence.
func (h *Handler) ListWorkspaceWorkingAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	workType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch workType {
	case "", "issue", "autopilot", "chat":
	default:
		writeError(w, http.StatusBadRequest, "invalid type: must be issue, autopilot, or chat")
		return
	}

	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	mineRelation := strings.TrimSpace(r.URL.Query().Get("relation"))
	var memberID pgtype.UUID
	switch scope {
	case "":
		if mineRelation != "" {
			writeError(w, http.StatusBadRequest, "relation requires scope=mine")
			return
		}
	case "mine":
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "scope=mine requires type=issue")
			return
		}
		if mineRelation == "" {
			mineRelation = "any"
		}
		switch mineRelation {
		case "assigned", "created", "involved", "any":
		default:
			writeError(w, http.StatusBadRequest, "invalid relation: must be assigned, created, involved, or any")
			return
		}
		userID, ok := requireUserID(w, r)
		if !ok {
			return
		}
		var err error
		memberID, err = util.ParseUUID(userID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not authenticated")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope: must be mine")
		return
	}

	// Narrows the projection to one issue's direct children, so an issue
	// detail's sub-issue header reads this endpoint instead of deriving a
	// count client-side. Left zero when absent, which the query treats as
	// NULL and therefore as "no narrowing" — an older client that never
	// sends it keeps the exact workspace-wide behaviour.
	var parentIssueID pgtype.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("parent")); raw != "" {
		if workType != "issue" {
			writeError(w, http.StatusBadRequest, "parent requires type=issue")
			return
		}
		if scope != "" {
			writeError(w, http.StatusBadRequest, "parent cannot be combined with scope")
			return
		}
		var ok bool
		parentIssueID, ok = parseUUIDOrBadRequest(w, raw, "parent")
		if !ok {
			return
		}
	}

	rows, err := h.Queries.ListWorkspaceWorkingAgents(
		r.Context(),
		db.ListWorkspaceWorkingAgentsParams{
			WorkspaceID:   parseUUID(workspaceID),
			WorkType:      workType,
			MineRelation:  mineRelation,
			MemberID:      memberID,
			ParentIssueID: parentIssueID,
		},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace working agents")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]WorkspaceWorkingAgent, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.ID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, WorkspaceWorkingAgent{
			ID:               agentID,
			Name:             row.Name,
			AvatarURL:        h.resolveAvatarURLPtr(textToPtr(row.AvatarUrl)),
			RunningTaskCount: row.RunningTaskCount,
			IssueIDs:         uuidStringsOrEmpty(row.IssueIds),
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentRunCounts returns 30-day total run counts for every
// agent in the workspace. Same single-fetch pattern as live-tasks /
// activity to keep the Agents list cheap regardless of agent count.
func (h *Handler) GetWorkspaceAgentRunCounts(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentRunCounts(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent run counts")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentRunCount, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentRunCount{
			AgentID:  agentID,
			RunCount: row.RunCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspaceAgentActivity30d returns per-agent daily task counts for the
// last 30 days, anchored on completed_at. Single workspace-wide read backs
// both the Agents list sparkline (uses the trailing 7 buckets) and the
// agent detail "Last 30 days" panel (uses all 30) — one fetch is cheaper
// than two. Front-end fills missing days with zero; the back-end omits
// empty buckets to keep the response small.
func (h *Handler) GetWorkspaceAgentActivity30d(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.GetWorkspaceAgentActivity30d(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get agent activity")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentActivityBucket, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, AgentActivityBucket{
			AgentID:        agentID,
			BucketAt:       timestampToString(row.Bucket),
			TaskCount:      row.TaskCount,
			FailedCount:    row.FailedCount,
			CompletedCount: row.CompletedCount,
			CancelledCount: row.CancelledCount,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListWorkspaceAgentTaskSnapshot returns the workspace-wide task data the
// front-end reads for two things: every active task
// (queued/dispatched/running/waiting_local_directory), which is the current
// workload presence derives from, plus each agent's most recent OUTCOME task
// (completed/failed only), which is no longer part of presence since #1823 and
// only feeds the Squad hover card's "last activity" line. Cancelled tasks are
// excluded from the outcome half by design — cancel is a procedural signal
// ("attempt aborted"), not an outcome, so it must not mask a prior failure.
// Per-agent filtering happens in the front-end against this snapshot.
//
// The outcome half is deliberately still served here so shipped desktop builds
// keep working; MUL-5436 tracks moving it to a dedicated lazy endpoint.
func (h *Handler) ListWorkspaceAgentTaskSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListWorkspaceAgentTaskSnapshot(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent task snapshot")
		return
	}

	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	resp := make([]AgentTaskResponse, 0, len(tasks))
	for _, t := range tasks {
		if _, ok := allowed[uuidToString(t.AgentID)]; !ok {
			continue
		}
		resp = append(resp, taskToResponse(t, workspaceID))
	}
	h.hydrateTaskAttributions(r.Context(), attributionsOf(resp))

	writeJSON(w, http.StatusOK, resp)
}

func agentIDs(agents []db.Agent) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.ID)
	}
	return ids
}

// AgentWorkPause is the platform's reason a seat is not taking work.
// RecoverAt is empty when nothing but a person can bring it back — a
// balance breaker waits for a top-up and a manual re-enable.
type AgentWorkPause struct {
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
	Condition string `json:"condition,omitempty"`
	RecoverAt string `json:"recover_at,omitempty"`
	OpenedAt  string `json:"opened_at"`
}

// workPauses maps agent id to its newest open breaker. A lookup failure
// only drops the explanation, never the agent list.
func (h *Handler) workPauses(ctx context.Context, workspaceID pgtype.UUID) map[string]AgentWorkPause {
	rows, err := h.Queries.ListOpenQuotaBreakers(ctx, workspaceID)
	if err != nil {
		slog.Warn("list open quota breakers failed", "error", err)
		return nil
	}
	out := make(map[string]AgentWorkPause, len(rows))
	for _, row := range rows {
		id := uuidToString(row.AgentID)
		if _, seen := out[id]; seen {
			continue
		}
		pause := AgentWorkPause{
			Reason:    row.Reason,
			Detail:    redact.Text(clipRunes(strings.TrimSpace(row.Detail), 300)),
			Condition: row.RecoverCondition,
			OpenedAt:  timestampToString(row.OpenedAt),
		}
		if !quotarelay.IsManualRecovery(quotarelay.Kind(row.Reason)) {
			pause.RecoverAt = timestampToString(row.RecoverAt)
		}
		out[id] = pause
	}
	return out
}

func applyWorkPause(resp *AgentResponse, agent db.Agent, pauses map[string]AgentWorkPause) {
	if agent.WorkEnabled {
		return
	}
	if pause, ok := pauses[uuidToString(agent.ID)]; ok {
		resp.WorkPause = &pause
	}
}
