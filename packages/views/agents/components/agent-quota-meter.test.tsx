// @vitest-environment jsdom

import type { ReactNode } from "react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import { dashboardKeys } from "@multica/core/dashboard";
import type { AgentRuntime, DashboardUsageByAgent, PlanLimitsSnapshot } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import { AgentQuotaCapsule, AgentQuotaMeter } from "./agent-quota-meter";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };
const NOW = Date.UTC(2026, 8, 13, 12);

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));

vi.mock("@multica/core/api", () => ({
  api: {
    getDashboardUsageByAgent: () => Promise.resolve([]),
  },
}));

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

// The daemon's snapshot for an agent bound to a numbered Claude account
// (DENE-715). Deliberately unlike CODEX so a test can tell the two apart.
const ACCOUNT2: PlanLimitsSnapshot = {
  provider: "claude",
  status: "available",
  observed_at: NOW / 1000 + 5,
  windows: [
    {
      name: "five_hour",
      used_percent: 80,
      window_minutes: 300,
      resets_at: NOW / 1000 + 30 * 60,
    },
    {
      name: "seven_day",
      used_percent: 40,
      window_minutes: 10_080,
      resets_at: NOW / 1000 + 3 * 24 * 60 * 60,
    },
  ],
};

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    workspace_id: "ws-1",
    daemon_id: "d-1",
    name: "Codex (host)",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "u-1",
    visibility: "private",
    last_seen_at: new Date(NOW).toISOString(),
    created_at: new Date(NOW).toISOString(),
    updated_at: new Date(NOW).toISOString(),
    ...overrides,
  };
}

function renderQuota(
  ui: ReactNode,
  usage: DashboardUsageByAgent[] = [],
) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(dashboardKeys.byAgent("ws-1", 30, null, "UTC"), usage);
  return render(
    <QueryClientProvider client={qc}>
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        {ui}
      </I18nProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("AgentQuotaCapsule", () => {
  it("renders subscription window percentages and a reset countdown", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({ plan_limits: CODEX })}
        now={NOW}
      />,
    );

    expect(screen.getByText(enAgents.profile_card.quota_label)).toBeInTheDocument();
    expect(screen.getByText("5h 25% · 7d 3%")).toBeInTheDocument();
    expect(
      screen.getByText(enAgents.quota.resets_in.replace("{{when}}", "2h")),
    ).toBeInTheDocument();
  });

  it("shows the agent's own account, not its runtime's default seat", () => {
    // The runtime row carries the daemon-default account. An agent switched to
    // a numbered account must read its own snapshot instead (DENE-715).
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({ plan_limits: CODEX })}
        planLimits={ACCOUNT2}
        now={NOW}
      />,
    );

    expect(screen.getByText("5h 80% · 7d 40%")).toBeInTheDocument();
    expect(screen.queryByText("5h 25% · 7d 3%")).toBeNull();
  });

  it("keeps the runtime's account for an agent with no binding", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({ plan_limits: CODEX })}
        planLimits={null}
        now={NOW}
      />,
    );

    expect(screen.getByText("5h 25% · 7d 3%")).toBeInTheDocument();
  });

  it("renders a DeepSeek remaining balance instead of 30d tokens", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({
          provider: "dsh",
          plan_limits: {
            provider: "dsh",
            status: "available",
            observed_at: NOW / 1000,
            windows: [{ name: "balance_cny", remaining: 42.5 }],
          },
        })}
        now={NOW}
      />,
    );

    expect(screen.getByText("¥42.5")).toBeInTheDocument();
  });

  it("renders Kimi coding-plan 5h and 7d percents", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({
          provider: "kimi",
          plan_limits: {
            provider: "kimi",
            status: "available",
            observed_at: NOW / 1000,
            windows: [
              { name: "five_hour", used_percent: 12, window_minutes: 300 },
              { name: "seven_day", used_percent: 40, window_minutes: 10_080 },
            ],
          },
        })}
        now={NOW}
      />,
    );

    expect(screen.getByText("5h 12% · 7d 40%")).toBeInTheDocument();
  });

  it("renders a 429 health badge for window-less exhausted snapshots", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({
          provider: "grok",
          plan_limits: {
            provider: "grok",
            status: "exhausted",
            observed_at: NOW / 1000,
          },
        })}
        now={NOW}
      />,
    );

    expect(screen.getByText(enAgents.quota.health_exhausted)).toBeInTheDocument();
  });

  it("renders 30d token usage for metered runtimes", () => {
    const usage: DashboardUsageByAgent[] = [
      {
        agent_id: "agent-1",
        provider: "grok",
        model: "grok-4",
        input_tokens: 1_200_000,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 34_000_000_000,
        task_count: 4,
      },
    ];
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({ provider: "grok" })}
        now={NOW}
      />,
      usage,
    );

    expect(screen.getByText(enAgents.profile_card.usage_label)).toBeInTheDocument();
    expect(screen.getByText(enAgents.quota.health_ok)).toBeInTheDocument();
    expect(screen.getByText(/1\.2M/)).toBeInTheDocument();
  });

  it("hides the metered capsule when the agent has no 30d usage", () => {
    renderQuota(
      <AgentQuotaCapsule
        agentId="agent-1"
        runtime={makeRuntime({ provider: "grok" })}
        now={NOW}
      />,
    );

    expect(screen.queryByText(enAgents.profile_card.usage_label)).toBeNull();
    expect(screen.queryByText(enAgents.quota.health_ok)).toBeNull();
  });
});

describe("AgentQuotaMeter", () => {
  it("renders progress bars for subscription windows", () => {
    renderQuota(
      <AgentQuotaMeter
        agentId="agent-1"
        runtime={makeRuntime({ plan_limits: CODEX })}
        now={NOW}
      />,
    );

    expect(screen.getByText(enAgents.quota.title)).toBeInTheDocument();
    expect(screen.getByText("5h")).toBeInTheDocument();
    expect(screen.getByText("7d")).toBeInTheDocument();
    expect(
      screen.getByText(enAgents.quota.used_percent.replace("{{percent}}", "25")),
    ).toBeInTheDocument();
  });

  it("draws the agent's own account bars, not the runtime's default seat", () => {
    renderQuota(
      <AgentQuotaMeter
        agentId="agent-1"
        runtime={makeRuntime({ plan_limits: CODEX })}
        planLimits={ACCOUNT2}
        now={NOW}
      />,
    );

    expect(
      screen.getByText(enAgents.quota.used_percent.replace("{{percent}}", "80")),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(
        enAgents.quota.used_percent.replace("{{percent}}", "25"),
      ),
    ).toBeNull();
  });

  it("renders 30d token and cost metrics for metered runtimes", () => {
    const usage: DashboardUsageByAgent[] = [
      {
        agent_id: "agent-1",
        provider: "dsh",
        model: "deepseek-chat",
        input_tokens: 800_000,
        output_tokens: 200_000,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd_ticks: 15_000_000_000,
        task_count: 2,
      },
    ];
    renderQuota(
      <AgentQuotaMeter
        agentId="agent-1"
        runtime={makeRuntime({ provider: "dsh" })}
        now={NOW}
      />,
      usage,
    );

    expect(screen.getByText(enAgents.quota.usage_title)).toBeInTheDocument();
    expect(screen.getByText("1M")).toBeInTheDocument();
    expect(screen.getByText(enAgents.quota.health_ok)).toBeInTheDocument();
  });
});
