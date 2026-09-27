import type { AgentTask } from "../types/agent";
import type { Issue } from "../types/issue";
import type { ParkingEvent, ParkingRecord, UnreadInboxIssue, WaitingSummon } from "../types/home";

/**
 * The one-row-per-issue inbox (DENE-882).
 *
 * Every issue lands in at most one lane, checked in this order:
 *   waiting — someone called the viewer and the call is still open, or the
 *             issue's parking record names the viewer as the next owner;
 *   stalled — the parking record says it stopped without explaining why;
 *   running — an agent is on it right now;
 *   fresh   — it has unread inbox rows for the viewer but none of the
 *             lanes above or below takes it (DENE-901);
 *   done    — it was finished today.
 *
 * Every row also carries how many unread inbox rows the viewer had on it when
 * the board was opened, so the board can mark what is new this visit.
 *
 * The board only reads server verdicts. It never decides on its own that an
 * issue is stuck: the category comes from the parking record, the call from
 * the summon table.
 */
export type BoardLane = "waiting" | "stalled" | "running" | "fresh" | "done";

export interface BoardOwner {
  type: string;
  id: string;
}

export interface BoardRow {
  issueId: string;
  identifier: string;
  title: string;
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
  fresh: BoardRow[];
  done: BoardRow[];
}

export interface InboxBoardInput {
  userId: string | null;
  summons: readonly WaitingSummon[];
  parking: readonly ParkingRecord[];
  tasks: readonly AgentTask[];
  /** Issues the running tasks point at, for titles and parents. */
  runningIssues: readonly Issue[];
  /** Issues finished today. */
  doneIssues: readonly Issue[];
  /** The unread snapshot taken when the board was opened. */
  unread?: readonly UnreadInboxIssue[];
}

const RUNNING_TASK_STATUSES = new Set<AgentTask["status"]>([
  "dispatched",
  "running",
]);

const CLOSED_STATUSES = new Set(["done", "cancelled"]);

/** The parking summary, unless it is only the fixed wording for its category. */
function spokenSummary(record: ParkingRecord | undefined): string {
  if (!record || record.summary_source === "template") return "";
  return record.summary.trim();
}

/** A record still describes the issue only while nobody moved it by hand since. */
function recordIsCurrent(record: ParkingRecord): boolean {
  return record.current_status === record.recorded_status;
}

function owner(o: { type: string; id: string } | null | undefined): BoardOwner | null {
  if (!o || !o.type || o.type === "none" || !o.id) return null;
  return { type: o.type, id: o.id };
}

function newestFirst(a: BoardRow, b: BoardRow): number {
  return Date.parse(b.at) - Date.parse(a.at);
}

/**
 * Nest rows under a parent in the same lane. A child whose parent sits in a
 * different lane, or nowhere, stays a row of its own.
 */
export function foldChildren(rows: BoardRow[]): BoardRow[] {
  const byId = new Map(rows.map((row) => [row.issueId, { ...row, children: [] as BoardRow[] }]));
  const top: BoardRow[] = [];
  for (const row of rows) {
    const node = byId.get(row.issueId)!;
    const parent = row.parentIssueId ? byId.get(row.parentIssueId) : undefined;
    if (parent && parent !== node) parent.children.push(node);
    else top.push(node);
  }
  return top;
}

