import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { buildIssueStatusCatalog } from "@multica/core/issue-statuses/queries";

vi.mock("@multica/core/issue-statuses/hooks", () => ({
  useIssueStatuses: () => buildIssueStatusCatalog([]),
}));
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@multica/core/api";
import { renderWithI18n } from "../test/i18n";
import { AppSidebar } from "./app-sidebar";

const { appForeground, chatDetail, chatSessions, chatStore, createPin, detail, deletePin, invitationApi, modules, navigation, pins, sidebarState, summary, workspaces } = vi.hoisted(() => ({
  appForeground: { current: true },
  sidebarState: { setOpenMobile: vi.fn() },
  chatSessions: { current: [] as { id?: string; title?: string | null; unread_count?: number }[] },
  createPin: vi.fn(),
  chatDetail: { current: { isPending: false, isError: false, data: null as unknown, error: null as unknown } },
  chatStore: { current: { activeSessionId: null as string | null, isOpen: false } },
  detail: { current: { isPending: false, isError: false, data: null as unknown, error: null as unknown } },
  deletePin: vi.fn(),
  // Captures the sidebar's invitation accept/decline mutations so the
  // self-heal wiring (error → invalidate the pending list) is observable.
  invitationApi: {
    accept: vi.fn(),
    decline: vi.fn(),
    invalidateQueries: vi.fn(),
    mutations: [] as Array<Record<string, unknown>>,
  },
  navigation: { current: { pathname: "/acme/issues" } },
  summary: { current: [] as { workspace_id: string; count: number }[] },
  workspaces: {
    current: [] as { id: string; name: string; slug: string; avatar_url: string | null }[],
  },
  pins: {
    current: [
      {
        id: "pin-1",
        workspace_id: "ws-1",
        user_id: "user-1",
        item_type: "issue" as "issue" | "chat",
        item_id: "issue-1",
        position: 0,
        created_at: "2026-05-06T00:00:00Z",
      },
    ],
  },
  modules: {
    current: undefined as
      | { key: string; visibility: string; project_id: string | null; allowed: boolean }[]
      | undefined,
  },
}));

