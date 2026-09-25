import {
  Fragment,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type RefObject,
} from "react";
import { motion, useReducedMotion } from "motion/react";
import { X, Plus, Pin, PinOff, ListX, AppWindow, ChevronDown, ChevronRight } from "lucide-react";
import {
  DndContext,
  PointerSensor,
  useDroppable,
  useSensor,
  useSensors,
  closestCenter,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  horizontalListSortingStrategy,
  useSortable,
} from "@dnd-kit/sortable";
import {
  restrictToHorizontalAxis,
  restrictToParentElement,
} from "@dnd-kit/modifiers";
import { CSS } from "@dnd-kit/utilities";
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuTrigger,
} from "@multica/ui/components/ui/context-menu";
import { useScrollFade } from "@multica/ui/hooks/use-scroll-fade";
import { SIDEBAR_WRAPPER_FILL_CLASS } from "@multica/ui/components/ui/sidebar";
import { cn } from "@multica/ui/lib/utils";
import {
  useTabStore,
  useActiveGroup,
  type Tab,
  type TabStripGroup,
  type TabStripGroupColor,
} from "@/stores/tab-store";
import { paths } from "@multica/core/paths";
import {
  useTabPresentation,
  ResourceLeadingVisual,
} from "@multica/views/layout";
import { parseIssueWindowPath } from "../../../shared/issue-window";

const TAB_SCROLL_FADE_SIZE = 24;
const TAB_ENTRY_EASE = [0.22, 1, 0.36, 1] as const;

const GROUP_CHIP_CLASS: Record<TabStripGroupColor, string> = {
  blue: "bg-blue-500/15 text-blue-800 dark:text-blue-200",
  red: "bg-red-500/15 text-red-800 dark:text-red-200",
  yellow: "bg-amber-500/20 text-amber-900 dark:text-amber-100",
  green: "bg-green-500/15 text-green-800 dark:text-green-200",
  pink: "bg-pink-500/15 text-pink-800 dark:text-pink-200",
  purple: "bg-purple-500/15 text-purple-800 dark:text-purple-200",
  cyan: "bg-cyan-500/15 text-cyan-900 dark:text-cyan-100",
  orange: "bg-orange-500/15 text-orange-900 dark:text-orange-100",
};

const GROUP_WASH_CLASS: Record<TabStripGroupColor, string> = {
  blue: "bg-blue-500/10",
  red: "bg-red-500/10",
  yellow: "bg-amber-500/10",
  green: "bg-green-500/10",
  pink: "bg-pink-500/10",
  purple: "bg-purple-500/10",
  cyan: "bg-cyan-500/10",
  orange: "bg-orange-500/10",
};

function stripGroupLabel(group: TabStripGroup): string {
  return group.name.trim() || "Group";
}

// Chrome-style merged tab: the active tab shares the content surface's fill and
// flares into it through concave bottom corners. Each flare is a small square
// whose radial gradient carves a quarter-circle notch (transparent, so the
// strip behind the flare shows through), strokes a 1px arc that continues the
// tab's side border into the content card's top ring, and fills the rest with
// the surface colour. The 0.4px spread on either side of the --surface-border
// pair anti-aliases that arc.
//
// That background-color is load-bearing, not decoration: the flare's bottom row
// overlaps the content card's top ring, and in dark mode --surface-border is
// translucent (oklch(1 0 0 / 10%)), so without an opaque layer beneath it the
// arc would composite over the ring instead of replacing it and the two keylines
// would stack into a markedly lighter line right where the straight ring meets
// the curve. It therefore has to be the strip's real backdrop — the sidebar
// wrapper's fill, which is conditional and not this file's to re-derive.
// SIDEBAR_WRAPPER_FILL_CLASS reads it off the wrapper itself.
//
// The backing may only reach as far as it is needed, though. A flare hangs 9px
// past the tab's edge, over the neighbouring tab, so an opaque square there
// prints over whatever that neighbour draws: its hover pill lost the whole
// corner it shares with the flare and read as a dark bite (MUL-6160). Masking
// the notch away removes the backing exactly where the gradient is transparent
// anyway, so the notch is a hole rather than a painted-on copy of the strip and
// shows whatever is actually behind it — bare strip usually, the neighbour's
// pill where it reaches in. The mask is opaque again by the radius where the
// arc's anti-aliasing starts, so every pixel the arc and the canvas fill cover
// keeps its backing.
const TAB_FLARE_RADIUS = 10;
const tabFlareGradient = (side: "left" | "right") => {
  const r = TAB_FLARE_RADIUS;
  return `radial-gradient(circle at top ${side}, transparent ${r - 1.2}px, var(--surface-border) ${r - 0.8}px, var(--surface-border) ${r - 0.2}px, var(--page-canvas) ${r + 0.2}px)`;
};
const tabFlareNotchMask = (side: "left" | "right") => {
  const r = TAB_FLARE_RADIUS;
  return `radial-gradient(circle at top ${side}, transparent ${r - 1.6}px, black ${r - 1.2}px)`;
};
// The flares are mirror images: the offset overlaps the tab edge by 1px so arc
// and side border meet, and gradient and mask share the corner the offset picks.
const tabFlareStyle = (side: "left" | "right"): React.CSSProperties => {
  const overhang = -TAB_FLARE_RADIUS + 1;
  const mask = tabFlareNotchMask(side);
  return {
    ...(side === "left" ? { left: overhang } : { right: overhang }),
    backgroundImage: tabFlareGradient(side),
    maskImage: mask,
    WebkitMaskImage: mask,
  };
};

