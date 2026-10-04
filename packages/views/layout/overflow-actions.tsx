"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { cn } from "@multica/ui/lib/utils";

export interface OverflowItem {
  key: string;
  /** Lower stays in the bar longer. Defaults to the item's position. */
  priority?: number;
  /** May render nothing; such an item takes no room and never overflows. */
  node: ReactNode;
}

const GAP = 4;

function sameSet(a: ReadonlySet<string>, b: ReadonlySet<string>) {
  if (a.size !== b.size) return false;
  for (const key of a) if (!b.has(key)) return false;
  return true;
}

/**
 * A row of header actions that never clips: whatever does not fit is handed
 * to `renderMore` instead, which puts it behind the host's own "⋯" menu.
 *
 * The row takes the space the pinned controls leave (`flex-1`), so a control
 * added to the header later can only push its neighbours into the menu, never
 * out of the card. An item lives in exactly one place — the bar or the menu —
 * and keeps the width it was last measured at while it is in the menu, which
 * is how it knows when the bar has grown enough to take it back.
 *
 * Give the menu section `keepMounted`: a dialog an overflowed item opened
 * belongs to that item, and survives the menu closing only if the item does.
 */
export function OverflowActions({
  items,
  renderMore,
  className,
}: {
  items: OverflowItem[];
  /** Receives the overflowed items (or `null` when all fit); renders the pinned trigger. */
  renderMore: (overflow: ReactNode | null) => ReactNode;
  className?: string;
}) {
  const barRef = useRef<HTMLDivElement>(null);
  const widths = useRef(new Map<string, number>());
  const itemsRef = useRef(items);
  itemsRef.current = items;
  const [overflowKeys, setOverflowKeys] = useState<ReadonlySet<string>>(() => new Set());

  const recompute = useCallback(() => {
    const bar = barRef.current;
    if (!bar) return;
    for (const el of bar.querySelectorAll<HTMLElement>("[data-overflow-key]")) {
      widths.current.set(el.dataset.overflowKey!, el.offsetWidth);
    }
    const available = bar.clientWidth;
    const ordered = itemsRef.current
      .map((item, index) => ({ key: item.key, priority: item.priority ?? index, index }))
      .sort((a, b) => a.priority - b.priority || a.index - b.index);
    const next = new Set<string>();
    let used = 0;
    let kept = 0;
    let full = false;
    for (const { key } of ordered) {
      const width = widths.current.get(key) ?? 0;
      // Nothing to show (or never measured): leave it in the bar, where it is
      // measured as soon as it has a size.
      if (width === 0) continue;
      const need = used + (kept > 0 ? GAP : 0) + width;
      if (!full && need <= available) {
        used = need;
        kept += 1;
      } else {
        full = true;
        next.add(key);
      }
    }
    setOverflowKeys((prev) => (sameSet(prev, next) ? prev : next));
  }, []);

  // Every render: item contents change width on their own (a chip appears,
  // a label changes) and `recompute` only sets state when the split moves.
  useLayoutEffect(() => {
    recompute();
  });

  const barKeys = items.filter((item) => !overflowKeys.has(item.key)).map((item) => item.key).join("|");
  useEffect(() => {
    const bar = barRef.current;
    if (!bar || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(recompute);
    observer.observe(bar);
    for (const el of bar.querySelectorAll("[data-overflow-key]")) observer.observe(el);
    return () => observer.disconnect();
  }, [recompute, barKeys]);

  const inBar = items.filter((item) => !overflowKeys.has(item.key));
  const inMenu = items.filter((item) => overflowKeys.has(item.key));

  return (
    <>
      <div
        ref={barRef}
        data-overflow-bar=""
        className={cn("flex min-w-0 flex-1 items-center justify-end gap-1 overflow-hidden", className)}
      >
        {inBar.map((item) => (
          <div key={item.key} data-overflow-key={item.key} className="flex shrink-0 items-center gap-1 empty:hidden">
            {item.node}
          </div>
        ))}
      </div>
      {renderMore(
        inMenu.length > 0 ? (
          <div
            data-overflow-menu=""
            className="mb-1 hidden flex-wrap items-center gap-1 border-b px-1 pb-1.5 has-[[data-overflow-key]:not(:empty)]:flex"
          >
            {inMenu.map((item) => (
              <div key={item.key} data-overflow-key={item.key} className="flex items-center gap-1 empty:hidden">
                {item.node}
              </div>
            ))}
          </div>
        ) : null,
      )}
    </>
  );
}
