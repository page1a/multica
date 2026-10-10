"use client";

import { useMemo, useRef, useState, type TouchEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronUp, Plus, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@multica/ui/lib/utils";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { Button } from "@multica/ui/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { Sheet, SheetContent, SheetTitle } from "@multica/ui/components/ui/sheet";
import { defaultStorage } from "@multica/core/platform";
import { useWorkspacePaths } from "@multica/core/paths";
import { chatTicketsOptions } from "@multica/core/chat/queries";
import { useSetChatTicket } from "@multica/core/chat/mutations";
import type { ChatTicket } from "@multica/core/types";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { CHAT_COLUMN, CHAT_GUTTER } from "./chat-column";
import { useStatusLabel } from "../../issues/utils/status-label";
import { useChatTicketSourceLabel } from "./chat-ticket-card";
import { ChatTicketPinDialog } from "./chat-ticket-pin-dialog";

// DENE-1667: the chat's progress bar over the tickets this chat opened or
// follows (DENE-1665's chat tickets — the same list as the in-thread ticket
// cards; DENE-1719 adds followed and pinned ones, each row saying which).
// What moved since the person last looked floats to the top as 「刚变」;
// "听汇报" asks the agent to tell the project's news (heard server-side).

const SWIPE_CLOSE_PX = 80;
// With no earlier look on this device, a move within the last day is fresh.
const FIRST_LOOK_WINDOW_MS = 24 * 60 * 60 * 1000;
const PHASE_RANK = { waiting_you: 0, in_progress: 1, done: 2 } as const;

// When the person last opened this chat's window, per person + chat.
function openedKey(userId: string, sessionId: string) {
  return `multica:chat-report-opened:${userId}:${sessionId}`;
}

function lastOpenedAt(userId: string, sessionId: string): number {
  const at = Date.parse(defaultStorage.getItem(openedKey(userId, sessionId)) ?? "");
  return Number.isNaN(at) ? Date.now() - FIRST_LOOK_WINDOW_MS : at;
}

interface ReportRow extends ChatTicket {
  fresh: boolean;
}

export function ChatReportBar({
  wsId,
  userId,
  sessionId,
  projectTitle,
  disabled,
  onHear,
}: {
  wsId: string;
  userId: string;
  sessionId: string;
  /** The chat's projects, named; empty hides 听汇报 (a report is per project). */
  projectTitle: string;
  disabled: boolean;
  onHear: (prompt: string) => void;
}) {
  const { t } = useT("chat");
  const isMobile = useIsMobile();
  const [open, setOpen] = useState(false);
  const [pinOpen, setPinOpen] = useState(false);
  const [seenAt, setSeenAt] = useState(() => lastOpenedAt(userId, sessionId));

  const { data } = useQuery({ ...chatTicketsOptions(wsId, sessionId), enabled: !!wsId && !!sessionId });

  const rows = useMemo<ReportRow[]>(() => {
    const all = (data?.tickets ?? []).map((ticket) => ({
      ...ticket,
      fresh: !!ticket.from_status && Date.parse(ticket.changed_at) > seenAt,
    }));
    // Fresh moves first, newest first; the rest by who they wait on.
    const fresh = all.filter((r) => r.fresh).sort((a, b) => Date.parse(b.changed_at) - Date.parse(a.changed_at));
    const rest = all
      .filter((r) => !r.fresh)
      .sort((a, b) => PHASE_RANK[a.phase] - PHASE_RANK[b.phase] || Date.parse(b.changed_at) - Date.parse(a.changed_at));
    return [...fresh, ...rest];
  }, [data, seenAt]);

  if (rows.length === 0) return null;

  const counts = { waiting_you: 0, in_progress: 0, done: 0 };
  for (const row of rows) counts[row.phase] += 1;
  const freshCount = rows.filter((r) => r.fresh).length;
  const hear = projectTitle
    ? () => {
        setOpen(false);
        onHear(t(($) => $.report.hear_prompt, { project: projectTitle }));
      }
    : null;

  const setWindowOpen = (next: boolean) => {
    setOpen(next);
    if (!next) {
      // Closing the window is the "look": the next opening marks only what
      // moved after it.
      const now = new Date().toISOString();
      defaultStorage.setItem(openedKey(userId, sessionId), now);
      setSeenAt(Date.parse(now));
    }
  };

  const summary = (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        aria-hidden
        className={cn("size-1.5 shrink-0 rounded-full", freshCount > 0 ? "bg-brand" : "bg-muted-foreground/40")}
      />
      <span className="truncate">
        {freshCount > 0
          ? t(($) => $.report.bar_fresh, { count: rows.length, fresh: freshCount })
          : t(($) => $.report.bar, { count: rows.length })}
        <span className="text-muted-foreground">
          {counts.waiting_you > 0 && ` · ${t(($) => $.report.waiting_you, { count: counts.waiting_you })}`}
          {counts.in_progress > 0 && ` · ${t(($) => $.report.in_progress, { count: counts.in_progress })}`}
          {counts.done > 0 && ` · ${t(($) => $.report.done, { count: counts.done })}`}
        </span>
      </span>
      <ChevronUp className="size-3.5 shrink-0 text-muted-foreground" />
    </span>
  );
  const triggerClass =
    "flex min-w-0 flex-1 items-center rounded-md py-1 text-left text-caption hover:bg-accent/60 min-h-11 md:min-h-0";

  const pin = () => {
    setWindowOpen(false);
    setPinOpen(true);
  };
  const panel = (
    <ReportPanel rows={rows} wsId={wsId} sessionId={sessionId} hearDisabled={disabled} onHear={hear} onPin={pin} />
  );

  return (
    <div className={cn(CHAT_GUTTER, "pb-1")} data-slot="chat-report-bar">
      <div className={cn(CHAT_COLUMN, "flex items-center gap-2")}>
        {isMobile ? (
          <>
            <button type="button" className={triggerClass} onClick={() => setOpen(true)}>
              {summary}
            </button>
            <MobileDrawer open={open} onOpenChange={setWindowOpen} title={t(($) => $.report.title)}>
              {panel}
            </MobileDrawer>
          </>
        ) : (
          <Popover open={open} onOpenChange={setWindowOpen}>
            <PopoverTrigger className={triggerClass}>{summary}</PopoverTrigger>
            <PopoverContent side="top" align="start" className="w-96 gap-0 p-0">
              <p className="border-b px-4 py-2 text-caption font-medium">{t(($) => $.report.title)}</p>
              {panel}
            </PopoverContent>
          </Popover>
        )}
        {hear && (
          <Button size="sm" variant="ghost" disabled={disabled} onClick={hear} className="shrink-0">
            {t(($) => $.report.hear)}
          </Button>
        )}
      </div>
      <ChatTicketPinDialog sessionId={sessionId} open={pinOpen} onOpenChange={setPinOpen} />
    </div>
  );
}

// The phone drawer: ✕, a tap on the mask, or a swipe down closes it.
function MobileDrawer({
  open,
  onOpenChange,
  title,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: React.ReactNode;
}) {
  const startY = useRef<number | null>(null);
  const [dragY, setDragY] = useState(0);
  const onTouchStart = (e: TouchEvent) => {
    startY.current = e.touches[0]?.clientY ?? null;
  };
  const onTouchMove = (e: TouchEvent) => {
    if (startY.current == null) return;
    setDragY(Math.max(0, (e.touches[0]?.clientY ?? 0) - startY.current));
  };
  const onTouchEnd = () => {
    if (dragY > SWIPE_CLOSE_PX) onOpenChange(false);
    startY.current = null;
    setDragY(0);
  };
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="bottom"
        className="max-h-[80dvh] gap-0 p-0"
        style={dragY ? { transform: `translateY(${dragY}px)`, transition: "none" } : undefined}
      >
        <div
          className="flex shrink-0 flex-col items-center pt-2 pb-1"
          onTouchStart={onTouchStart}
          onTouchMove={onTouchMove}
          onTouchEnd={onTouchEnd}
          data-slot="chat-report-drag-handle"
        >
          <span aria-hidden className="h-1 w-10 rounded-full bg-muted-foreground/30" />
          <SheetTitle className="mt-2 self-start px-4 text-body font-medium">{title}</SheetTitle>
        </div>
        {children}
      </SheetContent>
    </Sheet>
  );
}

