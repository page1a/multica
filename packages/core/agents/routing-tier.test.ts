// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  ROUTING_TIER_KEYS,
  isRoutingTierKey,
  routingTierKeyOf,
} from "./routing-tier";

describe("routing tier vocabulary", () => {
  it("is ordered strongest first — the ladder order the router walks", () => {
    expect([...ROUTING_TIER_KEYS]).toEqual([
      "strongest",
      "strong",
      "medium",
      "weak",
    ]);
  });

  it("narrows only known rungs", () => {
    expect(isRoutingTierKey("strong")).toBe(true);
    expect(isRoutingTierKey("strongest ")).toBe(false);
  });

  // A backend that grew a fifth rung must not make this build render a tier
  // name it has no label for.
  it("treats an unknown or missing rung as no rung", () => {
    expect(routingTierKeyOf("legendary")).toBe("");
    expect(routingTierKeyOf(undefined)).toBe("");
    expect(routingTierKeyOf(null)).toBe("");
    expect(routingTierKeyOf("  weak  ")).toBe("weak");
  });
});
