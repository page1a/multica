"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

/**
 * Fit calculator for a single-row toolbar that collapses its tail into a
 * trailing trigger (the view bar; reusable for any "N chips + overflow
 * menu" strip).
 *
 * The caller renders every candidate twice: once for real (slice to
 * `fitCount`) and once inside a hidden mirror row attached to
 * `measureRef` (absolutely positioned, `visibility: hidden`, same
 * classes). Natural widths are read off the mirror, so the fit answer
 * never depends on what is currently mounted for real — no feedback
 * loop between measurement and rendering.
 *
 * Rule: the longest prefix that fits alongside `reserve` px of trailing
 * chrome wins. `reserve` must cover everything that is ALWAYS rendered
 * after the items (triggers, menus) — it is subtracted even when every
 * item fits, otherwise a row that exactly fits its items clips the
 * chrome off the end.
 *
 * Remeasures on container resize (ResizeObserver) and after every
 * commit (labels/items change the mirror); state only updates when the
 * answer actually changes, so the extra passes are free.
 */
export function useSingleRowFit({
  count,
  gap,
  reserve,
}: {
  /** Number of candidate items (mirror children must match). */
  count: number;
  /** Horizontal gap between items in px (the container's `gap`). */
  gap: number;
  /** Width in px to hold back for the overflow trigger. */
  reserve: number;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const measureRef = useRef<HTMLDivElement | null>(null);
  const [fitCount, setFitCount] = useState(count);

  const recompute = useCallback(() => {
    const container = containerRef.current;
    const mirror = measureRef.current;
    if (!container || !mirror) return;
    const available = container.clientWidth;
    const widths = Array.from(mirror.children).map(
      (child) => (child as HTMLElement).offsetWidth,
    );

    let used = 0;
    let next = 0;
    for (let i = 0; i < widths.length; i++) {
      const withItem = used + (i > 0 ? gap : 0) + widths[i]!;
      if (withItem + gap + reserve > available) break;
      used = withItem;
      next = i + 1;
    }
    setFitCount((current) => (current === next ? current : next));
  }, [gap, reserve]);

  // After every commit: the mirror just re-rendered with current labels.
  // eslint-disable-next-line react-hooks/exhaustive-deps -- deliberate every-commit measure; the setState inside is change-guarded
  useLayoutEffect(() => {
    recompute();
  });

  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => recompute());
    observer.observe(container);
    return () => observer.disconnect();
  }, [recompute]);

  // Never report more than exists (items can shrink between renders).
  return { containerRef, measureRef, fitCount: Math.min(fitCount, count) };
}

function sameNumbers(a: readonly number[], b: readonly number[]) {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
}

/**
 * Width of a single-row strip and the natural width of each measured child.
 *
 * The mirror (`measureRef`) must size to its content (`width: max-content`,
 * `flex: none` children). A mirror that is stretched to the row reports
 * every chip as wide as the row, so the fit stays stuck at one chip while
 * the painted chip leaves a gap. Remeasures on row resize, mirror resize
 * (labels, font load), and every commit.
 */
export function useMeasuredRow() {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const measureRef = useRef<HTMLDivElement | null>(null);
  const [available, setAvailable] = useState(0);
  const [widths, setWidths] = useState<number[]>([]);

  const recompute = useCallback(() => {
    const container = containerRef.current;
    const mirror = measureRef.current;
    if (!container || !mirror) return;
    const nextAvailable = Math.floor(container.clientWidth);
    const nextWidths = Array.from(mirror.children).map((child) =>
      Math.ceil((child as HTMLElement).getBoundingClientRect().width),
    );
    setAvailable((current) => (current === nextAvailable ? current : nextAvailable));
    setWidths((current) => (sameNumbers(current, nextWidths) ? current : nextWidths));
  }, []);

  // After every commit the mirror has the current labels.
  // eslint-disable-next-line react-hooks/exhaustive-deps -- deliberate every-commit measure; the setState inside is change-guarded
  useLayoutEffect(() => {
    recompute();
  });

  useLayoutEffect(() => {
    if (typeof ResizeObserver === "undefined") return;
    const container = containerRef.current;
    const mirror = measureRef.current;
    if (!container && !mirror) return;
    const observer = new ResizeObserver(() => recompute());
    if (container) observer.observe(container);
    if (mirror) observer.observe(mirror);
    return () => observer.disconnect();
  }, [recompute]);

  useEffect(() => {
    const ready = document.fonts?.ready;
    if (!ready) return;
    let cancelled = false;
    void ready.then(
      () => {
        if (!cancelled) recompute();
      },
      () => {},
    );
    return () => {
      cancelled = true;
    };
  }, [recompute]);

  return { containerRef, measureRef, available, widths };
}
