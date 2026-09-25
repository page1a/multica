import type {
  DashboardUsageByAgent,
  PlanLimitWindow,
  PlanLimitsSnapshot,
} from "@multica/core/types";
import {
  displayPlanLimits,
  formatPlanLimitRemaining,
  isBalanceWindow,
  planLimitWindowShortLabel,
  quotaWindowSummaryParts,
} from "../../runtimes/components/plan-limits";
import { estimateCost } from "../../runtimes/utils";

export type AgentQuotaKind = "windows" | "exhausted" | "metered";

/**
 * The subscription snapshot an agent's quota surface should read (DENE-715).
 *
 * Plan limits live on the runtime, but one runtime serves every CLI seat on
 * the machine: an agent switched to a numbered account (`custom_env`
 * `CLAUDE_CONFIG_DIR`) shares its runtime row with its unbound siblings, and
 * that row can only ever carry the daemon-default account's windows. The daemon
 * therefore reports the bound agent's own account snapshot on the agent, and
 * this is where a reader prefers it.
 *
 * `null` and `undefined` both mean "nothing agent-specific to say" — the agent
 * binds no account, or the daemon has not run it yet — and the runtime's
 * snapshot is then the correct answer, exactly as it was before DENE-715.
 */
export function agentQuotaSnapshot(
  agentPlanLimits: PlanLimitsSnapshot | null | undefined,
  runtimePlanLimits: PlanLimitsSnapshot | null | undefined,
): PlanLimitsSnapshot | null | undefined {
  return agentPlanLimits ?? runtimePlanLimits;
}

/**
 * How an agent should present quota/usage:
 * - `windows`: subscription snapshot with live rolling percentages
 * - `exhausted`: 429 / session-limit with no live percentage (Claude, grok, …)
 * - `metered`: no subscription windows — show 30d token/cost instead
 *
 * A stale subscription snapshot (windows present, but displayPlanLimits
 * expired them) stays `windows` so the UI does not fall back to token cost.
 */
export function classifyAgentQuota(
  snapshot: PlanLimitsSnapshot | null | undefined,
  nowMs = Date.now(),
): AgentQuotaKind {
  const display = displayPlanLimits(snapshot, nowMs);
  if (display) {
    if (
      display.windows.some(
        (window) => window.used_percent != null || window.remaining != null,
      )
    ) {
      return "windows";
    }
    return "exhausted";
  }
  if ((snapshot?.windows?.length ?? 0) > 0) return "windows";
  if (snapshot?.status === "exhausted") return "exhausted";
  return "metered";
}

export function nearestResetAt(windows: PlanLimitWindow[]): number | null {
  let soonest: number | null = null;
  for (const window of windows) {
    if (window.resets_at == null || window.resets_at <= 0) continue;
    if (soonest == null || window.resets_at < soonest) soonest = window.resets_at;
  }
  return soonest;
}

/** Compact remaining duration (`2h`, `15m`, `3d`) for reset countdowns. */
export function compactRemaining(
  resetsAtSeconds: number,
  nowMs = Date.now(),
): string | null {
  const remainingMs = resetsAtSeconds * 1000 - nowMs;
  if (remainingMs <= 0) return null;
  const minutes = Math.max(1, Math.ceil(remainingMs / 60_000));
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

export function quotaWindowPercents(windows: PlanLimitWindow[]): Array<
  PlanLimitWindow & { used_percent: number; shortLabel: string }
> {
  return windows
    .filter(
      (window): window is PlanLimitWindow & { used_percent: number } =>
        window.used_percent != null,
    )
    .map((window) => ({
      ...window,
      shortLabel: planLimitWindowShortLabel(window),
    }));
}

export function sumAgentUsage30d(
  rows: DashboardUsageByAgent[],
  agentId: string,
): { tokens: number; cost: number } {
  let tokens = 0;
  let cost = 0;
  for (const row of rows) {
    if (row.agent_id !== agentId) continue;
    tokens +=
      row.input_tokens +
      row.output_tokens +
      row.cache_read_tokens +
      row.cache_write_tokens;
    cost += estimateCost(row);
  }
  return { tokens, cost };
}

export {
  displayPlanLimits,
  formatPlanLimitRemaining,
  isBalanceWindow,
  planLimitWindowShortLabel,
  quotaWindowSummaryParts,
};
