import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const listDomains = vi.hoisted(() => vi.fn());
const deleteDomain = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return { ...actual, api: { listDomains, deleteDomain } };
});

import { RoutingDomainsSection } from "./routing-domains";

function render(canManage = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={qc}>
      <RoutingDomainsSection wsId="ws-1" canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("RoutingDomainsSection", () => {
  beforeEach(() => {
    listDomains.mockReset().mockResolvedValue({
      domains: [
        { id: "d-sea", name: "出海", position: 1, project_count: 3, agent_count: 10 },
        { id: "d-relay", name: "中转", position: 2, project_count: 0, agent_count: 10 },
        { id: "d-free", name: "学术", position: 3, project_count: 0, agent_count: 0 },
      ],
    });
    deleteDomain.mockReset().mockResolvedValue(undefined);
  });

  it("shows usage and only lets an unused domain go", async () => {
    render();
    await screen.findByText("出海");
    expect(screen.getByText("3 projects · 10 specialisations")).toBeInTheDocument();
    expect(screen.getByText("No projects · 10 specialisations")).toBeInTheDocument();
    expect(screen.getByText("No projects or specialisations")).toBeInTheDocument();

    const deletes = screen.getAllByRole("button", { name: "Delete" });
    expect(deletes.map((b) => (b as HTMLButtonElement).disabled)).toEqual([true, true, false]);
    expect(deletes[0]!.parentElement).toHaveAttribute("title", "Still used by a project or specialisation");

    await userEvent.click(deletes[2]!);
    expect(deleteDomain).toHaveBeenCalledWith("d-free");
  });

  it("offers no edits to a member who cannot manage", async () => {
    render(false);
    await screen.findByText("出海");
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Add domain/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Rename" })).toBeNull();
  });
});
