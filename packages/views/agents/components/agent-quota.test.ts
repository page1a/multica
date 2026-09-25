// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { DashboardUsageByAgent, PlanLimitsSnapshot } from "@multica/core/types";
import {
  agentQuotaSnapshot,
  classifyAgentQuota,
  compactRemaining,
  nearestResetAt,
  quotaWindowPercents,
  sumAgentUsage30d,
} from "./agent-quota";

const NOW = Date.UTC(2026, 8, 13, 12);

const CODEX: PlanLimitsSnapshot = {
  provider: "codex",
  status: "available",
  observed_at: NOW / 1000,
  windows: [
    {
      name: "primary",
      used_percent: 25,
      window_minutes: 300,
      resets_at: NOW / 1000 + 2 * 60 * 60,
    },
    {
      name: "secondary",
      used_percent: 3,
      window_minutes: 10_080,
      resets_at: NOW / 1000 + 6 * 24 * 60 * 60,
    },
  ],
};

describe("classifyAgentQuota", () => {
  it("treats Codex rolling windows as subscription quota", () => {
    expect(classifyAgentQuota(CODEX, NOW)).toBe("windows");
  });

  it("keeps a stale subscription snapshot off the metered cost path", () => {
    expect(classifyAgentQuota(CODEX, NOW + 25 * 60 * 60 * 1000)).toBe("windows");
  });

  it("treats a window-less 429 snapshot as exhausted", () => {
    const grok: PlanLimitsSnapshot = {
      provider: "grok",
      status: "exhausted",
      observed_at: NOW / 1000,
    };
    expect(classifyAgentQuota(grok, NOW)).toBe("exhausted");
  });

  it("falls back to metered usage when no snapshot exists", () => {
    expect(classifyAgentQuota(undefined, NOW)).toBe("metered");
    expect(classifyAgentQuota(null, NOW)).toBe("metered");
  });
});

describe("agentQuotaSnapshot", () => {
  const ACCOUNT2: PlanLimitsSnapshot = { ...CODEX, observed_at: NOW / 1000 + 5 };

  it("prefers the agent's own account snapshot over the runtime's", () => {
    expect(agentQuotaSnapshot(ACCOUNT2, CODEX)).toBe(ACCOUNT2);
  });

  it("falls back to the runtime snapshot when the agent has none", () => {
    // No binding, or the daemon has not run this agent yet: both read as
    // "nothing agent-specific to say".
    expect(agentQuotaSnapshot(undefined, CODEX)).toBe(CODEX);
    expect(agentQuotaSnapshot(null, CODEX)).toBe(CODEX);
    expect(agentQuotaSnapshot(undefined, undefined)).toBeUndefined();
  });
});

describe("compactRemaining / nearestResetAt", () => {
  it("picks the soonest reset and formats a compact countdown", () => {
    expect(nearestResetAt(CODEX.windows!)).toBe(NOW / 1000 + 2 * 60 * 60);
    expect(compactRemaining(NOW / 1000 + 2 * 60 * 60, NOW)).toBe("2h");
    expect(compactRemaining(NOW / 1000 + 15 * 60, NOW)).toBe("15m");
    expect(compactRemaining(NOW / 1000 + 3 * 24 * 60 * 60, NOW)).toBe("3d");
  });

  it("returns null once the reset has passed", () => {
    expect(compactRemaining(NOW / 1000 - 1, NOW)).toBeNull();
  });
});

describe("quotaWindowPercents", () => {
  it("keeps provider window short labels for the capsule", () => {
    const percents = quotaWindowPercents(CODEX.windows!);
    expect(percents.map((w) => `${w.shortLabel} ${w.used_percent}`)).toEqual([
      "5h 25",
      "7d 3",
    ]);
  });

  it("labels Gemini Pro/Flash windows for the hover capsule", () => {
    const percents = quotaWindowPercents([
      { name: "gemini_pro", used_percent: 12 },
      { name: "gemini_flash", used_percent: 40 },
    ]);
    expect(percents.map((w) => `${w.shortLabel} ${w.used_percent}`)).toEqual([
      "Pro 12",
      "Flash 40",
    ]);
  });
});

describe("coding-plan and balance snapshots", () => {
  it("treats remaining-balance windows as subscription quota", () => {
    const deepseek: PlanLimitsSnapshot = {
      provider: "dsh",
      status: "available",
      observed_at: NOW / 1000,
      windows: [{ name: "balance_cny", remaining: 42.5 }],
    };
    expect(classifyAgentQuota(deepseek, NOW)).toBe("windows");
  });

  it("summarizes 5h/7d coding-plan percents for the capsule", () => {
    const kimi: PlanLimitsSnapshot = {
      provider: "kimi",
      status: "available",
      observed_at: NOW / 1000,
      windows: [
        { name: "five_hour", used_percent: 12, window_minutes: 300 },
        { name: "seven_day", used_percent: 40, window_minutes: 10_080 },
      ],
    };
    expect(classifyAgentQuota(kimi, NOW)).toBe("windows");
    expect(quotaWindowPercents(kimi.windows!).map((w) => w.shortLabel)).toEqual([
      "5h",
      "7d",
    ]);
  });
});

describe("sumAgentUsage30d", () => {
  it("folds this agent's token rows and ignores others", () => {
    const rows: DashboardUsageByAgent[] = [
      {
        agent_id: "agent-1",
        provider: "grok",
        model: "grok-4",
        input_tokens: 1_000,
        output_tokens: 500,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 2 * 10_000_000_000,
        task_count: 1,
      },
      {
        agent_id: "agent-2",
        provider: "grok",
        model: "grok-4",
        input_tokens: 9_000,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 50 * 10_000_000_000,
        task_count: 1,
      },
    ];
    expect(sumAgentUsage30d(rows, "agent-1")).toEqual({
      tokens: 1_500,
      cost: 2,
    });
  });
});
