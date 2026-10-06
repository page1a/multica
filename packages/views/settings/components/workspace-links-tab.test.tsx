import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const { api, ApiError } = vi.hoisted(() => {
  class ApiError extends Error {
    constructor(message: string, readonly status: number) {
      super(message);
    }
  }
  return {
    ApiError,
    api: {
      listWorkspaceLinks: vi.fn(),
      listWorkspaces: vi.fn(),
      lookupWorkspaceLinkTarget: vi.fn(),
      listWorkspaceLinkAudit: vi.fn(),
      listProjects: vi.fn(),
      createWorkspaceLink: vi.fn(),
      updateWorkspaceLink: vi.fn(),
      revokeWorkspaceLink: vi.fn(),
      getBaseUrl: () => "http://127.0.0.1:8080",
    },
  };
});

vi.mock("@multica/core/api", () => ({ api, ApiError }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { WorkspaceLinksTab } from "./workspace-links-tab";

const ws = (name: string, slug: string) => ({ name, slug, avatar_url: null });
const pendingIncoming = {
  id: "l-in",
  side: "viewer",
  status: "pending",
  source: ws("Partner", "partner"),
  target: ws("Acme", "acme"),
  projects: [],
  created_at: "2026-10-01T00:00:00Z",
  accepted_at: null,
};
const project = { id: "p1", title: "Roadmap", icon: null, visibility: "workspace", status: "planned" };

function renderTab() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <WorkspaceLinksTab />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  api.listProjects.mockResolvedValue({ projects: [project], total: 1 });
  api.listWorkspaceLinkAudit.mockResolvedValue([]);
  api.createWorkspaceLink.mockResolvedValue({});
  api.updateWorkspaceLink.mockResolvedValue({});
  api.listWorkspaces.mockResolvedValue([
    { id: "ws-1", name: "Acme", slug: "acme", avatar_url: null },
    { id: "ws-2", name: "Side project", slug: "side", avatar_url: null },
  ]);
});
afterEach(cleanup);

describe("WorkspaceLinksTab", () => {
  it("disables sharing and accepting with the reason when the server says no", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [pendingIncoming],
      can: { create: false, accept: false, manage: false, audit: false },
    });
    renderTab();
    expect(await screen.findByText("Only the owner can share this workspace's data.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Offer link" })).toBeDisabled();
    expect(await screen.findByRole("button", { name: "Accept" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Accept" })).toHaveAttribute(
      "title",
      "Only an owner or admin can accept or disconnect a link.",
    );
    expect(api.listWorkspaceLinkAudit).not.toHaveBeenCalled();
  });

  it("reads a pasted workspace link, names the workspace and offers the link", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, manage: true, audit: true },
    });
    api.lookupWorkspaceLinkTarget.mockResolvedValue({ workspace: ws("Partner Co", "partner") });
    const user = userEvent.setup();
    renderTab();
    expect(await screen.findByText("Pick the workspace to share with first.")).toBeInTheDocument();
    const pasted = "https://ai.ferryway.cc/partner";
    await user.click(screen.getByLabelText("Workspace URL to share with"));
    await user.paste(pasted);
    expect(await screen.findByText("Partner Co")).toBeInTheDocument();
    expect(api.lookupWorkspaceLinkTarget).toHaveBeenCalledWith(pasted);
    expect(screen.getByText("Tick at least one project.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Offer link" })).toBeDisabled();
    await user.click(await screen.findByRole("checkbox", { name: /Roadmap/ }));
    await user.click(screen.getByRole("button", { name: "Offer link" }));
    await waitFor(() =>
      expect(api.createWorkspaceLink).toHaveBeenCalledWith({ target_slug: "partner", project_ids: ["p1"] }),
    );
  });

  it("lists the caller's other workspaces to pick from", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, manage: true, audit: true },
    });
    const user = userEvent.setup();
    renderTab();
    await screen.findByText("Pick the workspace to share with first.");
    await user.click(screen.getByLabelText("Workspace URL to share with"));
    const list = await screen.findByRole("listbox");
    expect(screen.queryByRole("option", { name: /Acme/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: /Side project/ }));
    expect(list).not.toBeInTheDocument();
    expect(screen.getByLabelText("Workspace URL to share with")).toHaveValue("side");
    expect(await screen.findByTestId("workspace-link-target")).toHaveTextContent("Side project");
    expect(api.lookupWorkspaceLinkTarget).not.toHaveBeenCalled();
  });

  it("says when an address names no workspace", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, manage: true, audit: true },
    });
    api.lookupWorkspaceLinkTarget.mockRejectedValue(new ApiError("no workspace at that address", 404));
    const user = userEvent.setup();
    renderTab();
    await screen.findByText("Pick the workspace to share with first.");
    await user.type(screen.getByLabelText("Workspace URL to share with"), "nobody");
    expect(await screen.findByText("No workspace at this address.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Offer link" })).toBeDisabled();
  });

  it("accepts an incoming link", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [pendingIncoming],
      can: { create: false, accept: true, manage: true, audit: false },
    });
    const user = userEvent.setup();
    renderTab();
    await user.click(await screen.findByRole("button", { name: "Accept" }));
    await waitFor(() => expect(api.updateWorkspaceLink).toHaveBeenCalledWith("l-in", { accept: true }));
  });
});