vi.mock("@dnd-kit/core", () => ({
  DndContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  PointerSensor: vi.fn(),
  closestCenter: vi.fn(),
  useSensor: vi.fn(),
  useSensors: vi.fn(),
}));
vi.mock("@dnd-kit/sortable", () => ({
  SortableContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  useSortable: () => ({ attributes: {}, listeners: {}, setNodeRef: vi.fn() }),
  verticalListSortingStrategy: vi.fn(),
}));
vi.mock("@dnd-kit/utilities", () => ({ CSS: { Transform: { toString: () => undefined } } }));
vi.mock("@multica/ui/components/ui/sidebar", () => ({
  Sidebar: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarFooter: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarGroup: ({ children, onDragOver, onDrop }: { children: React.ReactNode; onDragOver?: React.DragEventHandler; onDrop?: React.DragEventHandler }) => (
    <div data-testid="sidebar-group" onDragOver={onDragOver} onDrop={onDrop}>{children}</div>
  ),
  SidebarGroupContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarGroupLabel: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarHeader: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarMenuButton: ({
    children,
    isActive,
    render,
    ...props
  }: {
    children: React.ReactNode;
    isActive?: boolean;
    render?: React.ReactElement<{ href?: string }>;
  } & React.ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props} type="button" data-active={isActive ? "true" : undefined} data-href={render?.props.href}>
      {children}
    </button>
  ),
  SidebarMenuItem: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  SidebarRail: () => null,
  useSidebar: () => ({ setOpenMobile: sidebarState.setOpenMobile }),
}));
vi.mock("@multica/ui/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuGroup: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuItem: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuLabel: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  DropdownMenuSeparator: () => null,
  DropdownMenuTrigger: ({ render }: { render: React.ReactNode }) => <>{render}</>,
}));
vi.mock("@multica/ui/components/ui/collapsible", () => ({
  Collapsible: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  CollapsibleContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  CollapsibleTrigger: () => <button type="button" />,
}));
vi.mock("@multica/ui/components/ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <button type="button">{children}</button>,
}));
vi.mock("../common/use-app-foreground", () => ({
  useAppForeground: () => appForeground.current,
}));
vi.mock("./help-launcher", () => ({ HelpLauncher: () => null }));
vi.mock("../auth", () => ({ useLogout: () => vi.fn() }));
vi.mock("../issues/components/status-icon", () => ({ StatusIcon: () => <span /> }));
vi.mock("../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a>,
  useNavigation: () => ({ pathname: navigation.current.pathname, push: vi.fn() }),
}));
vi.mock("../projects/components/project-icon", () => ({ ProjectIcon: () => <span /> }));
vi.mock("../workspace/workspace-avatar", () => ({ WorkspaceAvatar: () => <span /> }));
vi.mock("@multica/ui/components/common/actor-avatar", () => ({ ActorAvatar: () => <span /> }));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "user-1" } }),
}));
// Callable-store shape (selectorFn + getState) per the repo testing rules.
vi.mock("@multica/core/chat", () => ({
  useChatStore: Object.assign(
    (selector: (state: { activeSessionId: string | null; isOpen: boolean }) => unknown) =>
      selector(chatStore.current),
    { getState: () => chatStore.current },
  ),
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  // Spread the real module so pure helpers (resolveRouteIconName, used by the
  // nav to derive each item's icon from its href) stay intact; only the
  // workspace/context hooks below are stubbed to control routes in tests.
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  paths: { workspace: (slug: string) => ({ issues: () => `/${slug}/issues` }) },
  useCurrentWorkspace: () => ({ id: "ws-1", name: "Acme", slug: "acme" }),
  useWorkspacePaths: () => ({
    inbox: () => "/acme/inbox",
    chat: () => "/acme/chat",
    chatSession: (id: string) => `/acme/chat/${id}`,
    myIssues: () => "/acme/my-issues",
    issues: () => "/acme/issues",
    projects: () => "/acme/projects",
    autopilots: () => "/acme/autopilots",
    agents: () => "/acme/agents",
    squads: () => "/acme/squads",
    usage: () => "/acme/usage",
    runtimes: () => "/acme/runtimes",
    skills: () => "/acme/skills",
    settings: () => "/acme/settings",
    issueDetail: (id: string) => `/acme/issues/${id}`,
    projectDetail: (id: string) => `/acme/projects/${id}`,
  }),
}));
vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getBaseUrl: () => "http://127.0.0.1:8080",
      acceptInvitation: invitationApi.accept,
      declineInvitation: invitationApi.decline,
    },
  };
});
vi.mock("@multica/core/inbox/queries", () => ({
  inboxUnreadSummaryOptions: () => ({ queryKey: ["inbox", "unread-summary"] }),
  // The nav badge and the switcher dot read the SAME cross-workspace summary,
  // so the fixture that drives one drives the other.
  useInboxUnreadCount: (currentWsId: string | null) =>
    summary.current.find((s) => s.workspace_id === currentWsId)?.count ?? 0,
  hasOtherWorkspaceUnread: (
    entries: { workspace_id: string; count: number }[],
    currentWsId: string | null,
  ) => entries.some((s) => s.workspace_id !== currentWsId && s.count > 0),
  unreadWorkspaceIds: (entries: { workspace_id: string; count: number }[]) =>
    new Set(entries.filter((s) => s.count > 0).map((s) => s.workspace_id)),
}));
vi.mock("@multica/core/issues/queries", () => ({ issueDetailOptions: () => ({ queryKey: ["issue"] }) }));
vi.mock("@multica/core/issues/stores/create-mode-store", () => ({
  useCreateModeStore: { getState: () => ({ lastMode: "agent" }) },
  openCreateIssueWithPreference: vi.fn(),
}));
vi.mock("@multica/core/issues/stores/draft-store", () => ({ useIssueDraftStore: () => false }));
vi.mock("@multica/core/modals", () => ({ useModalStore: { getState: () => ({ modal: null, open: vi.fn() }) } }));
vi.mock("@multica/core/pins/mutations", () => ({
  CHAT_PIN_DRAG_TYPE: "application/x-multica-chat-session",
  useCreatePin: () => ({ mutate: createPin }),
  useDeletePin: () => ({ mutate: deletePin }),
  useReorderPins: () => ({ mutate: vi.fn() }),
}));
vi.mock("@multica/core/chat/queries", () => ({
  chatSessionsOptions: (wsId: string) => ({ queryKey: ["chat", wsId, "sessions"] }),
  chatSessionOptions: (wsId: string, id: string) => ({ queryKey: ["chat", wsId, "session", id] }),
}));
vi.mock("@multica/core/pins/queries", () => ({ pinListOptions: () => ({ queryKey: ["pins"] }) }));
vi.mock("@multica/core/projects/queries", () => ({ projectDetailOptions: () => ({ queryKey: ["project"] }) }));
vi.mock("@multica/core/workspace/queries", () => ({
  myInvitationListOptions: () => ({ queryKey: ["invitations"] }),
  workspaceKeys: { myInvitations: () => ["invitations"] },
  workspaceListOptions: () => ({ queryKey: ["workspaces"] }),
  moduleVisibilityOptions: (wsId: string) => ({ queryKey: ["workspaces", wsId, "modules"] }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useMutation: (options: Record<string, unknown> = {}) => {
    invitationApi.mutations.push(options);
    return { isPending: false, mutate: vi.fn() };
  },
  useQuery: ({ queryKey }: { queryKey: readonly unknown[] }) => {
    if (queryKey[0] === "pins") return { data: pins.current };
    if (queryKey[0] === "issue") return detail.current;
    if (queryKey[0] === "inbox" && queryKey[1] === "unread-summary") return { data: summary.current };
    if (queryKey[0] === "workspaces" && queryKey[2] === "modules") return { data: modules.current };
    if (queryKey[0] === "workspaces") return { data: workspaces.current };
    if (queryKey[0] === "chat" && queryKey[2] === "sessions") return { data: chatSessions.current, isSuccess: true };
    if (queryKey[0] === "chat" && queryKey[2] === "session") return chatDetail.current;
    return { data: [] };
  },
  useQueryClient: () => ({ fetchQuery: vi.fn(), invalidateQueries: invitationApi.invalidateQueries }),
}));

describe("PinRow", () => {
  beforeEach(() => {
    deletePin.mockReset();
    navigation.current.pathname = "/acme/issues";
    detail.current = { isPending: false, isError: false, data: null, error: null };
    summary.current = [];
    workspaces.current = [];
    modules.current = undefined;
  });

  it("unpins missing details", async () => {
    detail.current = { isPending: false, isError: true, data: null, error: new ApiError("missing", 404, "Not Found") };
    render(<AppSidebar />);
    await waitFor(() => expect(deletePin).toHaveBeenCalledTimes(1));
  });

  it("ignores non-404 errors", async () => {
    detail.current = { isPending: false, isError: true, data: null, error: new ApiError("error", 500, "Server Error") };
    render(<AppSidebar />);
    await waitFor(() => expect(deletePin).not.toHaveBeenCalled());
  });

  it("renders loaded details", async () => {
    detail.current = { isPending: false, isError: false, data: { identifier: "MUL-123", title: "Keep this pin", status: "todo" }, error: null };
    render(<AppSidebar />);
    expect(await screen.findByText("Keep this pin")).toBeInTheDocument();
    expect(screen.queryByText("MUL-123 Keep this pin")).not.toBeInTheDocument();
  });

  it("does not also highlight the parent workspace nav for an active pin", async () => {
    navigation.current.pathname = "/acme/issues/issue-1";
    detail.current = {
      isPending: false,
      isError: false,
      data: { identifier: "MUL-123", title: "Keep this pin", status: "todo" },
      error: null,
    };

    const { container } = render(<AppSidebar />);

    expect((await screen.findByText("Keep this pin")).closest("button")).toHaveAttribute(
      "data-active",
      "true",
    );
    expect(container.querySelector('button[data-href="/acme/issues"]')).not.toHaveAttribute("data-active");
  });

  it("keeps the parent route active until a hidden pin is expanded", () => {
    const originalPins = pins.current;
    pins.current = Array.from({ length: 6 }, (_, index) => ({
      ...originalPins[0]!,
      id: `pin-${index + 1}`,
      item_id: `issue-${index + 1}`,
      position: index,
    }));
    navigation.current.pathname = "/acme/issues/issue-6";
    detail.current = {
      isPending: false,
      isError: false,
      data: { identifier: "MUL-123", title: "Pinned issue", status: "todo" },
      error: null,
    };

    try {
      const { container } = renderWithI18n(<AppSidebar />);
      const parent = () => container.querySelector('button[data-href="/acme/issues"]');
      const lastPin = () => container.querySelector('button[data-href="/acme/issues/issue-6"]');

      expect(lastPin()).not.toBeInTheDocument();
      expect(parent()).toHaveAttribute("data-active", "true");

      fireEvent.click(screen.getByRole("button", { name: "Show 1 more…" }));
      expect(lastPin()).toHaveAttribute("data-active", "true");
      expect(parent()).not.toHaveAttribute("data-active");

      fireEvent.click(screen.getByRole("button", { name: "Show fewer" }));
      expect(lastPin()).not.toBeInTheDocument();
      expect(parent()).toHaveAttribute("data-active", "true");
    } finally {
      pins.current = originalPins;
    }
  });
});

// On a phone the sidebar is a Sheet laid over the page, so a nav tap that
// leaves it open renders the destination underneath and reads as a dead tap.
describe("mobile sheet dismissal", () => {
  beforeEach(() => {
    sidebarState.setOpenMobile.mockClear();
    navigation.current = { pathname: "/acme/issues" };
  });

  it("dismisses the sheet once the route changes", () => {
    const { rerender } = render(<AppSidebar />);
    sidebarState.setOpenMobile.mockClear();

    navigation.current = { pathname: "/acme/inbox" };
    rerender(<AppSidebar />);

    expect(sidebarState.setOpenMobile).toHaveBeenCalledWith(false);
  });

  // Closing on `pathname` rather than per-link keeps every route out of the
  // sidebar covered at once — nav groups, pins, and the switcher's own push.
  it("does not re-dismiss while the route holds still", () => {
    const { rerender } = render(<AppSidebar />);
    sidebarState.setOpenMobile.mockClear();

    rerender(<AppSidebar />);

    expect(sidebarState.setOpenMobile).not.toHaveBeenCalled();
  });
});

describe("module-level nav visibility", () => {
  beforeEach(() => {
    modules.current = [
      { key: "issues", visibility: "private", project_id: null, allowed: false },
      { key: "projects", visibility: "workspace", project_id: null, allowed: true },
      { key: "repos", visibility: "workspace", project_id: null, allowed: true },
      { key: "runtimes", visibility: "private", project_id: null, allowed: false },
    ];
  });
  afterEach(() => {
    modules.current = undefined;
  });

  it("hides Issues and Runtimes but keeps Agents and Squads", () => {
    const { container } = renderWithI18n(<AppSidebar />);
    expect(container.querySelector('button[data-href="/acme/issues"]')).not.toBeInTheDocument();
    expect(container.querySelector('button[data-href="/acme/my-issues"]')).not.toBeInTheDocument();
    expect(container.querySelector('button[data-href="/acme/runtimes"]')).not.toBeInTheDocument();
    expect(container.querySelector('button[data-href="/acme/agents"]')).toBeInTheDocument();
    expect(container.querySelector('button[data-href="/acme/squads"]')).toBeInTheDocument();
    expect(container.querySelector('button[data-href="/acme/projects"]')).toBeInTheDocument();
  });
});

describe("workspace-switcher unread dot", () => {
  beforeEach(() => {
    summary.current = [];
    workspaces.current = [];
  });

  // The aggregate switcher dot is the only `.ring-sidebar` span in the tree
  // (DraftDot is null when there's no draft, and there are no invitations).
  const dot = (container: HTMLElement) => container.querySelector("span.bg-brand.ring-sidebar");

  it("shows a dot when another workspace has unread inbox items", () => {
    summary.current = [{ workspace_id: "ws-2", count: 3 }];
    const { container } = render(<AppSidebar />);
    expect(dot(container)).not.toBeNull();
  });

  it("does not show a dot when only the active workspace has unread", () => {
    // Active workspace is ws-1 (see useCurrentWorkspace mock).
    summary.current = [{ workspace_id: "ws-1", count: 3 }];
    const { container } = render(<AppSidebar />);
    expect(dot(container)).toBeNull();
  });

  it("does not show a dot when no workspace has unread", () => {
    summary.current = [];
    const { container } = render(<AppSidebar />);
    expect(dot(container)).toBeNull();
  });
});

describe("workspace-switcher dropdown per-workspace dot", () => {
  beforeEach(() => {
    summary.current = [];
    // Active workspace is ws-1 (see useCurrentWorkspace mock); "Other" is ws-2.
    workspaces.current = [
      { id: "ws-1", name: "Active WS", slug: "active", avatar_url: null },
      { id: "ws-2", name: "Other WS", slug: "other", avatar_url: null },
    ];
  });

  // Row dots are brand dots WITHOUT the aggregate avatar dot's `ring-sidebar`.
  const rowDots = (container: HTMLElement) =>
    container.querySelectorAll("span.bg-brand:not(.ring-sidebar)");

  it("dots the specific other workspace that has unread", () => {
    summary.current = [{ workspace_id: "ws-2", count: 3 }];
    const { container } = render(<AppSidebar />);
    // Exactly one row dot, sitting right after the "Other WS" name; the active
    // row shows the check, not a dot.
    expect(rowDots(container)).toHaveLength(1);
    expect(screen.getByText("Other WS").nextElementSibling?.className).toContain("bg-brand");
    expect(screen.getByText("Active WS").nextElementSibling?.className ?? "").not.toContain("bg-brand");
  });

  it("does not dot a workspace whose unread count is zero", () => {
    summary.current = [{ workspace_id: "ws-2", count: 0 }];
    const { container } = render(<AppSidebar />);
    expect(rowDots(container)).toHaveLength(0);
  });

  it("never dots the active workspace even when it has unread", () => {
    summary.current = [{ workspace_id: "ws-1", count: 5 }];
    const { container } = render(<AppSidebar />);
    expect(rowDots(container)).toHaveLength(0);
  });
});

describe("navigation item presentation", () => {
  it("keeps Analytics and Settings styled like the other nav items", () => {
    const { container } = render(<AppSidebar />);
    const referenceClassName = container.querySelector(
      'button[data-href="/acme/issues"]',
    )?.className;

    expect(referenceClassName).toBeTruthy();

    for (const href of ["/acme/usage", "/acme/settings"]) {
      expect(container.querySelector(`button[data-href="${href}"]`)?.className).toBe(
        referenceClassName,
      );
    }
  });
});

describe("personal nav — Chat", () => {
  beforeEach(() => {
    chatSessions.current = [];
    summary.current = [];
    navigation.current = { pathname: "/acme/issues" };
    chatStore.current = { activeSessionId: null, isOpen: false };
    appForeground.current = true;
  });

  // The mocked SidebarMenuButton exposes the AppLink target as `data-href`
  // and renders the label + badge as its children.
  const chatNav = (container: HTMLElement) =>
    container.querySelector<HTMLElement>('button[data-href="/acme/chat"]');
  const chatBadge = (container: HTMLElement) =>
    chatNav(container)?.querySelector("number-flow-react") ?? null;

  it("keeps persistent Inbox and Chat counters static", () => {
    summary.current = [{ workspace_id: "ws-1", count: 1 }];
    chatSessions.current = [{ id: "chat-1", unread_count: 2 }];
    const { container } = render(<AppSidebar />);
    const inboxBadge = container
      .querySelector<HTMLElement>('button[data-href="/acme/inbox"]')
      ?.querySelector("number-flow-react") as (HTMLElement & { animated?: boolean }) | null;
    const currentChatBadge = chatBadge(container) as (HTMLElement & { animated?: boolean }) | null;

    expect(inboxBadge?.animated).toBe(false);
    expect(currentChatBadge?.animated).toBe(false);
  });

  it("renders a Chat nav link to the workspace chat route", () => {
    const { container } = render(<AppSidebar />);
    expect(chatNav(container)).not.toBeNull();
  });

  it("badges the Chat nav with the summed unread_count of chat sessions", () => {
    chatSessions.current = [{ id: "a", unread_count: 3 }, { id: "b", unread_count: 2 }, { id: "c", unread_count: 0 }];
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "5");
  });

  it("shows no Chat unread badge when every session is read", () => {
    chatSessions.current = [{ id: "a", unread_count: 0 }, { id: "b" }];
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toBeNull();
  });

  it("excludes the session being viewed on the chat page from the badge", () => {
    // The thread list zeroes the open session's row badge; the aggregate
    // must follow, or a reply landing in the open conversation flashes a
    // count with no matching row.
    chatSessions.current = [{ id: "a", unread_count: 2 }, { id: "b", unread_count: 3 }];
    navigation.current = { pathname: "/acme/chat" };
    chatStore.current = { activeSessionId: "a", isOpen: false };
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "3");
  });

  it("excludes the viewed session when the floating chat window is open off-route", () => {
    chatSessions.current = [{ id: "a", unread_count: 2 }, { id: "b", unread_count: 3 }];
    navigation.current = { pathname: "/acme/issues" };
    chatStore.current = { activeSessionId: "a", isOpen: true };
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "3");
  });

  it("still counts a remembered selection when no chat surface is showing it", () => {
    // activeSessionId persists after the chat page closes; with both
    // surfaces closed nothing will auto mark-read, so the badge must count.
    chatSessions.current = [{ id: "a", unread_count: 2 }, { id: "b", unread_count: 3 }];
    navigation.current = { pathname: "/acme/issues" };
    chatStore.current = { activeSessionId: "a", isOpen: false };
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "5");
  });

  it("counts the active session while the floating window is open but the app is backgrounded", () => {
    // A reply landing while the app is not in the foreground is NOT auto
    // marked-read (MUL-4485), so its unread must still badge — otherwise the
    // notification is silently eaten while the user is away.
    chatSessions.current = [{ id: "a", unread_count: 2 }, { id: "b", unread_count: 3 }];
    navigation.current = { pathname: "/acme/issues" };
    chatStore.current = { activeSessionId: "a", isOpen: true };
    appForeground.current = false;
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "5");
  });

  it("counts the active session on the chat route while the app is backgrounded", () => {
    chatSessions.current = [{ id: "a", unread_count: 2 }, { id: "b", unread_count: 3 }];
    navigation.current = { pathname: "/acme/chat" };
    chatStore.current = { activeSessionId: "a", isOpen: false };
    appForeground.current = false;
    const { container } = render(<AppSidebar />);
    expect(chatBadge(container)).toHaveAttribute("aria-label", "5");
  });
});

