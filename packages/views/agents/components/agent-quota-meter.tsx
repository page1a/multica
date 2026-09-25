"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { dashboardUsageByAgentOptions } from "@multica/core/dashboard";
import { useWorkspaceId } from "@multica/core/hooks";
import type {
  AgentRuntime,
  PlanLimitWindow,
  PlanLimitsSnapshot,
} from "@multica/core/types";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT } from "../../i18n";
import { formatTokens, formatUsd } from "../../runtimes/utils";
import {
  agentQuotaSnapshot,
  classifyAgentQuota,
  compactRemaining,
  displayPlanLimits,
  formatPlanLimitRemaining,
  isBalanceWindow,
  nearestResetAt,
  planLimitWindowShortLabel,
  quotaWindowPercents,
  quotaWindowSummaryParts,
  sumAgentUsage30d,
} from "./agent-quota";

const USAGE_DAYS = 30;

export function useNowTick(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

function percentageTone(value: number): string {
  if (value >= 100) return "text-destructive";
  if (value >= 80) return "text-warning";
  return "text-foreground";
}

function percentageBarTone(value: number): string {
  if (value >= 100) return "bg-destructive";
  if (value >= 80) return "bg-warning";
  return "bg-primary";
}

function HealthBadge({ exhausted }: { exhausted: boolean }) {
  const { t } = useT("agents");
  return (
    <span
      className={`shrink-0 rounded-md px-1.5 py-0.5 text-micro font-medium ${
        exhausted
          ? "bg-destructive/10 text-destructive"
          : "bg-success/10 text-success"
      }`}
    >
      {exhausted
        ? t(($) => $.quota.health_exhausted)
        : t(($) => $.quota.health_ok)}
    </span>
  );
}

function useAgentUsage30d(enabled: boolean) {
  const wsId = useWorkspaceId();
  const tz = useViewingTimezone();
  return useQuery({
    ...dashboardUsageByAgentOptions(wsId, USAGE_DAYS, null, tz),
    enabled: enabled && !!wsId,
  });
}

function MeteredUsageText({
  tokens,
  cost,
}: {
  tokens: number;
  cost: number;
}) {
  const { t } = useT("agents");
  if (tokens <= 0 && cost <= 0) return null;
  return (
    <span className="min-w-0 truncate font-mono text-micro tabular-nums">
      {t(($) => $.quota.usage_30d, {
        tokens: formatTokens(tokens),
        cost: formatUsd(cost),
      })}
    </span>
  );
}

function WindowsCapsule({
  windows,
  now,
}: {
  windows: PlanLimitWindow[];
  now: number;
}) {
  const { t } = useT("agents");
  const percents = quotaWindowPercents(windows);
  const reset = nearestResetAt(windows);
  const remaining = reset != null ? compactRemaining(reset, now) : null;
  const summary = quotaWindowSummaryParts(windows).slice(0, 2).join(" · ");
  const peak = percents.reduce(
    (max, window) => Math.max(max, window.used_percent),
    0,
  );

  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        className={`min-w-0 truncate font-mono text-micro tabular-nums ${percentageTone(peak)}`}
        title={summary}
      >
        {summary}
      </span>
      {remaining && (
        <span className="shrink-0 text-micro text-muted-foreground">
          {t(($) => $.quota.resets_in, { when: remaining })}
        </span>
      )}
    </span>
  );
}

/**
 * Compact quota/usage capsule for the agent hover card and the overview
 * Runtime row. Subscription CLIs show rolling-window percentages; metered
 * CLIs show 30d tokens/cost plus a 429 health badge.
 *
 * `planLimits` is the agent's own snapshot when the daemon reported one (an
 * agent bound to a numbered CLI account, DENE-715); the runtime's snapshot is
 * the fallback for every agent that binds nothing.
 */
export function AgentQuotaCapsule({
  agentId,
  runtime,
  planLimits,
  now = Date.now(),
  labeled = true,
}: {
  agentId: string;
  runtime: AgentRuntime | null;
  planLimits?: PlanLimitsSnapshot | null;
  now?: number;
  labeled?: boolean;
}) {
  const { t } = useT("agents");
  const snapshot = agentQuotaSnapshot(planLimits, runtime?.plan_limits);
  const kind = classifyAgentQuota(snapshot, now);
  const display = displayPlanLimits(snapshot, now);
  const exhausted = kind === "exhausted";
  const showUsage = kind === "metered" || exhausted;
  const showWindows = kind === "windows" && display != null && display.windows.length > 0;
  const usageQuery = useAgentUsage30d(showUsage);
  const usage = sumAgentUsage30d(usageQuery.data ?? [], agentId);
  const hasUsage = usage.tokens > 0 || usage.cost > 0;

  if (showWindows && display) {
    const label = t(($) => $.profile_card.quota_label);
    return (
      <div className="flex items-center gap-1.5" aria-label={label}>
        {labeled && (
          <span className="w-12 shrink-0 text-muted-foreground">{label}</span>
        )}
        <WindowsCapsule windows={display.windows} now={now} />
      </div>
    );
  }

  if (!exhausted && (!showUsage || usageQuery.isLoading || !hasUsage)) {
    return null;
  }

  const label = t(($) => $.profile_card.usage_label);
  return (
    <div className="flex items-center gap-1.5" aria-label={label}>
      {labeled && (
        <span className="w-12 shrink-0 text-muted-foreground">{label}</span>
      )}
      <span className="flex min-w-0 items-center gap-1.5">
        <MeteredUsageText tokens={usage.tokens} cost={usage.cost} />
        <HealthBadge exhausted={exhausted} />
      </span>
    </div>
  );
}

