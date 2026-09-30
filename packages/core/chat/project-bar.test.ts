import { describe, expect, it } from "vitest";
import {
  leadingFitCount,
  rankChatProjects,
  sessionMatchesChatProjectFilter,
  visibleBarProjectIdsForWidths,
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

describe("leadingFitCount", () => {
  const widths = [100, 100, 100, 100];
  const gap = 8;

  it("shows more chips as the row gets wider, and never a partial chip", () => {
    expect(leadingFitCount(widths, 0, gap)).toBe(0);
    expect(leadingFitCount(widths, 99, gap)).toBe(0);
    expect(leadingFitCount(widths, 100, gap)).toBe(1);
    // 100 + 8 + 100 = 208. One pixel less drops the second chip.
    expect(leadingFitCount(widths, 207, gap)).toBe(1);
    expect(leadingFitCount(widths, 208, gap)).toBe(2);
    expect(leadingFitCount(widths, 316, gap)).toBe(3);
    expect(leadingFitCount(widths, 424, gap)).toBe(4);
    expect(leadingFitCount(widths, 1000, gap)).toBe(4);
  });

  it("stops at a chip that has not been measured yet", () => {
    expect(leadingFitCount([100, 0, 100], 1000, gap)).toBe(1);
  });
});

describe("visibleBarProjectIdsForWidths", () => {
  const ids = ["p1", "p2", "p3", "p4"];
  const widths = [100, 100, 100, 100];
  const gap = 8;

  it("keeps pin order and overflows from the end", () => {
    expect(visibleBarProjectIdsForWidths(ids, widths, 208, gap, null)).toEqual(["p1", "p2"]);
    expect(visibleBarProjectIdsForWidths(ids, widths, 424, gap, null)).toEqual(ids);
  });

  it("keeps a later selection on the row by dropping the tail", () => {
    expect(visibleBarProjectIdsForWidths(ids, widths, 208, gap, "p4")).toEqual(["p1", "p4"]);
  });

  it("keeps the selection beside earlier chips when it is narrow enough", () => {
    expect(
      visibleBarProjectIdsForWidths(["a", "b", "c"], [50, 50, 200], 200, gap, "d", 50),
    ).toEqual(["a", "b", "d"]);
  });

  it("leaves a selection that cannot fit even alone off the row", () => {
    expect(visibleBarProjectIdsForWidths(ids, widths, 208, gap, "wide", 300)).toEqual([
      "p1",
      "p2",
    ]);
  });

  it("shows the selection alone when the other chips are wider than the row", () => {
    expect(visibleBarProjectIdsForWidths(["a", "b"], [300, 100], 120, gap, "b")).toEqual(["b"]);
  });
});
