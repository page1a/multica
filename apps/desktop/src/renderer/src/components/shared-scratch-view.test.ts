import { describe, expect, it } from "vitest";
import type { SharedScratchSession } from "../../../main/shared-scratch";
import { canRemove, scratchTotals, sortSessions } from "./shared-scratch-view";

function session(over: Partial<SharedScratchSession> = {}): SharedScratchSession {
  return {
    path: "/ws/.sessions/ws-1/sessions/chat-1",
    session_id: "chat-1",
    size_bytes: 100,
    in_use: false,
    ours: true,
    expired: false,
    ...over,
  };
}

describe("shared session folder list", () => {
  it("will not offer to remove a folder a task is in, or one Multica did not create", () => {
    expect(canRemove(session())).toBe(true);
    expect(canRemove(session({ in_use: true, expired: true }))).toBe(false);
    expect(canRemove(session({ ours: false, expired: true }))).toBe(false);
  });

  it("counts only idle, expired, Multica-created folders as reclaimable", () => {
    const totals = scratchTotals([
      session({ size_bytes: 10, expired: true }),
      session({ path: "/busy", size_bytes: 20, expired: true, in_use: true }),
      session({ path: "/stranger", size_bytes: 30, expired: true, ours: false }),
      session({ path: "/fresh", size_bytes: 40, expired: false }),
    ]);
    expect(totals).toEqual({ count: 4, bytes: 100, reclaimable: 10 });
  });

  it("lists the largest folder first and does not reshuffle equal sizes", () => {
    const sorted = sortSessions([
      session({ path: "/b", size_bytes: 5 }),
      session({ path: "/a", size_bytes: 5 }),
      session({ path: "/c", size_bytes: 9 }),
    ]);
    expect(sorted.map((item) => item.path)).toEqual(["/c", "/a", "/b"]);
  });
});
