import { describe, expect, it } from "vitest";
import { isSessionBreak, runSessionLineage } from "./session-lineage";

const run = (id: string, agent: string, minute: number | null, extra: Record<string, string> = {}) => ({
  id,
  agent_id: agent,
  started_at: minute === null ? null : new Date(Date.UTC(2026, 9, 4, 10, minute)).toISOString(),
  ...extra,
});

describe("runSessionLineage", () => {
  it("numbers rounds per agent and names the resumed round", () => {
    const lineage = runSessionLineage([
      run("c", "wukong", 30, { session_mode: "resumed", resumed_from_run: "a" }),
      run("b", "gohan", 20, { session_mode: "new", session_break_reason: "agent_changed" }),
      run("a", "wukong", 10, { session_mode: "new", session_break_reason: "first_run" }),
      run("q", "wukong", null),
    ]);
    expect(lineage.get("a")).toEqual({ round: 1, mode: "new", resumedFromRound: undefined, breakReason: "first_run" });
    expect(lineage.get("b")?.round).toBe(1);
    expect(lineage.get("c")).toMatchObject({ round: 2, mode: "resumed", resumedFromRound: 1 });
    expect(lineage.get("q")).toMatchObject({ round: undefined, mode: undefined });
  });

  it("treats only a non-first new session as a break", () => {
    const lineage = runSessionLineage([
      run("a", "wukong", 10, { session_mode: "new", session_break_reason: "first_run" }),
      run("b", "wukong", 20, { session_mode: "new", session_break_reason: "session_lost" }),
      run("c", "wukong", 30, { session_mode: "resumed", resumed_from_run: "b" }),
      run("d", "wukong", 40),
    ]);
    expect(["a", "b", "c", "d"].map((id) => isSessionBreak(lineage.get(id)))).toEqual([false, true, false, false]);
  });
});