type TabSnapshot = {
  workspaceSlug: string | null;
  ids: Set<string>;
};

function getAddedTabIds(
  previous: TabSnapshot | null,
  workspaceSlug: string | null,
  currentIds: string[],
) {
  if (
    !previous ||
    previous.workspaceSlug !== workspaceSlug ||
    currentIds.length <= previous.ids.size
  ) {
    return [];
  }

  return currentIds.filter((id) => !previous.ids.has(id));
}

function getTabElement(
  scroller: HTMLDivElement,
  tabId?: string,
): HTMLElement | null {
  if (!tabId) {
    return scroller.querySelector<HTMLElement>('[data-tab-active="true"]');
  }

  return Array.from(
    scroller.querySelectorAll<HTMLElement>("[data-tab-id]"),
  ).find((candidate) => candidate.dataset.tabId === tabId) ?? null;
}

function getTabScrollTarget(
  scroller: HTMLDivElement,
  tab: HTMLElement,
): number {
  const scrollerRect = scroller.getBoundingClientRect();
  const tabRect = tab.getBoundingClientRect();
  const maxScrollLeft = Math.max(0, scroller.scrollWidth - scroller.clientWidth);
  const hasHiddenLeft = scroller.scrollLeft > 1;
  const hasHiddenRight = scroller.scrollLeft < maxScrollLeft - 1;
  const visibleLeft =
    scrollerRect.left + (hasHiddenLeft ? TAB_SCROLL_FADE_SIZE : 0);
  const visibleRight =
    scrollerRect.right - (hasHiddenRight ? TAB_SCROLL_FADE_SIZE : 0);

  if (tabRect.left < visibleLeft) {
    return Math.max(0, scroller.scrollLeft - (visibleLeft - tabRect.left));
  }
  if (tabRect.right > visibleRight) {
    return Math.min(
      maxScrollLeft,
      scroller.scrollLeft + (tabRect.right - visibleRight),
    );
  }
  return scroller.scrollLeft;
}

// Keep scrolling scoped to the strip. Native scrollIntoView can also move
// scrollable desktop-shell ancestors and displace the whole window chrome.
function keepTabVisible(
  scroller: HTMLDivElement | null,
  tabId?: string,
  behavior: ScrollBehavior = "auto",
) {
  if (!scroller) return;
  const tab = getTabElement(scroller, tabId);
  if (!tab) return;

  const target = getTabScrollTarget(scroller, tab);
  if (Math.abs(target - scroller.scrollLeft) <= 1) return;

  if (behavior === "smooth" && typeof scroller.scrollTo === "function") {
    scroller.scrollTo({ left: target, behavior: "smooth" });
    return;
  }
  scroller.scrollLeft = target;
}

