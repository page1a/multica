import { describe, expect, it } from "vitest";
import {
  chatProjectMenuGroups,
  narrowestWidthFittingAll,
  rankChatProjects,
  sessionMatchesChatProjectFilter,
  visibleBarProjectIds,
  wrappedFitCount,
  type ChatProjectBarSession,
} from "./project-bar";

function session(
  projectIds: string[],
  updatedAt: string,
  extras: Partial<ChatProjectBarSession> = {},
): ChatProjectBarSession {
  return {
    projectIds,
    updatedAt,
    status: "active",
    hasUnread: false,
    ...extras,
  };
}

describe("rankChatProjects", () => {
  const projects = ["a", "b", "c", "d", "e"];

  it("puts pins first, then projects with chats by recency, and hides the rest from the bar", () => {
    const ranked = rankChatProjects(
      projects,
      ["c", "a"],
      [
        session(["b"], "2026-09-01T00:00:00Z"),
        session(["d", "b"], "2026-09-20T00:00:00Z", { hasUnread: true }),
        session(["e"], "2026-08-01T00:00:00Z", { status: "archived" }),
      ],
    );

    expect(ranked.pinned.map((row) => row.id)).toEqual(["c", "a"]);
    expect(ranked.bar.map((row) => row.id)).toEqual(["c", "a", "b", "d"]);
    expect(ranked.rest.map((row) => row.id)).toEqual(["b", "d", "e"]);
    expect(ranked.bar.find((row) => row.id === "b")).toMatchObject({
      chatCount: 2,
      hasUnread: true,
    });
    // An archived chat does not count as a recent chat, so "e" stays out of the bar.
    expect(ranked.bar.find((row) => row.id === "e")).toBeUndefined();
    expect(ranked.rest.find((row) => row.id === "e")?.chatCount).toBe(0);
  });

  it("drops pinned ids that are not projects in this workspace", () => {
    const ranked = rankChatProjects(["a"], ["missing", "a", "a"], []);
    expect(ranked.pinned.map((row) => row.id)).toEqual(["a"]);
    expect(ranked.bar.map((row) => row.id)).toEqual(["a"]);
  });
});

describe("sessionMatchesChatProjectFilter", () => {
  it("matches all, none, and a project the chat carries", () => {
    expect(sessionMatchesChatProjectFilter(["a"], { type: "all" })).toBe(true);
    expect(sessionMatchesChatProjectFilter([], { type: "none" })).toBe(true);
    expect(sessionMatchesChatProjectFilter(["a"], { type: "none" })).toBe(false);
    expect(sessionMatchesChatProjectFilter(["a", "b"], { type: "project", id: "b" })).toBe(true);
    expect(sessionMatchesChatProjectFilter(["a"], { type: "project", id: "b" })).toBe(false);
  });
});

describe("wrappedFitCount", () => {
  const widths = [100, 100, 100, 100];
  const gap = 8;

  it("fills a second row before anything overflows, and never keeps a partial chip", () => {
    expect(wrappedFitCount(widths, 0, gap, 2)).toBe(0);
    // One chip per row.
    expect(wrappedFitCount(widths, 100, gap, 2)).toBe(2);
    expect(wrappedFitCount(widths, 207, gap, 2)).toBe(2);
    // 100 + 8 + 100 = 208: two per row.
    expect(wrappedFitCount(widths, 208, gap, 2)).toBe(4);
    expect(wrappedFitCount(widths, 208, gap, 1)).toBe(2);
    expect(wrappedFitCount([100, 100, 100, 100, 100], 208, gap, 2)).toBe(4);
  });

  it("counts the All chip against the first row only", () => {
    // Row one: 40 + 8 + 100 = 148, no room for a second chip; row two takes two.
    expect(wrappedFitCount(widths, 208, gap, 2, 40)).toBe(3);
  });

  it("gives a chip wider than the row a row of its own", () => {
    expect(wrappedFitCount([300, 50, 50], 120, gap, 2)).toBe(3);
    expect(wrappedFitCount([50, 300, 50], 120, gap, 2)).toBe(2);
  });

  it("stops at a chip that has not been measured yet", () => {
    expect(wrappedFitCount([100, 0, 100], 1000, gap, 2)).toBe(1);
  });
});

