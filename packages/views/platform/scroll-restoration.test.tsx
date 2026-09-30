import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import {
  ScrollRestorationProvider,
  useRestoredScrollEntry,
  useRestoredScrollOffset,
  useRestoredScrollRef,
  useScrollRestorationAdapter,
  type ScrollRestorationAdapter,
} from "./scroll-restoration";

function makeAdapter(get: ScrollRestorationAdapter["get"]): ScrollRestorationAdapter {
  return { get };
}

describe("scroll-restoration public surface", () => {
  it("useScrollRestorationAdapter returns null without a provider", () => {
    const { result } = renderHook(() => useScrollRestorationAdapter());
    expect(result.current).toBeNull();
  });

  it("useRestoredScrollOffset remains a thin wrapper returning only top (back-compat)", () => {
    const adapter = makeAdapter(() => ({
      top: 123,
      height: 456,
      contentKey: "ck",
    }));
    const { result } = renderHook(() => useRestoredScrollOffset("main"), {
      wrapper: ({ children }) => (
        <ScrollRestorationProvider adapter={adapter}>
          {children}
        </ScrollRestorationProvider>
      ),
    });
    expect(result.current).toBe(123);
  });

  it("all hooks return undefined without a provider (web no-op path)", () => {
    const entry = renderHook(() => useRestoredScrollEntry("x"));
    const offset = renderHook(() => useRestoredScrollOffset("x"));
    expect(entry.result.current).toBeUndefined();
    expect(offset.result.current).toBeUndefined();
  });
});

/**
 * A container whose scrollable range the test controls: assignments clamp to
 * `max`, the way a browser clamps while the content is still loading.
 */
function makeScroller(max: { top: number; left: number }) {
  const el = document.createElement("div");
  document.body.appendChild(el);
  let top = 0;
  let left = 0;
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => top,
    set: (v: number) => { top = Math.max(0, Math.min(v, max.top)); },
  });
  Object.defineProperty(el, "scrollLeft", {
    configurable: true,
    get: () => left,
    set: (v: number) => { left = Math.max(0, Math.min(v, max.left)); },
  });
  return el;
}

describe("useRestoredScrollRef (DENE-978)", () => {
  let frames: FrameRequestCallback[] = [];
  const runFrame = () => {
    const pending = frames;
    frames = [];
    for (const cb of pending) cb(performance.now());
  };

  beforeEach(() => {
    frames = [];
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      frames.push(cb);
      return frames.length;
    });
    vi.stubGlobal("cancelAnimationFrame", () => {
      frames = [];
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  function attach(entry: { top: number; height: number; left?: number }, el: HTMLElement) {
    const adapter = makeAdapter(() => entry);
    const { result, unmount } = renderHook(() => useRestoredScrollRef("list"), {
      wrapper: ({ children }) => (
        <ScrollRestorationProvider adapter={adapter}>{children}</ScrollRestorationProvider>
      ),
    });
    result.current(el);
    return { unmount };
  }

  it("restores vertical and horizontal offsets at attach time", () => {
    const el = makeScroller({ top: 5000, left: 5000 });
    attach({ top: 600, height: 2000, left: 820 }, el);
    expect(el.scrollTop).toBe(600);
    expect(el.scrollLeft).toBe(820);
    expect(frames).toHaveLength(0);
  });

  it("keeps retrying while the content is too short, then lands", () => {
    const max = { top: 100, left: 0 };
    const el = makeScroller(max);
    attach({ top: 600, height: 2000 }, el);
    expect(el.scrollTop).toBe(100);

    runFrame();
    expect(el.scrollTop).toBe(100);

    // The rows arrive.
    max.top = 5000;
    runFrame();
    expect(el.scrollTop).toBe(600);
    expect(frames).toHaveLength(0);
  });

  it("stops retrying as soon as the user touches the container", () => {
    const max = { top: 100, left: 0 };
    const el = makeScroller(max);
    attach({ top: 600, height: 2000 }, el);

    el.dispatchEvent(new Event("touchstart"));
    max.top = 5000;
    runFrame();
    expect(el.scrollTop).toBe(100);
  });

  it("gives up after the retry window", () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const max = { top: 100, left: 0 };
    const el = makeScroller(max);
    attach({ top: 600, height: 2000 }, el);

    vi.setSystemTime(Date.now() + 5000);
    max.top = 5000;
    runFrame();
    expect(el.scrollTop).toBe(100);
    expect(frames).toHaveLength(0);
  });

  it("cancels a pending retry on unmount", () => {
    const max = { top: 100, left: 0 };
    const el = makeScroller(max);
    const { unmount } = attach({ top: 600, height: 2000 }, el);
    unmount();
    max.top = 5000;
    runFrame();
    expect(el.scrollTop).toBe(100);
  });
});
