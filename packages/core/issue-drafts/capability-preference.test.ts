// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import {
  readIssueDraftCapabilityPreference,
  writeIssueDraftCapabilityPreference,
} from "./capability-preference";
import { ISSUE_DRAFT_CAPABILITIES } from "./capabilities";

describe("issue draft capability preference", () => {
  beforeEach(() => localStorage.clear());

  it("keeps the preference per user while sharing it across workspaces", () => {
    writeIssueDraftCapabilityPreference(["grill"], "user-a");
    writeIssueDraftCapabilityPreference(["wayfinder", "grill-frontend-look"], "user-b");

    expect(readIssueDraftCapabilityPreference("user-a")).toEqual(["grill"]);
    expect(readIssueDraftCapabilityPreference("user-b")).toEqual([
      "wayfinder",
      "grill-frontend-look",
    ]);
  });

  it("falls back to the current defaults for a first-time user", () => {
    expect(readIssueDraftCapabilityPreference("new-user")).toEqual([...ISSUE_DRAFT_CAPABILITIES]);
  });

  it("drops unknown stored keys without blocking the picker", () => {
    localStorage.setItem("multica_alignment_capabilities:user-a", JSON.stringify(["grill", "retired"]));
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual(["grill"]);
  });
});