function SortableTabItem({
  tab,
  isActive,
  isOnly,
  canCloseOthers,
  isNew,
  shouldReduceMotion,
  showSeparator,
}: {
  tab: Tab;
  isActive: boolean;
  /**
   * True iff this is the only tab in the workspace. Hiding X on the last
   * tab matches existing behavior and avoids the surprise of the store's
   * last-tab reseed kicking in. Pinned tabs always hide X (RFC §3 D3c).
   */
  isOnly: boolean;
  canCloseOthers: boolean;
  isNew: boolean;
  shouldReduceMotion: boolean;
  /**
   * Hairline on the tab's left edge — hidden next to the active tab, and
   * faded out while either of the two tabs it divides is hovered.
   */
  showSeparator: boolean;
}) {
  const setActiveTab = useTabStore((s) => s.setActiveTab);
  const closeTab = useTabStore((s) => s.closeTab);
  const closeOtherTabs = useTabStore((s) => s.closeOtherTabs);
  const togglePin = useTabStore((s) => s.togglePin);
  const updateTab = useTabStore((s) => s.updateTab);
  const createTabGroup = useTabStore((s) => s.createTabGroup);
  const addTabToGroup = useTabStore((s) => s.addTabToGroup);
  const removeTabFromGroup = useTabStore((s) => s.removeTabFromGroup);
  const toggleTabGroupCollapsed = useTabStore((s) => s.toggleTabGroupCollapsed);
  const stripGroups = useActiveGroup()?.groups ?? [];
  const ownGroup = stripGroups.find((candidate) => candidate.id === tab.groupId);
  const issueWindowPath = parseIssueWindowPath(tab.url);

  // The tab's leading visual and title are derived live from its URL and the
  // query cache — a resource's own icon/status/avatar and its real title,
  // updated as the cache updates. `tab.title` is only a persisted first-frame
  // fallback. See @multica/views useTabPresentation.
  const { visual, title } = useTabPresentation(tab.url, tab.title);

  // Persist the active tab's resolved title so it survives as the next
  // session's first-frame fallback. The tab strip itself always renders the
  // live resolved `title`; `tab.title` is just the persisted seed. This
  // replaces the old document.title → MutationObserver → tab.title path (the OS
  // window title stays page-driven via useDocumentTitle).
  useEffect(() => {
    if (!isActive) return;
    if (tab.title !== title) updateTab(tab.id, { title });
  }, [isActive, title, tab.id, tab.title, updateTab]);

  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: tab.id });

  // Pin is a secondary interaction state, not an identity: a pinned tab keeps
  // its resource visual (a project's icon, an issue's status, an actor's
  // avatar) rather than collapsing to a Pin glyph. Pinned-ness is conveyed by
  // position, the suppressed close button, and the hover Pin/Unpin action.
  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    WebkitAppRegion: "no-drag",
    zIndex: isDragging ? 20 : undefined,
  } as React.CSSProperties;

  const handleClick = () => {
    if (isActive) return;
    setActiveTab(tab.id);
  };

  const handleClose = (e: React.MouseEvent) => {
    e.stopPropagation();
    closeTab(tab.id);
  };

  const handleTogglePin = (e: React.MouseEvent) => {
    e.stopPropagation();
    togglePin(tab.id);
  };

  const stopDragOnAction = (e: React.PointerEvent) => {
    e.stopPropagation();
  };

  const handleOpenAsWindow = () => {
    if (!issueWindowPath) return;
    void window.desktopAPI.openIssueWindow({
      path: issueWindowPath.path,
      title,
    });
  };

  // Pinned tabs keep their full title (RFC §3 D1v-ii FINAL). The only visual
  // differences vs. unpinned tabs are the suppressed X (closing requires
  // explicit Unpin) — the leading visual is the resource's own identity, same
  // as an unpinned tab. Pin/Unpin is reachable via the hover action button
  // below and the right-click menu.
  const showCloseButton = !tab.pinned && !isOnly;
  const [isEntering, setIsEntering] = useState(isNew && !shouldReduceMotion);
  const [showAddedHighlight, setShowAddedHighlight] = useState(isNew);

  useEffect(() => {
    if (!isDragging) return;
    setIsEntering(false);
    setShowAddedHighlight(false);
  }, [isDragging]);

  const tabButton = (
    <button
      type="button"
      {...attributes}
      {...listeners}
      onClick={handleClick}
      // Browser convention: middle click closes the tab. Pinned (and sole)
      // tabs suppress the close affordance, so middle click follows suit.
      onAuxClick={(e) => {
        if (e.button !== 1 || !showCloseButton) return;
        e.preventDefault();
        handleClose(e);
      }}
      aria-label={tab.pinned ? `${title} (pinned)` : title}
      data-tab-active={isActive ? "true" : undefined}
      data-tab-entering={isEntering ? "true" : undefined}
      title={tab.pinned ? `${title} (pinned)` : undefined}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      className={cn(
        "group relative flex size-full min-w-0 items-center gap-1.5 px-2.5 text-caption transition-colors",
        "select-none cursor-default",
        isActive
          ? "font-medium text-foreground"
          : "text-muted-foreground hover:text-sidebar-accent-foreground",
        isDragging && "opacity-60",
      )}
    >
      <ResourceLeadingVisual visual={visual} />
      <span
        className="min-w-0 flex-1 overflow-hidden whitespace-nowrap text-left"
        style={{
          maskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
          WebkitMaskImage: "linear-gradient(to right, black calc(100% - 12px), transparent)",
        }}
      >
        {title}
      </span>
      <span
        onClick={handleTogglePin}
        onPointerDown={stopDragOnAction}
        role="button"
        aria-label={tab.pinned ? "Unpin tab" : "Pin tab"}
        title={tab.pinned ? "Unpin tab" : "Pin tab"}
        className="hidden size-3.5 shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors group-hover:flex hover:bg-muted-foreground/20 hover:text-foreground"
      >
        {tab.pinned ? <PinOff className="size-2.5" /> : <Pin className="size-2.5" />}
      </span>
      {showCloseButton && (
        <span
          onClick={handleClose}
          onPointerDown={stopDragOnAction}
          role="button"
          aria-label="Close tab"
          className="hidden size-3.5 shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors group-hover:flex hover:bg-muted-foreground/20 hover:text-foreground"
        >
          <X className="size-2.5" />
        </span>
      )}
    </button>
  );

  return (
    <div
      ref={setNodeRef}
      style={style}
      data-tab-frame
      data-tab-id={tab.id}
      className={cn("h-9 w-40 min-w-32", isActive && "z-10")}
    >
      <motion.div
        className="group/tab relative size-full"
        initial={isEntering ? { opacity: 0, x: 8 } : false}
        animate={{ opacity: 1, x: 0 }}
        transition={{ duration: isEntering ? 0.18 : 0, ease: TAB_ENTRY_EASE }}
        onAnimationComplete={() => setIsEntering(false)}
      >
        {isActive ? (
          // Merged-tab chrome: a bordered cap, a borderless base whose fill
          // runs into the content card below (covering its top ring), and two
          // flares whose arcs hand the keyline over to the card's ring. The
          // flares overlap the tab edge by 1px so arc and side border meet.
          <span
            aria-hidden
            className={cn(
              "pointer-events-none absolute inset-0",
              isDragging && "opacity-60",
            )}
          >
            {/* Keep the fill inside the translucent keyline so it matches the
                flare arcs and content card ring. */}
            <span className="absolute inset-x-0 top-0 bottom-2.5 rounded-t-lg border border-b-0 border-surface-border bg-page-canvas bg-clip-padding" />
            <span className="absolute inset-x-0 bottom-0 h-2.5 bg-page-canvas" />
            <span
              className={cn("absolute bottom-0 size-2.5", SIDEBAR_WRAPPER_FILL_CLASS)}
              style={tabFlareStyle("left")}
            />
            <span
              className={cn("absolute bottom-0 size-2.5", SIDEBAR_WRAPPER_FILL_CLASS)}
              style={tabFlareStyle("right")}
            />
          </span>
        ) : (
          <span
            aria-hidden
            className="pointer-events-none absolute inset-x-0.5 top-1 bottom-1 rounded-lg bg-sidebar-accent opacity-0 transition-opacity group-hover/tab:opacity-100"
          />
        )}
        {showSeparator && (
          // Fades in step with the neighbouring hover pill: both use a bare
          // transition-opacity, so the hairline clears exactly as the pill
          // arrives rather than lingering 2px off its rounded edge.
          <span
            aria-hidden
            className="pointer-events-none absolute left-0 top-1/2 h-4 w-px -translate-y-1/2 bg-surface-border transition-opacity group-hover/tab:opacity-0 prev-tab-hover:opacity-0"
          />
        )}
        <ContextMenu>
          <ContextMenuTrigger render={tabButton} />
          <ContextMenuContent>
            {issueWindowPath && (
              <>
                <ContextMenuItem onClick={handleOpenAsWindow}>
                  <AppWindow />
                  Open as new window
                </ContextMenuItem>
                <ContextMenuSeparator />
              </>
            )}
            {!tab.pinned && (
              <>
                <ContextMenuItem onClick={() => createTabGroup(tab.id)}>
                  New group
                </ContextMenuItem>
                {stripGroups.some((candidate) => candidate.id !== tab.groupId) && (
                  <ContextMenuSub>
                    <ContextMenuSubTrigger>Add to group</ContextMenuSubTrigger>
                    <ContextMenuSubContent>
                      {stripGroups
                        .filter((candidate) => candidate.id !== tab.groupId)
                        .map((candidate) => (
                          <ContextMenuItem
                            key={candidate.id}
                            onClick={() => addTabToGroup(tab.id, candidate.id)}
                          >
                            {stripGroupLabel(candidate)}
                          </ContextMenuItem>
                        ))}
                    </ContextMenuSubContent>
                  </ContextMenuSub>
                )}
                {ownGroup && (
                  <>
                    <ContextMenuItem onClick={() => removeTabFromGroup(tab.id)}>
                      Remove from group
                    </ContextMenuItem>
                    <ContextMenuItem
                      onClick={() => toggleTabGroupCollapsed(ownGroup.id)}
                    >
                      {ownGroup.collapsed ? "Expand group" : "Collapse group"}
                    </ContextMenuItem>
                  </>
                )}
                <ContextMenuSeparator />
              </>
            )}
            <ContextMenuItem onClick={() => togglePin(tab.id)}>
              {tab.pinned ? (
                <>
                  <PinOff />
                  Unpin tab
                </>
              ) : (
                <>
                  <Pin />
                  Pin tab
                </>
              )}
            </ContextMenuItem>
            <ContextMenuSeparator />
            <ContextMenuItem
              variant="destructive"
              disabled={tab.pinned || isOnly}
              onClick={() => closeTab(tab.id)}
            >
              <X />
              Close tab
            </ContextMenuItem>
            <ContextMenuItem
              variant="destructive"
              disabled={!canCloseOthers}
              onClick={() => closeOtherTabs(tab.id)}
            >
              <ListX />
              Close other tabs
            </ContextMenuItem>
          </ContextMenuContent>
        </ContextMenu>
        {showAddedHighlight && (
          <motion.span
            aria-hidden
            className="pointer-events-none absolute inset-x-0.5 top-1 bottom-1 rounded-lg bg-primary/10 ring-1 ring-inset ring-primary/20"
            initial={{ opacity: shouldReduceMotion ? 0.25 : 0.65 }}
            animate={{ opacity: 0 }}
            transition={{ duration: shouldReduceMotion ? 0.16 : 0.42 }}
            onAnimationComplete={() => setShowAddedHighlight(false)}
          />
        )}
      </motion.div>
    </div>
  );
}

