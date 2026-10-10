import { describe, expect, it } from "vitest";
import { parseSpawnCount } from "./agent-permissions-tab";

describe("parseSpawnCount", () => {
  it("reads a blank box as unlimited", () => {
    expect(parseSpawnCount("", 5)).toBe(0);
    expect(parseSpawnCount("  ", 5)).toBe(0);
  });

  it("keeps the previous count on junk or negatives", () => {
    expect(parseSpawnCount("abc", 3)).toBe(3);
    expect(parseSpawnCount("-1", 3)).toBe(3);
  });

  it("accepts whole numbers and caps them", () => {
    expect(parseSpawnCount("7", 3)).toBe(7);
    expect(parseSpawnCount("5000", 3)).toBe(1000);
  });
});

describe("parseSpawnCount (required)", () => {
  it("has no unlimited: blank or below 1 keeps the previous count", () => {
    expect(parseSpawnCount("", 3, true)).toBe(3);
    expect(parseSpawnCount("0", 3, true)).toBe(3);
    expect(parseSpawnCount("5", 3, true)).toBe(5);
  });
});
