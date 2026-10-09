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

/**
 * Which model roles are on (DENE-923). Mirrors `Mode` in settings.go.
 *
 * - `none`            no model is called; every ticket takes the fallback rung
 * - `analysis`        the analysis model reads the ticket and picks the tier
 * - `analysis_judge`  the analysis model writes the facts, the judge picks
 * - `judge`           the judge reads the ticket and picks (the original path)
 */
export type RoutingMode = "none" | "analysis" | "analysis_judge" | "judge";

export type RoutingRole = "analysis" | "judge";

/**
 * The analysis role: a general chat model that reads the whole ticket and
 * reduces it to a few facts. Its own endpoint and key, like the judge's.
 */
export interface RoutingAnalysisSettings {
  enabled: boolean;
  model: string;
  base_url: string;
  source?: "api_gateway" | "runtime_subscription";
  runtime_id?: string;
  thinking_level?: string;
}

export interface RoutingSettings {
  enabled: boolean;
  /**
   * Model identifier for the routing judge. Empty while the judge is on is
   * the "incomplete" state — somebody flipped the switch and stopped.
   */
  model: string;
  /** Whether the judge role is on. */
  judge_enabled: boolean;
  analysis: RoutingAnalysisSettings;
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
  /**
   * Workspace wording for which tier should do the work. Empty means the
   * shared default in `routing-policy-prompt.ts` — a blank prompt is not
   * "let the model decide from habit".
   */
  policy_prompt?: string;
  /**
   * 「用量优先」: inside a rung, seats tagged ample go before tight ones.
   * Default ON; the server reads a missing key as on, so this client does too.
   */
  usage_priority: boolean;
  /**
   * 「允许上调一档」: when every seat on the judged rung is tight, the rung
   * above's ample seat takes the work. Default off, and meaningless while
   * `usage_priority` is off.
   */
  allow_upshift: boolean;
  /**
   * 「接着做」(DENE-1202): a ticket continuing a previous stage, its parent,
   * or its batch goes back to that work's executor when the seat can take it.
   * Default off, which is shadow mode — routing keeps its own pick and the
   * assignment comment says who this rule would have picked.
   */
  prefer_continuation: boolean;
  /**
   * 「负载分流」(DENE-1203): inside the rung and direction routing picked, a
   * seat with fewer unfinished runs goes before a busy one. Default off, which
   * is shadow mode. With both on, 「接着做」 wins.
   */
  prefer_idle: boolean;
  /**
   * 「按判断配验收」(DENE-1252): the reviewer slot gets a seat only when the
   * routing model asks for a check with confidence; otherwise it reads
   * 不需要验收. Default off, which keeps the fallback reviewer seat.
   */
  judged_review: boolean;
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

/** The block the analysis role's settings live in, inside the routing block. */
export const ROUTING_ANALYSIS_KEY = "analysis";

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

/**
 * A workspace that has never configured routing starts on the analysis
 * model alone: one general model is the cheapest thing that works, and the
 * judge is an opt-in second step.
 */
export const DEFAULT_ROUTING_SETTINGS: RoutingSettings = {
  enabled: false,
  model: "",
  judge_enabled: false,
  analysis: { enabled: true, model: "", base_url: "", source: "api_gateway", runtime_id: "", thinking_level: "low" },
  confidence_threshold: DEFAULT_CONFIDENCE_THRESHOLD,
  stale_review_hours: DEFAULT_STALE_REVIEW_HOURS,
  base_url: "",
  policy_prompt: "",
  usage_priority: true,
  allow_upshift: false,
  prefer_continuation: false,
  prefer_idle: false,
  judged_review: false,
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
  if (!isRecord(block)) {
    return {
      ...DEFAULT_ROUTING_SETTINGS,
      analysis: { ...DEFAULT_ROUTING_SETTINGS.analysis },
    };
  }
  const analysisBlock = block[ROUTING_ANALYSIS_KEY];
  const analysis: RoutingAnalysisSettings = isRecord(analysisBlock)
    ? {
        enabled: analysisBlock.enabled === true,
        model: typeof analysisBlock.model === "string" ? analysisBlock.model : "",
        base_url: typeof analysisBlock.base_url === "string" ? analysisBlock.base_url : "",
        ...(analysisBlock.source === "runtime_subscription" ? { source: "runtime_subscription" as const } : {}),
        ...(typeof analysisBlock.runtime_id === "string" ? { runtime_id: analysisBlock.runtime_id } : {}),
        ...(typeof analysisBlock.thinking_level === "string" ? { thinking_level: analysisBlock.thinking_level } : {}),
      }
    : { enabled: false, model: "", base_url: "" };
  return {
    enabled: block.enabled === true,
    model: typeof block.model === "string" ? block.model : "",
    // A block saved before the split has no switch: it only had the judge,
    // and it keeps it. Same reading as JudgeOn on the server.
    judge_enabled:
      typeof block.judge_enabled === "boolean" ? block.judge_enabled : !analysis.enabled,
    analysis,
    confidence_threshold: normalizeThreshold(block.confidence_threshold),
    stale_review_hours: normalizeStaleReviewHours(block.stale_review_hours),
    base_url: typeof block.base_url === "string" ? block.base_url : "",
    policy_prompt: typeof block.policy_prompt === "string" ? block.policy_prompt : "",
    usage_priority: block.usage_priority !== false,
    allow_upshift: block.allow_upshift === true,
    prefer_continuation: block.prefer_continuation === true,
    prefer_idle: block.prefer_idle === true,
    judged_review: block.judged_review === true,
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
  // A role that is on with no model is incomplete, whichever role it is.
  // Both off is not: it is the deliberate "no model" mode.
  if (settings.analysis.enabled && (settings.analysis.model.trim() === "" || (settings.analysis.source === "runtime_subscription" && (settings.analysis.runtime_id ?? "").trim() === ""))) return "incomplete";
  if (settings.judge_enabled && settings.model.trim() === "") return "incomplete";
  if (health?.state === "ineffective") return "ineffective";
  return "enabled";
}

/** Which roles are on. Mirrors `Settings.Mode` on the server. */
export function routingMode(settings: RoutingSettings): RoutingMode {
  if (settings.analysis.enabled) return settings.judge_enabled ? "analysis_judge" : "analysis";
  return settings.judge_enabled ? "judge" : "none";
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
  /** The same, for the analysis role's key. */
  analysisApiKey?: string,
): Record<string, unknown> {
  // Fields this form does not own — the project -> direction table the CLI
  // writes (`projects`), and anything a newer server adds — are carried
  // through. Rebuilding the block from the four form fields alone would erase
  // them on every unrelated save.
  const stored = settings?.[ROUTING_SETTINGS_KEY];
  const storedAnalysis = isRecord(stored) ? stored[ROUTING_ANALYSIS_KEY] : undefined;
  const analysis: Record<string, unknown> = {
    ...(isRecord(storedAnalysis) ? storedAnalysis : {}),
    enabled: next.analysis.enabled,
    model: next.analysis.model.trim(),
    base_url: next.analysis.base_url.trim(),
    // Persist the selected transport on every save. Keeping the previously
    // stored runtime value when the form switches back to the gateway makes
    // a refresh silently re-enable runtime analysis.
  };
  if (next.analysis.source === "runtime_subscription") {
    analysis.source = "runtime_subscription";
    analysis.runtime_id = (next.analysis.runtime_id ?? "").trim();
    analysis.thinking_level = (next.analysis.thinking_level ?? "low").trim() || "low";
  } else if (next.analysis.source === "api_gateway") {
    analysis.source = "api_gateway";
  }
  if (analysisApiKey !== undefined) {
    analysis[ROUTING_API_KEY_FIELD] = analysisApiKey.trim();
  }
  const block: Record<string, unknown> = {
    ...(isRecord(stored) ? stored : {}),
    enabled: next.enabled,
    model: next.model.trim(),
    confidence_threshold: normalizeThreshold(next.confidence_threshold),
    stale_review_hours: normalizeStaleReviewHours(next.stale_review_hours),
    base_url: next.base_url.trim(),
    policy_prompt: (next.policy_prompt ?? "").trim(),
    usage_priority: next.usage_priority,
    allow_upshift: next.allow_upshift,
    prefer_continuation: next.prefer_continuation,
    prefer_idle: next.prefer_idle,
    judged_review: next.judged_review,
    judge_enabled: next.judge_enabled,
    [ROUTING_ANALYSIS_KEY]: analysis,
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