describe("Pending invitation self-heal", () => {
  beforeEach(() => {
    invitationApi.accept.mockReset();
    invitationApi.decline.mockReset();
    invitationApi.invalidateQueries.mockClear();
    invitationApi.mutations.length = 0;
    invitationApi.accept.mockRejectedValue(new Error("invitation is not pending"));
    invitationApi.decline.mockRejectedValue(new Error("invitation is not pending"));
    navigation.current.pathname = "/acme/issues";
    workspaces.current = [];
  });

  // "invitation is not pending" means the row on screen was concluded from
  // another surface. Both mutations must invalidate the pending list on
  // failure so the stale row drops instead of surviving until restart.
  it("invalidates the pending-invitations list when accept or decline fails", async () => {
    render(<AppSidebar />);
    expect(invitationApi.mutations).toHaveLength(2);

    for (const options of invitationApi.mutations as Array<{
      mutationFn: (id: string) => Promise<unknown>;
      onError?: (...args: unknown[]) => unknown;
      onSettled?: (...args: unknown[]) => unknown;
    }>) {
      const settle = options.onError ?? options.onSettled;
      expect(settle).toBeTypeOf("function");
      invitationApi.invalidateQueries.mockClear();
      await expect(options.mutationFn("inv-1")).rejects.toThrow("invitation is not pending");
      await settle!(new Error("invitation is not pending"), "inv-1", undefined, undefined);
      expect(invitationApi.invalidateQueries).toHaveBeenCalledWith({ queryKey: ["invitations"] });
    }
    expect(invitationApi.accept).toHaveBeenCalledTimes(1);
    expect(invitationApi.decline).toHaveBeenCalledTimes(1);
  });
});

