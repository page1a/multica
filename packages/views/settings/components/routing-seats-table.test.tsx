import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const listAgents = vi.hoisted(() => vi.fn());
const bulkUpdateAgentRouting = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return { ...actual, api: { listAgents, bulkUpdateAgentRouting } };
});

import { RoutingSeatsTable } from "./routing-seats-table";

function agent(id: string, name: string, extra: Partial<Agent> = {}): Agent {
  return { id, name, model: `${id}-model`, ...extra } as Agent;
}

const ROSTER = [
  agent("a-weak", "Krillin", { routing_tier: "weak", routing_usage: "tight" }),
  agent("a-strong", "Goku", { routing_tier: "strong", routing_usage: "ample" }),
  agent("a-off", "Bulma"),
  agent("a-gone", "Raditz", { routing_tier: "strong", archived_at: "2026-01-01" }),
];

function render(canManage = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingSeatsTable wsId="ws-1" canManage={canManage} />
    </QueryClientProvider>,
  );
}

async function rows() {
  await screen.findByText("Goku");
  return screen.getAllByRole("row").slice(1);
}

beforeEach(() => {
  listAgents.mockReset();
  listAgents.mockResolvedValue(ROSTER);
  bulkUpdateAgentRouting.mockReset();
  bulkUpdateAgentRouting.mockImplementation(
    async (body: { agent_ids: string[]; routing_tier?: string; routing_usage?: string }) => ({
      agents: ROSTER.filter((a) => body.agent_ids.includes(a.id)).map((a) => ({
        ...a,
        ...(body.routing_tier !== undefined ? { routing_tier: body.routing_tier } : {}),
        ...(body.routing_usage !== undefined ? { routing_usage: body.routing_usage } : {}),
      })),
    }),
  );
});

