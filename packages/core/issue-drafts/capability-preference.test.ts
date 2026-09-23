// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  readIssueDraftCapabilityPreference,
  writeIssueDraftCapabilityPreference,
} from "./capability-preference";
import { ISSUE_DRAFT_CAPABILITIES } from "./capabilities";

describe("issue draft capability preference", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.restoreAllMocks());

  it("keeps the preference per user while sharing it across workspaces", () => {
    writeIssueDraftCapabilityPreference(["grill"], "user-a");
    writeIssueDraftCapabilityPreference(["wayfinder", "grill-frontend-look"], "user-b");

    // The key is the user, not a workspace. The same user reads the same
    // record wherever they open the entry.
    expect(localStorage.getItem("multica_alignment_capabilities:user-a")).toBe(
      JSON.stringify(["grill"]),
    );
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: ["grill"],
      failed: false,
    });
    expect(readIssueDraftCapabilityPreference("user-b")).toEqual({
      capabilities: ["wayfinder", "grill-frontend-look"],
      failed: false,
    });
  });

  it("falls back to the current defaults for a first-time user", () => {
    expect(readIssueDraftCapabilityPreference("new-user")).toEqual({
      capabilities: [...ISSUE_DRAFT_CAPABILITIES],
      failed: false,
    });
  });

  it("keeps an empty selection as a real last use, not as missing history", () => {
    writeIssueDraftCapabilityPreference([], "user-a");
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: [],
      failed: false,
    });
  });

  it("drops unknown stored keys without blocking the picker", () => {
    localStorage.setItem(
      "multica_alignment_capabilities:user-a",
      JSON.stringify(["grill", "retired"]),
    );
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: ["grill"],
      failed: false,
    });
  });

  it("uses the system default when the record names only retired keys", () => {
    localStorage.setItem(
      "multica_alignment_capabilities:user-a",
      JSON.stringify(["retired"]),
    );
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: [...ISSUE_DRAFT_CAPABILITIES],
      failed: false,
    });
  });

  it("reports an unreadable record and still returns the system default", () => {
    localStorage.setItem("multica_alignment_capabilities:user-a", "{");
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: [...ISSUE_DRAFT_CAPABILITIES],
      failed: true,
    });
  });

  it("reports a storage read that throws and still returns the system default", () => {
    vi.spyOn(localStorage, "getItem").mockImplementation(() => {
      throw new Error("denied");
    });
    expect(readIssueDraftCapabilityPreference("user-a")).toEqual({
      capabilities: [...ISSUE_DRAFT_CAPABILITIES],
      failed: true,
    });
  });

  it("swallows a storage write failure", () => {
    const setItem = vi.spyOn(localStorage, "setItem").mockImplementation(() => {
      throw new Error("quota");
    });
    expect(() => writeIssueDraftCapabilityPreference(["grill"], "user-a")).not.toThrow();
    expect(setItem).toHaveBeenCalled();
    expect(localStorage.getItem("multica_alignment_capabilities:user-a")).toBeNull();
  });
});