function ReportPanel({
  rows,
  wsId,
  sessionId,
  hearDisabled,
  onHear,
  onPin,
}: {
  rows: ReportRow[];
  wsId: string;
  sessionId: string;
  hearDisabled: boolean;
  onHear: (() => void) | null;
  onPin: () => void;
}) {
  const { t } = useT("chat");
  const sourceLabel = useChatTicketSourceLabel();
  const unpin = useSetChatTicket();
  // Taking a ticket off the chat leaves the issue as it is.
  const remove = (row: ReportRow) =>
    unpin.mutate(
      { sessionId, issue: row.id, remove: true },
      {
        onSuccess: () => toast.success(t(($) => $.tickets.removed, { id: row.identifier })),
        onError: () => toast.error(t(($) => $.tickets.remove_failed, { id: row.identifier })),
      },
    );
  const timeAgo = useTimeAgo();
  const statusLabel = useStatusLabel(wsId);
  const wsPaths = useWorkspacePaths();

  return (
    <div className="flex min-h-0 flex-col">
      <ul className="min-h-0 flex-1 overflow-y-auto py-1 md:max-h-80">
        {rows.map((row) => {
          const where = row.from_status
            ? `${statusLabel(row.from_status)} → ${statusLabel(row.status)}`
            : statusLabel(row.status);
          return (
            <li key={row.id} className="group/row flex items-center hover:bg-accent/60">
              <AppLink
                href={wsPaths.issueDetail(row.id)}
                className="flex min-h-11 min-w-0 flex-1 flex-col justify-center gap-0.5 py-1.5 pl-4"
              >
                <span className="flex min-w-0 items-baseline gap-2 text-body">
                  <span className="shrink-0 text-caption text-muted-foreground">{row.identifier}</span>
                  <span className="truncate">{row.title}</span>
                </span>
                <span className="line-clamp-2 text-caption text-muted-foreground">
                  {row.fresh && <span className="text-foreground">{t(($) => $.report.just_changed)}</span>}
                  {row.fresh && "："}
                  {sourceLabel(row.source)} · {where} · {timeAgo(row.changed_at)}
                  {row.needs_you && ` · ${t(($) => $.report.needs_you)}`}
                </span>
              </AppLink>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="mr-2 shrink-0 text-muted-foreground max-sm:size-11 [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover/row:opacity-100 focus-visible:opacity-100"
                disabled={unpin.isPending}
                onClick={() => remove(row)}
                aria-label={`${t(($) => $.tickets.remove)} ${row.identifier}`}
                title={t(($) => $.tickets.remove)}
              >
                <X className="size-3.5" />
              </Button>
            </li>
          );
        })}
      </ul>
      <div className="flex items-center justify-between gap-2 border-t px-3 py-2">
        <Button size="sm" variant="ghost" onClick={onPin} className="max-sm:h-11">
          <Plus className="size-3.5" />
          {t(($) => $.tickets.pin)}
        </Button>
        {onHear && (
          <Button size="sm" disabled={hearDisabled} onClick={onHear} className="max-sm:h-11">
            {t(($) => $.report.hear)}
          </Button>
        )}
      </div>
    </div>
  );
}
