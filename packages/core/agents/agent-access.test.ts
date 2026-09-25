import { describe, expect, it } from "vitest";
import { agentAccessPassExpiry } from "./agent-access";

describe("agentAccessPassExpiry", () => {
  const now = new Date("2026-09-24T10:00:00.000Z");

  it("2h is a relative grant", () => {
    expect(agentAccessPassExpiry("2h", undefined, now)).toEqual({ duration_minutes: 120 });
  });

  it("today ends at the local end of day", () => {
    const res = agentAccessPassExpiry("today", undefined, now);
    expect(res?.expires_at).toBeDefined();
    const end = new Date(res!.expires_at!);
    expect(end.getTime()).toBeGreaterThan(now.getTime());
    expect(end.getHours()).toBe(23);
    expect(end.getMinutes()).toBe(59);
  });

  it("today never issues a grant with under a minute left", () => {
    const late = new Date(now);
    late.setHours(23, 59, 30, 0);
    const res = agentAccessPassExpiry("today", undefined, late);
    const end = new Date(res!.expires_at!);
    expect(end.getTime() - late.getTime()).toBeGreaterThan(60_000);
  });

  it("custom rejects a missing or past expiry", () => {
    expect(agentAccessPassExpiry("custom", undefined, now)).toBeNull();
    expect(agentAccessPassExpiry("custom", new Date("2026-09-24T09:00:00Z"), now)).toBeNull();
    expect(agentAccessPassExpiry("custom", new Date("nope"), now)).toBeNull();
  });

  it("custom passes a future expiry through", () => {
    const future = new Date("2026-09-30T00:00:00.000Z");
    expect(agentAccessPassExpiry("custom", future, now)).toEqual({
      expires_at: future.toISOString(),
    });
  });
});
