"use client";

import { useEffect, useRef } from "react";

const MARKER = "__multicaDismissible";

/**
 * Give a full-screen overlay its own step in the browser's back stack, so the
 * phone's back gesture closes the overlay instead of navigating the page that
 * sits underneath it.
 *
 * The floating chat is the case this exists for: on a phone it covers the whole
 * screen but has no URL of its own, so a back swipe used to pop the page under
 * it — an issue detail stepped back to the issue list while the chat stayed on
 * top, and closing the chat then revealed a page the person never asked for.
 *
 * While `active`, one entry is pushed that repeats the current URL (and keeps
 * the router's own history state, so the router restores the same page when it
 * is popped). A back onto the entry below calls `onDismiss`. Closing the
 * overlay any other way steps back over the entry, so it never lingers as a
 * dead "forward" stop — unless the page already navigated past it, in which
 * case the entry is harmless: it restores the page it was pushed on.
 *
 * Only for surfaces backed by the real session history (the web app). The
 * desktop shell runs an in-memory router and never fires popstate for it.
 */
export function useBackToDismiss(active: boolean, onDismiss: () => void) {
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;

  useEffect(() => {
    if (!active || typeof window === "undefined") return;
    const token = Math.random().toString(36).slice(2);
    const base = (window.history.state ?? {}) as Record<string, unknown>;
    window.history.pushState({ ...base, [MARKER]: token }, "", window.location.href);

    let popped = false;
    const onPopState = () => {
      const state = window.history.state as Record<string, unknown> | null;
      if (state?.[MARKER] === token) return;
      popped = true;
      dismissRef.current();
    };
    window.addEventListener("popstate", onPopState);
    return () => {
      window.removeEventListener("popstate", onPopState);
      const state = window.history.state as Record<string, unknown> | null;
      if (!popped && state?.[MARKER] === token) window.history.back();
    };
  }, [active]);
}