function NewTabEdgeFeedback({
  newTabId,
  scrollerRef,
  shouldReduceMotion,
}: {
  newTabId: string | null;
  scrollerRef: RefObject<HTMLDivElement | null>;
  shouldReduceMotion: boolean;
}) {
  const sequenceRef = useRef(0);
  const [signal, setSignal] = useState<{
    tabId: string;
    sequence: number;
  } | null>(null);

  useLayoutEffect(() => {
    const scroller = scrollerRef.current;
    if (!scroller || !newTabId) return;
    const tab = getTabElement(scroller, newTabId);
    if (!tab || getTabScrollTarget(scroller, tab) === scroller.scrollLeft) {
      return;
    }

    sequenceRef.current += 1;
    setSignal({ tabId: newTabId, sequence: sequenceRef.current });
  }, [newTabId, scrollerRef]);

  if (!signal) return null;

  return (
    <motion.div
      key={`${signal.tabId}-${signal.sequence}`}
      aria-hidden
      data-new-tab-edge-feedback="true"
      className="pointer-events-none absolute top-4 bottom-1 right-0 z-20 w-8 rounded-r-lg bg-gradient-to-l from-primary/35 via-primary/10 to-transparent"
      initial={{
        opacity: shouldReduceMotion ? 0.45 : 0,
        x: shouldReduceMotion ? 0 : 4,
      }}
      animate={{
        opacity: shouldReduceMotion ? [0.45, 0] : [0, 0.8, 0],
        x: 0,
      }}
      transition={{
        opacity: {
          duration: shouldReduceMotion ? 0.2 : 0.45,
          times: shouldReduceMotion ? [0, 1] : [0, 0.18, 1],
          ease: "easeOut",
        },
        x: {
          duration: shouldReduceMotion ? 0 : 0.18,
          ease: TAB_ENTRY_EASE,
        },
      }}
      onAnimationComplete={() => {
        setSignal((current) =>
          current?.sequence === signal.sequence ? null : current,
        );
      }}
    />
  );
}

