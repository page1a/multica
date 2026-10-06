"use client";

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode, type SyntheticEvent } from "react";
import { ChevronDown, ChevronRight, Check, FolderKanban, History, MoreHorizontal, RotateCcw, Sparkles, X } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useActorName } from "@multica/core/workspace/hooks";
import { useBatchUpdateIssues, useCreateComment, useUpdateIssue } from "@multica/core/issues/mutations";
import { useQueryClient } from "@tanstack/react-query";
import type { UpdateIssueRequest } from "@multica/core/types";
import {
  BOARD_LANES,
  homeKeys,
  splitSeenDone,
  useBoardProject,
  useDoneSeenStore,
  useInboxBoard,
  type BoardLane,
  type BoardProject,
  type BoardRow,
  type InboxBoard,
} from "@multica/core/home";
import type { ParkingEvent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";
import { AppLink, resolveClickIntent, useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { PickerItem, PropertyPicker, PICKER_TRIGGER_CLASS } from "../../issues/components/pickers/property-picker";
import { ProjectIcon } from "../../projects/components/project-icon";
import { useTimeAgo } from "../../inbox/components/inbox-list-item";
import { ACTIVITY_LAYER_PARAM, LAYER_PARAM } from "../../inbox/components/inbox-view";
import { IssuePeekHost } from "../../issues/components/issue-peek";
import {
  PEEK_TARGET_ATTR,
  useIssuePeekActions,
  useIsIssuePeeked,
} from "../../issues/surface/peek-context";


export const LANE_TAG_CLASS: Record<BoardLane, string> = {
  waiting: "bg-destructive/10 text-destructive",
  stalled: "bg-warning/15 text-warning-foreground dark:text-warning",
  running: "bg-success/10 text-success",
  blocked: "bg-muted text-foreground",
  todo: "bg-primary/10 text-primary",
  fresh: "bg-info/10 text-info",
  done: "bg-muted text-muted-foreground",
};

const LANE_PILL_CLASS: Record<BoardLane, string> = {
  waiting: "border-destructive/30 text-destructive",
  stalled: "border-warning/40 text-warning-foreground dark:text-warning",
  running: "border-success/30 text-success",
  blocked: "text-foreground",
  todo: "border-primary/30 text-primary",
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
      if (row.lane === "todo") return t(($) => $.board.tag.todo);
      if (row.lane === "blocked") return t(($) => $.board.tag.blocked);
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
        case "blocked":
          return row.next ? ownerName(row.next) : "";
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

/**
 * Set when the board sits beside the notification list in the merged inbox
 * (DENE-1004): rows open in the page's own detail pane instead of navigating
 * away, and lane headings filter the list.
 */
export interface BoardLinking {
  /** The issue to mark on the board: the open one, or the one just closed. */
  highlightIssueId?: string;
  activeLane: BoardLane | null;
  onSelectIssue: (issueId: string) => void;
  onToggleLane: (lane: BoardLane) => void;
}

const BoardLinkingContext = createContext<BoardLinking | null>(null);

type BoardStatus = NonNullable<UpdateIssueRequest["status"]>;

interface WaitingActions {
  selected: ReadonlySet<string>;
  toggleSelected: (issueId: string) => void;
  applyStatus: (issueIds: string[], status: BoardStatus) => void;
}

const WaitingActionsContext = createContext<WaitingActions | null>(null);

function useWaitingActions() {
  return useContext(WaitingActionsContext);
}

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

function WaitingRowControls({ row, copy }: { row: BoardRow; copy: BoardCopy }) {
  const actions = useWaitingActions();
  if (!actions) return null;
  const selected = actions.selected.has(row.issueId);
  const ownerName = row.nextName || row.next?.id || "";
  const returnLabel = ownerName && row.next?.type === "agent"
    ? copy.t(($) => $.board.actions.return_to_agent, { name: ownerName })
    : copy.t(($) => $.board.actions.return_to_do);
  const stop = (event: SyntheticEvent) => event.stopPropagation();
  const setStatus = (status: BoardStatus) => {
    actions.applyStatus([row.issueId], status);
  };
  return (
    <div className="flex items-center gap-1" onClick={stop} onKeyDown={stop}>
      <input
        type="checkbox"
        checked={selected}
        onChange={() => actions.toggleSelected(row.issueId)}
        aria-label={copy.t(($) => $.board.actions.select, { title: row.title })}
        data-testid="waiting-row-checkbox"
        className="size-4 rounded-sm border-muted-foreground/50 accent-primary"
      />
      <div className="hidden items-center gap-0.5 [@media(hover:hover)]:group-hover:flex [@media(hover:hover)]:group-focus-within:flex">
        <button type="button" title={copy.t(($) => $.board.actions.backlog)} aria-label={copy.t(($) => $.board.actions.backlog)} onClick={() => setStatus("backlog")} className="rounded-sm p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
          <RotateCcw className="size-3.5" />
        </button>
        <button type="button" title={copy.t(($) => $.board.actions.done)} aria-label={copy.t(($) => $.board.actions.done)} onClick={() => setStatus("done")} className="rounded-sm p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
          <Check className="size-3.5" />
        </button>
        <button type="button" title={copy.t(($) => $.board.actions.cancelled)} aria-label={copy.t(($) => $.board.actions.cancelled)} onClick={() => setStatus("cancelled")} className="rounded-sm p-1 text-muted-foreground hover:bg-accent hover:text-destructive">
          <X className="size-3.5" />
        </button>
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<button type="button" aria-label={copy.t(($) => $.board.actions.more)} className="rounded-sm p-1 text-muted-foreground hover:bg-accent hover:text-foreground" />}
        >
          <MoreHorizontal className="size-3.5" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-auto">
          <DropdownMenuItem onClick={() => setStatus("backlog")}><RotateCcw className="size-4" />{copy.t(($) => $.board.actions.backlog)}</DropdownMenuItem>
          <DropdownMenuItem onClick={() => setStatus("done")}><Check className="size-4" />{copy.t(($) => $.board.actions.done)}</DropdownMenuItem>
          <DropdownMenuItem onClick={() => setStatus("cancelled")}><X className="size-4" />{copy.t(($) => $.board.actions.cancelled)}</DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setStatus("todo")}><RotateCcw className="size-4" />{returnLabel}</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
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
  const linking = useContext(BoardLinkingContext);
  const peek = useIssuePeekActions();
  const peeked = useIsIssuePeeked(row.issueId);
  const [open, setOpen] = useState(false);
  const href = wsPaths.issueDetail(row.issueId);
  const activate = () => {
    if (peek) {
      peek.open(row.issueId);
      linking?.onSelectIssue(row.issueId);
      return;
    }
    if (linking) {
      linking.onSelectIssue(row.issueId);
      return;
    }
    push(href);
  };
  const highlighted = peeked || (!!linking && linking.highlightIssueId === row.issueId);
  const rowRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (highlighted) rowRef.current?.scrollIntoView({ block: "nearest" });
  }, [highlighted]);
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
    <div
      ref={rowRef}
      className={cn(!nested && "border-b last:border-b-0")}
      data-testid={`board-row-${row.lane}`}
      data-highlighted={highlighted ? "" : undefined}
    >
      <div
        role="link"
        tabIndex={0}
        {...{ [PEEK_TARGET_ATTR]: row.issueId }}
        onClick={activate}
        onKeyDown={(e) => {
          if (e.key === "Enter") activate();
        }}
        className={cn(
          "group grid cursor-pointer gap-3 px-4 py-3 outline-none transition-colors hover:bg-accent/40 focus-visible:bg-accent/40",
          row.lane === "waiting" && !nested
            ? "grid-cols-[auto_6.5rem_1fr_auto]"
            : nested
              ? "grid-cols-[auto_minmax(0,1fr)_auto] sm:grid-cols-[6.5rem_1fr_auto]"
              : "grid-cols-[6.5rem_1fr_auto]",
          nested && "py-2 pl-4 sm:pl-8",
          highlighted && "bg-accent/60 shadow-[inset_3px_0_0_var(--color-primary)]",
        )}
      >
        {row.lane === "waiting" && !nested && <span aria-hidden />}
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
              onClick={(e) => {
                e.stopPropagation();
                // A plain click stays in the merged inbox; modifier clicks
                // still open the issue as its own page or tab.
                if ((linking || peek) && resolveClickIntent(e) === "push") {
                  e.preventDefault();
                  activate();
                }
              }}
              className="min-w-0 break-words text-body font-medium hover:underline sm:truncate"
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
          <div className="flex items-center gap-2">
            <span>{time}</span>
            {row.lane === "waiting" && !nested && <WaitingRowControls row={row} copy={copy} />}
          </div>
        </div>
      </div>
      {open && (
        <div className={cn("space-y-3 px-4 pb-3 sm:pl-[8.75rem]", nested && "sm:pl-[10.75rem]")}>
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
  const linking = useContext(BoardLinkingContext);
  const title = copy.t(($) => $.board.lanes[lane].title);
  const hint = copy.t(($) => $.board.lanes[lane].hint);
  const active = linking?.activeLane === lane;
  return (
    <section aria-label={title} data-testid={`board-lane-${lane}`}>
      <div className="mb-2 flex items-baseline gap-2">
        {linking ? (
          <h2 className="text-body font-semibold">
            <button
              type="button"
              aria-pressed={active}
              title={copy.t(($) => $.board.lane_filter_hint)}
              onClick={() => linking.onToggleLane(lane)}
              data-testid={`board-lane-filter-${lane}`}
              className={cn(
                "rounded-sm outline-none transition-colors hover:text-primary focus-visible:ring-1 focus-visible:ring-ring",
                active && "text-primary",
              )}
            >
              {title}
              <span className="ml-1.5 font-normal text-muted-foreground tabular-nums">{rows.length}</span>
            </button>
          </h2>
        ) : (
          <h2 className="text-body font-semibold">{title}</h2>
        )}
        <span className="text-caption text-muted-foreground">{hint}</span>
      </div>
      <div className={cn("overflow-hidden rounded-lg border bg-card", active && "ring-2 ring-primary/60")}>
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

function WaitingSelectionToolbar({ copy }: { copy: BoardCopy }) {
  const actions = useWaitingActions();
  if (!actions || actions.selected.size === 0) return null;
  const ids = [...actions.selected];
  return (
    <div className="mb-2 flex items-center gap-2 rounded-md border bg-card px-3 py-2" data-testid="waiting-selection-toolbar">
      <span className="text-caption font-medium">{copy.t(($) => $.board.actions.selected, { count: ids.length })}</span>
      <div className="ml-auto flex items-center gap-1">
        <Button size="sm" variant="ghost" onClick={() => actions.applyStatus(ids, "backlog")}>{copy.t(($) => $.board.actions.backlog)}</Button>
        <Button size="sm" variant="ghost" onClick={() => actions.applyStatus(ids, "done")}>{copy.t(($) => $.board.actions.done)}</Button>
        <Button size="sm" variant="ghost" onClick={() => actions.applyStatus(ids, "cancelled")}>{copy.t(($) => $.board.actions.cancelled)}</Button>
        <Button size="sm" variant="ghost" onClick={() => actions.applyStatus(ids, "todo")}>{copy.t(($) => $.board.actions.return_to_do)}</Button>
        <Button size="sm" variant="ghost" onClick={() => ids.forEach((id) => actions.toggleSelected(id))}>{copy.t(($) => $.board.actions.clear_selection)}</Button>
      </div>
    </div>
  );
}

/**
 * The board's lanes (DENE-882): one row per issue. Rows that were unread on
 * arrival keep a marker for this visit (DENE-901). Shared by the stand-alone
 * board and the merged inbox, which passes `linking`.
 */
export function InboxBoardLanes({
  board,
  isLoading,
  isError,
  linking,
}: {
  board: InboxBoard;
  isLoading: boolean;
  isError: boolean;
  linking?: BoardLinking;
}) {
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const updateIssue = useUpdateIssue();
  const batchUpdate = useBatchUpdateIssues();
  const copy = useBoardCopy();
  const peek = useIssuePeekActions();
  const [selectedIds, setSelectedIds] = useState<Set<string>>(() => new Set());
  const [hiddenIds, setHiddenIds] = useState<Set<string>>(() => new Set());
  const selected = useMemo(() => selectedIds, [selectedIds]);

  const toggleSelected = (issueId: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(issueId)) next.delete(issueId);
      else next.add(issueId);
      return next;
    });
  };

  const rowsById = useMemo(() => {
    const map = new Map<string, BoardRow>();
    const visit = (rows: BoardRow[]) => rows.forEach((row) => {
      map.set(row.issueId, row);
      visit(row.children);
    });
    visit(board.waiting);
    return map;
  }, [board.waiting]);

  const applyStatus = (issueIds: string[], status: BoardStatus) => {
    const ids = [...new Set(issueIds)].filter((id) => rowsById.has(id));
    if (ids.length === 0) return;
    const previous = ids.map((id) => ({ id, status: rowsById.get(id)?.status }));
    setHiddenIds((current) => new Set([...current, ...ids]));
    setSelectedIds((current) => {
      const next = new Set(current);
      ids.forEach((id) => next.delete(id));
      return next;
    });
    const isBatch = ids.length > 1;
    const write = !isBatch
      ? updateIssue.mutateAsync({ id: ids[0]!, status })
      : batchUpdate.mutateAsync({ ids, updates: { status } });
    void write.then((result) => {
      void queryClient.invalidateQueries({ queryKey: homeKeys.all(wsId) });

      const rejected = isBatch && "rejected" in result ? result.rejected : [];
      const rejectedIds = new Set(rejected.map(({ issue_id }) => issue_id));
      const successfulIds = ids.filter((id) => !rejectedIds.has(id));
      if (rejected.length > 0) {
        setHiddenIds((current) => {
          const next = new Set(current);
          rejectedIds.forEach((id) => next.delete(id));
          return next;
        });
        const reason = rejected[0]?.reason;
        toast.error(reason || copy.t(($) => $.board.errors.status_failed));
      }
      if (successfulIds.length === 0) return;

      const successfulPrevious = previous.filter(({ id }) => successfulIds.includes(id));
      toast.success(
        status === "cancelled"
          ? copy.t(($) => $.board.toasts.cancelled)
          : copy.t(($) => $.board.toasts.status_changed),
        {
          action: {
            label: copy.t(($) => $.board.actions.undo),
            onClick: () => {
              const restore = successfulPrevious.map(({ id, status: oldStatus }) =>
                oldStatus ? updateIssue.mutateAsync({ id, status: oldStatus }) : Promise.resolve(),
              );
              setHiddenIds((current) => {
                const next = new Set(current);
                successfulIds.forEach((id) => next.delete(id));
                return next;
              });
              void Promise.all(restore).then(
                () => void queryClient.invalidateQueries({ queryKey: homeKeys.all(wsId) }),
                () => toast.error(copy.t(($) => $.board.errors.undo_failed)),
              );
            },
          },
        },
      );
    }).catch((error: unknown) => {
      setHiddenIds((current) => {
        const next = new Set(current);
        ids.forEach((id) => next.delete(id));
        return next;
      });
      toast.error(error instanceof Error && error.message ? error.message : copy.t(($) => $.board.errors.status_failed));
    });
  };

  const filteredRows = useMemo(() => {
    const filter = (rows: BoardRow[], hideWaiting = false): BoardRow[] => rows
      .filter((row) => !hideWaiting || !hiddenIds.has(row.issueId))
      .map((row) => ({ ...row, children: filter(row.children, hideWaiting) }));
    return {
      waiting: filter(board.waiting, true),
      stalled: filter(board.stalled),
      running: filter(board.running),
      blocked: filter(board.blocked),
      todo: filter(board.todo),
      fresh: filter(board.fresh),
      done: filter(board.done),
    };
  }, [board, hiddenIds]);

  // Once the board refetch shows a changed issue in another lane, release its
  // local optimistic hiding so an undo can still restore it from the toast.
  useEffect(() => {
    const waitingIds = new Set<string>();
    const visit = (rows: BoardRow[]) => rows.forEach((row) => {
      waitingIds.add(row.issueId);
      visit(row.children);
    });
    visit(board.waiting);
    setHiddenIds((current) => {
      const next = new Set([...current].filter((id) => waitingIds.has(id)));
      return next.size === current.size ? current : next;
    });
    setSelectedIds((current) => {
      const next = new Set([...current].filter((id) => waitingIds.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [board.waiting]);

  // "Done today" shows once. Read the mark left by the previous visit, then
  // move it to now — on arrival and again on leaving, so rows that finish
  // while the page is open also count as seen next time.
  const [seenBefore] = useState(() => useDoneSeenStore.getState().seenAt[wsId] ?? null);
  const markSeen = useDoneSeenStore((s) => s.markSeen);
  useEffect(() => {
    markSeen(wsId, new Date().toISOString());
    return () => markSeen(wsId, new Date().toISOString());
  }, [markSeen, wsId]);
  const done = useMemo(() => splitSeenDone(filteredRows.done, seenBefore), [filteredRows.done, seenBefore]);
  const [showSeen, setShowSeen] = useState(false);
  // The issue being pointed at may sit among the folded rows.
  const highlightSeen = !!linking?.highlightIssueId && done.seen.some((r) => r.issueId === linking.highlightIssueId);

  const peekOrder = useMemo(() => {
    const ordered: string[] = [];
    const visit = (rows: BoardRow[]) => rows.forEach((row) => {
      ordered.push(row.issueId);
      visit(row.children);
    });
    visit(filteredRows.waiting);
    visit(filteredRows.stalled);
    visit(filteredRows.running);
    visit(filteredRows.blocked);
    visit(filteredRows.todo);
    visit(filteredRows.fresh);
    visit(done.fresh);
    if (showSeen || highlightSeen) visit(done.seen);
    return ordered;
  }, [filteredRows, done.fresh, done.seen, highlightSeen, showSeen]);

  useEffect(() => {
    if (!peek) return;
    peek.publishColumns([peekOrder]);
    return () => peek.publishColumns(null);
  }, [peek, peekOrder]);

  const seenFooter =
    done.seen.length > 0 ? (
      <>
        {(showSeen || highlightSeen) && done.seen.map((row) => <BoardRowView key={row.issueId} row={row} copy={copy} />)}
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
    <BoardLinkingContext.Provider value={linking ?? null}>
      <WaitingActionsContext.Provider value={{ selected, toggleSelected, applyStatus }}>
      <div className="space-y-3">
        <div className="flex flex-wrap gap-2">
          {BOARD_LANES.map((lane) => {
            const pill = (
              <>
                <b className="mr-1 font-semibold tabular-nums">{filteredRows[lane].length}</b>
                {copy.t(($) => $.board.lanes[lane].title)}
              </>
            );
            const className = cn("rounded-full border px-2.5 py-0.5 text-caption", LANE_PILL_CLASS[lane]);
            return linking ? (
              <button
                key={lane}
                type="button"
                aria-pressed={linking.activeLane === lane}
                onClick={() => linking.onToggleLane(lane)}
                className={cn(className, "transition-colors hover:bg-accent/60", linking.activeLane === lane && "bg-accent")}
              >
                {pill}
              </button>
            ) : (
              <span key={lane} className={className}>
                {pill}
              </span>
            );
          })}
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
          <WaitingSelectionToolbar copy={copy} />
          <LaneSection lane="waiting" rows={filteredRows.waiting} copy={copy} />
          <LaneSection lane="stalled" rows={filteredRows.stalled} copy={copy} />
          <LaneSection lane="running" rows={filteredRows.running} copy={copy} />
          {filteredRows.blocked.length > 0 && <LaneSection lane="blocked" rows={filteredRows.blocked} copy={copy} />}
          <LaneSection lane="todo" rows={filteredRows.todo} copy={copy} />
          {filteredRows.fresh.length > 0 && <LaneSection lane="fresh" rows={filteredRows.fresh} copy={copy} />}
          <LaneSection lane="done" rows={done.fresh} copy={copy} footer={seenFooter} />
        </>
      )}
      </WaitingActionsContext.Provider>
    </BoardLinkingContext.Provider>
  );
}

/**
 * The inbox board on its own page — the compact-width inbox (DENE-882). Wide
 * screens show it beside the notification list instead (DENE-1004).
 */
function HomePageContent() {
  const wsId = useWorkspaceId();
  const wsPaths = useWorkspacePaths();
  const copy = useBoardCopy();
  const project = useBoardProject(wsId);
  const { board, isLoading, isError } = useInboxBoard(wsId, { projectId: project.projectId });

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader>
        <h1 className="flex-1 text-body font-semibold">{copy.t(($) => $.board.title)}</h1>
        <BoardProjectControls project={project} />
        <Button
          variant="ghost"
          size="sm"
          className="min-h-11 min-w-11 justify-center text-muted-foreground sm:min-h-0 sm:min-w-0"
          aria-label={copy.t(($) => $.board.activity_link)}
          nativeButton={false}
          render={<AppLink href={`${wsPaths.inbox()}?${LAYER_PARAM}=${ACTIVITY_LAYER_PARAM}`} />}
        >
          <History className="size-4" />
          <span className="hidden sm:inline">{copy.t(($) => $.board.activity_link)}</span>
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-4xl space-y-6 px-6 py-6">
          <InboxBoardLanes board={board} isLoading={isLoading} isError={isError} />
        </div>
      </div>
    </div>
  );
}

export function HomePage() {
  return (
    <IssuePeekHost>
      <HomePageContent />
    </IssuePeekHost>
  );
}

/**
 * The board's project dropdown and the "walk me through it" button beside it
 * (DENE-1019). The button follows the dropdown: with a project picked it sends
 * the chat there, and the prompt names the project so the agent reads the board
 * with `--project` and tells the same tickets the page shows.
 */
export function BoardProjectControls({ project }: { project: BoardProject }) {
  const { t } = useT("inbox");
  const { t: tChat } = useT("chat");
  const { projects, project: current } = project;
  const [open, setOpen] = useState(false);
  const pick = (id: string | null) => {
    project.setProjectId(id);
    setOpen(false);
  };
  const prompt = current
    ? tChat(($) => $.conversation_starters.inbox.prompt_project, { name: current.title, id: current.id })
    : tChat(($) => $.conversation_starters.inbox.prompt);
  return (
    <>
      <div className="inline-flex min-w-0" data-testid="board-project-filter">
        <PropertyPicker
          open={open}
          onOpenChange={setOpen}
          width="w-56"
          align="end"
          searchable={projects.length > 8}
          triggerRender={
            <button
              type="button"
              aria-label={t(($) => $.board.project_filter_aria)}
              className={cn(PICKER_TRIGGER_CLASS, "h-7 max-w-48 rounded-md border px-2 text-caption")}
            />
          }
          trigger={
            <>
              {current ? (
                <ProjectIcon project={current} size="sm" />
              ) : (
                <FolderKanban className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="truncate">{current?.title ?? t(($) => $.board.project_all)}</span>
              <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
            </>
          }
        >
          <PickerItem emptyValue selected={!current} onClick={() => pick(null)}>
            <FolderKanban className="size-3.5 text-muted-foreground" />
            <span className="text-muted-foreground">{t(($) => $.board.project_all)}</span>
          </PickerItem>
          {projects.map((p) => (
            <PickerItem key={p.id} selected={p.id === current?.id} onClick={() => pick(p.id)}>
              <ProjectIcon project={p} size="sm" />
              <span className="truncate">{p.title}</span>
            </PickerItem>
          ))}
        </PropertyPicker>
      </div>
      <BoardAskAiButton
        prompt={prompt}
        label={current ? t(($) => $.board.ask_ai_project) : t(($) => $.board.ask_ai)}
        projectIds={current ? [current.id] : []}
      />
    </>
  );
}

/** DENE-975: the chat agent reads this same board (`multica inbox board`) and tells it back. */
export function BoardAskAiButton({ prompt, label, projectIds = [] }: { prompt: string; label: string; projectIds?: readonly string[] }) {
  const wsPaths = useWorkspacePaths();
  return (
    <Button
      variant="ghost"
      size="sm"
      className="min-h-11 min-w-11 justify-center text-muted-foreground sm:min-h-0 sm:min-w-0"
      aria-label={label}
      nativeButton={false}
      render={<AppLink href={wsPaths.chatWithPrompt(prompt, ...projectIds)} data-testid="board-ask-ai" />}
    >
      <Sparkles className="size-4" />
      <span className="hidden sm:inline">{label}</span>
    </Button>
  );
}
