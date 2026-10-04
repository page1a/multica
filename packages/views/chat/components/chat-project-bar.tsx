"use client";

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { ChevronsUpDown, GripVertical, Pin } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { useShortcut } from "@multica/core/shortcuts";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { chatSessionProjectIds } from "@multica/core/chat/project-context";
import {
  CHAT_PROJECT_BAR_ROWS,
  chatProjectMenuGroups,
  narrowestWidthFittingAll,
  rankChatProjects,
  visibleBarProjectIds,
  type ChatProjectFilter,
  type ChatProjectMenuFilter,
  type ChatProjectMenuGrouping,
} from "@multica/core/chat/project-bar";
import type { ChatSession, Project } from "@multica/core/types";
import { useMeasuredRow } from "../../common/single-row-fit";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";
import { ProjectIcon } from "../../projects/components/project-icon";

const CHIP_GAP = 6;
/** Chip height (`h-7`). With the gap it caps the chip area at the allowed rows. */
const CHIP_HEIGHT = 28;
const CHIPS_MAX_HEIGHT =
  CHAT_PROJECT_BAR_ROWS * CHIP_HEIGHT + (CHAT_PROJECT_BAR_ROWS - 1) * CHIP_GAP;
/** Added to the reported fit width so a rounding pixel cannot wrap the last chip. */
const FIT_WIDTH_SLACK = 2;
// Tabular digits keep the trigger from getting wider as its count drops: a
// proportional "(6)" → "(7)" can differ by a pixel, which takes that pixel
// from the chips, which drops a chip, which changes the count back — forever.
const MORE_TRIGGER_CLASS = "h-7 shrink-0 rounded-full px-2.5 text-caption tabular-nums";
const MORE_GHOST_STYLE: CSSProperties = { position: "absolute", visibility: "hidden", whiteSpace: "nowrap" };
/** Not a project id. Stands in for the "no project" filter when it is promoted onto the bar. */
const NONE_CHIP = "\0none";

// Natural chip width. A mirror stretched to the bar makes every chip measure
// as wide as the bar, so the fit stays at one chip per row and the painted
// chip leaves an empty gap.
const MIRROR_STYLE: CSSProperties = {
  position: "absolute",
  left: 0,
  top: 0,
  display: "flex",
  flexDirection: "row",
  flexWrap: "nowrap",
  width: "max-content",
  gap: CHIP_GAP,
  visibility: "hidden",
  pointerEvents: "none",
};
const CHIP_SLOT_STYLE: CSSProperties = {
  display: "inline-flex",
  flex: "none",
  width: "max-content",
};
// On the bar a chip wider than the whole row is clipped instead of pushing
// the row wider than the list.
const PAINTED_SLOT_STYLE: CSSProperties = { ...CHIP_SLOT_STYLE, maxWidth: "100%" };

/**
 * Above the chat list, on up to two rows: All, then the person's pinned
 * projects, then projects they have chatted in recently. The second row only
 * appears when the first is full. Whatever still does not fit goes into More,
 * the one place every project is listed once: searched, filtered, grouped,
 * pinned or unpinned, and — for pins — reordered. Rows that are behind More
 * rather than on the bar are marked.
 */
