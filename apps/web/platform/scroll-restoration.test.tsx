// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { useRestoredScrollOffset } from "@multica/views/platform";
import { WebScrollRestorationProvider } from "./scroll-restoration";

function ScrollProbe({ containerKey }: { containerKey: string }) {
  const restored = useRestoredScrollOffset(containerKey);
  return <output data-testid="restored">{restored ?? "none"}</output>;
}

/** Dispatch a real scroll event from a marked container element. */
function scrollContainer(el: HTMLElement, top: number) {
  Object.defineProperty(el, "scrollTop", { configurable: true, value: top });
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    value: 2000,
  });
  el.dispatchEvent(new Event("scroll", { bubbles: false }));
}

describe("WebScrollRestorationProvider", () => {
  it("serves a captured container offset back for the same pathname", () => {
    const view = render(
      <WebScrollRestorationProvider>
        <div data-tab-scroll-root="main" data-testid="container" />
        <ScrollProbe containerKey="main" />
      </WebScrollRestorationProvider>,
    );

    scrollContainer(screen.getByTestId("container"), 480);

    // Remount — the same pull the view performs after navigating back.
    view.rerender(
      <WebScrollRestorationProvider>
        <ScrollProbe containerKey="main" />
      </WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("restored").textContent).toBe("480");
  });

  it("clears the saved offset when the container scrolls back to the top", () => {
    const view = render(
      <WebScrollRestorationProvider>
        <div data-tab-scroll-root="cleared" data-testid="container" />
        <ScrollProbe containerKey="cleared" />
      </WebScrollRestorationProvider>,
    );

    scrollContainer(screen.getByTestId("container"), 480);
    scrollContainer(screen.getByTestId("container"), 0);

    view.rerender(
      <WebScrollRestorationProvider>
        <ScrollProbe containerKey="cleared" />
      </WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("restored").textContent).toBe("none");
  });

  it("shields a just-served memento from the clamped scroll events a restore produces", () => {
    const view = render(
      <WebScrollRestorationProvider>
        <div data-tab-scroll-root="shielded" data-testid="container" />
        <ScrollProbe containerKey="shielded" />
      </WebScrollRestorationProvider>,
    );

    scrollContainer(screen.getByTestId("container"), 480);

    // Remount serves 480 (and arms the write shield) …
    view.rerender(
      <WebScrollRestorationProvider>
        <div data-tab-scroll-root="shielded" data-testid="container" />
        <ScrollProbe containerKey="shielded" />
      </WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("restored").textContent).toBe("480");

    // … so the browser clamping the restore assignment (content not yet at
    // full height → scrollTop lands at 100) must not overwrite the memento.
    scrollContainer(screen.getByTestId("container"), 100);
    view.rerender(
      <WebScrollRestorationProvider>
        <ScrollProbe containerKey="shielded" />
      </WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("restored").textContent).toBe("480");
  });

  it("ignores scrolls from unmarked elements", () => {
    render(
      <WebScrollRestorationProvider>
        <div data-testid="plain" />
        <ScrollProbe containerKey="plain" />
      </WebScrollRestorationProvider>,
    );

    scrollContainer(screen.getByTestId("plain"), 480);
    expect(screen.getByTestId("restored").textContent).toBe("none");
  });
});

describe("WebScrollRestorationProvider across a tab reload (DENE-978)", () => {
  // A phone browser discards a background tab and reloads it on return; the
  // module maps are gone then, so the mementos have to come back from
  // sessionStorage.
  afterEach(() => {
    sessionStorage.clear();
    vi.useRealTimers();
  });

  async function freshModule() {
    vi.resetModules();
    return import("./scroll-restoration");
  }

  function scrollBoth(el: HTMLElement, top: number, left: number) {
    Object.defineProperty(el, "scrollLeft", { configurable: true, value: left });
    scrollContainer(el, top);
  }

  it("serves offsets saved before the reload, horizontal included", async () => {
    const before = await freshModule();
    const view = render(
      <before.WebScrollRestorationProvider>
        <div data-tab-scroll-root="board" data-testid="container" />
      </before.WebScrollRestorationProvider>,
    );
    scrollBoth(screen.getByTestId("container"), 40, 700);
    // Going to the background flushes the coalesced write.
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    view.unmount();

    const after = await freshModule();
    const { useRestoredScrollEntry } = await import("@multica/views/platform");
    function EntryProbe() {
      const entry = useRestoredScrollEntry("board");
      return <output data-testid="entry">{entry ? `${entry.top}/${entry.left ?? 0}` : "none"}</output>;
    }
    render(
      <after.WebScrollRestorationProvider>
        <EntryProbe />
      </after.WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("entry").textContent).toBe("40/700");
  });

  it("coalesces scroll writes instead of hitting storage every frame", async () => {
    vi.useFakeTimers();
    const mod = await freshModule();
    render(
      <mod.WebScrollRestorationProvider>
        <div data-tab-scroll-root="coalesced" data-testid="container" />
      </mod.WebScrollRestorationProvider>,
    );
    scrollContainer(screen.getByTestId("container"), 100);
    scrollContainer(screen.getByTestId("container"), 200);
    expect(sessionStorage.getItem("multica:scroll-restoration")).toBeNull();

    vi.advanceTimersByTime(300);
    const saved = JSON.parse(sessionStorage.getItem("multica:scroll-restoration") ?? "{}");
    expect(saved.offsets[`${window.location.pathname}::coalesced`].top).toBe(200);
  });

  it("survives a corrupt snapshot", async () => {
    sessionStorage.setItem("multica:scroll-restoration", "{not json");
    const mod = await freshModule();
    const views = await import("@multica/views/platform");
    function Probe() {
      return <output data-testid="probe">{views.useRestoredScrollOffset("main") ?? "none"}</output>;
    }
    const view = render(
      <mod.WebScrollRestorationProvider>
        <div data-tab-scroll-root="main" data-testid="container" />
        <Probe />
      </mod.WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("probe").textContent).toBe("none");
    // …and keeps working for new offsets.
    scrollContainer(screen.getByTestId("container"), 300);
    view.rerender(
      <mod.WebScrollRestorationProvider>
        <Probe />
      </mod.WebScrollRestorationProvider>,
    );
    expect(screen.getByTestId("probe").textContent).toBe("300");
  });
});
