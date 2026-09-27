import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "../api";
import { agentTaskSnapshotOptions } from "../agents/queries";
import { onInboxInvalidate, onInboxSummaryInvalidate } from "../inbox/ws-updaters";
import type { UnreadInboxIssue } from "../types/home";
import { buildInboxBoard, type InboxBoard } from "./board";

export const homeKeys = {
  all: (wsId: string) => ["workspaces", wsId, "home"] as const,
  parking: (wsId: string) => [...homeKeys.all(wsId), "parking"] as const,
  summons: (wsId: string) => [...homeKeys.all(wsId), "summons"] as const,
  doneToday: (wsId: string, dayStart: string) =>
    [...homeKeys.all(wsId), "done-today", dayStart] as const,
  issuesByIds: (wsId: string, ids: readonly string[]) =>
    [...homeKeys.all(wsId), "issues", ids.join(",")] as const,
};

// Server verdicts change at a run's end and on replies; WS events invalidate
// `homeKeys.all` right away (use-realtime-sync). The stale time is only the
// reconnect / missed-event safety net.
const HOME_STALE_TIME = 30 * 1000;

export function parkingRecordsOptions(wsId: string) {
  return queryOptions({
    queryKey: homeKeys.parking(wsId),
    queryFn: () => api.listIssueParkingRecords({ limit: 500 }),
    select: (data) => data.records ?? [],
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

export function waitingSummonsOptions(wsId: string) {
  return queryOptions({
    queryKey: homeKeys.summons(wsId),
    queryFn: () => api.listWaitingSummons(),
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

/** Local midnight of `now`, and of the next day, as ISO strings. */
export function localDayWindow(now: Date): { start: string; end: string } {
  const start = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const end = new Date(start);
  end.setDate(end.getDate() + 1);
  return { start: start.toISOString(), end: end.toISOString() };
}

export function doneTodayOptions(wsId: string, day: { start: string; end: string }) {
  return queryOptions({
    queryKey: homeKeys.doneToday(wsId, day.start),
    queryFn: () =>
      api.listIssues({
        status: "done",
        date_field: "updated_at",
        date_start: day.start,
        date_end: day.end,
        sort_by: "updated_at",
        sort_direction: "desc",
        limit: 100,
      }),
    select: (data) => data.issues,
    staleTime: HOME_STALE_TIME,
    refetchOnWindowFocus: true,
  });
}

export function issuesByIdsOptions(wsId: string, ids: readonly string[]) {
  return queryOptions({
    queryKey: homeKeys.issuesByIds(wsId, ids),
    queryFn: () => api.listIssues({ ids: [...ids], limit: ids.length }),
    select: (data) => data.issues,
    staleTime: HOME_STALE_TIME,
  });
}

/**
 * Opening the board reads everything (DENE-901): take the unread snapshot
 * first, then mark all read. The snapshot is what the board marks as new for
 * this visit; it lives in the mutation, so the refetches the read itself
 * triggers cannot wipe the markers mid-visit. Rows an open call hangs on are
 * skipped by the server and stay unread.
 *
 * Runs once per workspace per mount; the ref keeps StrictMode's double effect
 * from reading twice. The result is kept in state rather than read from the
 * mutation: StrictMode's remount detaches the mutation observer, and the
 * snapshot would land nowhere.
 */
export function useBoardUnreadSnapshot(wsId: string): readonly UnreadInboxIssue[] | undefined {
  const qc = useQueryClient();
  const { mutateAsync } = useMutation({
    mutationFn: async () => {
      const unread = await api.listUnreadInboxIssues();
      // A failed read still leaves the markers to show; the badge just stays.
      if (unread.some((u) => u.unread_count > u.held_count)) {
        await api.markAllInboxRead().catch(() => undefined);
      }
      return unread;
    },
    onSettled: () => {
      void onInboxInvalidate(qc, wsId);
      void onInboxSummaryInvalidate(qc);
    },
  });
  const [snapshot, setSnapshot] = useState<{ wsId: string; rows: UnreadInboxIssue[] } | null>(null);
  const took = useRef<string | null>(null);
  useEffect(() => {
    if (took.current === wsId) return;
    took.current = wsId;
    mutateAsync().then(
      (rows) => setSnapshot({ wsId, rows }),
      () => undefined,
    );
  }, [wsId, mutateAsync]);
  return snapshot?.wsId === wsId ? snapshot.rows : undefined;
}

export interface InboxBoardResult {
  board: InboxBoard;
  isLoading: boolean;
  isError: boolean;
}

const EMPTY: never[] = [];

/**
 * The lanes of the inbox, assembled from the summon list, the parking
 * records, the workspace task snapshot and today's finished issues.
 */
export function useInboxBoard(
  wsId: string,
  userId: string | null,
  now: Date = new Date(),
  unread?: readonly UnreadInboxIssue[],
): InboxBoardResult {
  const dayStart = localDayWindow(now).start;
  const day = useMemo(() => localDayWindow(new Date(dayStart)), [dayStart]);

  const summons = useQuery(waitingSummonsOptions(wsId));
  const parking = useQuery(parkingRecordsOptions(wsId));
  const tasks = useQuery(agentTaskSnapshotOptions(wsId));
  const done = useQuery(doneTodayOptions(wsId, day));

  const runningIds = useMemo(() => {
    const ids = new Set<string>();
    for (const t of tasks.data ?? EMPTY) {
      if (t.issue_id && (t.status === "running" || t.status === "dispatched")) ids.add(t.issue_id);
    }
    return [...ids].sort();
  }, [tasks.data]);
  const runningIssues = useQuery({
    ...issuesByIdsOptions(wsId, runningIds),
    enabled: runningIds.length > 0,
  });

  const board = useMemo(
    () =>
      buildInboxBoard({
        userId,
        summons: summons.data ?? EMPTY,
        parking: parking.data ?? EMPTY,
        tasks: tasks.data ?? EMPTY,
        runningIssues: runningIds.length > 0 ? (runningIssues.data ?? EMPTY) : EMPTY,
        doneIssues: done.data ?? EMPTY,
        unread,
      }),
    [userId, summons.data, parking.data, tasks.data, runningIds.length, runningIssues.data, done.data, unread],
  );

  return {
    board,
    isLoading: summons.isLoading || parking.isLoading || tasks.isLoading || done.isLoading,
    isError: summons.isError && parking.isError,
  };
}