function TabGroupChip({
  group,
  count,
  containsActive,
}: {
  group: TabStripGroup;
  count: number;
  containsActive: boolean;
}) {
  const toggleTabGroupCollapsed = useTabStore((s) => s.toggleTabGroupCollapsed);
  const renameTabGroup = useTabStore((s) => s.renameTabGroup);
  const ungroupTabs = useTabStore((s) => s.ungroupTabs);
  const { setNodeRef, isOver } = useDroppable({ id: `group:${group.id}` });
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(group.name);
  const skipCommitRef = useRef(false);
  const label = stripGroupLabel(group);

  const startRename = () => {
    setDraft(group.name);
    setEditing(true);
  };

  const commitRename = () => {
    renameTabGroup(group.id, draft);
    setEditing(false);
  };

  const chip = editing ? (
    <input
      autoFocus
      aria-label="Group name"
      value={draft}
      onChange={(event) => setDraft(event.target.value)}
      onBlur={() => {
        if (skipCommitRef.current) {
          skipCommitRef.current = false;
          return;
        }
        commitRename();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          commitRename();
        } else if (event.key === "Escape") {
          event.preventDefault();
          skipCommitRef.current = true;
          setDraft(group.name);
          setEditing(false);
        }
      }}
      onPointerDown={(event) => event.stopPropagation()}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      className={cn(
        "h-6 w-24 rounded-md px-2 text-caption outline-none ring-1 ring-inset ring-foreground/30",
        GROUP_CHIP_CLASS[group.color],
      )}
    />
  ) : (
    <button
      type="button"
      aria-expanded={!group.collapsed}
      aria-label={`${label}, ${group.collapsed ? "collapsed" : "expanded"}`}
      data-tab-active={containsActive && group.collapsed ? "true" : undefined}
      onClick={() => toggleTabGroupCollapsed(group.id)}
      onDoubleClick={startRename}
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      className={cn(
        "flex h-6 max-w-40 items-center gap-1 rounded-md px-2 text-caption",
        GROUP_CHIP_CLASS[group.color],
        isOver && "ring-1 ring-inset ring-foreground/40",
      )}
    >
      {group.collapsed ? (
        <ChevronRight className="size-3 shrink-0" />
      ) : (
        <ChevronDown className="size-3 shrink-0" />
      )}
      <span className="truncate">{label}</span>
      {group.collapsed && (
        <span className="text-micro opacity-70">{count}</span>
      )}
    </button>
  );

  return (
    <div
      ref={setNodeRef}
      data-tab-group={group.id}
      data-tab-group-collapsed={group.collapsed ? "true" : "false"}
      className="mb-1 flex items-center self-end"
    >
      <ContextMenu>
        <ContextMenuTrigger render={chip} />
        <ContextMenuContent>
          <ContextMenuItem onClick={startRename}>Rename group</ContextMenuItem>
          <ContextMenuItem onClick={() => toggleTabGroupCollapsed(group.id)}>
            {group.collapsed ? "Expand group" : "Collapse group"}
          </ContextMenuItem>
          <ContextMenuItem onClick={() => ungroupTabs(group.id)}>
            Ungroup
          </ContextMenuItem>
        </ContextMenuContent>
      </ContextMenu>
    </div>
  );
}

