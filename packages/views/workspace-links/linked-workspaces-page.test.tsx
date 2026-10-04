import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../test/i18n";

const api = vi.hoisted(() => ({
  listWorkspaceLinks: vi.fn(),
  getLinkedView: vi.fn(),
  getBaseUrl: () => "http://127.0.0.1:8080",
}));

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { LinkedWorkspacesPage } from "./linked-workspaces-page";

const activeLink = {
  id: "l-1",
  side: "viewer",
  status: "active",
  source: { name: "Partner", slug: "partner", avatar_url: null },
  target: { name: "Acme", slug: "acme", avatar_url: null },
  projects: [],
  created_at: "2026-10-01T00:00:00Z",
  accepted_at: "2026-10-02T00:00:00Z",
};

function renderPage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <LinkedWorkspacesPage />
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe("LinkedWorkspacesPage", () => {
  it("shows the shared projects and tasks without links into them", async () => {
    api.listWorkspaceLinks.mockResolvedValue({ links: [activeLink], can: {} });
    api.getLinkedView.mockResolvedValue({
      link_id: "l-1",
      source: { name: "Partner", avatar_url: null },
      projects: [{ id: "p1", title: "Roadmap", icon: null, status: "in_progress", done: 1, total: 4 }],
      issues: [
        {
          identifier: "PT-7",
          title: "Ship the beta",
          status: "in_progress",
          priority: "high",
          project_id: "p1",
          assignee_name: "Lin",
          assignee_avatar_url: null,
          due_date: null,
          updated_at: "2026-10-02T00:00:00Z",
        },
      ],
      next_cursor: null,
      statuses: [{ key: "in_progress", name: "Doing", category: "started" }],
    });
    renderPage();
    expect(await screen.findByText("Ship the beta")).toBeInTheDocument();
    expect(screen.getByText("Read-only view of Partner")).toBeInTheDocument();
    expect(screen.getByText("1 / 4 done")).toBeInTheDocument();
    expect(screen.getByText("Doing")).toBeInTheDocument();
    // Read-only: nothing on the panel navigates into the source workspace.
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("says the link is gone when the server refuses it", async () => {
    api.listWorkspaceLinks.mockResolvedValue({ links: [activeLink], can: {} });
    api.getLinkedView.mockRejectedValue(new Error("link not found"));
    renderPage();
    expect(await screen.findByText("This link is no longer visible")).toBeInTheDocument();
  });

  it("shows the empty state without an accepted link", async () => {
    api.listWorkspaceLinks.mockResolvedValue({ links: [{ ...activeLink, status: "pending" }], can: {} });
    renderPage();
    expect(await screen.findByText("Nothing linked yet")).toBeInTheDocument();
    expect(api.getLinkedView).not.toHaveBeenCalled();
  });
});
