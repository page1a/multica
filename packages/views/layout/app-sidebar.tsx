"use client";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { AppLink, useNavigation } from "../navigation";
import { HelpLauncher } from "./help-launcher";
import { WriteAction } from "./guest-readonly";
import { JoinDiscordCard } from "./join-discord-card";
import {
  DndContext,
  PointerSensor,
  useSensor,
  useSensors,
  closestCenter,
  type DragEndEvent,
} from "@dnd-kit/core";
import { SortableContext, verticalListSortingStrategy, useSortable, arrayMove } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import {
  ChevronDown,
  ChevronRight,
  LogOut,
  Plus,
  Check,
  ArrowUpDown,
  Pin,
  PinOff,
  Search,
  SquarePen,
  Sparkles,
  X,
} from "lucide-react";
import { WorkspaceAvatar } from "../workspace/workspace-avatar";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@multica/ui/components/ui/collapsible";
import { CappedNumberFlow } from "@multica/ui/components/ui/number-flow";
import {
  openAlignIssue,
  openCreateIssueWithPreference,
} from "@multica/core/issues/stores/create-mode-store";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from "@multica/ui/components/ui/sidebar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useAuthStore } from "@multica/core/auth";
import { useCurrentWorkspace, useWorkspacePaths, paths } from "@multica/core/paths";
import { workspaceListOptions, myInvitationListOptions, workspaceKeys, moduleVisibilityOptions } from "@multica/core/workspace/queries";
import { canAccessModule, navItemModule } from "@multica/core/workspace";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  arrangeWorkspaces,
  useWorkspaceSwitcherPreference,
  useWorkspaceSwitcherPreferenceStore,
} from "@multica/core/workspace/switcher-preference";
import { isImeComposing } from "@multica/core/utils";
import type { Workspace } from "@multica/core/types";
import { WorkspaceOrganizeDialog } from "./workspace-organize-dialog";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { inboxUnreadSummaryOptions, useInboxUnreadCount, hasOtherWorkspaceUnread, unreadWorkspaceIds } from "@multica/core/inbox/queries";
import { chatSessionsOptions } from "@multica/core/chat/queries";
import { countUnreadChatMessages } from "@multica/core/chat/unread";
import { useChatStore } from "@multica/core/chat";
import { api, ApiError } from "@multica/core/api";
import { useConfigStore } from "@multica/core/config";
import { pinListOptions } from "@multica/core/pins/queries";
import { useDeletePin, useReorderPins } from "@multica/core/pins/mutations";
import { projectDetailOptions } from "@multica/core/projects/queries";
import type { PinnedItem } from "@multica/core/types";
import { useLogout } from "../auth";
import { ProjectIcon } from "../projects/components/project-icon";
import { routeIconForPath } from "./route-icon-components";
import { useT } from "../i18n";
import {
  useShortcut,
} from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";
import { useAppForeground } from "../common/use-app-foreground";

// Top-level nav items stay active when the user is on a child route
// (e.g. "Projects" stays lit on /:slug/projects/:id). Pinned items keep
// strict equality elsewhere — a pinned project shouldn't highlight on
// sub-pages of itself.
function isNavActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(href + "/");
}

// Stable empty arrays for query defaults. Using an inline `= []` default on
// `useQuery` creates a new array reference on every render when `data` is
// undefined (e.g. query disabled or loading) — which in turn breaks any
// `useEffect`/`useMemo` that depends on the value, and can trigger infinite
// re-render loops when the effect itself calls `setState`.
const EMPTY_PINS: PinnedItem[] = [];
const EMPTY_WORKSPACES: Awaited<ReturnType<typeof api.listWorkspaces>> = [];
const EMPTY_INVITATIONS: Awaited<ReturnType<typeof api.listMyInvitations>> = [];
const EMPTY_INBOX_SUMMARY: Awaited<ReturnType<typeof api.getInboxUnreadSummary>> = [];
const PINNED_PREVIEW_LIMIT = 5;
// The switcher grows a search box once the list stops fitting at a glance.
const WORKSPACE_SEARCH_THRESHOLD = 5;

// Nav items reference WorkspacePaths method names so they can be resolved
// against the current workspace slug at render time (see AppSidebar body).
// Only parameterless paths are valid nav destinations.
type NavKey =
  | "inbox"
  | "chat"
  | "myIssues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "runtimes"
  | "skills"
  | "settings";

