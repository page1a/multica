import type { InboxBoardResponse, InboxBoardRowPayload, ParkingEvent } from "../types/home";

/**
 * The one-row-per-issue inbox (DENE-882).
 *
 * Every issue lands in at most one lane, checked in this order:
 *   waiting — someone called the viewer and the call is still open, or the
 *             issue's parking record names the viewer as the next owner;
 *   stalled — the parking record says it stopped without explaining why;
 *   running — an agent is on it right now;
 *   todo    — it is assigned to the viewer and still in todo (DENE-975);
 *   fresh   — it has unread inbox rows for the viewer but none of the
 *             lanes above or below takes it (DENE-901);
 *   done    — it was finished today.
 *
 * Every row also carries how many unread inbox rows the viewer had on it when
 * the board was opened, so the board can mark what is new this visit.
 *
 * The lanes are built server-side (GET /api/inbox/board, DENE-975), so the
 * page, the CLI and an agent reading the inbox for its person all see the
 * same board. Here it is only reshaped for the page.
 */
export type BoardLane = "waiting" | "stalled" | "running" | "todo" | "fresh" | "done";

export interface BoardOwner {
  type: string;
  id: string;
}

export interface BoardRow {
  issueId: string;
  identifier: string;
  title: string;
  status?: string;
  parentIssueId: string | null;
  lane: BoardLane;
  /**
   * What put the row in its lane: the summon source or `waiting_person` for
   * waiting, the parking category for stalled, `running` / `done` otherwise.
   */
  kind: string;
  /** The parking record's stuck kind, when the lane came from one. */
  stuckKind: string;
  /** Why it stopped, in the server's or the caller's words. May be empty. */
  reason: string;
  /** What happened before it stopped, from the parking summary. May be empty. */
  before: string;
  /** Who raised the row: the caller of a summon. */
  from: BoardOwner | null;
  fromName: string;
  /** Who holds the next move; for a running row, who is on it. */
  next: BoardOwner | null;
  /** The server's name for `next`; empty when it could not name them. */
  nextName: string;
  /** The moment the row is about: call time, verdict time, start, finish. */
  at: string;
  timeline: ParkingEvent[];
  /** Unread inbox rows on this issue when the board was opened. */
  unread: number;
  children: BoardRow[];
}

export interface InboxBoard {
  waiting: BoardRow[];
  stalled: BoardRow[];
  running: BoardRow[];
  todo: BoardRow[];
  fresh: BoardRow[];
  done: BoardRow[];
}

function row(r: InboxBoardRowPayload): BoardRow {
  return {
    issueId: r.issue_id,
    identifier: r.identifier,
    title: r.title,
    status: r.status,
    parentIssueId: r.parent_issue_id,
    lane: r.lane,
    kind: r.kind,
    stuckKind: r.stuck_kind,
    reason: r.reason,
    before: r.before,
    from: r.from,
    fromName: r.from_name,
    next: r.next,
    nextName: r.next_name,
    at: r.at,
    timeline: r.timeline ?? [],
    unread: r.unread,
    children: (r.children ?? []).map(row),
  };
}

/** The server's board (GET /api/inbox/board) in the shape the page reads. */
export function boardFromResponse(res: InboxBoardResponse): InboxBoard {
  return {
    waiting: (res.waiting ?? []).map(row),
    stalled: (res.stalled ?? []).map(row),
    running: (res.running ?? []).map(row),
    todo: (res.todo ?? []).map(row),
    fresh: (res.fresh ?? []).map(row),
    done: (res.done ?? []).map(row),
  };
}

/**
 * Split the done lane at the last time the viewer looked: rows finished after
 * it are new and shown; the rest fold away. With no mark yet everything is new.
 * A row with unread activity stays shown whatever its finish time.
 */
export function splitSeenDone(
  rows: readonly BoardRow[],
  seenAt: string | null,
): { fresh: BoardRow[]; seen: BoardRow[] } {
  if (!seenAt) return { fresh: [...rows], seen: [] };
  const mark = Date.parse(seenAt);
  const fresh: BoardRow[] = [];
  const seen: BoardRow[] = [];
  for (const row of rows) (row.unread > 0 || Date.parse(row.at) > mark ? fresh : seen).push(row);
  return { fresh, seen };
}

export const BOARD_LANES: readonly BoardLane[] = ["waiting", "stalled", "running", "todo", "fresh", "done"];

/**
 * Which lane each issue sits in on the board, sub-issues filed under the lane
 * their parent row shows in. The merged inbox (DENE-1004) tags and filters
 * its notification list with this.
 */
export function boardLaneByIssue(board: InboxBoard): Map<string, BoardLane> {
  const lanes = new Map<string, BoardLane>();
  const file = (r: BoardRow, lane: BoardLane) => {
    if (!lanes.has(r.issueId)) lanes.set(r.issueId, lane);
    for (const child of r.children) file(child, lane);
  };
  for (const lane of BOARD_LANES) for (const r of board[lane]) file(r, lane);
  return lanes;
}
