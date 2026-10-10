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
      can: { create: false, accept: false, pull: false, manage: false, audit: false },
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
      can: { create: true, accept: true, pull: true, manage: true, audit: true },
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
      can: { create: true, accept: true, pull: true, manage: true, audit: true },
    });
    const user = userEvent.setup();
    renderTab();
    await screen.findByText("Pick the workspace to share with first.");
    await user.click(screen.getByLabelText("Workspace URL to share with"));
    const list = await screen.findByRole("listbox");
    // Floats in a portal: the settings card would clip it otherwise.
    expect(list.closest('[data-slot="card"]')).toBeNull();
    expect(screen.queryByRole("option", { name: /Acme/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: /Side project/ }));
    expect(list).not.toBeInTheDocument();
    expect(screen.getByLabelText("Workspace URL to share with")).toHaveValue("side");
    expect(await screen.findByTestId("workspace-link-target")).toHaveTextContent("Side project");
    expect(api.lookupWorkspaceLinkTarget).not.toHaveBeenCalled();
  });

  it("lists the other workspace's projects and pulls them in when the caller owns it", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, pull: true, manage: true, audit: true },
    });
    api.lookupWorkspaceLinkTarget.mockResolvedValue({
      workspace: ws("Side project", "side"),
      pull: { allowed: true, projects: [{ id: "their-1", title: "Launch", icon: null }] },
    });
    const user = userEvent.setup();
    renderTab();
    expect(await screen.findByText("Pick the workspace to read from first.")).toBeInTheDocument();
    await user.click(screen.getByLabelText("Workspace to read from"));
    await user.click(await screen.findByRole("option", { name: /Side project/ }));
    expect(await screen.findByRole("checkbox", { name: /Launch/ })).toBeInTheDocument();
    expect(api.lookupWorkspaceLinkTarget).toHaveBeenCalledWith("side");
    // Only their projects are offered here, never this workspace's own.
    expect(screen.getAllByRole("checkbox", { name: /Roadmap/ })).toHaveLength(1);
    await user.click(screen.getByRole("checkbox", { name: /Launch/ }));
    await user.click(screen.getByRole("button", { name: "Link in" }));
    await waitFor(() =>
      expect(api.createWorkspaceLink).toHaveBeenCalledWith({
        target_slug: "side",
        project_ids: ["their-1"],
        direction: "pull",
      }),
    );
  });

  it("says to ask the other owner when the caller does not own that workspace", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, pull: true, manage: true, audit: true },
    });
    api.lookupWorkspaceLinkTarget.mockResolvedValue({
      workspace: ws("Partner Co", "partner"),
      pull: { allowed: false, projects: [] },
    });
    const user = userEvent.setup();
    renderTab();
    await screen.findByText("Pick the workspace to read from first.");
    await user.type(screen.getByLabelText("Workspace to read from"), "partner");
    expect(
      await screen.findByText("You don't own that workspace. Ask its owner to offer the link from there."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Link in" })).toBeDisabled();
  });

  it("says when an address names no workspace", async () => {
    api.listWorkspaceLinks.mockResolvedValue({
      links: [],
      can: { create: true, accept: true, pull: true, manage: true, audit: true },
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

  it("lets the source owner switch managed access and tells everyone else why not", async () => {
    const outgoing = {
      ...pendingIncoming,
      id: "l-out",
      side: "source",
      status: "active",
      source: ws("Acme", "acme"),
      target: ws("Partner", "partner"),
      projects: [{ id: "p1", title: "Roadmap", icon: null }],
      managed: false,
      can_set_managed: true,
    };
    const incoming = { ...pendingIncoming, status: "active", managed: true, can_set_managed: false };
    api.listWorkspaceLinks.mockResolvedValue({
      links: [outgoing, incoming],
      can: { create: true, accept: true, pull: true, manage: true, audit: false },
    });
    const user = userEvent.setup();
    renderTab();
    const switches = await screen.findAllByRole("switch", { name: "Let the viewing side manage tasks and autopilots" });
    expect(switches).toHaveLength(2);
    const [mine, theirs] = switches as [HTMLElement, HTMLElement];
    expect(theirs).toHaveAttribute("aria-disabled", "true");
    expect(theirs).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("Only an owner of the sharing workspace can switch this.")).toBeInTheDocument();
    expect(mine).not.toHaveAttribute("aria-disabled", "true");
    await user.click(mine);
    await waitFor(() => expect(api.updateWorkspaceLink).toHaveBeenCalledWith("l-out", { managed: true }));
  });
});