describe("visibleBarProjectIds", () => {
  const gap = 8;
  const bar = (
    ids: string[],
    widths: number[],
    available: number,
    promotedId: string | null,
    extra: { pinnedCount?: number; rows?: number; promotedWidth?: number } = {},
  ) => {
    const widthById = new Map(ids.map((id, index) => [id, widths[index]!]));
    if (promotedId && extra.promotedWidth != null) widthById.set(promotedId, extra.promotedWidth);
    return visibleBarProjectIds({
      ids,
      pinnedCount: extra.pinnedCount ?? 0,
      widthById,
      available,
      gap,
      rows: extra.rows ?? 2,
      promotedId,
    });
  };
  const ids = ["p1", "p2", "p3", "p4", "p5", "p6"];
  const widths = [100, 100, 100, 100, 100, 100];

  it("wraps onto a second row and overflows from the end", () => {
    expect(bar(ids, widths, 208, null)).toEqual(["p1", "p2", "p3", "p4"]);
    expect(bar(ids, widths, 316, null)).toEqual(ids);
  });

  it("moves a selection that would be in More to right after the pins", () => {
    expect(bar(ids, widths, 208, "p6", { pinnedCount: 2 })).toEqual(["p1", "p2", "p6", "p3"]);
    expect(bar(ids, widths, 208, "p6")).toEqual(["p6", "p1", "p2", "p3"]);
  });

  it("leaves a selection that is already on the bar where it is", () => {
    expect(bar(ids, widths, 208, "p4", { pinnedCount: 1 })).toEqual(["p1", "p2", "p3", "p4"]);
  });

  it("promotes a chip that is not a bar project", () => {
    expect(
      bar(["a", "b", "c"], [50, 50, 200], 200, "d", { rows: 1, promotedWidth: 50 }),
    ).toEqual(["d", "a", "b"]);
  });

  it("gives the selection the last slot when pins alone fill the bar", () => {
    expect(bar(ids, widths, 208, "p6", { pinnedCount: 5 })).toEqual(["p1", "p2", "p3", "p6"]);
  });

  it("leaves an unmeasured selection off the bar", () => {
    expect(bar(ids, widths, 208, "ghost")).toEqual(["p1", "p2", "p3", "p4"]);
  });
});

describe("narrowestWidthFittingAll", () => {
  const gap = 8;

  it("finds the width where the last chip stops overflowing two rows", () => {
    const widths = [100, 100, 100, 100];
    expect(narrowestWidthFittingAll(widths, gap, 2)).toBe(208);
    expect(narrowestWidthFittingAll(widths, gap, 1)).toBe(424);
    // All (40) shares row one: 40 + 8 + 100 + 8 + 100 = 256.
    expect(narrowestWidthFittingAll(widths, gap, 2, 40)).toBe(256);
    expect(wrappedFitCount(widths, 255, gap, 2, 40)).toBe(3);
  });

  it("never answers with a width that clips a chip", () => {
    expect(narrowestWidthFittingAll([300, 50], gap, 2)).toBe(300);
  });

  it("waits for every chip to be measured", () => {
    expect(narrowestWidthFittingAll([100, 0], gap, 2)).toBeNull();
    expect(narrowestWidthFittingAll([], gap, 2, 40)).toBe(40);
  });
});

describe("chatProjectMenuGroups", () => {
  const row = (id: string, hasUnread = false) => ({ id, chatCount: 1, hasUnread, recentAt: 0 });
  const pinned = [row("a"), row("b", true)];
  const rest = [row("c", true), row("d")];
  const statusById = new Map([
    ["a", "completed"],
    ["b", "in_progress"],
    ["c", "in_progress"],
    ["d", "planned"],
  ]);
  const ids = (groups: ReturnType<typeof chatProjectMenuGroups>) =>
    groups.map((group) => [group.key, group.rows.map((r) => r.id).join(",")]);

  it("splits pins from the rest and only lets pins reorder", () => {
    const groups = chatProjectMenuGroups({ pinned, rest, statusById, filter: "all", grouping: "pin" });
    expect(ids(groups)).toEqual([["pinned", "a,b"], ["unpinned", "c,d"]]);
    expect(groups.map((group) => group.reorderable)).toEqual([true, false]);
  });

  it("groups by status in status order, pins first inside a group", () => {
    const groups = chatProjectMenuGroups({ pinned, rest, statusById, filter: "all", grouping: "status" });
    expect(ids(groups)).toEqual([["in_progress", "b,c"], ["planned", "d"], ["completed", "a"]]);
  });

  it("filters before grouping and drops empty groups", () => {
    expect(ids(chatProjectMenuGroups({ pinned, rest, statusById, filter: "unread", grouping: "pin" })))
      .toEqual([["pinned", "b"], ["unpinned", "c"]]);
    expect(ids(chatProjectMenuGroups({ pinned, rest, statusById, filter: "pinned", grouping: "none" })))
      .toEqual([["all", "a,b"]]);
    expect(ids(chatProjectMenuGroups({
      pinned, rest, statusById, filter: "unpinned", grouping: "pin", matches: (id) => id === "d",
    }))).toEqual([["unpinned", "d"]]);
  });
});
