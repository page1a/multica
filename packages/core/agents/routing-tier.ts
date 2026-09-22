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
