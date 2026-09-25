import { describe, expect, it } from "vitest";
import {
  rankChatProjects,
  sessionMatchesChatProjectFilter,
  visibleBarProjectIds,
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

describe("visibleBarProjectIds", () => {
  const ordered = ["p1", "p2", "p3", "p4", "p5"];

  it("keeps the prefix that fits", () => {
    expect(visibleBarProjectIds(ordered, 3, null)).toEqual(["p1", "p2", "p3"]);
  });

  it("keeps a selection from the overflow menu on the row", () => {
    expect(visibleBarProjectIds(ordered, 3, "p5")).toEqual(["p1", "p2", "p5"]);
  });

  it("shows a project that is not a bar candidate when nothing else fits", () => {
    expect(visibleBarProjectIds(ordered, 0, "elsewhere")).toEqual(["elsewhere"]);
  });
});
