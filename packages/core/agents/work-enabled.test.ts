// @vitest-environment node
import { describe, expect, it } from "vitest";
import { isAgentWorkEnabled } from "./work-enabled";

describe("isAgentWorkEnabled", () => {
  it("treats a missing field as on", () => {
    expect(isAgentWorkEnabled({})).toBe(true);
    expect(isAgentWorkEnabled({ work_enabled: undefined })).toBe(true);
  });

  it("treats explicit true as on and explicit false as off", () => {
    expect(isAgentWorkEnabled({ work_enabled: true })).toBe(true);
    expect(isAgentWorkEnabled({ work_enabled: false })).toBe(false);
  });
});
