import type { Agent } from "../types";

/**
 * A seat accepts new work unless the backend explicitly sent `false`.
 * Older servers omit `work_enabled`; missing must keep the historical
 * "always takes work" behaviour rather than fail closed.
 */
export function isAgentWorkEnabled(
  agent: Pick<Agent, "work_enabled">,
): boolean {
  return agent.work_enabled !== false;
}