export function ChatProjectBar({
  projects,
  sessions,
  pinnedIds,
  onTogglePin,
  onMovePin,
  filter,
  onFilterChange,
  onOpenSwitcher,
  onFitWidthChange,
}: {
  projects: Project[];
  sessions: ChatSession[];
  /** Project pins from the server-backed sidebar pin list, in pin order. */
  pinnedIds: readonly string[];
  onTogglePin: (projectId: string) => void;
  onMovePin: (fromProjectId: string, toProjectId: string) => void;
  filter: ChatProjectFilter;
  onFilterChange: (filter: ChatProjectFilter) => void;
  /** Opens the searchable jump list. Omitted on surfaces that don't switch. */
  onOpenSwitcher?: () => void;
  /**
   * Reports how wide this bar has to be for every chip to fit on its rows
   * (null until measured). The chat page turns it into the divider's
   * "fits every project" width.
   */
  onFitWidthChange?: (width: number | null) => void;
}) {
  const { t } = useT("chat");
  const { t: tProjects } = useT("projects");
  const projectSwitchChord = useShortcut("switchChatProject");

  const [menuOpen, setMenuOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [menuFilter, setMenuFilter] = useState<ChatProjectMenuFilter>("all");
  const [grouping, setGrouping] = useState<ChatProjectMenuGrouping>("pin");
  const [dragOverId, setDragOverId] = useState<string | null>(null);

  const titleById = useMemo(
    () => new Map(projects.map((project) => [project.id, project.title])),
    [projects],
  );
  // Chips and More rows carry the project's own icon, as the sidebar pins do.
  const projectById = useMemo(
    () => new Map(projects.map((project) => [project.id, project])),
    [projects],
  );

  const ranked = useMemo(
    () =>
      rankChatProjects(
        projects.map((project) => project.id),
        pinnedIds,
        sessions.map((session) => ({
          projectIds: chatSessionProjectIds(session),
          updatedAt: session.updated_at,
          status: session.status,
          hasUnread: session.has_unread,
        })),
      ),
    [projects, pinnedIds, sessions],
  );
  const stats = useMemo(() => {
    const map = new Map<string, (typeof ranked.bar)[number]>();
    for (const row of [...ranked.pinned, ...ranked.rest]) map.set(row.id, row);
    return map;
  }, [ranked]);

  const orderedIds = useMemo(() => ranked.bar.map((row) => row.id), [ranked]);
  // The open filter stays on the row even when it sits past the fold.
  const promotedId =
    filter.type === "project" ? filter.id : filter.type === "none" ? NONE_CHIP : null;
  const measureIds = useMemo(() => {
    if (!promotedId || orderedIds.includes(promotedId)) return orderedIds;
    return [...orderedIds, promotedId];
  }, [orderedIds, promotedId]);
  // The mirror holds the All chip first, then one chip per `measureIds`.
  const { containerRef, measureRef, available, widths } = useMeasuredRow();
  const leadWidth = widths[0] ?? 0;
  const widthById = new Map(measureIds.map((id, index) => [id, widths[index + 1] ?? 0]));
  const visibleIds = visibleBarProjectIds({
    ids: orderedIds,
    pinnedCount: ranked.pinned.length,
    widthById,
    available,
    gap: CHIP_GAP,
    rows: CHAT_PROJECT_BAR_ROWS,
    lead: leadWidth,
    promotedId,
  });
  const fitAvailable =
    widths.length === measureIds.length + 1
      ? narrowestWidthFittingAll(widths.slice(1), CHIP_GAP, CHAT_PROJECT_BAR_ROWS, leadWidth)
      : null;

  const rootRef = useRef<HTMLDivElement | null>(null);
  const moreRef = useRef<HTMLButtonElement | null>(null);
  const moreWhenAllFitRef = useRef<HTMLButtonElement | null>(null);
  const reportedFitWidth = useRef<number | null>(null);
  // After every commit, like the measurement itself: the chrome beside the
  // chips (padding, switcher, More) is read off the painted bar, live from
  // the DOM in one pass. More is counted at the width it has once every chip
  // fits — its label changes with the overflow count, and a fit width that
  // moved with the painted label would send the divider's snap chasing it
  // (snap → all fit → shorter label → narrower fit → one chip drops → ...).
  useLayoutEffect(() => {
    if (!onFitWidthChange) return;
    const root = rootRef.current;
    const chips = containerRef.current;
    const more = moreRef.current;
    const moreWhenAllFit = moreWhenAllFitRef.current;
    let next: number | null = null;
    if (fitAvailable != null && root && chips && chips.clientWidth > 0) {
      const chrome = root.offsetWidth - Math.floor(chips.clientWidth);
      const moreDelta = more && moreWhenAllFit ? moreWhenAllFit.offsetWidth - more.offsetWidth : 0;
      next = fitAvailable + chrome + moreDelta + FIT_WIDTH_SLACK;
    }
    if (reportedFitWidth.current === next) return;
    reportedFitWidth.current = next;
    onFitWidthChange(next);
  });
  const visibleProjectCount = visibleIds.filter(
    (id) => id !== NONE_CHIP && titleById.has(id),
  ).length;
  const overflowCount = Math.max(0, projects.length - visibleProjectCount);
  const overflowCountWhenAllFit = Math.max(
    0,
    projects.length - orderedIds.filter((id) => titleById.has(id)).length,
  );
  const menuOwnsSelection =
    (filter.type === "project" && !visibleIds.includes(filter.id)) ||
    (filter.type === "none" && !visibleIds.includes(NONE_CHIP));

  useEffect(() => {
    if (filter.type !== "project" || projects.length === 0) return;
    if (titleById.has(filter.id)) return;
    onFilterChange({ type: "all" });
  }, [filter, projects.length, titleById, onFilterChange]);

  const select = (next: ChatProjectFilter) => {
    onFilterChange(next);
    setMenuOpen(false);
  };

  const togglePin = (projectId: string) => {
    onTogglePin(projectId);
  };

  const queryText = query.trim().toLowerCase();
  const matches = (label: string) =>
    !queryText ||
    label.toLowerCase().includes(queryText) ||
    matchesPinyin(label, queryText);

  const noneLabel = t(($) => $.project_bar.none);
  // Bar projects that did not fit: what the More count stands for.
  const collapsedIds = new Set(orderedIds.filter((id) => !visibleIds.includes(id)));
  const statusById = useMemo(
    () => new Map(projects.map((project) => [project.id, project.status as string])),
    [projects],
  );
  const menuGroups = chatProjectMenuGroups({
    pinned: ranked.pinned,
    rest: ranked.rest,
    statusById,
    filter: menuFilter,
    grouping,
    matches: (id) => matches(titleById.get(id) ?? ""),
  });
  const showNone = menuFilter === "all" && matches(noneLabel);
  const groupLabel = (key: string) => {
    if (key === "pinned") return t(($) => $.project_bar.pinned);
    if (key === "unpinned") return t(($) => $.project_bar.other);
    if (key === "all" || key === "") return null;
    return tProjects(($) => $.status[key as Project["status"]]);
  };
  const menuFilters: { value: ChatProjectMenuFilter; label: string }[] = [
    { value: "all", label: t(($) => $.project_bar.filter_all) },
    { value: "pinned", label: t(($) => $.project_bar.filter_pinned) },
    { value: "unpinned", label: t(($) => $.project_bar.filter_unpinned) },
    { value: "unread", label: t(($) => $.project_bar.filter_unread) },
  ];
  const groupings: { value: ChatProjectMenuGrouping; label: string }[] = [
    { value: "pin", label: t(($) => $.project_bar.group_pin) },
    { value: "status", label: t(($) => $.project_bar.group_status) },
    { value: "none", label: t(($) => $.project_bar.group_none) },
  ];

  const chipClass = (active: boolean) =>
    cn(
      "h-7 max-w-full shrink-0 gap-1 rounded-full px-2.5 text-caption",
      active &&
        "border-foreground bg-foreground text-background hover:bg-foreground/90 hover:text-background",
    );

  const renderProjectChip = (id: string) => {
    const row = stats.get(id);
    const active = filter.type === "project" && filter.id === id;
    const title = titleById.get(id) ?? "";
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-pressed={active}
        className={chipClass(active)}
        onClick={() => select({ type: "project", id })}
      >
        {pinnedIds.includes(id) && <Pin className="size-3 shrink-0" aria-hidden />}
        <ProjectIcon project={projectById.get(id)} size="sm" />
        {row?.hasUnread && (
          <span
            aria-label={t(($) => $.project_bar.unread)}
            className={cn("size-1.5 shrink-0 rounded-full", active ? "bg-background" : "bg-brand")}
          />
        )}
        <span className="min-w-0 max-w-32 truncate">{title}</span>
        <span className={cn("tabular-nums", active ? "text-background/70" : "text-muted-foreground")}>
          {row?.chatCount ?? 0}
        </span>
      </Button>
    );
  };

  const renderSlot = (id: string) =>
    id === NONE_CHIP ? (
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-pressed={filter.type === "none"}
        className={chipClass(filter.type === "none")}
        onClick={() => select({ type: "none" })}
      >
        {noneLabel}
      </Button>
    ) : (
      renderProjectChip(id)
    );

  const allChip = (
    <Button
      type="button"
      variant="outline"
      size="sm"
      aria-pressed={filter.type === "all"}
      className={chipClass(filter.type === "all")}
      onClick={() => select({ type: "all" })}
    >
      {t(($) => $.project_bar.all)}
    </Button>
  );

  return (
    <div ref={rootRef} className="relative flex items-start gap-1.5 border-b px-2 pb-2">
      <div
        ref={containerRef}
        data-slot="chat-project-chips"
        className="flex min-w-0 flex-1 flex-wrap content-start items-center overflow-hidden"
        style={{ gap: CHIP_GAP, maxHeight: CHIPS_MAX_HEIGHT }}
      >
        <span style={PAINTED_SLOT_STYLE}>{allChip}</span>
        {visibleIds.map((id) => (
          <span key={id} style={PAINTED_SLOT_STYLE}>
            {renderSlot(id)}
          </span>
        ))}
      </div>

      {onOpenSwitcher && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                type="button"
                variant="outline"
                size="icon-sm"
                className="size-7 shrink-0 rounded-full"
                aria-label={t(($) => $.project_bar.switch)}
                onClick={onOpenSwitcher}
              />
            }
          >
            <ChevronsUpDown className="size-3.5" />
          </TooltipTrigger>
          <TooltipContent side="bottom">
            <span className="inline-flex items-center gap-1.5">
              {t(($) => $.project_bar.switch)}
              {projectSwitchChord ? <ShortcutKeycaps shortcut={projectSwitchChord} /> : null}
            </span>
          </TooltipContent>
        </Tooltip>
      )}

      <Popover
        open={menuOpen}
        onOpenChange={(open) => {
          setMenuOpen(open);
          if (open) {
            setQuery("");
            setMenuFilter("all");
          }
        }}
      >
        <PopoverTrigger
          render={
            <Button
              ref={moreRef}
              type="button"
              variant="outline"
              size="sm"
              aria-pressed={menuOwnsSelection}
              className={cn(
                MORE_TRIGGER_CLASS,
                menuOwnsSelection &&
                  "border-foreground bg-foreground text-background hover:bg-foreground/90 hover:text-background",
              )}
            />
          }
        >
          {t(($) => $.project_bar.more, { count: overflowCount })}
        </PopoverTrigger>
        <PopoverContent
          align="end"
          side="bottom"
          className="max-h-[min(32rem,var(--available-height,32rem))] w-80 gap-0 overflow-hidden p-0"
        >
          <div className="flex items-center justify-between border-b px-3 py-2">
            <span className="text-body font-medium">{t(($) => $.project_bar.menu_title)}</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 px-2 text-caption"
              onClick={() => setMenuOpen(false)}
            >
              {t(($) => $.project_bar.done)}
            </Button>
          </div>
          <div className="px-3 py-2">
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.project_bar.search)}
              aria-label={t(($) => $.project_bar.search)}
            />
          </div>
          <div className="flex flex-col gap-1.5 border-b px-3 pb-2">
            <Segmented
              label={t(($) => $.project_bar.filter_label)}
              options={menuFilters}
              value={menuFilter}
              onChange={setMenuFilter}
            />
            <Segmented
              label={t(($) => $.project_bar.group_label)}
              options={groupings}
              value={grouping}
              onChange={setGrouping}
            />
          </div>
          <div className="max-h-80 overflow-y-auto pb-2">
            {menuGroups.length === 0 && !showNone && (
              <p className="px-3 py-3 text-caption text-muted-foreground">
                {menuFilter === "pinned" && !queryText
                  ? t(($) => $.project_bar.pinned_empty)
                  : t(($) => $.project_bar.menu_empty)}
              </p>
            )}
            {menuGroups.map((group) => {
              const label = groupLabel(group.key);
              return (
                <div key={group.key} role="group" aria-label={label ?? undefined}>
                  {label && <SectionLabel>{label}</SectionLabel>}
                  {group.rows.map((row) => {
                    const pinned = pinnedIds.includes(row.id);
                    const reorderable = group.reorderable && pinned;
                    return (
                      <ProjectRow
                        key={row.id}
                        title={titleById.get(row.id) ?? ""}
                        icon={<ProjectIcon project={projectById.get(row.id)} size="sm" />}
                        countLabel={t(($) => $.project_bar.chat_count, { count: row.chatCount })}
                        pinned={pinned}
                        draggable={reorderable}
                        unread={row.hasUnread}
                        unreadLabel={t(($) => $.project_bar.unread)}
                        collapsedLabel={
                          collapsedIds.has(row.id) ? t(($) => $.project_bar.collapsed_badge) : undefined
                        }
                        dragOver={reorderable && dragOverId === row.id}
                        pinLabel={pinned ? t(($) => $.project_bar.unpin) : t(($) => $.project_bar.pin)}
                        dragLabel={t(($) => $.project_bar.drag)}
                        onSelect={() => select({ type: "project", id: row.id })}
                        onTogglePin={() => togglePin(row.id)}
                        {...(reorderable && {
                          onDragStart: (event: React.DragEvent<HTMLDivElement>) => {
                            event.dataTransfer.setData("text/plain", row.id);
                            event.dataTransfer.effectAllowed = "move";
                          },
                          onDragOver: (event: React.DragEvent<HTMLDivElement>) => {
                            event.preventDefault();
                            setDragOverId(row.id);
                          },
                          onDragLeave: () =>
                            setDragOverId((current) => (current === row.id ? null : current)),
                          onDrop: (event: React.DragEvent<HTMLDivElement>) => {
                            event.preventDefault();
                            setDragOverId(null);
                            const from = event.dataTransfer.getData("text/plain");
                            if (from) onMovePin(from, row.id);
                          },
                        })}
                      />
                    );
                  })}
                </div>
              );
            })}

            {showNone && (
              <>
                <SectionLabel>&nbsp;</SectionLabel>
                <ProjectRow
                  title={noneLabel}
                  countLabel={t(($) => $.project_bar.chat_count, {
                    count: sessions.filter(
                      (session) =>
                        session.status !== "archived" &&
                        chatSessionProjectIds(session).length === 0,
                    ).length,
                  })}
                  pinned={false}
                  hidePin
                  onSelect={() => select({ type: "none" })}
                  onTogglePin={() => {}}
                />
              </>
            )}
          </div>
        </PopoverContent>
      </Popover>
      {/* Zero box so the max-content mirror cannot widen the page. */}
      <div className="pointer-events-none absolute size-0 overflow-hidden" aria-hidden>
        {/* More as it reads once every chip fits; only its width is used. */}
        <Button
          ref={moreWhenAllFitRef}
          type="button"
          variant="outline"
          size="sm"
          tabIndex={-1}
          className={MORE_TRIGGER_CLASS}
          style={MORE_GHOST_STYLE}
        >
          {t(($) => $.project_bar.more, { count: overflowCountWhenAllFit })}
        </Button>
        <div ref={measureRef} style={MIRROR_STYLE}>
          <span style={CHIP_SLOT_STYLE}>{allChip}</span>
          {measureIds.map((id) => (
            <span key={id} style={CHIP_SLOT_STYLE}>
              {renderSlot(id)}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}

function Segmented<T extends string>({
  label,
  options,
  value,
  onChange,
}: {
  label: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div role="radiogroup" aria-label={label} className="flex items-center gap-1">
      <span className="w-8 shrink-0 text-micro text-muted-foreground">{label}</span>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={value === option.value}
          onClick={() => onChange(option.value)}
          className={cn(
            "h-6 rounded-full px-2 text-caption outline-none focus-visible:ring-1 focus-visible:ring-ring",
            value === option.value
              ? "bg-foreground text-background"
              : "text-muted-foreground hover:bg-accent hover:text-foreground",
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3 pt-2 pb-1 text-micro text-muted-foreground">{children}</div>
  );
}

function ProjectRow({
  title,
  icon,
  countLabel,
  pinned,
  draggable = pinned,
  hidePin,
  unread,
  unreadLabel,
  collapsedLabel,
  pinLabel,
  dragLabel,
  dragOver,
  onSelect,
  onTogglePin,
  onDragStart,
  onDragOver,
  onDragLeave,
  onDrop,
}: {
  title: string;
  /** The project's own icon; the no-project row has none. */
  icon?: React.ReactNode;
  countLabel: string;
  pinned: boolean;
  /** Pins are dragged to reorder; defaults to `pinned`. */
  draggable?: boolean;
  hidePin?: boolean;
  unread?: boolean;
  unreadLabel?: string;
  /** Set when the project is behind More instead of on the bar. */
  collapsedLabel?: string;
  pinLabel?: string;
  dragLabel?: string;
  dragOver?: boolean;
  onSelect: () => void;
  onTogglePin: () => void;
  onDragStart?: (event: React.DragEvent<HTMLDivElement>) => void;
  onDragOver?: (event: React.DragEvent<HTMLDivElement>) => void;
  onDragLeave?: () => void;
  onDrop?: (event: React.DragEvent<HTMLDivElement>) => void;
}) {
  return (
    <div
      draggable={draggable}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      className={cn(
        "flex items-center gap-2 px-3 py-1.5 hover:bg-accent",
        dragOver && "bg-accent",
      )}
    >
      {draggable ? (
        <span aria-label={dragLabel} className="cursor-grab text-muted-foreground select-none">
          <GripVertical className="size-3.5" />
        </span>
      ) : (
        <span className="w-3.5 shrink-0" />
      )}
      <button
        type="button"
        aria-label={`${title}, ${countLabel}`}
        onClick={onSelect}
        className="flex min-w-0 flex-1 items-center gap-2 text-left"
      >
        {unread && (
          <span aria-label={unreadLabel} className="size-1.5 shrink-0 rounded-full bg-brand" />
        )}
        {icon}
        <span className="min-w-0 flex-1 truncate text-body">{title}</span>
        {collapsedLabel && (
          <span className="shrink-0 rounded-sm border px-1 text-micro text-muted-foreground">
            {collapsedLabel}
          </span>
        )}
        <span className="shrink-0 text-caption text-muted-foreground">{countLabel}</span>
      </button>
      {!hidePin && (
        <button
          type="button"
          aria-label={pinLabel}
          aria-pressed={pinned}
          title={pinLabel}
          onClick={(event) => {
            event.stopPropagation();
            onTogglePin();
          }}
          className={cn(
            "shrink-0 rounded-sm p-1 outline-none hover:bg-accent focus-visible:ring-1 focus-visible:ring-ring",
            pinned ? "text-muted-foreground" : "text-faint-foreground hover:text-muted-foreground",
          )}
        >
          <Pin className="size-3.5" />
        </button>
      )}
    </div>
  );
}
