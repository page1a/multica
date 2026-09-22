// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  DEFAULT_AUTOMATION_LIMITS,
  parseAutomationLimits,
  parseLimitInput,
} from "./automation-limits";

describe("parseAutomationLimits", () => {
  it("falls back to the server defaults when nothing was saved", () => {
    expect(parseAutomationLimits(undefined)).toEqual(DEFAULT_AUTOMATION_LIMITS);
    expect(parseAutomationLimits({})).toEqual(DEFAULT_AUTOMATION_LIMITS);
  });

  it("keeps saved values, including zero as unlimited", () => {
    expect(
      parseAutomationLimits({
        agent_chain_budget: 0,
        agent_task_timeout_minutes: 90,
      }),
    ).toEqual({ agent_chain_budget: 0, agent_task_timeout_minutes: 90 });
  });

  it.each([["12"], [-1], [1.5], [null], [10_000_000], [Number.NaN]])(
    "ignores a malformed value %j",
    (value) => {
      expect(
        parseAutomationLimits({
          agent_chain_budget: value,
          agent_task_timeout_minutes: value,
        }),
      ).toEqual(DEFAULT_AUTOMATION_LIMITS);
    },
  );
});

describe("parseLimitInput", () => {
  it("accepts a positive integer", () => {
    expect(parseLimitInput("45", 30)).toBe(45);
  });

  it.each([[""], ["0"], ["-3"], ["abc"]])(
    "never turns %j into unlimited",
    (text) => {
      expect(parseLimitInput(text, 30)).toBe(30);
    },
  );

  it("caps at what the server accepts", () => {
    expect(parseLimitInput("123456789", 30)).toBe(999_999);
  });
});
