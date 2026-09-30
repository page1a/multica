"use client";

import { useEffect, useMemo, type ReactNode } from "react";
import { sessionStorageAdapter } from "@multica/core/platform";
import {
  ScrollRestorationProvider,
  type ScrollRestorationAdapter,
} from "@multica/views/platform";

/**
 * Web half of the MUL-4741 scroll-restoration protocol (desktop's half lives
 * in the tab coordinator). The browser only restores the window's own scroll
 * position on back/forward — the app's scrollable containers
 * (`data-tab-scroll-root`) are inner divs and virtualized lists it knows
 * nothing about, so without this, navigating back lands every list at the
 * top.
 *
 * Capture is continuous rather than before-navigation: the App Router has no
 * reliable "about to leave" hook, so a capture-phase scroll listener records
 * each marked container's offset as the user scrolls, keyed by
 * `pathname::containerKey`. Serving is the same pull-based adapter desktop
 * uses — views read the offset at mount through `useRestoredScrollOffset` /
 * `useRestoredScrollRef`.
 *
 * Keying by pathname (not by history entry) matches desktop's memento
 * semantics: returning to a route — via back or via a fresh link — restores
 * where you last were on it. The maps are per browser tab and mirrored into
 * sessionStorage, so they also survive a reload of the tab — which is what a
 * phone browser does to a tab it discarded in the background. The browser's
 * own restoration only covers the window, never these inner containers.
 */
type SavedOffset = { top: number; height: number; left?: number };

/**
 * Generic view-state entries (the desktop memento's second cargo — e.g.
 * "this route's comment-highlight deep link already landed"). Views write
 * through `setViewState` when the state changes and read back at mount.
 * Same lifetime as the offsets.
 */
const STORAGE_KEY = "multica:scroll-restoration";
/** Oldest routes are dropped past this, so a long session stays small. */
const MAX_ENTRIES = 200;
const PERSIST_DELAY_MS = 250;

const savedOffsets = new Map<string, SavedOffset>();
const savedViewState = new Map<string, string>();
let loaded = false;
let persistTimer: ReturnType<typeof setTimeout> | null = null;

function isSavedOffset(value: unknown): value is SavedOffset {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.top === "number" &&
    typeof v.height === "number" &&
    (v.left === undefined || typeof v.left === "number")
  );
}

/** Read the tab's saved mementos once, on first use. */
function ensureLoaded() {
  if (loaded) return;
  loaded = true;
  const raw = sessionStorageAdapter.getItem(STORAGE_KEY);
  if (!raw) return;
  try {
    const parsed = JSON.parse(raw) as { offsets?: unknown; view?: unknown };
    if (parsed.offsets && typeof parsed.offsets === "object") {
      for (const [key, value] of Object.entries(parsed.offsets)) {
        if (isSavedOffset(value)) savedOffsets.set(key, value);
      }
    }
    if (parsed.view && typeof parsed.view === "object") {
      for (const [key, value] of Object.entries(parsed.view)) {
        if (typeof value === "string") savedViewState.set(key, value);
      }
    }
  } catch {
    // A corrupt snapshot only costs the restore points.
  }
}

function trim<V>(map: Map<string, V>) {
  // Maps iterate in insertion order and `touch` re-inserts on write, so the
  // first keys are the least recently written.
  for (const key of map.keys()) {
    if (map.size <= MAX_ENTRIES) break;
    map.delete(key);
  }
}

function touch<V>(map: Map<string, V>, key: string, value: V) {
  map.delete(key);
  map.set(key, value);
  trim(map);
}

function persistNow() {
  if (persistTimer !== null) {
    clearTimeout(persistTimer);
    persistTimer = null;
  }
  sessionStorageAdapter.setItem(
    STORAGE_KEY,
    JSON.stringify({
      offsets: Object.fromEntries(savedOffsets),
      view: Object.fromEntries(savedViewState),
    }),
  );
}

/** Scroll events fire every frame; coalesce the storage writes. */
function schedulePersist() {
  if (persistTimer !== null) return;
  persistTimer = setTimeout(persistNow, PERSIST_DELAY_MS);
}

/**
 * Keys recently served through `adapter.get` are write-suppressed briefly:
 * the restoring container assigns `scrollTop` at attach time, and if its
 * content height isn't final yet the browser clamps the assignment and fires
 * a scroll event — without suppression that clamped value (or a clamp to 0)
 * would overwrite the very memento being restored. A real user scroll within
 * the window is lost, which is the cheap side of the trade.
 */
const suppressedUntil = new Map<string, number>();
const RESTORE_SUPPRESS_MS = 1000;

function mementoKey(pathname: string, containerKey: string): string {
  return `${pathname}::${containerKey}`;
}

export function WebScrollRestorationProvider({
  children,
}: {
  children: ReactNode;
}) {
  useEffect(() => {
    const onScroll = (e: Event) => {
      const el = e.target;
      if (!(el instanceof HTMLElement)) return;
      const raw = el.getAttribute("data-tab-scroll-root");
      if (raw === null) return;
      const key = mementoKey(window.location.pathname, raw || "main");
      const suppressed = suppressedUntil.get(key);
      if (suppressed !== undefined) {
        if (performance.now() < suppressed) return;
        suppressedUntil.delete(key);
      }
      ensureLoaded();
      if (el.scrollTop <= 0 && el.scrollLeft <= 0) {
        // Back at the origin is the default state — a stale offset would
        // otherwise resurrect on the next visit.
        if (!savedOffsets.delete(key)) return;
      } else {
        touch(savedOffsets, key, {
          top: el.scrollTop,
          height: el.scrollHeight,
          ...(el.scrollLeft > 0 ? { left: el.scrollLeft } : {}),
        });
      }
      schedulePersist();
    };
    // Scroll events don't bubble, but they do propagate on the capture
    // phase, so one window listener observes every container.
    window.addEventListener("scroll", onScroll, {
      capture: true,
      passive: true,
    });
    // A phone browser may discard the tab soon after it goes to the
    // background, before the coalesced write fires: flush on the way out.
    const flush = () => {
      if (persistTimer !== null) persistNow();
    };
    const onVisibility = () => {
      if (document.visibilityState === "hidden") flush();
    };
    window.addEventListener("pagehide", flush);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.removeEventListener("scroll", onScroll, { capture: true });
      window.removeEventListener("pagehide", flush);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, []);

  const adapter = useMemo<ScrollRestorationAdapter>(
    () => ({
      get(containerKey) {
        ensureLoaded();
        const key = mementoKey(window.location.pathname, containerKey);
        const saved = savedOffsets.get(key);
        if (saved) {
          // The caller is about to restore this offset — shield the memento
          // from the clamped scroll events the restore itself can produce.
          suppressedUntil.set(key, performance.now() + RESTORE_SUPPRESS_MS);
        }
        return saved;
      },
      getViewState(entryKey) {
        ensureLoaded();
        return savedViewState.get(
          mementoKey(window.location.pathname, entryKey),
        );
      },
      setViewState(entryKey, value) {
        ensureLoaded();
        const key = mementoKey(window.location.pathname, entryKey);
        if (value === undefined) {
          if (!savedViewState.delete(key)) return;
        } else {
          touch(savedViewState, key, value);
        }
        schedulePersist();
      },
    }),
    [],
  );

  return (
    <ScrollRestorationProvider adapter={adapter}>
      {children}
    </ScrollRestorationProvider>
  );
}
