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
  narrowestWidthFittingAll,
  rankChatProjects,
  visibleBarProjectIds,
  type ChatProjectFilter,
} from "@multica/core/chat/project-bar";
import {
  selectPinnedProjectIds,
  useChatProjectBarStore,
} from "@multica/core/chat/project-bar-store";
import type { ChatSession, Project } from "@multica/core/types";
import { useMeasuredRow } from "../../common/single-row-fit";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

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
 * which lists those collapsed projects first and is also where every project
 * can be searched, pinned, and — for pins — reordered.
 */
export function ChatProjectBar({
  projects,
  sessions,
  userId,
  filter,
  onFilterChange,
  onOpenSwitcher,
  onFitWidthChange,
}: {
  projects: Project[];
  sessions: ChatSession[];
  userId: string | null;
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
  const projectSwitchChord = useShortcut("switchChatProject");
  const pinnedIds = useChatProjectBarStore(selectPinnedProjectIds(userId));
  const pin = useChatProjectBarStore((s) => s.pin);
  const unpin = useChatProjectBarStore((s) => s.unpin);
  const move = useChatProjectBarStore((s) => s.move);

  const [menuOpen, setMenuOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [dragOverId, setDragOverId] = useState<string | null>(null);

  const titleById = useMemo(
    () => new Map(projects.map((project) => [project.id, project.title])),
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
    if (!userId) return;
    if (pinnedIds.includes(projectId)) unpin(userId, projectId);
    else pin(userId, projectId);
  };

  const queryText = query.trim().toLowerCase();
  const matches = (label: string) =>
    !queryText ||
    label.toLowerCase().includes(queryText) ||
    matchesPinyin(label, queryText);

  const noneLabel = t(($) => $.project_bar.none);
  // Bar projects that did not fit. Collapsed pins stay under Pinned, where
  // they can also be reordered, so no project is listed twice.
  const collapsedIds = new Set(
    orderedIds.filter((id) => !visibleIds.includes(id) && !pinnedIds.includes(id)),
  );
  const collapsedRows = ranked.bar.filter(
    (row) => collapsedIds.has(row.id) && matches(titleById.get(row.id) ?? ""),
  );
  const pinnedRows = ranked.pinned.filter((row) => matches(titleById.get(row.id) ?? ""));
  const restRows = ranked.rest.filter(
    (row) => !collapsedIds.has(row.id) && matches(titleById.get(row.id) ?? ""),
  );
  const showNone = matches(noneLabel);

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
          if (open) setQuery("");
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
          className="max-h-[min(28rem,var(--available-height,28rem))] w-72 gap-0 overflow-hidden p-0"
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
          <div className="max-h-80 overflow-y-auto pb-2">
            {collapsedRows.length > 0 && (
              <div role="group" aria-label={t(($) => $.project_bar.collapsed)}>
                <SectionLabel>{t(($) => $.project_bar.collapsed)}</SectionLabel>
                {collapsedRows.map((row) => (
                  <ProjectRow
                    key={row.id}
                    title={titleById.get(row.id) ?? ""}
                    countLabel={t(($) => $.project_bar.chat_count, { count: row.chatCount })}
                    pinned={false}
                    pinLabel={t(($) => $.project_bar.pin)}
                    onSelect={() => select({ type: "project", id: row.id })}
                    onTogglePin={() => togglePin(row.id)}
                  />
                ))}
              </div>
            )}

            <SectionLabel>{t(($) => $.project_bar.pinned)}</SectionLabel>
            {pinnedRows.length === 0 ? (
              <p className="px-3 py-1.5 text-caption text-muted-foreground">
                {t(($) => $.project_bar.pinned_empty)}
              </p>
            ) : (
              pinnedRows.map((row) => (
                <ProjectRow
                  key={row.id}
                  title={titleById.get(row.id) ?? ""}
                  countLabel={t(($) => $.project_bar.chat_count, { count: row.chatCount })}
                  pinned
                  dragOver={dragOverId === row.id}
                  pinLabel={t(($) => $.project_bar.unpin)}
                  dragLabel={t(($) => $.project_bar.drag)}
                  onSelect={() => select({ type: "project", id: row.id })}
                  onTogglePin={() => togglePin(row.id)}
                  onDragStart={(event) => {
                    event.dataTransfer.setData("text/plain", row.id);
                    event.dataTransfer.effectAllowed = "move";
                  }}
                  onDragOver={(event) => {
                    event.preventDefault();
                    setDragOverId(row.id);
                  }}
                  onDragLeave={() => setDragOverId((current) => (current === row.id ? null : current))}
                  onDrop={(event) => {
                    event.preventDefault();
                    setDragOverId(null);
                    const from = event.dataTransfer.getData("text/plain");
                    if (userId && from) move(userId, from, row.id);
                  }}
                />
              ))
            )}

            <SectionLabel>{t(($) => $.project_bar.other)}</SectionLabel>
            {restRows.map((row) => (
              <ProjectRow
                key={row.id}
                title={titleById.get(row.id) ?? ""}
                countLabel={t(($) => $.project_bar.chat_count, { count: row.chatCount })}
                pinned={false}
                pinLabel={t(($) => $.project_bar.pin)}
                onSelect={() => select({ type: "project", id: row.id })}
                onTogglePin={() => togglePin(row.id)}
              />
            ))}

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

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3 pt-2 pb-1 text-micro text-muted-foreground">{children}</div>
  );
}

function ProjectRow({
  title,
  countLabel,
  pinned,
  hidePin,
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
  countLabel: string;
  pinned: boolean;
  hidePin?: boolean;
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
      draggable={pinned}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
      className={cn(
        "flex items-center gap-2 px-3 py-1.5 hover:bg-accent",
        dragOver && "bg-accent",
      )}
    >
      {pinned ? (
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
        <span className="min-w-0 flex-1 truncate text-body">{title}</span>
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
