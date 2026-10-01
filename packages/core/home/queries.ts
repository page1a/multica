import { queryOptions, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { onInboxInvalidate, onInboxSummaryInvalidate } from "../inbox/ws-updaters";
import { boardFromResponse, type InboxBoard } from "./board";

export const homeKeys = {
  all: (wsId: string) => ["workspaces", wsId, "home"] as const,
  board: (wsId: string, tz: string, unreadSince: string | null, projectId: string | null = null) =>
    [...homeKeys.all(wsId), "board", tz, unreadSince ?? "live", projectId ?? "all"] as const,
};

// Server verdicts change at a run's end and on replies; WS events invalidate
// `homeKeys.all` right away (use-realtime-sync). The stale time is only the
// reconnect / missed-event safety net.
const HOME_STALE_TIME = 30 * 1000;

/** This browser's IANA zone; "done today" is counted in it. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/**
 * The board as the server builds it (DENE-975). Without `unreadSince` the
 * unread markers are live; with it, rows read at or after that moment still
 * count, which replays a visit's markers after the visit marked all read.
 */
export function inboxBoardOptions(
  wsId: string,
  tz: string,
  unreadSince: string | null,
  projectId: string | null = null,
) {
  return queryOptions({
    queryKey: homeKeys.board(wsId, tz, unreadSince, projectId),
    queryFn: () =>
      api.getInboxBoard({ tz, unread_since: unreadSince ?? undefined, project_id: projectId ?? undefined }),
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

export interface InboxBoardResult {
  board: InboxBoard;
  isLoading: boolean;
  isError: boolean;
}

const EMPTY_BOARD: InboxBoard = { waiting: [], stalled: [], running: [], todo: [], fresh: [], done: [] };

/**
 * The inbox lanes for this visit. Opening the board reads everything
 * (DENE-901): the first read is live, and once it lands the visit notes the
 * server's clock (`as_of`), marks all read, and from then on asks for the
 * board with `unread_since` set to that mark. The markers of this visit
 * survive the read itself and every later refetch; rows an open call hangs on
 * are skipped by the server and stay unread.
 *
 * `projectId` narrows the board to one project (DENE-1019). The arrival read
 * marks everything read, including other projects' rows the viewer is not
 * looking at, so a narrowed board never reads on arrival.
 *
 * The mark is the server's clock, not this device's, so a skewed clock cannot
 * drop or double the markers. The ref keeps StrictMode's double effect from
 * reading twice.
 *
 * `autoRead: false` skips the arrival read and keeps asking for the live
 * board: the merged inbox (DENE-1004) shows the notification list beside the
 * board, and reading everything on arrival would wipe that list's unread
 * badges before the viewer could see them.
 */
export function useInboxBoard(
  wsId: string,
  { autoRead = true, projectId = null }: { autoRead?: boolean; projectId?: string | null } = {},
): InboxBoardResult {
  const qc = useQueryClient();
  const tz = useMemo(browserTimeZone, []);
  const [visit, setVisit] = useState<{ wsId: string; since: string } | null>(null);
  const since = visit?.wsId === wsId ? visit.since : null;

  const query = useQuery({
    ...inboxBoardOptions(wsId, tz, since, projectId),
    // The arrival read must be fresh: a cached board from an earlier visit
    // would mark the wrong things read.
    ...(since === null ? { staleTime: 0, refetchOnMount: "always" as const } : {}),
    // Keep this workspace's board up while the replay loads; never another's.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === wsId ? prev : undefined),
  });

  const marked = useRef<string | null>(null);
  const arrival = autoRead && !projectId && since === null && query.isFetchedAfterMount && !query.isFetching && !query.isPlaceholderData
    ? query.data
    : undefined;
  useEffect(() => {
    if (!arrival || marked.current === wsId) return;
    marked.current = wsId;
    const mark = arrival.as_of;
    const read = arrival.unread_markable > 0
      // A failed read still leaves the markers to show; the badge just stays.
      ? api.markAllInboxRead().then(
          () => {
            void onInboxInvalidate(qc, wsId);
            void onInboxSummaryInvalidate(qc);
          },
          () => undefined,
        )
      : Promise.resolve();
    void read.then(() => setVisit({ wsId, since: mark }));
  }, [arrival, wsId, qc]);

  const board = useMemo(() => (query.data ? boardFromResponse(query.data) : EMPTY_BOARD), [query.data]);
  return { board, isLoading: query.isLoading, isError: query.isError };
}