describe("RoutingSeatsTable", () => {
  it("lists live seats strongest first, off-ladder last", async () => {
    render();
    const names = (await rows()).map((row) => within(row).getAllByRole("cell")[1]?.textContent);
    expect(names).toEqual(["Goku", "Krillin", "Bulma"]);
    expect(screen.getByRole("combobox", { name: "Goku · Tier" })).toHaveTextContent("Strong");
    expect(screen.getByRole("combobox", { name: "Goku · Usage" })).toHaveTextContent("Ample");
    // Untagged usage reads as the server default.
    expect(screen.getByRole("combobox", { name: "Bulma · Usage" })).toHaveTextContent("Normal");
    expect(screen.getByRole("combobox", { name: "Bulma · Tier" })).toHaveTextContent("Off");
  });

  it("edits one seat in place", async () => {
    const user = userEvent.setup();
    render();
    await rows();
    await user.click(screen.getByRole("combobox", { name: "Krillin · Usage" }));
    await user.click(await screen.findByRole("option", { name: "Ample" }));
    await waitFor(() =>
      expect(bulkUpdateAgentRouting).toHaveBeenCalledWith({
        agent_ids: ["a-weak"],
        routing_usage: "ample",
      }),
    );
  });

  it("applies one change to every selected seat in one write", async () => {
    const user = userEvent.setup();
    render();
    await rows();
    expect(screen.queryByRole("toolbar")).toBeNull();

    await user.click(screen.getByRole("checkbox", { name: "Select Goku" }));
    await user.click(screen.getByRole("checkbox", { name: "Select Bulma" }));
    const bar = screen.getByRole("toolbar");
    expect(bar).toHaveTextContent("2 selected");

    await user.click(within(bar).getByRole("combobox", { name: "Tier…" }));
    await user.click(await screen.findByRole("option", { name: "Medium" }));
    await waitFor(() => expect(bulkUpdateAgentRouting).toHaveBeenCalledTimes(1));
    expect(bulkUpdateAgentRouting).toHaveBeenCalledWith({
      agent_ids: ["a-strong", "a-off"],
      routing_tier: "medium",
    });

    // Off the ladder travels as an empty tier.
    await user.click(within(screen.getByRole("toolbar")).getByRole("combobox", { name: "Tier…" }));
    await user.click(await screen.findByRole("option", { name: "Off" }));
    await waitFor(() => expect(bulkUpdateAgentRouting).toHaveBeenCalledTimes(2));
    expect(bulkUpdateAgentRouting).toHaveBeenLastCalledWith({
      agent_ids: ["a-strong", "a-off"],
      routing_tier: "",
    });

    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("toolbar")).toBeNull();
  });

  it("selects every seat from the header", async () => {
    const user = userEvent.setup();
    render();
    await rows();
    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    expect(screen.getByRole("toolbar")).toHaveTextContent("3 selected");
  });

  it("says so when a write is refused", async () => {
    const user = userEvent.setup();
    bulkUpdateAgentRouting.mockRejectedValue(new Error("404"));
    render();
    await rows();
    await user.click(screen.getByRole("combobox", { name: "Goku · Usage" }));
    await user.click(await screen.findByRole("option", { name: "Tight" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Not saved");
  });

  it("is read-only for members", async () => {
    render(false);
    await rows();
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    expect(screen.getByRole("combobox", { name: "Goku · Tier" })).toBeDisabled();
  });

  it("nests a specialisation under its base role", async () => {
    listAgents.mockResolvedValue([
      ...ROSTER,
      agent("a-gohan", "Gohan", {
        parent_agent_id: "a-strong",
        parent_agent_name: "Goku",
        runtime_inherited: false,
        routing_tier: "weak",
        routing_usage: "normal",
      }),
      agent("a-orphan", "Pan", {
        parent_agent_id: "missing-parent",
        routing_tier: "medium",
      }),
    ]);
    render();
    const names = (await rows()).map((row) => within(row).getAllByRole("cell")[1]?.textContent);
    // Gohan sits under Goku even though Gohan's own rung is weak.
    // Pan's base role is not in the list, so she stays a root on her rung.
    expect(names).toEqual(["Goku", "Gohan", "Pan", "Krillin", "Bulma"]);
    const gohan = screen.getByText("Gohan").closest("tr");
    expect(gohan).toHaveAttribute("data-seat-depth", "1");
    expect(screen.getByText("Goku").closest("tr")).toHaveAttribute("data-seat-depth", "0");
  });

  it("keeps a follower's tier and usage read-only", async () => {
    const user = userEvent.setup();
    listAgents.mockResolvedValue([
      agent("a-goku", "Goku", { routing_tier: "strong", routing_usage: "tight" }),
      agent("a-gohan", "Gohan", {
        parent_agent_id: "a-goku",
        parent_agent_name: "Goku",
        runtime_inherited: true,
        routing_tier: "strong",
        routing_usage: "tight",
      }),
      agent("a-own", "Trunks", {
        parent_agent_id: "a-goku",
        parent_agent_name: "Goku",
        runtime_inherited: false,
        routing_tier: "weak",
        routing_usage: "ample",
      }),
    ]);
    render();
    await screen.findByText("Gohan");
    expect(screen.getByRole("combobox", { name: "Gohan · Tier" })).toBeDisabled();
    expect(screen.getByRole("combobox", { name: "Gohan · Usage" })).toBeDisabled();
    expect(screen.getByText("Follows Goku")).toBeInTheDocument();
    // This checkbox is a Base UI span: disabled shows up as aria-disabled,
    // which is what a screen reader and the pointer both honor.
    expect(screen.getByRole("checkbox", { name: "Select Gohan" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );

    await user.click(screen.getByRole("checkbox", { name: "Select all" }));
    expect(screen.getByRole("toolbar")).toHaveTextContent("2 selected");

    expect(screen.getByRole("combobox", { name: "Trunks · Tier" })).toBeEnabled();
    await user.click(screen.getByRole("combobox", { name: "Trunks · Usage" }));
    await user.click(await screen.findByRole("option", { name: "Normal" }));
    await waitFor(() =>
      expect(bulkUpdateAgentRouting).toHaveBeenCalledWith({
        agent_ids: ["a-own"],
        routing_usage: "normal",
      }),
    );
  });
});
