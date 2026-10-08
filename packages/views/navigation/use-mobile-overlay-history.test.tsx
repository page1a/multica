// @vitest-environment jsdom

import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useMobileOverlayHistory } from "@multica/ui/lib/mobile-history";

beforeEach(() => {
  vi.useFakeTimers();
  window.matchMedia = ((query: string) => ({
    matches: true, media: query, addEventListener() {}, removeEventListener() {},
    addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
afterEach(() => { cleanup(); vi.runAllTimers(); vi.useRealTimers(); });

describe("useMobileOverlayHistory", () => {
  it("keeps one entry under StrictMode's double effect run, so opening is not undone by a stray popstate (DENE-1585)", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/1");
    const before = window.history.length;
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const onDismiss = vi.fn();
    renderHook(() => useMobileOverlayHistory(true, onDismiss), { wrapper: StrictMode });
    act(() => { vi.runAllTimers(); });
    expect(window.history.length).toBe(before + 1);
    expect(back).not.toHaveBeenCalled();
    // The browser's own back traversal must not be mistaken for our entry's pop.
    expect(onDismiss).not.toHaveBeenCalled();
    back.mockRestore();
  });

  it("steps back over its entry when closed another way", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/2");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const { rerender } = renderHook(({ active }) => useMobileOverlayHistory(active, vi.fn()), { initialProps: { active: true } });
    rerender({ active: false });
    act(() => { vi.runAllTimers(); });
    expect(back).toHaveBeenCalledTimes(1);
    back.mockRestore();
  });

  it("closes on the system back gesture without stepping back again", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/3");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const onDismiss = vi.fn();
    const { rerender } = renderHook(({ active }) => useMobileOverlayHistory(active, onDismiss), { initialProps: { active: true } });
    act(() => {
      window.history.replaceState({ __NA: true }, "", window.location.href);
      window.dispatchEvent(new PopStateEvent("popstate", { state: { __NA: true } }));
    });
    expect(onDismiss).toHaveBeenCalledTimes(1);
    rerender({ active: false });
    act(() => { vi.runAllTimers(); });
    expect(back).not.toHaveBeenCalled();
    back.mockRestore();
  });
});
