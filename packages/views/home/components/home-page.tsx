"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ChevronDown, ChevronRight, History } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useCreateComment } from "@multica/core/issues/mutations";
import {
  splitSeenDone,
  useBoardUnreadSnapshot,
  useDoneSeenStore,
  useInboxBoard,
  type BoardLane,
  type BoardRow,
} from "@multica/core/home";
import type { ParkingEvent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { useTimeAgo } from "../../inbox/components/inbox-list-item";
import { ACTIVITY_LAYER_PARAM, LAYER_PARAM } from "../../inbox/components/inbox-view";


const LANE_TAG_CLASS: Record<BoardLane, string> = {
  waiting: "bg-destructive/10 text-destructive",
  stalled: "bg-warning/15 text-warning-foreground dark:text-warning",
  running: "bg-success/10 text-success",
  fresh: "bg-info/10 text-info",
  done: "bg-muted text-muted-foreground",
};

const LANE_PILL_CLASS: Record<BoardLane, string> = {
  waiting: "border-destructive/30 text-destructive",
  stalled: "border-warning/40 text-warning-foreground dark:text-warning",
  running: "border-success/30 text-success",
  fresh: "border-info/30 text-info",
  done: "text-muted-foreground",
};

function useBoardCopy() {
  const { t } = useT("inbox");
  const { getActorName } = useActorName();
  const timeAgo = useTimeAgo();

  {
    const tag = (row: BoardRow): string => {
      const tags = t(($) => $.board.tag, { returnObjects: true }) as Record<string, string>;
      if (row.lane === "waiting") return tags[row.kind] ?? t(($) => $.board.tag.waiting_default);
      if (row.lane === "stalled") return tags[row.kind] ?? t(($) => $.board.stuck.default);
      if (row.lane === "fresh") return t(($) => $.board.tag.fresh);
      return row.lane === "running" ? t(($) => $.board.tag.running) : t(($) => $.board.tag.done);
    };
    const reason = (row: BoardRow): string => {
      if (row.reason) return row.reason;
      if (row.lane !== "stalled") return "";
      const stuck = t(($) => $.board.stuck, { returnObjects: true }) as Record<string, string>;
      return stuck[row.stuckKind] ?? t(($) => $.board.stuck.default);
    };
    const ownerName = (o: { type: string; id: string }): string =>
      o.type === "issue" ? t(($) => $.board.next_issue, { id: o.id }) : getActorName(o.type, o.id);
    const meta = (row: BoardRow): string => {
      switch (row.lane) {
        case "waiting":
          return t(($) => $.board.from_to_you, {
            name: row.fromName || (row.from ? getActorName(row.from.type, row.from.id) : "Multica"),
          });
        case "stalled":
          return row.next
            ? t(($) => $.board.next, { name: ownerName(row.next) })
            : t(($) => $.board.next_none);
        case "running":
          return row.next ? getActorName(row.next.type, row.next.id) : "";
        default:
          return "";
      }
    };
    const event = (e: ParkingEvent): string => {
      const labels = t(($) => $.board.event, { returnObjects: true }) as Record<string, string>;
      const label = labels[e.kind] ?? e.kind;
      return e.detail ? `${label}：${e.detail}` : label;
    };
    return { t, tag, reason, meta, event, timeAgo, getActorName };
  }
}

type BoardCopy = ReturnType<typeof useBoardCopy>;

function formatClock(iso: string): string {
  const d = new Date(iso);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function Timeline({ events, copy }: { events: ParkingEvent[]; copy: BoardCopy }) {
  return (
    <ol className="space-y-1 border-l pl-3" data-testid="board-timeline">
      {events.map((e, i) => (
        <li
          key={`${e.at}-${e.kind}-${i}`}
          className={cn(
            "flex gap-3 text-caption text-muted-foreground",
            (e.kind === "run_failed" || e.kind === "rejected") && "text-foreground",
          )}
        >
          <time className="shrink-0 tabular-nums">{formatClock(e.at)}</time>
          <span className="min-w-0 break-words">{copy.event(e)}</span>
        </li>
      ))}
      <li className="flex gap-3 text-caption text-muted-foreground">
        <span className="shrink-0">…</span>
        <span>{copy.t(($) => $.board.event.after)}</span>
      </li>
    </ol>
  );
}

function ReplyBox({ row, copy }: { row: BoardRow; copy: BoardCopy }) {
  const [draft, setDraft] = useState("");
  const createComment = useCreateComment(row.issueId);
  const callerName =
    row.fromName || (row.from ? copy.getActorName(row.from.type, row.from.id) : "");
  const submit = () => {
    const content = draft.trim();
    if (!content || createComment.isPending) return;
    createComment.mutate(
      { content },
      {
        onSuccess: () => {
          setDraft("");
          toast.success(copy.t(($) => $.board.reply_sent));
        },
        onError: () => toast.error(copy.t(($) => $.board.reply_failed)),
      },
    );
  };
  return (
    <form
      className="flex gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <input
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        placeholder={
          callerName
            ? copy.t(($) => $.board.reply_placeholder, { name: callerName })
            : copy.t(($) => $.board.reply_placeholder_default)
        }
        className="h-8 min-w-0 flex-1 rounded-md border bg-background px-2.5 text-body outline-none focus-visible:ring-1 focus-visible:ring-ring"
      />
      <Button type="submit" size="sm" disabled={!draft.trim() || createComment.isPending}>
        {copy.t(($) => $.board.reply)}
      </Button>
    </form>
  );
}

function BoardRowView({
  row,
  copy,
  nested = false,
}: {
  row: BoardRow;
  copy: BoardCopy;
  nested?: boolean;
}) {
  const wsPaths = useWorkspacePaths();
  const { push } = useNavigation();
  const [open, setOpen] = useState(false);
  const href = wsPaths.issueDetail(row.issueId);
  const reason = copy.reason(row);
  const meta = copy.meta(row);
  const hasTimeline = row.lane === "stalled" && row.timeline.length > 0;
  const canReply = row.lane === "waiting" && !nested;
  const expandable = hasTimeline || canReply || row.children.length > 0;
  const time =
    row.lane === "running"
      ? copy.t(($) => $.board.running_for, { time: copy.timeAgo(row.at) })
      : copy.timeAgo(row.at);

  return (
    <div className={cn(!nested && "border-b last:border-b-0")} data-testid={`board-row-${row.lane}`}>
      <div
        role="link"
        tabIndex={0}
        onClick={() => push(href)}
        onKeyDown={(e) => {
          if (e.key === "Enter") push(href);
        }}
        className={cn(
          "grid cursor-pointer grid-cols-[6.5rem_1fr_auto] gap-3 px-4 py-3 outline-none transition-colors hover:bg-accent/40 focus-visible:bg-accent/40",
          nested && "py-2 pl-8",
        )}
      >
        <span
          className={cn(
            "h-fit w-fit rounded-sm px-1.5 py-0.5 text-caption font-medium",
            LANE_TAG_CLASS[row.lane],
          )}
        >
          {copy.tag(row)}
        </span>
        <div className="min-w-0 space-y-0.5">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 text-caption text-muted-foreground tabular-nums">
              {row.identifier}
            </span>
            <AppLink
              href={href}
              onClick={(e) => e.stopPropagation()}
              className="truncate text-body font-medium hover:underline"
            >
              {row.title}
            </AppLink>
            {row.unread > 0 && (
              <span
                className="inline-flex shrink-0 items-center gap-1 text-[11px] font-medium text-info"
                data-testid="board-row-unread"
              >
                <span className="size-1.5 rounded-full bg-info" aria-hidden />
                {copy.t(($) => $.board.unread, { count: row.unread })}
              </span>
            )}
            {row.lane === "stalled" && (
              <span className="shrink-0 rounded-sm bg-info/10 px-1 text-[11px] text-info">
                {copy.t(($) => $.board.server_mark)}
              </span>
            )}
          </div>
          {reason && <p className="text-body text-foreground">{reason}</p>}
          {row.before && (
            <p className="line-clamp-2 text-caption text-muted-foreground">
              {copy.t(($) => $.board.before, { text: row.before })}
            </p>
          )}
          {expandable && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                setOpen((v) => !v);
              }}
              className="mt-1 inline-flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground"
            >
              {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
              {row.children.length > 0
                ? copy.t(($) => $.board.children, { count: row.children.length })
                : open
                  ? copy.t(($) => $.board.collapse)
                  : canReply
                    ? copy.t(($) => $.board.reply)
                    : copy.t(($) => $.board.expand)}
            </button>
          )}
        </div>
        <div className="flex flex-col items-end gap-0.5 text-right text-caption text-muted-foreground">
          {meta && <span className="text-foreground">{meta}</span>}
          <span>{time}</span>
        </div>
      </div>
      {open && (
        <div className={cn("space-y-3 px-4 pb-3 pl-[8.75rem]", nested && "pl-[10.75rem]")}>
          {hasTimeline && <Timeline events={row.timeline} copy={copy} />}
          {canReply && <ReplyBox row={row} copy={copy} />}
          {row.children.length > 0 && (
            <div className="rounded-md border">
              {row.children.map((child) => (
                <BoardRowView key={child.issueId} row={child} copy={copy} nested />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function LaneSection({
  lane,
  rows,
  copy,
  footer,
}: {
  lane: BoardLane;
  rows: BoardRow[];
  copy: BoardCopy;
  footer?: ReactNode;
}) {
  const title = copy.t(($) => $.board.lanes[lane].title);
  const hint = copy.t(($) => $.board.lanes[lane].hint);
  return (
    <section aria-label={title} data-testid={`board-lane-${lane}`}>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="text-body font-semibold">{title}</h2>
        <span className="text-caption text-muted-foreground">{hint}</span>
      </div>
      <div className="overflow-hidden rounded-lg border bg-card">
        {rows.length === 0 && !footer ? (
          <p className="px-4 py-3 text-caption text-muted-foreground">
            {copy.t(($) => $.board.lanes[lane].empty)}
          </p>
        ) : (
          rows.map((row) => <BoardRowView key={row.issueId} row={row} copy={copy} />)
        )}
        {footer}
      </div>
    </section>
  );
}

/**
 * The inbox: one row per issue in five lanes (DENE-882). Arriving reads
 * everything but open calls; rows that were unread on arrival keep a marker
 * for this visit (DENE-901).
 */
export function HomePage() {
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const copy = useBoardCopy();
  const unread = useBoardUnreadSnapshot(wsId);
  const { board, isLoading, isError } = useInboxBoard(wsId, userId, undefined, unread);

  // "Done today" shows once. Read the mark left by the previous visit, then
  // move it to now — on arrival and again on leaving, so rows that finish
  // while the page is open also count as seen next time.
  const [seenBefore] = useState(() => useDoneSeenStore.getState().seenAt[wsId] ?? null);
  const markSeen = useDoneSeenStore((s) => s.markSeen);
  useEffect(() => {
    markSeen(wsId, new Date().toISOString());
    return () => markSeen(wsId, new Date().toISOString());
  }, [markSeen, wsId]);
  const done = useMemo(() => splitSeenDone(board.done, seenBefore), [board.done, seenBefore]);
  const [showSeen, setShowSeen] = useState(false);

  const counts: Record<BoardLane, number> = {
    waiting: board.waiting.length,
    stalled: board.stalled.length,
    running: board.running.length,
    fresh: board.fresh.length,
    done: board.done.length,
  };
  const lanes: BoardLane[] = ["waiting", "stalled", "running", "fresh", "done"];

  const seenFooter =
    done.seen.length > 0 ? (
      <>
        {showSeen && done.seen.map((row) => <BoardRowView key={row.issueId} row={row} copy={copy} />)}
        <button
          type="button"
          onClick={() => setShowSeen((v) => !v)}
          className="flex w-full items-center gap-1 border-t px-4 py-2 text-left text-caption text-muted-foreground hover:bg-accent/40 hover:text-foreground"
        >
          {showSeen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          {showSeen
            ? copy.t(($) => $.board.hide_seen)
            : copy.t(($) => $.board.seen, { count: done.seen.length })}
        </button>
      </>
    ) : undefined;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <h1 className="flex-1 text-body font-semibold">{copy.t(($) => $.board.title)}</h1>
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          nativeButton={false}
          render={<AppLink href={`${wsPaths.inbox()}?${LAYER_PARAM}=${ACTIVITY_LAYER_PARAM}`} />}
        >
          <History className="size-4" />
          {copy.t(($) => $.board.activity_link)}
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl space-y-6 px-6 py-6">
          <div className="space-y-3">
            <p className="text-caption text-muted-foreground">{copy.t(($) => $.board.lede)}</p>
            <div className="flex flex-wrap gap-2">
              {lanes.map((lane) => (
                <span
                  key={lane}
                  className={cn("rounded-full border px-2.5 py-0.5 text-caption", LANE_PILL_CLASS[lane])}
                >
                  <b className="mr-1 font-semibold tabular-nums">{counts[lane]}</b>
                  {copy.t(($) => $.board.lanes[lane].title)}
                </span>
              ))}
            </div>
            {isError && <p className="text-caption text-destructive">{copy.t(($) => $.board.load_error)}</p>}
          </div>
          {isLoading ? (
            <div className="space-y-3">
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-24 w-full" />
            </div>
          ) : (
            <>
              <LaneSection lane="waiting" rows={board.waiting} copy={copy} />
              <LaneSection lane="stalled" rows={board.stalled} copy={copy} />
              <LaneSection lane="running" rows={board.running} copy={copy} />
              {board.fresh.length > 0 && <LaneSection lane="fresh" rows={board.fresh} copy={copy} />}
              <LaneSection lane="done" rows={done.fresh} copy={copy} footer={seenFooter} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
