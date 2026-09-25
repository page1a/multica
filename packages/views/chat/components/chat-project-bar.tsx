"use client";

import { useEffect, useMemo, useState } from "react";
import { GripVertical, Pin } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { chatSessionProjectIds } from "@multica/core/chat/project-context";
import {
  rankChatProjects,
  visibleBarProjectIds,
  type ChatProjectFilter,
} from "@multica/core/chat/project-bar";
import {
  selectPinnedProjectIds,
  useChatProjectBarStore,
} from "@multica/core/chat/project-bar-store";
import type { ChatSession, Project } from "@multica/core/types";
import { useSingleRowFit } from "../../common/single-row-fit";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";

const CHIP_GAP = 6;
/** Not a project id. Stands in for the "no project" filter when it is promoted onto the bar. */
const NONE_CHIP = "\0none";

/**
 * One row above the chat list: All, then the person's pinned projects, then
 * projects they have chatted in recently. Whatever does not fit goes into
 * More, where every project can be searched, pinned, and — for pins — reordered.
 */
export function ChatProjectBar({
  projects,
  sessions,
  userId,
  filter,
  onFilterChange,
}: {
  projects: Project[];
  sessions: ChatSession[];
  userId: string | null;
  filter: ChatProjectFilter;
  onFilterChange: (filter: ChatProjectFilter) => void;
}) {
  const { t } = useT("chat");
  const pinnedIds = useChatProjectBarStore(selectPinnedProjectIds(userId));
  const pin = useChatProjectBarStore((s) => s.pin);
  const unpin = useChatProjectBarStore((s) => s.unpin);
  const move = useChatProjectBarStore((s) => s.move);

  const [menuOpen, setMenuOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [promotedId, setPromotedId] = useState<string | null>(null);
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
  const { containerRef, measureRef, fitCount } = useSingleRowFit({
    count: orderedIds.length,
    gap: CHIP_GAP,
    reserve: 0,
  });
  const visibleIds = visibleBarProjectIds(orderedIds, fitCount, promotedId);

  useEffect(() => {
    if (filter.type !== "project" || projects.length === 0) return;
    if (titleById.has(filter.id)) return;
    setPromotedId(null);
    onFilterChange({ type: "all" });
  }, [filter, projects.length, titleById, onFilterChange]);

  const select = (next: ChatProjectFilter, promote: string | null) => {
    onFilterChange(next);
    setPromotedId(promote);
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
  const pinnedRows = ranked.pinned.filter((row) => matches(titleById.get(row.id) ?? ""));
  const restRows = ranked.rest.filter((row) => matches(titleById.get(row.id) ?? ""));
  const showNone = matches(noneLabel);

  const chipClass = (active: boolean) =>
    cn(
      "h-7 shrink-0 gap-1 rounded-full px-2.5 text-caption",
      active &&
        "border-foreground bg-foreground text-background hover:bg-foreground/90 hover:text-background",
    );

  const renderProjectChip = (id: string) => {
    const row = stats.get(id);
    const active = filter.type === "project" && filter.id === id;
    const title = titleById.get(id) ?? "";
    return (
      <Button
        key={id}
        type="button"
        variant="outline"
        size="sm"
        aria-pressed={active}
        className={chipClass(active)}
        onClick={() => {
          const natural = orderedIds.slice(0, fitCount);
          select({ type: "project", id }, natural.includes(id) ? null : id);
        }}
      >
        {pinnedIds.includes(id) && <Pin className="size-3 shrink-0" aria-hidden />}
        {row?.hasUnread && (
          <span
            aria-label={t(($) => $.project_bar.unread)}
            className={cn("size-1.5 shrink-0 rounded-full", active ? "bg-background" : "bg-brand")}
          />
        )}
        <span className="max-w-32 truncate">{title}</span>
        <span className={cn("tabular-nums", active ? "text-background/70" : "text-muted-foreground")}>
          {row?.chatCount ?? 0}
        </span>
      </Button>
    );
  };

  return (
    <div className="flex items-center gap-1.5 border-b px-2 pb-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        aria-pressed={filter.type === "all"}
        className={chipClass(filter.type === "all")}
        onClick={() => select({ type: "all" }, null)}
      >
        {t(($) => $.project_bar.all)}
      </Button>

      <div
        ref={containerRef}
        className="relative flex min-w-0 flex-1 items-center overflow-hidden"
        style={{ gap: CHIP_GAP }}
      >
        {visibleIds.map((id) =>
          id === NONE_CHIP ? (
            <Button
              key={NONE_CHIP}
              type="button"
              variant="outline"
              size="sm"
              aria-pressed={filter.type === "none"}
              className={chipClass(filter.type === "none")}
              onClick={() => select({ type: "none" }, NONE_CHIP)}
            >
              {noneLabel}
            </Button>
          ) : (
            renderProjectChip(id)
          ),
        )}
        <div
          ref={measureRef}
          aria-hidden
          className="pointer-events-none invisible absolute flex"
          style={{ gap: CHIP_GAP }}
        >
          {orderedIds.map((id) => renderProjectChip(id))}
        </div>
      </div>

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
              type="button"
              variant="outline"
              size="sm"
              className="h-7 shrink-0 rounded-full px-2.5 text-caption"
            />
          }
        >
          {t(($) => $.project_bar.more, { count: projects.length })}
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
                  onSelect={() => select({ type: "project", id: row.id }, row.id)}
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
                onSelect={() => select({ type: "project", id: row.id }, row.id)}
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
                  onSelect={() => select({ type: "none" }, NONE_CHIP)}
                  onTogglePin={() => {}}
                />
              </>
            )}
          </div>
        </PopoverContent>
      </Popover>
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