function WindowBars({
  windows,
  now,
}: {
  windows: PlanLimitWindow[];
  now: number;
}) {
  const { t } = useT("agents");
  const reset = nearestResetAt(windows);
  const remaining = reset != null ? compactRemaining(reset, now) : null;

  return (
    <div className="space-y-2.5">
      {windows.map((window) => {
        const balance = formatPlanLimitRemaining(window);
        if (balance) {
          return (
            <div key={window.name}>
              <div className="flex items-center justify-between gap-3">
                <span className="text-caption font-medium">
                  {window.name === "balance_cny" ? "¥" : "$"}
                </span>
                <span className="text-caption font-semibold tabular-nums">
                  {balance}
                </span>
              </div>
            </div>
          );
        }
        if (window.used_percent == null || isBalanceWindow(window)) return null;
        return (
          <div key={window.name}>
            <div className="flex items-center justify-between gap-3">
              <span className="text-caption font-medium">
                {planLimitWindowShortLabel(window)}
              </span>
              <span
                className={`text-caption font-semibold tabular-nums ${percentageTone(window.used_percent)}`}
              >
                {t(($) => $.quota.used_percent, {
                  percent: Math.round(window.used_percent),
                })}
              </span>
            </div>
            <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
              <div
                className={`h-full rounded-full ${percentageBarTone(window.used_percent)}`}
                style={{ width: `${Math.min(100, window.used_percent)}%` }}
              />
            </div>
          </div>
        );
      })}
      {remaining && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.quota.resets_in, { when: remaining })}
        </p>
      )}
    </div>
  );
}

/**
 * Overview sidebar block: progress bars + reset countdown for subscription
 * runtimes, or 30d token/cost + health for metered CLIs.
 *
 * `planLimits` is the agent's own snapshot when the daemon reported one (an
 * agent bound to a numbered CLI account, DENE-715); the runtime's snapshot is
 * the fallback for every agent that binds nothing.
 */
export function AgentQuotaMeter({
  agentId,
  runtime,
  planLimits,
  now = Date.now(),
}: {
  agentId: string;
  runtime: AgentRuntime | null;
  planLimits?: PlanLimitsSnapshot | null;
  now?: number;
}) {
  const { t } = useT("agents");
  const snapshot = agentQuotaSnapshot(planLimits, runtime?.plan_limits);
  const kind = classifyAgentQuota(snapshot, now);
  const display = displayPlanLimits(snapshot, now);
  const exhausted = kind === "exhausted";
  const showWindows = kind === "windows" && display != null && display.windows.length > 0;
  const showUsage = kind === "metered" || exhausted;
  const usageQuery = useAgentUsage30d(showUsage);
  const usage = sumAgentUsage30d(usageQuery.data ?? [], agentId);
  const hasUsage = usage.tokens > 0 || usage.cost > 0;

  if (!showWindows && !showUsage && !exhausted) return null;
  if (showWindows && !display) return null;

  return (
    <section className="mt-5 border-t pt-5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-body font-medium">
          {showWindows
            ? t(($) => $.quota.title)
            : t(($) => $.quota.usage_title)}
        </h2>
        {(exhausted || kind === "metered") && (
          <HealthBadge exhausted={exhausted} />
        )}
      </div>

      {showWindows && display ? (
        <div className="mt-3">
          <WindowBars windows={display.windows} now={now} />
        </div>
      ) : usageQuery.isLoading ? null : hasUsage ? (
        <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3">
          <div className="min-w-0">
            <div className="text-title font-semibold tabular-nums">
              {formatTokens(usage.tokens)}
            </div>
            <div className="truncate text-micro text-muted-foreground">
              {t(($) => $.quota.tokens_label)}
            </div>
          </div>
          <div className="min-w-0">
            <div className="text-title font-semibold tabular-nums">
              {formatUsd(usage.cost)}
            </div>
            <div className="truncate text-micro text-muted-foreground">
              {t(($) => $.quota.cost_label)}
            </div>
          </div>
        </div>
      ) : (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.quota.usage_empty)}
        </p>
      )}
    </section>
  );
}
