/**
 * The routing configuration stored in `workspace.settings.routing` (DENE-633).
 *
 * This file is the client half of a cross-surface contract: the field names
 * and types here must match `server/internal/routing/settings.go` exactly.
 * Renaming one breaks every workspace that has already been configured, so
 * they are named once, here and there, and nowhere else.
 */

/** The key the routing block lives under inside `workspace.settings`. */
export const ROUTING_SETTINGS_KEY = "routing";

/**
 * The confidence floor applied when settings carry no explicit one. Mirrors
 * DefaultConfidenceThreshold on the server; a client that guessed a different
 * default would show a threshold the server does not apply.
 */
export const DEFAULT_CONFIDENCE_THRESHOLD = 0.6;

/**
 * How long a ticket may sit awaiting acceptance before the stale sweep looks
 * at it, when settings carry no explicit value. Mirrors
 * DefaultStaleReviewHours on the server (DENE-712).
 */
export const DEFAULT_STALE_REVIEW_HOURS = 24;

/**
 * The largest threshold worth storing. A year of silence is already far past
 * "nobody is coming back to this"; anything beyond it is a typo, and a typo
 * that silently disables the sweep is worse than one the form rejects.
 */
export const MAX_STALE_REVIEW_HOURS = 24 * 365;

export interface RoutingSettings {
  enabled: boolean;
  /**
   * Model identifier for the routing judge. Empty while `enabled` is true is
   * the "incomplete" state — somebody flipped the switch and stopped.
   */
  model: string;
  /** Confidence floor in (0, 1]. */
  confidence_threshold: number;
  /**
   * How long a ticket must sit in review with nothing happening on it before
   * the stale sweep looks at it, in hours.
   *
   * Deliberately not paired with its own switch: the sweep runs only while
   * `enabled` is true, exactly like every other routing behaviour, so there
   * is one answer to "is routing touching my tickets" and not two.
   */
  stale_review_hours: number;
  /**
   * This workspace's own OpenAI-compatible endpoint. Empty means the
   * deployment's, which is all this section could ever use before.
   *
   * Not a secret, and stored in the clear on purpose: somebody who cannot see
   * which endpoint their tickets are described to cannot consent to it.
   */
  base_url: string;
}

/**
 * The write-only key field.
 *
 * It is not part of `RoutingSettings` because it is never READ: the server
 * strips the stored key from every response, so there is no value to hold in
 * form state between saves. A save sends this field only when somebody typed
 * into the key box:
 *
 * - a non-empty string — store this key
 * - an empty string    — clear the stored key
 * - omitted            — leave the stored key alone
 *
 * The third case is the important one. Every unrelated settings save omits
 * it, and if omission meant "clear", the first such save would silently break
 * routing.
 */
export const ROUTING_API_KEY_FIELD = "api_key";

/**
 * What the section shows. Derived, never stored — a stored copy would drift
 * from the three fields above.
 *
 * - `off`          switch off (the default). Completely the pre-routing product.
 * - `incomplete`   switch on, no model chosen. ALSO completely the pre-routing
 *                  product — which is exactly why it has to be shown: somebody
 *                  who flipped the switch will otherwise believe it is working.
 * - `enabled`      switch on and a model chosen.
 * - `ineffective`  configured, but the model is rejected, unreachable, deleted,
 *                  or cooling down after repeated failures. Also the pre-routing
 *                  product, and the reason is shown HERE and never on a ticket.
 */
export type RoutingState = "off" | "incomplete" | "enabled" | "ineffective";

export const DEFAULT_ROUTING_SETTINGS: RoutingSettings = {
  enabled: false,
  model: "",
  confidence_threshold: DEFAULT_CONFIDENCE_THRESHOLD,
  stale_review_hours: DEFAULT_STALE_REVIEW_HOURS,
  base_url: "",
};

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Read the routing block out of a workspace's settings.
 *
 * Every unreadable shape — missing block, null, wrong type, a string where a
 * number belongs — yields the defaults, which is the switched-off state. A
 * settings payload this client cannot interpret must never render as enabled.
 */
export function parseRoutingSettings(
  settings: Record<string, unknown> | null | undefined,
): RoutingSettings {
  const block = settings?.[ROUTING_SETTINGS_KEY];
  if (!isRecord(block)) return { ...DEFAULT_ROUTING_SETTINGS };
  return {
    enabled: block.enabled === true,
    model: typeof block.model === "string" ? block.model : "",
    confidence_threshold: normalizeThreshold(block.confidence_threshold),
    stale_review_hours: normalizeStaleReviewHours(block.stale_review_hours),
    base_url: typeof block.base_url === "string" ? block.base_url : "",
  };
}

