import { describe, expect, it } from "vitest";
import type { AgentTask } from "../types/agent";
import type { Issue } from "../types/issue";
import type { ParkingRecord, UnreadInboxIssue, WaitingSummon } from "../types/home";
import { buildInboxBoard, foldChildren, splitSeenDone, type BoardRow } from "./board";
import { localDayWindow } from "./queries";

const ME = "user-me";

function record(over: Partial<ParkingRecord> & { issue_id: string }): ParkingRecord {
  return {
    identifier: `DENE-${over.issue_id}`,
    number: 1,
    title: `title ${over.issue_id}`,
    parent_issue_id: null,
    current_status: "in_progress",
    recorded_status: "in_progress",
    state: "parked",
    category: "stalled_unclosed",
    stuck_kind: "no_close",
    unexplained: true,
    summary: "PR 已合入",
    summary_source: "agent",
    next_owner: { type: "agent", id: "agent-1" },
    task_id: null,
    timeline: [],
    evaluated_at: "2026-09-26T08:00:00Z",
    ...over,
  };
}

function summon(over: Partial<WaitingSummon> & { issue_id: string }): WaitingSummon {
  return {
    id: `s-${over.issue_id}`,
    identifier: `DENE-${over.issue_id}`,
    issue_title: `title ${over.issue_id}`,
    issue_status: "blocked",
    issue_priority: "high",
    caller_type: "agent",
    caller_id: "agent-1",
    caller_name: "孙悟空",
    source: "needs_human",
    reason: "要你先回答 3 个问题",
    comment_id: null,
    inbox_item_id: null,
    created_at: "2026-09-26T07:00:00Z",
    ...over,
  };
}

function task(over: Partial<AgentTask> & { issue_id: string }): AgentTask {
  return {
    id: `t-${over.issue_id}`,
    agent_id: "agent-2",
    runtime_id: "rt",
    status: "running",
    priority: 0,
    dispatched_at: "2026-09-26T08:00:00Z",
    started_at: "2026-09-26T08:01:00Z",
    completed_at: null,
    result: null,
    error: null,
    created_at: "2026-09-26T08:00:00Z",
    ...over,
  } as AgentTask;
}

function issue(over: Partial<Issue> & { id: string }): Issue {
  return {
    identifier: `DENE-${over.id}`,
    title: `title ${over.id}`,
    status: "done",
    parent_issue_id: null,
    updated_at: "2026-09-26T09:00:00Z",
    ...over,
  } as Issue;
}

function unread(over: Partial<UnreadInboxIssue> & { issue_id: string }): UnreadInboxIssue {
  return {
    identifier: `DENE-${over.issue_id}`,
    title: `title ${over.issue_id}`,
    status: "in_progress",
    parent_issue_id: null,
    unread_count: 1,
    held_count: 0,
    latest_at: "2026-09-26T08:30:00Z",
    ...over,
  };
}

const empty = { summons: [], parking: [], tasks: [], runningIssues: [], doneIssues: [] };

describe("buildInboxBoard", () => {
  it("puts a called issue in waiting even when its record says stalled", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      summons: [summon({ issue_id: "1" })],
      parking: [record({ issue_id: "1" })],
    });
    expect(board.waiting.map((r) => r.issueId)).toEqual(["1"]);
    expect(board.stalled).toEqual([]);
    expect(board.waiting[0]).toMatchObject({
      reason: "要你先回答 3 个问题",
      before: "PR 已合入",
      fromName: "孙悟空",
    });
  });

  it("keeps one row per issue when several calls are open", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      summons: [
        summon({ issue_id: "1", id: "a", reason: "old", created_at: "2026-09-26T01:00:00Z" }),
        summon({ issue_id: "1", id: "b", reason: "new", created_at: "2026-09-26T02:00:00Z" }),
      ],
    });
    expect(board.waiting).toHaveLength(1);
    expect(board.waiting[0]!.reason).toBe("new");
  });

  it("lists a parking record that names the viewer as waiting", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      parking: [
        record({
          issue_id: "2",
          category: "waiting_person",
          unexplained: false,
          next_owner: { type: "member", id: ME },
          summary: "等你拍板",
        }),
        record({
          issue_id: "3",
          category: "waiting_person",
          unexplained: false,
          next_owner: { type: "member", id: "someone-else" },
        }),
      ],
    });
    expect(board.waiting.map((r) => r.issueId)).toEqual(["2"]);
    expect(board.waiting[0]!.reason).toBe("等你拍板");
  });

  it("drops stalled records a person moved since, or that are running again", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      parking: [
        record({ issue_id: "1" }),
        record({ issue_id: "2", current_status: "done" }),
        record({ issue_id: "3", current_status: "in_review" }),
        record({ issue_id: "4" }),
        record({ issue_id: "5", unexplained: false, category: "blocked" }),
      ],
      tasks: [task({ issue_id: "4" })],
      runningIssues: [issue({ id: "4", status: "in_progress" })],
    });
    expect(board.stalled.map((r) => r.issueId)).toEqual(["1"]);
    expect(board.running.map((r) => r.issueId)).toEqual(["4"]);
  });

  it("hides the fixed template wording as 'before'", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      parking: [record({ issue_id: "1", summary: "运行已结束", summary_source: "template" })],
    });
    expect(board.stalled[0]!.before).toBe("");
  });

  it("shows running issues once, with the agent on them", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      tasks: [
        task({ issue_id: "7", id: "a" }),
        task({ issue_id: "7", id: "b", agent_id: "agent-3", started_at: "2026-09-26T09:00:00Z" }),
        task({ issue_id: "8", status: "queued" }),
        task({ issue_id: "", id: "chat" }),
      ],
      runningIssues: [issue({ id: "7", status: "in_progress" })],
    });
    expect(board.running).toHaveLength(1);
    expect(board.running[0]).toMatchObject({ issueId: "7", next: { type: "agent", id: "agent-2" } });
  });

  it("folds done children under a done parent", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      doneIssues: [
        issue({ id: "p" }),
        issue({ id: "c1", parent_issue_id: "p" }),
        issue({ id: "c2", parent_issue_id: "p" }),
        issue({ id: "x", parent_issue_id: "elsewhere" }),
      ],
    });
    expect(board.done.map((r) => r.issueId).sort()).toEqual(["p", "x"]);
    expect(board.done.find((r) => r.issueId === "p")!.children.map((c) => c.issueId)).toEqual([
      "c1",
      "c2",
    ]);
  });
});