// Static schema (key only) — labels resolved at render via useT("layout"),
// icons derived from the destination path via routeIconForPath.
type NavLabelKey =
  | "inbox"
  | "chat"
  | "my_issues"
  | "issues"
  | "projects"
  | "autopilots"
  | "agents"
  | "squads"
  | "usage"
  | "runtimes"
  | "skills"
  | "permissions"
  | "settings";

// Nav icons are NOT declared here: they are derived from each item's
// destination path at render time, so the sidebar and the desktop tab bar
// always agree. See route-icon-components.tsx.
const personalNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "inbox", labelKey: "inbox" },
  { key: "myIssues", labelKey: "my_issues" },
  { key: "chat", labelKey: "chat" },
];

const workNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "issues", labelKey: "issues" },
  { key: "projects", labelKey: "projects" },
  { key: "autopilots", labelKey: "autopilots" },
];

const aiTeamNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "agents", labelKey: "agents" },
  { key: "squads", labelKey: "squads" },
  { key: "skills", labelKey: "skills" },
  { key: "runtimes", labelKey: "runtimes" },
];

const utilityNav: { key: NavKey; labelKey: NavLabelKey }[] = [
  { key: "usage", labelKey: "usage" },
  { key: "settings", labelKey: "settings" },
];

const NAV_ITEM_CLASS_NAME =
  "text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground";

/**
 * Presentational pin row. The `label` and `iconNode` are computed by the
 * parent `PinRow` from the cached project detail query — keeping
 * this component dumb means the dnd-kit / navigation wiring lives in
 * one place and the data flow is explicit.
 */
function SortablePinItem({
  pin,
  href,
  pathname,
  onUnpin,
  label,
  iconNode,
}: {
  pin: PinnedItem;
  href: string;
  pathname: string;
  onUnpin: () => void;
  label: string;
  iconNode: React.ReactNode;
}) {
  const { t } = useT("layout");
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id: pin.id });
  const wasDragged = useRef(false);

  useEffect(() => {
    if (isDragging) wasDragged.current = true;
  }, [isDragging]);

  const style = { transform: CSS.Transform.toString(transform), transition };
  const isActive = pathname === href;

  return (
    <SidebarMenuItem
      ref={setNodeRef}
      style={style}
      className={cn("group/pin", isDragging && "opacity-30")}
      {...attributes}
      {...listeners}
    >
      <SidebarMenuButton
        size="sm"
        isActive={isActive}
        render={<AppLink href={href} newTabTitle={label} draggable={false} />}
        onClick={(event) => {
          if (wasDragged.current) {
            wasDragged.current = false;
            event.preventDefault();
          }
        }}
        className={cn(
          "text-muted-foreground hover:not-data-active:bg-sidebar-accent/70 data-active:bg-sidebar-accent data-active:text-sidebar-accent-foreground",
          isDragging && "pointer-events-none",
        )}
      >
        {iconNode}
        <span
          className="min-w-0 flex-1 overflow-hidden whitespace-nowrap"
          style={{
            maskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
            WebkitMaskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
          }}
        >{label}</span>
        <Tooltip>
          <TooltipTrigger
            render={<span role="button" />}
            className="hidden size-2.5 shrink-0 items-center justify-center rounded-sm text-muted-foreground group-hover/pin:flex hover:text-foreground"
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              onUnpin();
            }}
          >
            <X className="size-1" />
          </TooltipTrigger>
          <TooltipContent side="top" sideOffset={4}>{t(($) => $.sidebar.unpin_tooltip)}</TooltipContent>
        </Tooltip>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}

/**
 * Resolves a project pin's display data from the project detail cache.
 *
 * Loading: render a flat skeleton so the sidebar height doesn't jump.
 * Missing (deleted project / 404): auto-unpin; any other error hides the row.
 */
function PinRow({
  pin,
  href,
  pathname,
  onUnpin,
  wsId,
}: {
  pin: PinnedItem;
  href: string;
  pathname: string;
  onUnpin: () => void;
  wsId: string;
}) {
  const projectQuery = useQuery(projectDetailOptions(wsId, pin.item_id));

  const triggeredRef = useRef(false);
  useEffect(() => {
    const err = projectQuery.error;
    if (err instanceof ApiError && err.status === 404 && !triggeredRef.current) {
      triggeredRef.current = true;
      onUnpin();
    }
  }, [onUnpin, projectQuery.error]);

  if (projectQuery.isPending) return <PinSkeleton />;
  if (projectQuery.isError || !projectQuery.data) return null;
  const project = projectQuery.data;
  const iconNode = <ProjectIcon project={project} size="sm" />;
  return (
    <SortablePinItem
      pin={pin}
      href={href}
      pathname={pathname}
      onUnpin={onUnpin}
      label={project.title}
      iconNode={iconNode}
    />
  );
}

