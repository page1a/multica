import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  listProjects: vi.fn(),
  listMembers: vi.fn(),
  listProjectMembers: vi.fn(),
  previewProjectVisibility: vi.fn(),
  setProjectVisibility: vi.fn(),
}));
const memberState = vi.hoisted(() => ({ role: "admin" as string, userId: "u-admin" }));

vi.mock("@multica/core/api", () => ({ api }));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
}));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ ...memberState, member: null, isLoading: false }),
}));
vi.mock("../../common/share-scope-dialog", () => ({
  ShareScopeDialog: ({ target, initialScope }: { target: { resourceId: string; currentScope?: string }; initialScope?: string }) => (
    <div role="dialog">share dialog {target.resourceId} {target.currentScope} {initialScope ? `start:${initialScope}` : ""}</div>
  ),
}));

import { ProjectSharingTab } from "./project-sharing-tab";

const project = { id: "p1", workspace_id: "ws-1", title: "Roadmap", description: null, icon: null, status: "planned", priority: "none", lead_type: "member", lead_id: "u-creator", created_by: "u-creator", start_date: null, due_date: null, created_at: "2026-01-01", updated_at: "2026-01-01", issue_count: 2, done_count: 0, resource_count: 1, visibility: "private" as const };

function renderTab() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(<QueryClientProvider client={queryClient}><ProjectSharingTab /></QueryClientProvider>);
}

beforeEach(() => {
  vi.clearAllMocks();
  memberState.role = "admin";
  memberState.userId = "u-admin";
  api.listProjects.mockResolvedValue({ projects: [project], total: 1 });
  api.listMembers.mockResolvedValue([
    { user_id: "u-guest", role: "guest", name: "Guest", email: "guest@example.com" },
    { user_id: "u-member", role: "member", name: "Member", email: "member@example.com" },
  ]);
  api.listProjectMembers.mockResolvedValue([{ member_id: "u-member", id: "pm1", project_id: "p1", workspace_id: "ws-1", added_by: null, created_at: "2026-01-01", name: "Member", email: "member@example.com", avatar_url: null }]);
  api.previewProjectVisibility.mockResolvedValue({ project_id: "p1", visibility: "private", affected_count: 7, previously_private_count: 3 });
  api.setProjectVisibility.mockResolvedValue({});
});

describe("ProjectSharingTab", () => {
  it("counts guests from the project's members", async () => {
    renderTab();
    await waitFor(() => expect(screen.getByText("0 guests")).toBeInTheDocument());
    expect(screen.getByText("1 members")).toBeInTheDocument();
  });

  it("uses preview counts and blocks confirmation while preview is unavailable", async () => {
    api.previewProjectVisibility.mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup();
    renderTab();
    const select = await screen.findByRole("combobox", { name: "Change sharing for Roadmap" });
    await user.selectOptions(select, "workspace");
    expect(await screen.findByText(/Loading the preview/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    expect(api.setProjectVisibility).not.toHaveBeenCalled();
  });

  it("shows the exact counts returned by the preview", async () => {
    const user = userEvent.setup();
    renderTab();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Change sharing for Roadmap" }), "workspace");
    expect(await screen.findByText(/update 7 resources, including 3/)).toBeInTheDocument();
  });

  it("lets a project creator edit while guests remain read-only", async () => {
    memberState.role = "member";
    memberState.userId = "u-creator";
    renderTab();
    expect(await screen.findByRole("combobox", { name: "Change sharing for Roadmap" })).not.toBeDisabled();

    cleanup();
    memberState.role = "guest";
    memberState.userId = "u-guest";
    renderTab();
    expect(await screen.findByRole("combobox", { name: "Change sharing for Roadmap" })).toBeDisabled();
  });

  it("opens the people picker when a specific-people project has nobody picked", async () => {
    api.listProjects.mockResolvedValue({ projects: [{ ...project, visibility: "project" }], total: 1 });
    api.listProjectMembers.mockResolvedValue([]);
    const user = userEvent.setup();
    renderTab();
    expect(await screen.findByText(/No one picked yet/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Pick people" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("share dialog p1 project");
  });

  it("opens the people picker straight away when switching to specific people", async () => {
    const user = userEvent.setup();
    renderTab();
    await user.selectOptions(await screen.findByRole("combobox", { name: "Change sharing for Roadmap" }), "project");
    expect(screen.getByRole("dialog")).toHaveTextContent("share dialog p1 private start:project");
    expect(screen.queryByText(/update 7 resources/)).not.toBeInTheDocument();
  });

  it("keeps the people picker reachable once people are picked", async () => {
    api.listProjects.mockResolvedValue({ projects: [{ ...project, visibility: "project" }], total: 1 });
    renderTab();
    expect(await screen.findByRole("button", { name: "Pick people" })).toBeInTheDocument();
    expect(screen.queryByText(/No one picked yet/)).not.toBeInTheDocument();
  });

  it("does not claim a workspace-wide project is missing people", async () => {
    api.listProjects.mockResolvedValue({ projects: [{ ...project, visibility: "workspace" }], total: 1 });
    api.listProjectMembers.mockResolvedValue([]);
    renderTab();
    await screen.findByRole("combobox", { name: "Change sharing for Roadmap" });
    expect(screen.queryByText(/No one picked yet/)).not.toBeInTheDocument();
  });

  it("does not claim a private project is missing people", async () => {
    api.listProjectMembers.mockResolvedValue([]);
    renderTab();
    await screen.findByRole("combobox", { name: "Change sharing for Roadmap" });
    expect(screen.queryByText(/No one picked yet/)).not.toBeInTheDocument();
  });
});
