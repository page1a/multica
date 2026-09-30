import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import { useUnseenCount } from "./use-unseen-count";

function setup(keys: string[], away: boolean) {
  return renderHook((p: { keys: string[]; away: boolean }) => useUnseenCount(p.keys, p.away), {
    initialProps: { keys, away },
  });
}

describe("useUnseenCount", () => {
  it("is 0 while the reader is at the bottom, however much arrives", () => {
    const { result, rerender } = setup(["a"], false);
    rerender({ keys: ["a", "b", "c"], away: false });
    expect(result.current).toBe(0);
  });

  it("counts entries appended after the reader left", () => {
    const { result, rerender } = setup(["a", "b"], true);
    expect(result.current).toBe(0);
    rerender({ keys: ["a", "b", "c", "d"], away: true });
    expect(result.current).toBe(2);
  });

  it("resets when the reader returns and starts over from the new tail", () => {
    const { result, rerender } = setup(["a"], true);
    rerender({ keys: ["a", "b"], away: true });
    expect(result.current).toBe(1);
    rerender({ keys: ["a", "b"], away: false });
    expect(result.current).toBe(0);
    rerender({ keys: ["a", "b"], away: true });
    expect(result.current).toBe(0);
    rerender({ keys: ["a", "b", "c"], away: true });
    expect(result.current).toBe(1);
  });

  it("ignores older history prepended at the front", () => {
    const { result, rerender } = setup(["c", "d"], true);
    rerender({ keys: ["a", "b", "c", "d"], away: true });
    expect(result.current).toBe(0);
  });

  it("counts nothing when the baseline disappears", () => {
    const { result, rerender } = setup(["a", "b"], true);
    rerender({ keys: ["x", "y"], away: true });
    expect(result.current).toBe(0);
  });
});
