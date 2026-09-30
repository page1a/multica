import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { AWAY_FROM_BOTTOM_PX, useScrolledAwayFromBottom } from "./use-scroll-away-from-bottom";

function scroller(scrollHeight: number, clientHeight: number) {
  const el = document.createElement("div");
  const state = { scrollTop: 0, scrollHeight };
  Object.defineProperties(el, {
    scrollHeight: { get: () => state.scrollHeight },
    clientHeight: { get: () => clientHeight },
    scrollTop: { get: () => state.scrollTop },
  });
  return {
    el,
    scrollTo(top: number) {
      state.scrollTop = top;
      act(() => {
        el.dispatchEvent(new Event("scroll"));
      });
    },
  };
}

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
});
afterEach(() => vi.unstubAllGlobals());

describe("useScrolledAwayFromBottom", () => {
  it("flips at the threshold as the reader scrolls", () => {
    const { el, scrollTo } = scroller(3000, 600);
    const { result } = renderHook(() => useScrolledAwayFromBottom(el, 0));
    scrollTo(2400); // at the end
    expect(result.current).toBe(false);
    scrollTo(2400 - AWAY_FROM_BOTTOM_PX); // exactly at the threshold: still "at bottom"
    expect(result.current).toBe(false);
    scrollTo(0);
    expect(result.current).toBe(true);
    scrollTo(2300);
    expect(result.current).toBe(false);
  });

  it("stays false for content shorter than the viewport", () => {
    const { el } = scroller(400, 600);
    const { result } = renderHook(() => useScrolledAwayFromBottom(el, 0));
    expect(result.current).toBe(false);
  });

  it("is false without a container", () => {
    const { result } = renderHook(() => useScrolledAwayFromBottom(null, 0));
    expect(result.current).toBe(false);
  });
});