describe("buildInboxBoard unread (DENE-901)", () => {
  it("marks every lane's rows, children included, with their unread count", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      summons: [summon({ issue_id: "w" })],
      parking: [record({ issue_id: "s" })],
      tasks: [task({ issue_id: "r" })],
      runningIssues: [issue({ id: "r", status: "in_progress" })],
      doneIssues: [issue({ id: "p" }), issue({ id: "c", parent_issue_id: "p" })],
      unread: [
        unread({ issue_id: "w", unread_count: 2, held_count: 1 }),
        unread({ issue_id: "s" }),
        unread({ issue_id: "r", unread_count: 3 }),
        unread({ issue_id: "c" }),
      ],
    });
    expect(board.waiting[0]!.unread).toBe(2);
    expect(board.stalled[0]!.unread).toBe(1);
    expect(board.running[0]!.unread).toBe(3);
    const parent = board.done.find((r) => r.issueId === "p")!;
    expect(parent.unread).toBe(0);
    expect(parent.children[0]!.unread).toBe(1);
    expect(board.fresh).toEqual([]);
  });

  it("puts unread tickets no lane took in fresh, one row each, newest first", () => {
    const board = buildInboxBoard({
      ...empty,
      userId: ME,
      doneIssues: [issue({ id: "d" })],
      unread: [
        unread({ issue_id: "d" }),
        unread({ issue_id: "p", latest_at: "2026-09-26T07:00:00Z" }),
        unread({ issue_id: "c", parent_issue_id: "p", latest_at: "2026-09-26T09:00:00Z" }),
        unread({ issue_id: "old", status: "done", unread_count: 0 }),
      ],
    });
    expect(board.done.map((r) => r.issueId)).toEqual(["d"]);
    expect(board.fresh.map((r) => r.issueId)).toEqual(["c", "p"]);
    expect(board.fresh.every((r) => r.lane === "fresh" && r.children.length === 0)).toBe(true);
    expect(board.fresh[0]).toMatchObject({ identifier: "DENE-c", unread: 1 });
  });

  it("leaves every row unmarked without a snapshot", () => {
    const board = buildInboxBoard({ ...empty, userId: ME, parking: [record({ issue_id: "1" })] });
    expect(board.stalled[0]!.unread).toBe(0);
    expect(board.fresh).toEqual([]);
  });
});

describe("foldChildren", () => {
  it("leaves a child alone when its parent is in another lane", () => {
    const rows = [{ issueId: "c", parentIssueId: "p", children: [] }] as unknown as BoardRow[];
    expect(foldChildren(rows).map((r) => r.issueId)).toEqual(["c"]);
  });
});

describe("splitSeenDone", () => {
  const rows = [
    { issueId: "a", at: "2026-09-26T10:00:00Z" },
    { issueId: "b", at: "2026-09-26T08:00:00Z" },
  ] as unknown as BoardRow[];

  it("shows everything before the first visit", () => {
    expect(splitSeenDone(rows, null).fresh).toHaveLength(2);
  });

  it("keeps a seen row shown while it has unread activity", () => {
    const marked = [...rows.slice(0, 1), { ...rows[1]!, unread: 2 }];
    const { fresh, seen } = splitSeenDone(marked, "2026-09-26T11:00:00Z");
    expect(fresh.map((r) => r.issueId)).toEqual(["b"]);
    expect(seen.map((r) => r.issueId)).toEqual(["a"]);
  });

  it("folds what finished before the last visit", () => {
    const { fresh, seen } = splitSeenDone(rows, "2026-09-26T09:00:00Z");
    expect(fresh.map((r) => r.issueId)).toEqual(["a"]);
    expect(seen.map((r) => r.issueId)).toEqual(["b"]);
  });
});

describe("localDayWindow", () => {
  it("spans one local day", () => {
    const { start, end } = localDayWindow(new Date(2026, 8, 26, 15, 30));
    expect(Date.parse(end) - Date.parse(start)).toBe(24 * 3600 * 1000);
    expect(new Date(start).getHours()).toBe(0);
  });
});
