// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AgentTask, TaskUsage } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

import { RunCostBreakdown } from "./run-cost-breakdown";

// Happy path, wiring and accessibility only. The arithmetic matrix — floored
// hit rate, tie-breaking on the largest segment, per-slice metadata, the
// cost-split reconciliation — is canonical in run-cost.test.ts.

function makeTask(overrides: Partial<AgentTask> = {}): AgentTask {
  return {
    id: "task-1",
    agent_id: "agent-1",
    runtime_id: "runtime-1",
    issue_id: "issue-1",
    status: "completed",
    priority: 0,
    dispatched_at: null,
    started_at: "2026-09-05T08:00:00Z",
    completed_at: "2026-09-05T08:11:00Z",
    result: null,
    error: null,
    created_at: "2026-09-05T08:00:00Z",
    trigger_summary: "Initial run",
    ...overrides,
  };
}

function usage(overrides: Partial<TaskUsage> = {}): TaskUsage {
  return {
    provider: "anthropic",
    model: "claude-opus-5",
    input_tokens: 10_000,
    output_tokens: 5_000,
    cache_read_tokens: 80_000,
    cache_write_tokens: 5_000,
    ...overrides,
  };
}

beforeEach(() => {
  cleanup();
  vi.clearAllMocks();
});

afterEach(cleanup);

describe("RunCostBreakdown", () => {
  it("renders the four billing segments with tokens", () => {
    renderWithI18n(
      <RunCostBreakdown task={makeTask({ usage: [usage()] })} issueCost={10} />,
    );

    // `getAllByText`: the largest segment's name also appears as the "Biggest
    // block" figure above the legend, so the winning label is legitimately on
    // screen twice.
    for (const label of ["Fresh input", "Cache write", "Cache read", "Output"]) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    expect(screen.getByText("80K")).toBeInTheDocument();
  });

  it("names the biggest block and this run's share of the issue", () => {
    // The two things the view exists to let someone say out loud.
    renderWithI18n(
      <RunCostBreakdown
        task={makeTask({ usage: [usage({ cost_usd_ticks: 25_000_000_000 })] })}
        issueCost={10}
      />,
    );

    expect(screen.getByText("Biggest block")).toBeInTheDocument();
    expect(screen.getByText(/25% of this issue/)).toBeInTheDocument();
    expect(screen.getByText(/80% · 80K tokens/)).toBeInTheDocument();
  });

  it("gives the composition bar a text equivalent", () => {
    // The bar is the only place the proportions are drawn; without a label a
    // screen reader gets four unlabelled boxes.
    renderWithI18n(
      <RunCostBreakdown task={makeTask({ usage: [usage()] })} issueCost={10} />,
    );

    expect(
      screen.getByRole("img", { name: /Fresh input 10%.*Cache read 80%/ }),
    ).toBeInTheDocument();
  });

  it("surfaces the daemon's run facts when they were reported", () => {
    renderWithI18n(
      <RunCostBreakdown
        task={makeTask({
          usage: [usage({ num_turns: 14, resumed: true, prepare_ms: 340 })],
        })}
        issueCost={10}
      />,
    );

    expect(screen.getByText("Turns")).toBeInTheDocument();
    expect(screen.getByText("14")).toBeInTheDocument();
    expect(screen.getByText("Resumed")).toBeInTheDocument();
    expect(screen.getByText("340ms")).toBeInTheDocument();
  });

  it("says so when the run predates run-metadata reporting", () => {
    // Eight em dashes would be a wall of nothing where "no timing for this
    // run" is the whole message.
    renderWithI18n(
      <RunCostBreakdown task={makeTask({ usage: [usage()] })} issueCost={10} />,
    );

    expect(screen.getByText(/reported no turn count or timing/)).toBeInTheDocument();
    expect(screen.queryByText("Turns")).not.toBeInTheDocument();
  });

  it("states that the prompt-segment split is not metered", () => {
    // The view's honesty guard: nothing in the pipeline attributes tokens to
    // the runtime brief / tool schemas / history / tool results, so the view
    // must not imply that it does. Named regression for DENE-670.
    renderWithI18n(
      <RunCostBreakdown task={makeTask({ usage: [usage()] })} issueCost={10} />,
    );

    expect(
      screen.getByText(/Tokens are not attributed to the runtime brief/),
    ).toBeInTheDocument();
  });

  it("renders nothing for a run with no usage recorded", () => {
    const { container } = renderWithI18n(
      <RunCostBreakdown task={makeTask()} issueCost={10} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("does not claim a share when the issue total is zero", () => {
    renderWithI18n(
      <RunCostBreakdown
        task={makeTask({ usage: [usage({ model: "unknown-model", provider: "mystery" })] })}
        issueCost={0}
      />,
    );

    expect(screen.getByText("Share of issue unavailable")).toBeInTheDocument();
    expect(screen.queryByText(/% of this issue/)).not.toBeInTheDocument();
  });
});
