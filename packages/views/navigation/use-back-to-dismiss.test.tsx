// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useBackToDismiss } from "./use-back-to-dismiss";

function popTo(state: unknown) {
  // jsdom's history.back() is async and flaky across versions; replay what the
  // browser does instead: the current entry becomes the one below, then
  // popstate fires.
  window.history.replaceState(state, "", window.location.href);
  window.dispatchEvent(new PopStateEvent("popstate", { state }));
}

describe("useBackToDismiss", () => {
  it("pushes one entry that keeps the router's state and closes on back", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/1");
    const before = window.history.length;
    const onDismiss = vi.fn();
    const { rerender } = renderHook(
      ({ active }) => useBackToDismiss(active, onDismiss),
      { initialProps: { active: false } },
    );
    expect(window.history.length).toBe(before);

    rerender({ active: true });
    expect(window.history.length).toBe(before + 1);
    expect(window.location.pathname).toBe("/acme/issues/1");
    expect((window.history.state as { __NA?: boolean }).__NA).toBe(true);

    act(() => popTo({ __NA: true }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("steps back over its entry when closed another way", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/2");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const { rerender } = renderHook(
      ({ active }) => useBackToDismiss(active, vi.fn()),
      { initialProps: { active: true } },
    );
    rerender({ active: false });
    expect(back).toHaveBeenCalledTimes(1);
    back.mockRestore();
  });

  it("leaves history alone when the page already navigated past its entry", () => {
    window.history.replaceState({ __NA: true }, "", "/acme/issues/3");
    const back = vi.spyOn(window.history, "back").mockImplementation(() => {});
    const onDismiss = vi.fn();
    const { rerender } = renderHook(
      ({ active }) => useBackToDismiss(active, onDismiss),
      { initialProps: { active: true } },
    );
    // A link followed from inside the overlay: the router pushes a new entry.
    window.history.pushState({ __NA: true }, "", "/acme/projects");
    rerender({ active: false });
    expect(back).not.toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
    back.mockRestore();
  });
});