export function buildInboxBoard(input: InboxBoardInput): InboxBoard {
  const records = new Map(input.parking.map((r) => [r.issue_id, r]));
  const placed = new Set<string>();

  const activeByIssue = new Map<string, AgentTask>();
  for (const task of input.tasks) {
    if (!task.issue_id || !RUNNING_TASK_STATUSES.has(task.status)) continue;
    const seen = activeByIssue.get(task.issue_id);
    const began = task.started_at ?? task.dispatched_at ?? task.created_at;
    const seenBegan = seen ? (seen.started_at ?? seen.dispatched_at ?? seen.created_at) : null;
    if (!seen || (seenBegan && Date.parse(began) < Date.parse(seenBegan))) {
      activeByIssue.set(task.issue_id, task);
    }
  }

  // --- waiting: open calls on the viewer, newest call per issue.
  const waiting: BoardRow[] = [];
  const summons = [...input.summons].sort(
    (a, b) => Date.parse(b.created_at) - Date.parse(a.created_at),
  );
  for (const s of summons) {
    if (placed.has(s.issue_id) || CLOSED_STATUSES.has(s.issue_status)) continue;
    placed.add(s.issue_id);
    const record = records.get(s.issue_id);
    waiting.push({
      issueId: s.issue_id,
      identifier: s.identifier,
      title: s.issue_title,
      parentIssueId: record?.parent_issue_id ?? null,
      lane: "waiting",
      kind: s.source,
      stuckKind: "",
      reason: s.reason.trim(),
      before: spokenSummary(record),
      from: s.caller_type === "system" ? null : owner({ type: s.caller_type, id: s.caller_id ?? "" }),
      fromName: s.caller_name,
      next: input.userId ? { type: "member", id: input.userId } : null,
      at: s.created_at,
      timeline: record?.timeline ?? [],
      unread: 0,
      children: [],
    });
  }
  // A parking record that names the viewer counts too, even without a call row.
  for (const r of input.parking) {
    if (placed.has(r.issue_id) || !input.userId) continue;
    if (r.category !== "waiting_person" || !recordIsCurrent(r)) continue;
    if (r.next_owner.type !== "member" || r.next_owner.id !== input.userId) continue;
    if (CLOSED_STATUSES.has(r.current_status) || activeByIssue.has(r.issue_id)) continue;
    placed.add(r.issue_id);
    waiting.push({
      issueId: r.issue_id,
      identifier: r.identifier,
      title: r.title,
      parentIssueId: r.parent_issue_id,
      lane: "waiting",
      kind: "waiting_person",
      stuckKind: r.stuck_kind,
      reason: spokenSummary(r),
      before: "",
      from: null,
      fromName: "",
      next: owner(r.next_owner),
      at: r.evaluated_at,
      timeline: r.timeline,
      unread: 0,
      children: [],
    });
  }

  // --- stalled: the server's "stopped without saying why".
  const stalled: BoardRow[] = [];
  for (const r of input.parking) {
    if (placed.has(r.issue_id) || !r.unexplained || !recordIsCurrent(r)) continue;
    if (CLOSED_STATUSES.has(r.current_status) || activeByIssue.has(r.issue_id)) continue;
    placed.add(r.issue_id);
    stalled.push({
      issueId: r.issue_id,
      identifier: r.identifier,
      title: r.title,
      parentIssueId: r.parent_issue_id,
      lane: "stalled",
      kind: r.category,
      stuckKind: r.stuck_kind,
      reason: "",
      before: spokenSummary(r),
      from: null,
      fromName: "",
      next: owner(r.next_owner),
      at: r.evaluated_at,
      timeline: r.timeline,
      unread: 0,
      children: [],
    });
  }

  // --- running: one row per issue an agent is on.
  const issuesById = new Map(input.runningIssues.map((i) => [i.id, i]));
  const running: BoardRow[] = [];
  for (const [issueId, task] of activeByIssue) {
    if (placed.has(issueId)) continue;
    const issue = issuesById.get(issueId);
    const record = records.get(issueId);
    const identifier = issue?.identifier ?? record?.identifier;
    if (!identifier) continue;
    placed.add(issueId);
    running.push({
      issueId,
      identifier,
      title: issue?.title ?? record?.title ?? "",
      parentIssueId: issue?.parent_issue_id ?? record?.parent_issue_id ?? null,
      lane: "running",
      kind: "running",
      stuckKind: "",
      reason: "",
      before: "",
      from: null,
      fromName: "",
      next: { type: "agent", id: task.agent_id },
      at: task.started_at ?? task.dispatched_at ?? task.created_at,
      timeline: [],
      unread: 0,
      children: [],
    });
  }

  // --- done today.
  const done: BoardRow[] = [];
  for (const issue of input.doneIssues) {
    if (placed.has(issue.id) || issue.status !== "done") continue;
    placed.add(issue.id);
    const record = records.get(issue.id);
    done.push({
      issueId: issue.id,
      identifier: issue.identifier,
      title: issue.title,
      parentIssueId: issue.parent_issue_id,
      lane: "done",
      kind: "done",
      stuckKind: "",
      reason: "",
      before: spokenSummary(record),
      from: null,
      fromName: "",
      next: null,
      at: issue.updated_at,
      timeline: [],
      unread: 0,
      children: [],
    });
  }

  // --- fresh: unread tickets no other lane took, one row each.
  const unreadById = new Map((input.unread ?? []).map((u) => [u.issue_id, u]));
  const fresh: BoardRow[] = [];
  for (const u of unreadById.values()) {
    if (placed.has(u.issue_id) || u.unread_count <= 0) continue;
    placed.add(u.issue_id);
    fresh.push({
      issueId: u.issue_id,
      identifier: u.identifier,
      title: u.title,
      parentIssueId: u.parent_issue_id,
      lane: "fresh",
      kind: "fresh",
      stuckKind: "",
      reason: "",
      before: spokenSummary(records.get(u.issue_id)),
      from: null,
      fromName: "",
      next: null,
      at: u.latest_at,
      timeline: [],
      unread: 0,
      children: [],
    });
  }

  const mark = (rows: BoardRow[]) => {
    for (const row of rows) row.unread = unreadById.get(row.issueId)?.unread_count ?? 0;
    return rows.sort(newestFirst);
  };

  return {
    waiting: foldChildren(mark(waiting)),
    stalled: foldChildren(mark(stalled)),
    running: foldChildren(mark(running)),
    // A ticket per row: a child here is new on its own account, not part of
    // its parent's story.
    fresh: mark(fresh),
    done: foldChildren(mark(done)),
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
