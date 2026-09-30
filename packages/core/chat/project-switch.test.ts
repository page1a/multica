import { describe, expect, it } from "vitest";
import {
  draftProjectIdsForNewChat,
  openMemoryScope,
  orderProjectsForQuickSwitch,
  sessionToLandOn,
  type SwitchLandingSession,
} from "./project-switch";

function session(
  id: string,
  projectIds: string[],
  updatedAt: string,
  status: SwitchLandingSession["status"] = "active",
): SwitchLandingSession {
  return { id, projectIds, updatedAt, status };
}

describe("draftProjectIdsForNewChat", () => {
  it("binds a new chat to the project being browsed", () => {
    expect(draftProjectIdsForNewChat({ type: "project", id: "proj-x" })).toEqual(["proj-x"]);
  });

  it("leaves All and the no-project view unbound", () => {
    expect(draftProjectIdsForNewChat({ type: "all" })).toEqual([]);
    expect(draftProjectIdsForNewChat({ type: "none" })).toEqual([]);
  });
});

describe("openMemoryScope", () => {
  it("remembers a project and the no-project view, not All", () => {
    expect(openMemoryScope({ type: "project", id: "proj-x" })).toBe("proj-x");
    expect(openMemoryScope({ type: "none" })).toBe("\0none");
    expect(openMemoryScope({ type: "all" })).toBeNull();
  });
});

describe("sessionToLandOn", () => {
  const sessions = [
    session("old", ["proj-x"], "2026-09-01T00:00:00Z"),
    session("recent", ["proj-x"], "2026-09-20T00:00:00Z"),
    session("other", ["proj-y"], "2026-09-28T00:00:00Z"),
    session("loose", [], "2026-09-15T00:00:00Z"),
    session("archived", ["proj-x"], "2026-09-29T00:00:00Z", "archived"),
  ];

  it("stays put on All, and when the open chat already belongs", () => {
    expect(
      sessionToLandOn({
        filter: { type: "all" },
        sessions,
        activeSessionId: "other",
        rememberedSessionId: "recent",
      }),
    ).toBeNull();
    expect(
      sessionToLandOn({
        filter: { type: "project", id: "proj-x" },
        sessions,
        activeSessionId: "old",
        rememberedSessionId: "recent",
      }),
    ).toBeNull();
  });

  it("opens the conversation last opened in that project", () => {
    expect(
      sessionToLandOn({
        filter: { type: "project", id: "proj-x" },
        sessions,
        activeSessionId: "other",
        rememberedSessionId: "old",
      }),
    ).toBe("old");
  });

  it("falls back to the newest chat, skipping archived and other projects", () => {
    expect(
      sessionToLandOn({
        filter: { type: "project", id: "proj-x" },
        sessions,
        activeSessionId: "other",
        rememberedSessionId: "archived",
      }),
    ).toBe("recent");
  });

  it("lands on the newest unbound chat from the no-project view", () => {
    expect(
      sessionToLandOn({
        filter: { type: "none" },
        sessions,
        activeSessionId: "other",
        rememberedSessionId: null,
      }),
    ).toBe("loose");
  });

  it("leaves the current chat when the view has nothing to open", () => {
    expect(
      sessionToLandOn({
        filter: { type: "project", id: "empty" },
        sessions,
        activeSessionId: "other",
        rememberedSessionId: null,
      }),
    ).toBeNull();
  });
});

describe("orderProjectsForQuickSwitch", () => {
  const projects = [
    { id: "b", title: "Beta" },
    { id: "a", title: "Alpha" },
    { id: "c", title: "Cedar" },
  ];

  it("puts the most recently chatted project first", () => {
    const recent = new Map<string, number>([
      ["a", 10],
      ["c", 50],
    ]);
    expect(orderProjectsForQuickSwitch(projects, recent).map((row) => row.id)).toEqual([
      "c",
      "a",
      "b",
    ]);
  });

  it("breaks a tie by title", () => {
    expect(orderProjectsForQuickSwitch(projects, new Map()).map((row) => row.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });
});
