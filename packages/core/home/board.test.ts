import { describe, expect, it } from "vitest";
import type { InboxBoardResponse, InboxBoardRowPayload } from "../types/home";
import { boardFromResponse, boardLaneByIssue, splitSeenDone, type BoardRow } from "./board";

// The lane rules live server-side since DENE-975 (server/internal/inboxboard,
// whose tests carry the cases that used to be here). The page only reshapes.

function payload(over: Partial<InboxBoardRowPayload> & { issue_id: string }): InboxBoardRowPayload {
  return {
    identifier: `DENE-${over.issue_id}`,
    title: `title ${over.issue_id}`,
    parent_issue_id: null,
    lane: "stalled",
    kind: "stalled_unclosed",
    stuck_kind: "no_close",
    reason: "",
    before: "PR 已合入",
    from: null,
    from_name: "",
    next: { type: "agent", id: "agent-1" },
    next_name: "孙悟空",
    at: "2026-09-26T08:00:00Z",
    timeline: null,
    unread: 0,
    children: null,
    ...over,
  };
}

describe("boardFromResponse", () => {
  it("reshapes every lane, children included, and fills absent lists", () => {
    const res = {
      waiting: [payload({ issue_id: "w", lane: "waiting", from: { type: "agent", id: "a" }, from_name: "悟空" })],
      stalled: [payload({ issue_id: "s", unread: 2, children: [payload({ issue_id: "c", parent_issue_id: "s" })] })],
      running: [],
      todo: [payload({ issue_id: "t", lane: "todo", kind: "todo" })],
      fresh: [],
      done: null as unknown as InboxBoardRowPayload[],
      viewer_id: "me",
      as_of: "2026-09-26T09:00:00Z",
      unread_since: null,
      tz: "Asia/Shanghai",
      day_start: "2026-09-25T16:00:00Z",
      unread_markable: 2,
    } satisfies InboxBoardResponse;
    const board = boardFromResponse(res);
    expect(board.waiting[0]).toMatchObject({ issueId: "w", fromName: "悟空", from: { type: "agent", id: "a" } });
    const s = board.stalled[0]!;
    expect(s).toMatchObject({ stuckKind: "no_close", before: "PR 已合入", nextName: "孙悟空", unread: 2, timeline: [] });
    expect(s.children.map((c) => c.issueId)).toEqual(["c"]);
    expect(s.children[0]!.parentIssueId).toBe("s");
    expect(s.children[0]!.children).toEqual([]);
    expect(board.todo[0]).toMatchObject({ issueId: "t", lane: "todo" });
    expect(board.done).toEqual([]);
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

describe("boardLaneByIssue", () => {
  it("files every issue under the lane it shows in, sub-issues under their parent's", () => {
    const board = boardFromResponse({
      waiting: [payload({ issue_id: "w", lane: "waiting" })],
      stalled: [payload({ issue_id: "s", children: [payload({ issue_id: "c", lane: "running", parent_issue_id: "s" })] })],
      done: [payload({ issue_id: "d", lane: "done" })],
    } as InboxBoardResponse);
    const lanes = boardLaneByIssue(board);
    expect(Object.fromEntries(lanes)).toEqual({ w: "waiting", s: "stalled", c: "stalled", d: "done" });
  });
});