describe("chat pins (DENE-866)", () => {
  const chatPin = {
    id: "pin-chat",
    workspace_id: "ws-1",
    user_id: "user-1",
    item_type: "chat" as const,
    item_id: "chat-1",
    position: 1,
    created_at: "2026-05-06T00:00:00Z",
  };
  const savedPins = pins.current;

  beforeEach(() => {
    createPin.mockReset();
    deletePin.mockReset();
    navigation.current.pathname = "/acme/issues";
    detail.current = { isPending: false, isError: false, data: { identifier: "MUL-1", title: "Issue pin", status: "todo" }, error: null };
    chatDetail.current = { isPending: false, isError: false, data: null, error: null };
    chatSessions.current = [];
    summary.current = [];
    workspaces.current = [];
    modules.current = undefined;
  });

  afterEach(() => {
    pins.current = savedPins;
  });

  it("renders a pinned chat by its title from the loaded chat list", async () => {
    pins.current = [...savedPins, chatPin];
    chatSessions.current = [{ id: "chat-1", title: "Roadmap sync", unread_count: 0 }];
    renderWithI18n(<AppSidebar />);
    const button = (await screen.findByText("Roadmap sync")).closest("button");
    expect(button).toHaveAttribute("data-href", "/acme/chat/chat-1");
    expect(deletePin).not.toHaveBeenCalled();
  });

  it("falls back to the untitled label for a pinned chat without a title", async () => {
    pins.current = [...savedPins, chatPin];
    chatSessions.current = [{ id: "chat-1", title: null, unread_count: 0 }];
    renderWithI18n(<AppSidebar />);
    expect(await screen.findByText("New chat")).toBeInTheDocument();
  });

  it("unpins a chat whose detail 404s after the list came back without it", async () => {
    pins.current = [...savedPins, chatPin];
    chatSessions.current = [{ id: "other", title: "Other", unread_count: 0 }];
    chatDetail.current = { isPending: false, isError: true, data: null, error: new ApiError("missing", 404, "Not Found") };
    renderWithI18n(<AppSidebar />);
    await waitFor(() => expect(deletePin).toHaveBeenCalledWith({ itemType: "chat", itemId: "chat-1" }));
  });

  it("pins a chat dropped onto the pinned group, once", () => {
    chatSessions.current = [{ id: "chat-9", title: "Dropped", unread_count: 0 }];
    renderWithI18n(<AppSidebar />);
    const group = screen.getAllByTestId("sidebar-group").find((el) => el.textContent?.includes("Issue pin"))!;
    const dataTransfer = {
      types: ["application/x-multica-chat-session"],
      getData: (type: string) => (type === "application/x-multica-chat-session" ? "chat-9" : ""),
      dropEffect: "none",
    };
    fireEvent.dragOver(group, { dataTransfer });
    fireEvent.drop(group, { dataTransfer });
    expect(createPin).toHaveBeenCalledTimes(1);
    expect(createPin).toHaveBeenCalledWith({ item_type: "chat", item_id: "chat-9" });
  });

  it("ignores a drop of a chat that is already pinned", () => {
    pins.current = [...savedPins, chatPin];
    chatSessions.current = [{ id: "chat-1", title: "Roadmap sync", unread_count: 0 }];
    renderWithI18n(<AppSidebar />);
    const group = screen.getAllByTestId("sidebar-group").find((el) => el.textContent?.includes("Roadmap sync"))!;
    fireEvent.drop(group, {
      dataTransfer: { types: ["application/x-multica-chat-session"], getData: () => "chat-1", dropEffect: "none" },
    });
    expect(createPin).not.toHaveBeenCalled();
  });
});
