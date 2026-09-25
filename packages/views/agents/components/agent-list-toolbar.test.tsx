import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen } from "@testing-library/react";
import { EMPTY_AGENT_FILTERS } from "@multica/core/agents/stores";

vi.mock("@multica/core/api", () => ({
  api: { getBaseUrl: () => "http://localhost" },
}));
import type { Squad } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import {
  AgentListToolbar,
  countActiveFilterDimensions,
} from "./agent-list-toolbar";
import type { AgentListRow } from "./agents-page";

function makeRow(
  id: string,
  name: string,
  squadIds: string[] = [],
): AgentListRow {
  return {
    agent: {
      id,
      workspace_id: "ws-1",
      runtime_id: "rt-1",
      name,
      description: "",
      instructions: "",
      avatar_url: null,
      runtime_mode: "local",
      runtime_config: {},
      max_concurrent_tasks: 1,
      owner_id: "user-1",
      archived_at: null,
      custom_args: [],
      visibility: "private",
      permission_mode: "private",
      invocation_targets: [],
      model: "claude",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      status: "idle",
      skills: [],
      archived_by: null,
    },
    runtime: null,
    presence: null,
    activity: null,
    runCount: 0,
    lastActiveDays: null,
    owner: null,
    isOwnedByMe: false,
    canManage: false,
    squadIds,
  };
}

const SQUAD: Squad = {
  id: "sq-alpha",
  workspace_id: "ws-1",
  name: "Alpha Squad",
  description: "",
  instructions: "",
  avatar_url: null,
  leader_id: "leader-1",
  creator_id: "user-1",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  archived_at: null,
  archived_by: null,
  members: [{ member_type: "agent", member_id: "a-1", role: "member" }],
};

describe("countActiveFilterDimensions", () => {
  it("counts squads as its own active dimension", () => {
    expect(countActiveFilterDimensions(EMPTY_AGENT_FILTERS)).toBe(0);
    expect(
      countActiveFilterDimensions({
        ...EMPTY_AGENT_FILTERS,
        squads: ["sq-alpha"],
      }),
    ).toBe(1);
    expect(
      countActiveFilterDimensions({
        ...EMPTY_AGENT_FILTERS,
        access: ["workspace"],
        squads: ["__none__"],
      }),
    ).toBe(2);
  });
});

describe("AgentListToolbar grouping", () => {
  it("lets the display popover switch grouping to squad", () => {
    const onGroupingChange = vi.fn();
    renderWithI18n(
      <AgentListToolbar
        scope="all"
        onScopeChange={vi.fn()}
        scopeCounts={{ mine: 0, all: 1, archived: 0 }}
        search=""
        onSearchChange={vi.fn()}
        filters={EMPTY_AGENT_FILTERS}
        onToggleFilter={vi.fn()}
        onClearFilters={vi.fn()}
        grouping="none"
        onGroupingChange={onGroupingChange}
        sortField="name"
        sortDirection="asc"
        onSortFieldChange={vi.fn()}
        onSortDirectionChange={vi.fn()}
        hiddenColumns={["model", "created"]}
        onToggleColumn={vi.fn()}
        allRows={[makeRow("a-1", "Alpha", ["sq-alpha"])]}
        members={[]}
        squads={[SQUAD]}
        visibleCount={1}
      />,
    );

    fireEvent.click(screen.getByText("Agent"));
    expect(screen.getByText("Group by")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Squad" }));
    expect(onGroupingChange).toHaveBeenCalledWith("squad");
  });
});

describe("AgentListToolbar model facet", () => {
  // Regression (DENE-708): the facet label is display text, the facet VALUE is
  // the stored `agent.model`. Decoding the value too would merge two distinct
  // seat strings into one facet and filter on a string no agent has.
  // Canonical decoding matrix: `provider-seat-model.test.ts`.
  it("labels a provider-preset seat decoded but still filters on the raw string", () => {
    const onToggleFilter = vi.fn();
    const row = makeRow("a-1", "Alpha");
    row.agent.model = "deepseek-official/deepseek-v4%2Fflash";

    renderWithI18n(
      <AgentListToolbar
        scope="all"
        onScopeChange={vi.fn()}
        scopeCounts={{ mine: 0, all: 1, archived: 0 }}
        search=""
        onSearchChange={vi.fn()}
        filters={EMPTY_AGENT_FILTERS}
        onToggleFilter={onToggleFilter}
        onClearFilters={vi.fn()}
        grouping="none"
        onGroupingChange={vi.fn()}
        sortField="name"
        sortDirection="asc"
        onSortFieldChange={vi.fn()}
        onSortDirectionChange={vi.fn()}
        hiddenColumns={[]}
        onToggleColumn={vi.fn()}
        allRows={[row]}
        members={[]}
        squads={[]}
        visibleCount={1}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Filter/ }));
    fireEvent.click(screen.getByText("Model"));

    const option = screen.getByText("deepseek-official \u00b7 deepseek-v4/flash");
    expect(option).toBeInTheDocument();

    fireEvent.click(option);
    expect(onToggleFilter).toHaveBeenCalledWith(
      "models",
      "deepseek-official/deepseek-v4%2Fflash",
    );
  });
});