/**
 * Clamp a stored or typed threshold to something the server will honour.
 * Anything outside (0, 1] falls back to the default rather than to a value
 * that would accept every verdict.
 */
export function normalizeThreshold(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_CONFIDENCE_THRESHOLD;
  }
  if (value <= 0 || value > 1) return DEFAULT_CONFIDENCE_THRESHOLD;
  return value;
}

/**
 * Clamp a stored or typed stall threshold. Zero, negative, non-finite and
 * absurd values all fall back to the default: the fallback direction for a
 * row that can write a status is "look at fewer tickets, later", never
 * "sweep everything now".
 */
export function normalizeStaleReviewHours(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_STALE_REVIEW_HOURS;
  }
  if (value <= 0 || value > MAX_STALE_REVIEW_HOURS) {
    return DEFAULT_STALE_REVIEW_HOURS;
  }
  return value;
}

/**
 * Classify the stored fields.
 *
 * `ineffective` is never derived here: it depends on live model health, which
 * only the server knows. Pass the server's health report to get it.
 *
 * The gate is the server's own `state`, and deliberately NOT `usable === false`.
 * `usable` means "state is enabled", so it is also false for a workspace that
 * has routing switched off or has not chosen a model yet, and false in the
 * fallback a client uses when it cannot read the response at all. Reading it as
 * "the model is broken" turns three harmless situations into a red chip — most
 * visibly the first one anybody hits: flip the switch, type a model, and the
 * health still cached from a second ago says `off`.
 *
 * An unreadable response therefore lands on `enabled` with no success
 * timestamp, which renders as "enabled, not self-checked yet" — the honest
 * reading of "this client does not know", and not the green "connected" claim
 * that would hide a real fault.
 */
export function routingState(
  settings: RoutingSettings,
  health?: { state?: string } | null,
): RoutingState {
  if (!settings.enabled) return "off";
  if (settings.model.trim() === "") return "incomplete";
  if (health?.state === "ineffective") return "ineffective";
  return "enabled";
}

/** Only `enabled` routes issues. The other three are the pre-routing product. */
export function routingIsActive(state: RoutingState): boolean {
  return state === "enabled";
}

/**
 * Merge a routing block back into a full settings object for the update call.
 * The rest of `settings` is carried through untouched: this section shares the
 * column with everything else the workspace stores.
 */
export function withRoutingSettings(
  settings: Record<string, unknown> | null | undefined,
  next: RoutingSettings,
  /**
   * A newly typed key, or `""` to clear the stored one. Omit it — do not pass
   * `""` — for every save that is not about the key, or the stored key is
   * deleted.
   */
  apiKey?: string,
): Record<string, unknown> {
  // Fields this form does not own — the project -> direction table the CLI
  // writes (`projects`), and anything a newer server adds — are carried
  // through. Rebuilding the block from the four form fields alone would erase
  // them on every unrelated save.
  const stored = settings?.[ROUTING_SETTINGS_KEY];
  const block: Record<string, unknown> = {
    ...(isRecord(stored) ? stored : {}),
    enabled: next.enabled,
    model: next.model.trim(),
    confidence_threshold: normalizeThreshold(next.confidence_threshold),
    stale_review_hours: normalizeStaleReviewHours(next.stale_review_hours),
    base_url: next.base_url.trim(),
  };
  if (apiKey !== undefined) {
    block[ROUTING_API_KEY_FIELD] = apiKey.trim();
  }
  return { ...(settings ?? {}), [ROUTING_SETTINGS_KEY]: block };
}

/**
 * Whether the pair of endpoint fields is complete enough to be used.
 *
 * Both halves are required, and a half-filled pair is NOT an error — it means
 * the deployment gateway is used instead. The section says so, because
 * silently ignoring a typed-in endpoint is how somebody spends an afternoon
 * wondering why their own model is not being called.
 */
export function routingGatewayIsComplete(
  baseUrl: string,
  keyStored: boolean,
  typedKey?: string,
): boolean {
  const hasKey = typedKey !== undefined ? typedKey.trim() !== "" : keyStored;
  return baseUrl.trim() !== "" && hasKey;
}
