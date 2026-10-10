import { render, renderHook, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { InboxItem } from "@multica/core/types";
import en from "../../locales/en/inbox.json";
import enWorkspace from "../../locales/en/workspace.json";
import {
  WorkspaceLinkNotice,
  useWorkspaceLinkNoticeTitle,
  workspaceLinkNoticeHref,
} from "./workspace-link-notice";

const links = vi.hoisted(() => ({
  data: undefined as unknown,
  accept: vi.fn(),
  decline: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: links.data }),
}));

vi.mock("@multica/core/workspace-links", () => ({
  workspaceLinksOptions: (wsId: string) => ({ queryKey: ["links", wsId] }),
  useAcceptWorkspaceLink: () => ({ mutate: links.accept, isPending: false }),
  useRevokeWorkspaceLink: () => ({ mutate: links.decline, isPending: false }),
}));

vi.mock("../../i18n", () => ({
  useT: (ns: string) => ({
    t: (accessor: (dict: unknown) => string, params?: Record<string, string>) =>
      accessor(ns === "workspace" ? enWorkspace : en).replace(/\{\{(\w+)\}\}/g, (_, key: string) => params?.[key] ?? ""),
  }),
}));

vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, className }: { href: string; children: React.ReactNode; className?: string }) => (
    <a href={href} className={className}>{children}</a>
  ),
}));

function item(overrides: Partial<InboxItem> = {}): InboxItem {
  return {
    id: "inbox-1",
    workspace_id: "viewer-ws",
    recipient_type: "member",
    recipient_id: "member-1",
    actor_type: "system",
    actor_id: null,
    type: "workspace_link_request",
    severity: "action_required",
    issue_id: null,
    title: "Acme wants to share projects with Beta",
    body: "Roadmap, Design",
    issue_status: null,
    read: false,
    archived: false,
    created_at: "2026-10-08T08:00:00Z",
    details: {
      link_id: "link-1",
      source_name: "Acme",
      source_slug: "acme",
      target_name: "Beta",
      target_slug: "beta",
      project_titles: "Roadmap, Design",
    },
    ...overrides,
  };
}

describe("workspace link notices", () => {
  it("sends a request to the receiving workspace's incoming links", () => {
    expect(workspaceLinkNoticeHref(item())).toBe("/beta/settings?tab=workspace-links&section=incoming");
    expect(workspaceLinkNoticeHref(item({ type: "workspace_link_accepted" }))).toBe(
      "/acme/settings?tab=workspace-links&section=outgoing",
    );
  });

  it("titles each notice from its workspace names", () => {
    const { result } = renderHook(() => useWorkspaceLinkNoticeTitle());
    expect(result.current(item())).toBe("Acme wants to share projects with you");
    expect(result.current(item({ type: "workspace_link_declined" }))).toBe("Beta declined your link");
    expect(result.current(item({ type: "new_comment" }))).toBeNull();
  });

  it("shows the projects and the way to answer, and drops it once answered", () => {
    const { rerender } = render(<WorkspaceLinkNotice item={item()} />);
    expect(screen.getByText("Projects: Roadmap, Design")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Review in Linked workspaces" })).toHaveAttribute(
      "href",
      "/beta/settings?tab=workspace-links&section=incoming",
    );
    rerender(<WorkspaceLinkNotice item={item({ archived: true })} />);
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("answers a still-pending offer in place when the reader may accept", () => {
    links.data = {
      can: { accept: true },
      links: [{ id: "link-1", status: "pending" }],
    };
    render(<WorkspaceLinkNotice item={item()} />);
    screen.getByRole("button", { name: "Accept" }).click();
    expect(links.accept).toHaveBeenCalledWith("link-1", expect.anything());
    screen.getByRole("button", { name: "Decline" }).click();
    expect(links.decline).toHaveBeenCalledWith("link-1", expect.anything());

    links.data = { can: { accept: false }, links: [{ id: "link-1", status: "pending" }] };
    render(<WorkspaceLinkNotice item={item({ id: "inbox-2" })} />);
    expect(screen.getAllByRole("button", { name: "Accept" })).toHaveLength(1);
    links.data = undefined;
  });
});
