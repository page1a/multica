import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  listAutopilotLinkedChanges: vi.fn(),
  getBaseUrl: () => "http://127.0.0.1:8080",
}));

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { LinkedChangesSection } from "./linked-changes-section";

function renderSection() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <LinkedChangesSection autopilotId="ap-1" />
    </QueryClientProvider>,
  );
}

const change = {
  id: "c1",
  route: "autopilot.update",
  actor_id: "u1",
  actor_name: "Kun",
  via_workspace_id: "ws-2",
  via_workspace_name: "Partner",
  via_slug: "partner",
  agent_id: "a1",
  agent_name: "Builder",
  task_id: "t1",
  created_at: "2026-10-09T08:00:00Z",
};

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe("LinkedChangesSection", () => {
  it("names who changed the autopilot, through which workspace and agent", async () => {
    api.listAutopilotLinkedChanges.mockResolvedValue([change, { ...change, id: "c2", route: "autopilot.something_new" }]);
    renderSection();
    expect(await screen.findByText("Changes through linked workspaces")).toBeInTheDocument();
    expect(screen.getAllByText("Kun via Partner · Builder")).toHaveLength(2);
    expect(screen.getByText("changed settings")).toBeInTheDocument();
    // An unknown route still reads as a change, never as a raw key.
    expect(screen.getByText("changed it")).toBeInTheDocument();
    expect(api.listAutopilotLinkedChanges).toHaveBeenCalledWith("ap-1");
  });

  it("takes no space without changes", async () => {
    api.listAutopilotLinkedChanges.mockResolvedValue([]);
    const { container } = renderSection();
    await waitFor(() => expect(api.listAutopilotLinkedChanges).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });
});