function PinSkeleton() {
  return (
    <SidebarMenuItem>
      <div className="flex h-7 w-full items-center gap-2 px-2">
        <div className="size-3.5 shrink-0 rounded-sm bg-sidebar-accent/40" />
        <div className="h-3 w-24 rounded-xs bg-sidebar-accent/40" />
      </div>
    </SidebarMenuItem>
  );
}

interface AppSidebarProps {
  /** Rendered above SidebarHeader (e.g. desktop traffic light spacer) */
  topSlot?: React.ReactNode;
  /** Rendered in the header between workspace switcher and new-issue button (e.g. search trigger) */
  searchSlot?: React.ReactNode;
  /** Extra className for SidebarHeader */
  headerClassName?: string;
  /** Extra style for SidebarHeader */
  headerStyle?: React.CSSProperties;
}

export function AppSidebar({ topSlot, searchSlot, headerClassName, headerStyle }: AppSidebarProps = {}) {
  const { t } = useT("layout");
  const { pathname, push, searchParams } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const userId = useAuthStore((s) => s.user?.id);
  const logout = useLogout();
  const workspace = useCurrentWorkspace();
  const p = useWorkspacePaths();
  const PermissionsIcon = routeIconForPath(p.settings());
  const { data: workspaces = EMPTY_WORKSPACES } = useQuery(workspaceListOptions());
  const { data: myInvitations = EMPTY_INVITATIONS } = useQuery(myInvitationListOptions());
  const workspaceCreationDisabled = useConfigStore((s) => s.workspaceCreationDisabled);

  // On a phone the sidebar is a Sheet covering the page, so navigating out of
  // it has to dismiss it — otherwise the destination renders underneath and the
  // tap reads as "nothing happened". Closing on `pathname` rather than on each
  // link's onClick covers every route out of here at once: the nav groups, the
  // pinned items, the workspace switcher's programmatic push, and anything
  // added later. `setOpenMobile` is a no-op on desktop, where the sheet is not
  // the sidebar's rendering at all.
  const { setOpenMobile } = useSidebar();
  useEffect(() => {
    setOpenMobile(false);
  }, [pathname, setOpenMobile]);

  const wsId = workspace?.id;
  // Nav badge. Reads the cross-workspace unread summary fetched just below
  // for the switcher dot, so the count costs no request of its own — it used
  // to download the whole inbox list here just to count it (MUL-6967).
  const unreadCount = useInboxUnreadCount(wsId);
  // Chat tab unread badge: IM-style total of unread *messages* across chat
  // threads (countUnreadChatMessages is the shared definition — mobile's tab
  // badge derives from the same function, keeping the platforms in agreement).
  const { data: chatSessions = [] } = useQuery({
    ...chatSessionsOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  // The session the user is reading right now must not count: the thread list
  // renders its row badge as 0 (auto mark-read is about to clear it), and a
  // reply landing in the open conversation would otherwise flash a sidebar
  // count with no matching row. "Reading right now" = a session is active, a
  // chat surface is actually showing it (chat page route or the floating
  // window), AND the app is in the foreground. When the app is backgrounded,
  // auto mark-read is suppressed (MUL-4485) so the reply stays unread — the
  // badge must count it, or the notification is silently eaten while the user
  // is away. A remembered selection while both surfaces are closed also still
  // counts, for the same reason.
  const activeChatSessionId = useChatStore((s) => s.activeSessionId);
  const floatingChatOpen = useChatStore((s) => s.isOpen);
  const appForeground = useAppForeground();
  const chatHref = p.chat();
  const viewedChatSessionId =
    appForeground && (floatingChatOpen || isNavActive(pathname, chatHref))
      ? activeChatSessionId
      : null;
  const chatUnreadCount = React.useMemo(
    () => countUnreadChatMessages(chatSessions, viewedChatSessionId),
    [chatSessions, viewedChatSessionId],
  );
  // Cross-workspace unread summary backs the workspace-switcher dot. One
  // shared cache entry across workspaces; gated on an active workspace since
  // the endpoint resolves through the workspace-member middleware.
  const { data: unreadSummary = EMPTY_INBOX_SUMMARY } = useQuery({
    ...inboxUnreadSummaryOptions(),
    enabled: !!wsId,
  });
  const otherWorkspaceUnread = React.useMemo(
    () => hasOtherWorkspaceUnread(unreadSummary, wsId),
    [unreadSummary, wsId],
  );
  // Which workspaces have unread, so the switcher dropdown can point at the
  // specific one(s) rather than just the aggregate avatar dot.
  const unreadWsIds = React.useMemo(() => unreadWorkspaceIds(unreadSummary), [unreadSummary]);
  const { data: pinnedItems = EMPTY_PINS } = useQuery({
    ...pinListOptions(wsId ?? "", userId ?? ""),
    enabled: !!wsId && !!userId,
  });
  const { data: moduleAccess } = useQuery({
    ...moduleVisibilityOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const navVisible = (key: NavKey) => {
    const module = navItemModule(key);
    return module === null || canAccessModule(moduleAccess, module);
  };
  const deletePin = useDeletePin();
  const reorderPins = useReorderPins();
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 5 } }));
  const sidebarScrollRef = useRef<HTMLDivElement>(null);
  const sidebarFadeStyle = useScrollFade(sidebarScrollRef, 24);
  const getPinHref = useCallback((pin: PinnedItem) => p.projectDetail(pin.item_id), [p]);

  // Local presentational copy of pinnedItems for drop-animation stability.
  // Follows TQ at rest; frozen during a drag gesture so a mid-drag cache
  // write (our own optimistic update, or a WS refetch) cannot reorder the
  // DOM under dnd-kit while its drop animation is still interpolating.
  const [localPinned, setLocalPinned] = useState<PinnedItem[]>(pinnedItems);
  const [localPinnedWsId, setLocalPinnedWsId] = useState<string | null>(wsId ?? null);
  const [expandedPinsWorkspaceId, setExpandedPinsWorkspaceId] = useState<string | null>(null);
  const isDraggingRef = useRef(false);
  useEffect(() => {
    if (!isDraggingRef.current) {
      setLocalPinned(pinnedItems);
    }
  }, [pinnedItems]);
  useEffect(() => {
    setLocalPinnedWsId(wsId ?? null);
  }, [wsId]);
  // The sidebar's pinned group holds projects only (DENE-876). Issue pins
  // still rank issues to the top of their lists and chat pins still fill the
  // Chat list's own pinned group; they just don't surface here.
  const visiblePinned = useMemo(
    () =>
      localPinnedWsId === (wsId ?? null)
        ? localPinned.filter((pin) => pin.item_type === "project")
        : EMPTY_PINS,
    [localPinned, localPinnedWsId, wsId],
  );
  const pinsExpanded = expandedPinsWorkspaceId === wsId;
  const displayedPinned = pinsExpanded ? visiblePinned : visiblePinned.slice(0, PINNED_PREVIEW_LIMIT);
  const isActivePinnedRoute = displayedPinned.some((pin) => pathname === getPinHref(pin));

  const handleDragStart = useCallback(() => {
    isDraggingRef.current = true;
  }, []);
  const handleDragEnd = useCallback(
    (event: DragEndEvent) => {
      isDraggingRef.current = false;
      const { active, over } = event;
      if (!over || active.id === over.id) return;
      // Move within the full list so hidden (non-project) pins keep their
      // positions and stay in the shared cache.
      const oldIndex = localPinned.findIndex((p) => p.id === active.id);
      const newIndex = localPinned.findIndex((p) => p.id === over.id);
      if (oldIndex === -1 || newIndex === -1) return;
      const reordered = arrayMove(localPinned, oldIndex, newIndex);
      setLocalPinned(reordered);
      reorderPins.mutate(reordered);
    },
    [localPinned, reorderPins],
  );

  const queryClient = useQueryClient();
  const acceptInvitationMut = useMutation({
    mutationFn: (id: string) => api.acceptInvitation(id),
    // After accepting an invitation, navigate INTO the newly-joined workspace.
    // Otherwise the user stays on their current workspace and just sees the
    // new one appear in the dropdown — silent and confusing (this is MUL-820).
    onSuccess: async (_, invitationId) => {
      const invitation = myInvitations.find((i) => i.id === invitationId);
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
      // staleTime: 0 forces a real network fetch — we need the joined workspace
      // in the list before we can resolve its slug for navigation.
      const list = await queryClient.fetchQuery({
        ...workspaceListOptions(),
        staleTime: 0,
      });
      const joined = invitation
        ? list.find((w) => w.id === invitation.workspace_id)
        : null;
      if (joined) {
        push(paths.workspace(joined.slug).issues());
      }
    },
    onError: () => {
      // "invitation is not pending" means the invite was concluded from
      // another surface while this row was on screen. Refetch so the stale
      // row drops instead of sticking around until restart — a silent
      // failure here reads as "the button does nothing".
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    },
  });
  const declineInvitationMut = useMutation({
    mutationFn: (id: string) => api.declineInvitation(id),
    // Either outcome must refresh the list: success drops the declined row,
    // and a failure ("invitation is not pending") means the invite was
    // concluded from another surface — refetch drops the stale row.
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: workspaceKeys.myInvitations() });
    },
  });

  const createIssueShortcut = useShortcut("createIssue");

  // Workspace switcher arrangement: this person's pins and order on this
  // device, applied over the server list, then narrowed by the search box.
  const switcherPreference = useWorkspaceSwitcherPreference(userId);
  const toggleWorkspacePinned = useWorkspaceSwitcherPreferenceStore((s) => s.togglePinned);
  const [switcherOpen, setSwitcherOpen] = useState(false);
  const [organizeOpen, setOrganizeOpen] = useState(false);
  const [workspaceQuery, setWorkspaceQuery] = useState("");
  const showWorkspaceSearch = workspaces.length > WORKSPACE_SEARCH_THRESHOLD;
  const arrangedWorkspaces = useMemo(() => {
    const { pinned, rest } = arrangeWorkspaces(workspaces, switcherPreference);
    const query = workspaceQuery.trim().toLowerCase();
    const matches = (ws: Workspace) => !query || ws.name.toLowerCase().includes(query);
    return { pinned: pinned.filter(matches), rest: rest.filter(matches) };
  }, [workspaces, switcherPreference, workspaceQuery]);
  const firstWorkspaceMatch = arrangedWorkspaces.pinned[0] ?? arrangedWorkspaces.rest[0];
  const pinnedWorkspaceIds = useMemo(
    () => new Set(switcherPreference.pinned),
    [switcherPreference.pinned],
  );

  const renderWorkspaceItem = (ws: Workspace) => {
    const isPinned = pinnedWorkspaceIds.has(ws.id);
    const pinLabel = isPinned
      ? t(($) => $.sidebar.unpin_workspace)
      : t(($) => $.sidebar.pin_workspace);
    return (
      <DropdownMenuItem
        key={ws.id}
        className="group/ws"
        render={
          <AppLink href={paths.workspace(ws.slug).issues()} />
        }
      >
        <WorkspaceAvatar name={ws.name} avatarUrl={ws.avatar_url} size="sm" />
        <span className="flex-1 truncate">{ws.name}</span>
        {/* Points at the specific workspace holding unread
            inbox items. Sits in the same right-edge slot as the
            active-workspace check; the active workspace is
            excluded (its unread is the Inbox nav count), so dot
            and check never collide on one row. */}
        {ws.id !== workspace?.id && unreadWsIds.has(ws.id) && (
          <span className="size-2 rounded-full bg-brand" />
        )}
        {ws.id === workspace?.id && (
          <Check className="h-3.5 w-3.5 text-primary" />
        )}
        {userId && (
          <span
            role="button"
            aria-label={pinLabel}
            title={pinLabel}
            aria-pressed={isPinned}
            className="hidden size-4 shrink-0 items-center justify-center rounded-sm text-muted-foreground group-hover/ws:flex group-data-highlighted/ws:flex hover:text-foreground"
            onClick={(event) => {
              // Pinning is a tweak to the list, not a choice of workspace:
              // keep the menu open and stay where we are.
              event.preventDefault();
              event.stopPropagation();
              toggleWorkspacePinned(userId, workspaces.map((w) => w.id), ws.id);
            }}
          >
            {isPinned ? <PinOff className="size-3.5" /> : <Pin className="size-3.5" />}
          </span>
        )}
      </DropdownMenuItem>
    );
  };

  return (
      <Sidebar variant="inset">
        {topSlot}
        {/* Workspace Switcher */}
        <SidebarHeader className={cn("py-3", headerClassName)} style={headerStyle}>
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu
                open={switcherOpen}
                onOpenChange={(open) => {
                  setSwitcherOpen(open);
                  if (!open) setWorkspaceQuery("");
                }}
              >
                <DropdownMenuTrigger
                  render={
                    <SidebarMenuButton>
                      <span className="relative">
                        <WorkspaceAvatar name={workspace?.name ?? "M"} avatarUrl={workspace?.avatar_url} size="sm" />
                        {/* Shared brand dot: a pending invitation OR another
                            workspace with unread inbox items. The active
                            workspace's own unread stays on the Inbox nav count
                            (below), so it is deliberately excluded here. */}
                        {(myInvitations.length > 0 || otherWorkspaceUnread) && (
                          <span className="absolute -top-0.5 -right-0.5 size-2 rounded-full bg-brand ring-1 ring-sidebar" />
                        )}
                      </span>
                      <span className="flex-1 truncate font-medium">
                        {workspace?.name ?? "Multica"}
                      </span>
                      <ChevronDown className="size-3 text-muted-foreground" />
                    </SidebarMenuButton>
                  }
                />
                <DropdownMenuContent
                  className="w-auto min-w-56"
                  align="start"
                  side="bottom"
                  sideOffset={4}
                >
                  <div className="flex items-center gap-2.5 px-2 py-1.5">
                    <ActorAvatar
                      name={user?.name ?? ""}
                      initials={(user?.name ?? "U").charAt(0).toUpperCase()}
                      avatarUrl={resolvePublicFileUrl(user?.avatar_url)}
                      size="lg"
                    />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-body font-medium leading-tight">
                        {user?.name}
                      </p>
                      <p className="truncate text-caption text-muted-foreground leading-tight">
                        {user?.email}
                      </p>
                    </div>
                  </div>
                  <DropdownMenuSeparator />
                  <DropdownMenuGroup>
                    <DropdownMenuLabel className="text-caption text-muted-foreground">
                      {t(($) => $.sidebar.workspaces_label)}
                    </DropdownMenuLabel>
                    {showWorkspaceSearch && (
                      <div className="mx-1 mb-1 flex items-center gap-1.5 rounded-md border border-border px-2 py-1">
                        <Search className="size-3.5 shrink-0 text-muted-foreground" />
                        <input
                          type="text"
                          value={workspaceQuery}
                          onChange={(e) => setWorkspaceQuery(e.target.value)}
                          onKeyDown={(e) => {
                            if (isImeComposing(e)) return;
                            if (e.key === "Enter") {
                              e.preventDefault();
                              if (firstWorkspaceMatch) {
                                setSwitcherOpen(false);
                                setWorkspaceQuery("");
                                push(paths.workspace(firstWorkspaceMatch.slug).issues());
                              }
                              return;
                            }
                            // Arrows move into the list and Escape closes the
                            // menu; every other key is typing, which the
                            // menu's typeahead must not swallow.
                            if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Escape") {
                              e.stopPropagation();
                            }
                          }}
                          placeholder={t(($) => $.sidebar.search_workspaces_placeholder)}
                          aria-label={t(($) => $.sidebar.search_workspaces_placeholder)}
                          className="min-w-0 flex-1 bg-transparent text-body outline-none placeholder:text-muted-foreground"
                          autoFocus
                        />
                      </div>
                    )}
                    <div className="max-h-[50vh] overflow-y-auto">
                      {arrangedWorkspaces.pinned.map(renderWorkspaceItem)}
                      {arrangedWorkspaces.pinned.length > 0 && arrangedWorkspaces.rest.length > 0 && (
                        <DropdownMenuSeparator />
                      )}
                      {arrangedWorkspaces.rest.map(renderWorkspaceItem)}
                      {!firstWorkspaceMatch && workspaceQuery.trim() && (
                        <p className="px-2 py-1.5 text-caption text-muted-foreground">
                          {t(($) => $.sidebar.no_matching_workspaces, { query: workspaceQuery.trim() })}
                        </p>
                      )}
                    </div>
                    {userId && workspaces.length > 1 && (
                      <DropdownMenuItem onClick={() => setOrganizeOpen(true)}>
                        <ArrowUpDown className="h-3.5 w-3.5" />
                        {t(($) => $.sidebar.organize_workspaces)}
                      </DropdownMenuItem>
                    )}
                    {!workspaceCreationDisabled && (
                      <DropdownMenuItem
                        onClick={() => push(paths.newWorkspace())}
                      >
                        <Plus className="h-3.5 w-3.5" />
                        {t(($) => $.sidebar.create_workspace)}
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuGroup>
                  {myInvitations.length > 0 && (
                    <>
                      <DropdownMenuSeparator />
                      <DropdownMenuGroup>
                        <DropdownMenuLabel className="text-caption text-muted-foreground">
                          {t(($) => $.sidebar.pending_invitations_label)}
                        </DropdownMenuLabel>
                        {myInvitations.map((inv) => (
                          <div key={inv.id} className="flex items-center gap-2 px-2 py-1.5">
                            <WorkspaceAvatar name={inv.workspace_name ?? "W"} size="sm" />
                            <span className="flex-1 truncate text-body">{inv.workspace_name ?? t(($) => $.sidebar.invitation_workspace_fallback)}</span>
                            <button
                              type="button"
                              className="text-caption px-2 py-0.5 rounded-xs bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
                              disabled={acceptInvitationMut.isPending}
                              onClick={(e) => {
                                e.stopPropagation();
                                acceptInvitationMut.mutate(inv.id);
                              }}
                            >
                              {t(($) => $.sidebar.invitation_join)}
                            </button>
                            <button
                              type="button"
                              className="text-caption px-2 py-0.5 rounded-xs bg-muted text-muted-foreground hover:bg-muted/80 disabled:opacity-50"
                              disabled={declineInvitationMut.isPending}
                              onClick={(e) => {
                                e.stopPropagation();
                                declineInvitationMut.mutate(inv.id);
                              }}
                            >
                              {t(($) => $.sidebar.invitation_decline)}
                            </button>
                          </div>
                        ))}
                      </DropdownMenuGroup>
                    </>
                  )}
                  <DropdownMenuSeparator />
                  <DropdownMenuGroup>
                    <DropdownMenuItem variant="destructive" onClick={logout}>
                      <LogOut className="h-3.5 w-3.5" />
                      {t(($) => $.sidebar.log_out)}
                    </DropdownMenuItem>
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenu>
              {userId && (
                <WorkspaceOrganizeDialog
                  open={organizeOpen}
                  onOpenChange={setOrganizeOpen}
                  workspaces={workspaces}
                  userId={userId}
                />
              )}
            </SidebarMenuItem>
          </SidebarMenu>
          <SidebarMenu>
            {searchSlot && (
              <SidebarMenuItem>
                {searchSlot}
              </SidebarMenuItem>
            )}
            <SidebarMenuItem>
              <WriteAction className="flex w-full">
              <SidebarMenuButton
                className="text-muted-foreground"
                onClick={() => openCreateIssueWithPreference()}
              >
                <SquarePen />
                <span>{t(($) => $.sidebar.new_issue)}</span>
                {createIssueShortcut ? (
                  <ShortcutKeycaps shortcut={createIssueShortcut} decorative className="pointer-events-none ml-auto" />
                ) : null}
              </SidebarMenuButton>
              </WriteAction>
            </SidebarMenuItem>
            <SidebarMenuItem>
              {/* The way into requirement alignment. It sits beside "New
                  issue" rather than replacing it: skipping alignment stays a
                  deliberate choice, not something a user has to undo. It opens
                  the create-issue dialog on its alignment face — the same
                  shell, so the shared draft and uploads come with it. */}
              <WriteAction className="flex w-full">
              <SidebarMenuButton
                className="text-muted-foreground"
                title={t(($) => $.sidebar.align_issue_hint)}
                onClick={() => openAlignIssue()}
              >
                <Sparkles />
                <span>{t(($) => $.sidebar.align_issue)}</span>
              </SidebarMenuButton>
              </WriteAction>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>

        {/* Navigation */}
        <SidebarContent ref={sidebarScrollRef} style={sidebarFadeStyle}>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {personalNav.filter((item) => navVisible(item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className={NAV_ITEM_CLASS_NAME}
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                        {item.key === "inbox" && unreadCount > 0 && (
                          <CappedNumberFlow
                            value={unreadCount}
                            animated={false}
                            className="ml-auto text-caption"
                          />
                        )}
                        {item.key === "chat" && chatUnreadCount > 0 && (
                          <CappedNumberFlow
                            value={chatUnreadCount}
                            animated={false}
                            className="ml-auto text-caption"
                          />
                        )}
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          {visiblePinned.length > 0 && (
            <Collapsible defaultOpen>
              <SidebarGroup className="group/pinned">
                <SidebarGroupLabel
                  render={<CollapsibleTrigger />}
                  className="group/trigger cursor-pointer hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground"
                >
                  <span>{t(($) => $.sidebar.pinned_label)}</span>
                  <ChevronRight className="!size-3 ml-1 stroke-[2.5] transition-transform duration-200 group-data-[panel-open]/trigger:rotate-90" />
                  <span className="ml-auto text-micro text-muted-foreground opacity-0 transition-opacity group-hover/pinned:opacity-100">{visiblePinned.length}</span>
                </SidebarGroupLabel>
                <CollapsibleContent>
                  <SidebarGroupContent>
                    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragStart={handleDragStart} onDragEnd={handleDragEnd}>
                      <SortableContext items={displayedPinned.map((p) => p.id)} strategy={verticalListSortingStrategy}>
                        <SidebarMenu className="gap-0.5">
                          {displayedPinned.map((pin: PinnedItem) => (
                            <PinRow
                              key={pin.id}
                              pin={pin}
                              href={getPinHref(pin)}
                              pathname={pathname}
                              onUnpin={() => deletePin.mutate({ itemType: pin.item_type, itemId: pin.item_id })}
                              wsId={wsId ?? ""}
                            />
                          ))}
                        </SidebarMenu>
                      </SortableContext>
                    </DndContext>
                    {visiblePinned.length > PINNED_PREVIEW_LIMIT && (
                      <SidebarMenuButton
                        size="sm"
                        aria-expanded={pinsExpanded}
                        className="mt-0.5 pl-7 text-muted-foreground"
                        onClick={() => setExpandedPinsWorkspaceId(pinsExpanded ? null : wsId ?? null)}
                      >
                        <span>
                          {pinsExpanded
                            ? t(($) => $.sidebar.show_fewer_pins)
                            : t(($) => $.sidebar.show_more_pins, { count: visiblePinned.length - PINNED_PREVIEW_LIMIT })}
                        </span>
                      </SidebarMenuButton>
                    )}
                  </SidebarGroupContent>
                </CollapsibleContent>
              </SidebarGroup>
            </Collapsible>
          )}

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.sidebar.work_group)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {workNav.filter((item) => navVisible(item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = !isActivePinnedRoute && isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className={NAV_ITEM_CLASS_NAME}
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          <SidebarGroup>
            <SidebarGroupLabel>{t(($) => $.sidebar.ai_team_group)}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu className="gap-0.5">
                {aiTeamNav.filter((item) => navVisible(item.key)).map((item) => {
                  const href = p[item.key]();
                  const Icon = routeIconForPath(href);
                  const isActive = isNavActive(pathname, href);
                  return (
                    <SidebarMenuItem key={item.key}>
                      <SidebarMenuButton
                        isActive={isActive}
                        render={<AppLink href={href} />}
                        className={NAV_ITEM_CLASS_NAME}
                      >
                        <Icon />
                        <span>{t(($) => $.nav[item.labelKey])}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
                <SidebarMenuItem>
                  <SidebarMenuButton
                    isActive={pathname === p.settings() && searchParams.get("tab") === "project-sharing"}
                    render={<AppLink href={`${p.settings()}?tab=project-sharing`} />}
                    className={NAV_ITEM_CLASS_NAME}
                  >
                    <PermissionsIcon />
                    <span>{t(($) => $.nav.permissions)}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>

        <SidebarFooter className="p-2">
          <SidebarMenu className="gap-0.5">
            {utilityNav.map((item) => {
              const href = p[item.key]();
              const Icon = routeIconForPath(href);
              return (
                <SidebarMenuItem key={item.key}>
                  <SidebarMenuButton
                    isActive={isNavActive(pathname, href)}
                    render={<AppLink href={href} />}
                    className={NAV_ITEM_CLASS_NAME}
                  >
                    <Icon />
                    <span>{t(($) => $.nav[item.labelKey])}</span>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              );
            })}
          </SidebarMenu>
          {/* Discord fills the strip while visible; once dismissed, help
              aligns with the navigation icons above. */}
          <div className="flex items-center gap-1">
            <JoinDiscordCard />
            <HelpLauncher />
          </div>
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>
  );
}