function NewTabButton() {
  const addTab = useTabStore((s) => s.addTab);
  const setActiveTab = useTabStore((s) => s.setActiveTab);

  const handleClick = () => {
    // New tab opens in the currently active workspace — tabs are scoped
    // per workspace, so there is no cross-workspace ambiguity to resolve.
    const activeSlug = useTabStore.getState().activeWorkspaceSlug;
    if (!activeSlug) return;
    const path = paths.workspace(activeSlug).issues();
    const tabId = addTab(path, "Issues");
    if (tabId) setActiveTab(tabId);
  };

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label="New tab"
      title="New tab"
      style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
      className="mb-1 flex size-7 shrink-0 items-center justify-center self-end rounded-md text-faint-foreground transition-colors hover:bg-muted/50 hover:text-muted-foreground"
    >
      <Plus className="size-3.5" />
    </button>
  );
}

export function TabBar() {
  const group = useActiveGroup();
  const moveTab = useTabStore((s) => s.moveTab);
  const addTabToGroup = useTabStore((s) => s.addTabToGroup);
  const activeWorkspaceSlug = useTabStore((s) => s.activeWorkspaceSlug);
  const shouldReduceMotion = useReducedMotion() ?? false;
  const tabScrollRef = useRef<HTMLDivElement>(null);
  const previousTabsRef = useRef<TabSnapshot | null>(null);
  const tabFadeStyle = useScrollFade(
    tabScrollRef,
    TAB_SCROLL_FADE_SIZE,
    "horizontal",
  );

  const sensors = useSensors(
    useSensor(PointerSensor, {
      activationConstraint: { distance: 5 },
    }),
  );

  const tabs = group?.tabs ?? [];
  const activeTabId = group?.activeTabId ?? "";
  const tabIds = tabs.map((t) => t.id);
  const tabOrder = tabIds.join("\0");
  const tabLayoutKey = tabs
    .map((tab) => `${tab.id}:${tab.pinned ? "pinned" : "unpinned"}`)
    .join("\0");
  const addedTabIds = getAddedTabIds(
    previousTabsRef.current,
    activeWorkspaceSlug,
    tabIds,
  );
  const addedTabIdSet = new Set(addedTabIds);
  const newestTabId = addedTabIds.at(-1) ?? null;
  const backgroundAddedTabId =
    newestTabId && newestTabId !== activeTabId ? newestTabId : null;
  const pinnedCount = tabs.filter((t) => t.pinned).length;
  const unpinnedCount = tabs.length - pinnedCount;

  useLayoutEffect(() => {
    const currentTabIds = tabOrder ? tabOrder.split("\0") : [];
    const newlyAddedIds = getAddedTabIds(
      previousTabsRef.current,
      activeWorkspaceSlug,
      currentTabIds,
    );
    previousTabsRef.current = {
      workspaceSlug: activeWorkspaceSlug,
      ids: new Set(currentTabIds),
    };

    if (newlyAddedIds.length > 0) {
      if (newlyAddedIds.includes(activeTabId)) {
        keepTabVisible(
          tabScrollRef.current,
          activeTabId,
          shouldReduceMotion ? "auto" : "smooth",
        );
      }
      // Background additions intentionally preserve the user's current tab and
      // scroll position. NewTabEdgeFeedback handles the offscreen acknowledgement.
      return;
    }

    keepTabVisible(tabScrollRef.current);
  }, [
    activeWorkspaceSlug,
    activeTabId,
    shouldReduceMotion,
    tabLayoutKey,
    tabOrder,
  ]);

  useEffect(() => {
    const scroller = tabScrollRef.current;
    if (!scroller || typeof ResizeObserver === "undefined") return;

    // Sidebar and window resizing can hide the active tab without changing
    // activeTabId, so keep it visible as the strip's viewport changes.
    const resizeObserver = new ResizeObserver(() => keepTabVisible(scroller));
    resizeObserver.observe(scroller);
    return () => resizeObserver.disconnect();
  }, []);

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const overId = String(over.id);
    if (overId.startsWith("group:")) {
      addTabToGroup(String(active.id), overId.slice("group:".length));
      return;
    }
    const from = tabs.findIndex((t) => t.id === active.id);
    const to = tabs.findIndex((t) => t.id === over.id);
    // The store clamps the destination to within the source tab's zone
    // (pinned vs unpinned), so this call is safe even when the user tries
    // to drag across the boundary — the tab will land at the boundary.
    // Dropping onto a grouped tab also joins that group.
    if (from !== -1 && to !== -1) moveTab(from, to);
  };

  const stripGroups = group?.groups ?? [];
  const groupById = new Map(stripGroups.map((candidate) => [candidate.id, candidate]));
  const collapsedGroupIds = new Set(
    stripGroups.filter((candidate) => candidate.collapsed).map((candidate) => candidate.id),
  );
  const visibleTabIds = tabs
    .filter((tab) => !tab.groupId || !collapsedGroupIds.has(tab.groupId))
    .map((tab) => tab.id);

  type StripRun =
    | { kind: "tab"; tab: Tab; index: number }
    | { kind: "group"; group: TabStripGroup; tabs: Array<{ tab: Tab; index: number }> };

  const runs: StripRun[] = [];
  for (let index = 0; index < tabs.length; ) {
    const tab = tabs[index];
    const stripGroup =
      tab.groupId && !tab.pinned ? groupById.get(tab.groupId) : undefined;
    if (stripGroup) {
      const members: Array<{ tab: Tab; index: number }> = [];
      while (
        index < tabs.length &&
        tabs[index].groupId === stripGroup.id &&
        !tabs[index].pinned
      ) {
        members.push({ tab: tabs[index], index });
        index += 1;
      }
      runs.push({ kind: "group", group: stripGroup, tabs: members });
    } else {
      runs.push({ kind: "tab", tab, index });
      index += 1;
    }
  }

  return (
    <div className="flex h-full w-full min-w-0 max-w-full items-center justify-start gap-0.5 px-2">
      <div className="relative flex h-full min-w-0 flex-1 items-center">
        <DndContext
          sensors={sensors}
          collisionDetection={closestCenter}
          modifiers={[restrictToHorizontalAxis, restrictToParentElement]}
          onDragEnd={handleDragEnd}
        >
          {/* px-4 keeps the active tab's flares inside the scroller's clip
              (overflow clips at the padding box) and clears the content
              card's rounded top-left corner below the first tab. */}
          <div
            ref={tabScrollRef}
            data-tab-scroll-container
            className="no-scrollbar flex h-full min-w-0 flex-1 items-end overflow-x-auto overflow-y-hidden overscroll-x-contain px-4"
            style={tabFadeStyle}
          >
            <SortableContext items={visibleTabIds} strategy={horizontalListSortingStrategy}>
              {runs.map((run) => {
                if (run.kind === "group") {
                  const collapsed = run.group.collapsed;
                  const containsActive = run.tabs.some(
                    (member) => member.tab.id === activeTabId,
                  );
                  return (
                    <div
                      key={run.group.id}
                      className={cn(
                        "flex h-full items-end rounded-t-md",
                        !collapsed && GROUP_WASH_CLASS[run.group.color],
                      )}
                    >
                      <TabGroupChip
                        group={run.group}
                        count={run.tabs.length}
                        containsActive={containsActive}
                      />
                      {!collapsed &&
                        run.tabs.map(({ tab }) => (
                          <SortableTabItem
                            key={tab.id}
                            tab={tab}
                            isActive={tab.id === activeTabId}
                            isOnly={tabs.length === 1}
                            canCloseOthers={tabs.some(
                              (candidate) =>
                                candidate.id !== tab.id && !candidate.pinned,
                            )}
                            isNew={addedTabIdSet.has(tab.id)}
                            shouldReduceMotion={shouldReduceMotion}
                            showSeparator={false}
                          />
                        ))}
                    </div>
                  );
                }

                const { tab, index } = run;
                const previousTab = index > 0 ? tabs[index - 1] : null;
                return (
                  <Fragment key={tab.id}>
                    <SortableTabItem
                      tab={tab}
                      isActive={tab.id === activeTabId}
                      isOnly={tabs.length === 1}
                      canCloseOthers={tabs.some(
                        (candidate) => candidate.id !== tab.id && !candidate.pinned,
                      )}
                      isNew={addedTabIdSet.has(tab.id)}
                      shouldReduceMotion={shouldReduceMotion}
                      showSeparator={
                        !!previousTab &&
                        tab.id !== activeTabId &&
                        previousTab.id !== activeTabId &&
                        // the pinned-zone divider already separates this pair
                        !(previousTab.pinned && !tab.pinned) &&
                        // a strip group already frames its own tabs
                        !previousTab.groupId
                      }
                    />
                    {tab.pinned &&
                      index === pinnedCount - 1 &&
                      unpinnedCount > 0 && (
                        <div
                          aria-hidden
                          className="mx-1 mb-2.5 h-4 w-px shrink-0 self-end bg-surface-border"
                        />
                      )}
                  </Fragment>
                );
              })}
            </SortableContext>
          </div>
        </DndContext>
        <NewTabEdgeFeedback
          newTabId={backgroundAddedTabId}
          scrollerRef={tabScrollRef}
          shouldReduceMotion={shouldReduceMotion}
        />
      </div>
      {group && <NewTabButton />}
    </div>
  );
}
