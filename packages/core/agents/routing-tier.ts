/**
 * The dispatch ladder's vocabulary.
 *
 * A seat's rung is a tag a person puts on the agent, not something derived
 * from its model id: the same model at another thinking level is another rung,
 * and two seats may deliberately sit on one model at different rungs. The
 * server stores the KEY; every surface that shows a rung translates it.
 *
 * Order is strongest first and is the ladder order the router walks, so it is
 * also the order these options are rendered in.
 */
export const ROUTING_TIER_KEYS = [
  "strongest",
  "strong",
  "medium",
  "weak",
] as const;

export type RoutingTierKey = (typeof ROUTING_TIER_KEYS)[number];

/** Narrows a server-provided string to a known rung. */
export function isRoutingTierKey(value: string): value is RoutingTierKey {
  return (ROUTING_TIER_KEYS as readonly string[]).includes(value);
}

/**
 * Resolves what the server sent to a rung this build knows.
 *
 * An unknown value is treated as "no rung" rather than rendered raw: a newer
 * backend that grew a fifth rung must not make an installed desktop build show
 * a tier name it cannot explain.
 */
export function routingTierKeyOf(value: string | undefined | null): RoutingTierKey | "" {
  const raw = (value ?? "").trim();
  return isRoutingTierKey(raw) ? raw : "";
}

/**
 * How much account headroom a person says a seat has (DENE-922). Routing
 * reads it only to order seats inside one rung — ample first — and never
 * shows it to the judge. Every seat has one; the server default is `normal`.
 *
 * Order is tightest first, which is the order the options are rendered in.
 */
export const ROUTING_USAGE_KEYS = ["tight", "normal", "ample"] as const;

export type RoutingUsageKey = (typeof ROUTING_USAGE_KEYS)[number];

export const DEFAULT_ROUTING_USAGE: RoutingUsageKey = "normal";

/** Resolves what the server sent; unknown or missing reads as the default. */
export function routingUsageKeyOf(value: string | undefined | null): RoutingUsageKey {
  const raw = (value ?? "").trim();
  return (ROUTING_USAGE_KEYS as readonly string[]).includes(raw)
    ? (raw as RoutingUsageKey)
    : DEFAULT_ROUTING_USAGE;
}

/**
 * Whether automatic dispatch may pick a seat at all (DENE-1600, ADR-0008).
 * Orthogonal to the rung: `mention_only` keeps its tier and still takes work
 * by @mention, assignment and delegation, but routing never selects it.
 *
 * Order is the default first, which is the order the options are rendered in.
 */
export const DISPATCH_MODE_KEYS = ["auto", "mention_only"] as const;

export type DispatchModeKey = (typeof DISPATCH_MODE_KEYS)[number];

export const DEFAULT_DISPATCH_MODE: DispatchModeKey = "auto";

/** Resolves what the server sent; unknown or missing reads as the default. */
export function dispatchModeKeyOf(value: string | undefined | null): DispatchModeKey {
  const raw = (value ?? "").trim();
  return (DISPATCH_MODE_KEYS as readonly string[]).includes(raw)
    ? (raw as DispatchModeKey)
    : DEFAULT_DISPATCH_MODE;
}
