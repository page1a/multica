import { describe, expect, it } from "vitest";
import type { Agent } from "../types";
import {
  agentListRefetchInterval,
  patchAgentListStatus,
  readAgentStatusChange,
} from "./list-freshness";

const agent = (over: Partial<Agent>) => ({ id: "a1", status: "idle", ...over }) as Agent;

describe("readAgentStatusChange", () => {
  it("accepts only the explicit agent_id + status pair", () => {
    expect(readAgentStatusChange({ agent_id: "a1", status: "working" })).toEqual({
      agentId: "a1",
      status: "working",
    });
    // A full-row broadcast (config edit, runtime unbind) must refetch.
    expect(readAgentStatusChange({ agent: agent({}) })).toBeNull();
    // Skill changes carry agent_id without a status.
    expect(readAgentStatusChange({ agent_id: "a1", skills: [] })).toBeNull();
    expect(readAgentStatusChange({ agent_id: "a1", status: "bogus" })).toBeNull();
    expect(readAgentStatusChange(null)).toBeNull();
  });
});

describe("patchAgentListStatus", () => {
  it("patches the one row and keeps the others by reference", () => {
    const other = agent({ id: "a2" });
    const list = [agent({}), other];
    const next = patchAgentListStatus(list, { agentId: "a1", status: "working" });
    expect(next?.[0]?.status).toBe("working");
    expect(next?.[1]).toBe(other);
    expect(patchAgentListStatus(list, { agentId: "a1", status: "idle" })).toBe(list);
  });

  it("asks for a refetch when the list is cold or the row is missing", () => {
    expect(patchAgentListStatus(undefined, { agentId: "a1", status: "idle" })).toBeNull();
    expect(patchAgentListStatus([agent({})], { agentId: "zz", status: "idle" })).toBeNull();
  });
});

describe("agentListRefetchInterval", () => {
  it("polls fast only while a live agent is unstable", () => {
    expect(agentListRefetchInterval([agent({ runtime_availability: "unstable" })])).toBe(30_000);
    expect(agentListRefetchInterval([agent({ runtime_availability: "online" })])).toBe(300_000);
    expect(
      agentListRefetchInterval([
        agent({ runtime_availability: "online" }),
        agent({ id: "a2", runtime_availability: "unstable" }),
      ]),
    ).toBe(30_000);
    expect(agentListRefetchInterval([agent({ runtime_availability: "offline" })])).toBe(false);
    expect(
      agentListRefetchInterval([
        agent({ archived_at: "2026-09-04T00:00:00Z", runtime_availability: "unstable" }),
      ]),
    ).toBe(false);
    expect(agentListRefetchInterval(undefined)).toBe(false);
  });
});
