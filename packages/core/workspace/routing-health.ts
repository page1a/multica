import { z } from "zod";

import { parseWithFallback } from "../api/schema";
import type { RoutingState } from "./routing-settings";

/**
 * The server's read-only report on whether routing is actually working
 * (DENE-633).
 *
 * Why this exists at all: the routing design keeps failures OFF tickets on
 * purpose — a broken model must not leave a trail of identical comments. That
 * makes the settings section the only place a person can find out, so without
 * this endpoint a revoked key is visible nowhere but the server log.
 *
 * This is the client half of `routingHealthResponse` in
 * `server/internal/handler/routing.go`.
 */
export interface RoutingHealth {
  /** The four-way state, server-authoritative. */
  state: RoutingState;
  /** True only when routing is actually doing its work right now. */
  usable: boolean;
  /** Why it is not working, in words. Empty unless `state` is ineffective. */
  reason: string;
  /** Remaining cooldown in seconds; 0 when not cooling down. */
  retry_after_seconds: number;
  /** Unix seconds of the last successful model call; 0 for never. */
  last_success_at: number;
  /** Unix seconds of the last failure; 0 for never. */
  last_failure_at: number;
  model: string;
  threshold: number;
  /**
   * Host of the endpoint in use, host only — the workspace's own when it
   * supplied one, otherwise the deployment's (`MULTICA_LLM_BASE_URL`). Empty
   * when neither is configured, or when the configured value did not parse as
   * a URL.
   *
   * Host only, never the raw URL: a value pasted from a provider dashboard
   * routinely carries a token in its userinfo.
   */
  gateway_host: string;
  /** `MULTICA_LLM_DEFAULT_MODEL`, the deployment's own default. */
  gateway_default_model: string;
  /** False when there is no endpoint at all — neither workspace nor deployment. */
  gateway_configured: boolean;
  /**
   * Whose endpoint `gateway_host` is. The host alone does not say, and the two
   * answers lead to different next actions: edit the field in this section, or
   * go and talk to whoever runs the server.
   */
  gateway_scope: "workspace" | "deployment";
  /**
   * Which wire format the endpoint speaks. `systemone` is a TypeSafe System
   * One endpoint (Jev), which answers with typed judgments and the
   * probabilities behind them; `openai` is a chat-completions gateway.
   *
   * It matters to a reader, not just to the server: the confidence the
   * threshold gates on is a measured quantity on one protocol and a number the
   * model wrote about itself on the other.
   */
  gateway_protocol: "openai" | "systemone";
  /**
   * A workspace key is stored AND openable. Never the key itself.
   *
   * False for a key sealed under a deployment secret this server no longer
   * has (restored dump, rotated secret) — which has to read as "type it
   * again", not as a configured workspace.
   */
  gateway_key_set: boolean;
  /**
   * This deployment can store a workspace key at all. False disables the key
   * field rather than letting somebody type a credential into a form that
   * will refuse it.
   */
  workspace_key_storable: boolean;
}

/**
 * Deliberately lenient about `state`: an installed desktop build talking to a
 * newer backend must not blank the section because the server grew a fifth
 * state name. `normalizeRoutingHealth` maps anything unrecognised onto the
 * safe reading below.
 */
export const RoutingHealthSchema = z.object({
  state: z.string(),
  usable: z.boolean().optional(),
  reason: z.string().optional(),
  retry_after_seconds: z.number().optional(),
  last_success_at: z.number().optional(),
  last_failure_at: z.number().optional(),
  model: z.string().optional(),
  threshold: z.number().optional(),
  gateway_host: z.string().optional(),
  gateway_default_model: z.string().optional(),
  gateway_configured: z.boolean().optional(),
  gateway_scope: z.string().optional(),
  gateway_protocol: z.string().optional(),
  gateway_key_set: z.boolean().optional(),
  workspace_key_storable: z.boolean().optional(),
});

/**
 * What the UI shows when the server said nothing usable.
 *
 * `state: "off"` with `usable: false` is the honest fallback: a client that
 * cannot read the health report does not know routing is working, and the one
 * reading it must never invent is a green "enabled" chip.
 */
export const UNKNOWN_ROUTING_HEALTH: RoutingHealth = {
  state: "off",
  usable: false,
  reason: "",
  retry_after_seconds: 0,
  last_success_at: 0,
  last_failure_at: 0,
  model: "",
  threshold: 0,
  gateway_host: "",
  gateway_default_model: "",
  gateway_configured: false,
  gateway_scope: "deployment",
  gateway_protocol: "openai",
  gateway_key_set: false,
  workspace_key_storable: false,
};

const KNOWN_STATES: readonly RoutingState[] = [
  "off",
  "incomplete",
  "enabled",
  "ineffective",
];

/** Narrow a server-supplied state, defaulting an unknown one to `off`. */
export function normalizeRoutingState(value: string): RoutingState {
  return (KNOWN_STATES as readonly string[]).includes(value)
    ? (value as RoutingState)
    : "off";
}

/** Parse a health response, falling back rather than throwing. */
export function parseRoutingHealth(raw: unknown): RoutingHealth {
  const parsed = parseWithFallback(
    raw,
    RoutingHealthSchema,
    UNKNOWN_ROUTING_HEALTH,
    { endpoint: "GET /api/workspaces/{id}/routing/health" },
  );
  return {
    state: normalizeRoutingState(parsed.state ?? ""),
    // `=== true`, not truthy: a server that starts sending the field as a
    // string must not read as "routing is fine".
    usable: parsed.usable === true,
    reason: typeof parsed.reason === "string" ? parsed.reason : "",
    retry_after_seconds: toCount(parsed.retry_after_seconds),
    last_success_at: toCount(parsed.last_success_at),
    last_failure_at: toCount(parsed.last_failure_at),
    model: typeof parsed.model === "string" ? parsed.model : "",
    threshold: typeof parsed.threshold === "number" ? parsed.threshold : 0,
    gateway_host:
      typeof parsed.gateway_host === "string" ? parsed.gateway_host : "",
    gateway_default_model:
      typeof parsed.gateway_default_model === "string"
        ? parsed.gateway_default_model
        : "",
    // A backend that predates the field omits it; "absent" must not read as
    // "this deployment has no LLM", which would put a scary line under a
    // section that is working fine.
    gateway_configured: parsed.gateway_configured !== false,
    // Anything other than the one known override value reads as the
    // deployment gateway. A backend that predates the field omits it, and
    // "deployment" is what it meant.
    gateway_scope: parsed.gateway_scope === "workspace" ? "workspace" : "deployment",
    // A backend that predates the field omits it, and every endpoint before
    // this field existed was OpenAI-compatible. Anything unrecognised reads
    // the same way rather than claiming a protocol this client cannot check.
    gateway_protocol:
      parsed.gateway_protocol === "systemone" ? "systemone" : "openai",
    gateway_key_set: parsed.gateway_key_set === true,
    workspace_key_storable: parsed.workspace_key_storable === true,
  };
}

function toCount(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0
    ? value
    : 0;
}
