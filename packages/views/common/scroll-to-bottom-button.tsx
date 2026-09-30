"use client";

import { ArrowDown } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

export type ScrollToBottomUnit = "messages" | "comments";

interface ScrollToBottomButtonProps {
  /** Reader is away from the live end. Hidden (and inert) while false. */
  visible: boolean;
  /** Items that arrived while the reader was away. 0 renders the bare arrow. */
  newCount?: number;
  /** What the count counts — picks the badge wording. */
  unit?: ScrollToBottomUnit;
  onClick: () => void;
  /** Where the button floats. The host positions it; the button owns the look. */
  className?: string;
  style?: React.CSSProperties;
}

/**
 * Floating "jump to latest" button shared by the chat list and the issue
 * timeline. It only renders; whether the reader is away and how many items
 * arrived meanwhile are decided by the host's own scroll logic.
 *
 * Always mounted so the fade runs in both directions: while hidden it is
 * transparent, non-interactive, out of the tab order and hidden from
 * assistive tech. The hit area is at least 44px (touch target).
 */
export function ScrollToBottomButton({
  visible,
  newCount = 0,
  unit = "messages",
  onClick,
  className,
  style,
}: ScrollToBottomButtonProps) {
  const { t } = useT("common");
  const label = t(($) => $.scroll_to_bottom.label);
  const badge =
    newCount > 0
      ? unit === "comments"
        ? t(($) => $.scroll_to_bottom.new_comments, { count: newCount })
        : t(($) => $.scroll_to_bottom.new_messages, { count: newCount })
      : null;

  return (
    <button
      type="button"
      data-testid="scroll-to-bottom"
      aria-label={badge ? `${label} · ${badge}` : label}
      aria-hidden={!visible}
      tabIndex={visible ? 0 : -1}
      onClick={onClick}
      style={style}
      className={cn(
        "absolute z-20 mb-[env(safe-area-inset-bottom)] flex h-11 min-w-11 touch-manipulation items-center justify-center gap-1.5 rounded-full bg-surface-raised text-sm text-foreground shadow-[var(--floating-shadow)] ring-1 ring-surface-border transition-[opacity,transform,background-color] duration-150 hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70 active:bg-surface-hover",
        badge ? "px-3.5" : "px-0",
        visible ? "translate-y-0 opacity-100" : "pointer-events-none translate-y-1 opacity-0",
        className,
      )}
    >
      <ArrowDown className="size-4 shrink-0" />
      {badge && <span className="whitespace-nowrap font-medium tabular-nums">{badge}</span>}
    </button>
  );
}
