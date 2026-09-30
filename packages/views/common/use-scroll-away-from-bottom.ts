"use client";

import { useEffect, useState } from "react";

/** How far above the end the reader must be before the jump button shows. */
export const AWAY_FROM_BOTTOM_PX = 240;

/**
 * Whether `el` is scrolled more than `threshold` px above its end. For plain
 * scroll containers (the issue timeline); the chat list keeps its own latch in
 * `useStickToBottom`. `recheckKey` re-measures when content grows without a
 * scroll event (new comment lands while the reader sits still).
 */
export function useScrolledAwayFromBottom(
  el: HTMLElement | null,
  recheckKey: unknown,
  threshold = AWAY_FROM_BOTTOM_PX,
): boolean {
  const [away, setAway] = useState(false);
  useEffect(() => {
    if (!el) return;
    const measure = () =>
      setAway(el.scrollHeight - el.scrollTop - el.clientHeight > threshold);
    measure();
    const frame = requestAnimationFrame(measure);
    el.addEventListener("scroll", measure, { passive: true });
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => {
      cancelAnimationFrame(frame);
      el.removeEventListener("scroll", measure);
      observer.disconnect();
    };
  }, [el, threshold, recheckKey]);
  return away;
}
