"use client";

/**
 * PreviewPoster — the phone-width stand-in for an inline HTML preview.
 *
 * Inline previews are a fixed-height iframe that scrolls on its own. On a
 * phone that box fills most of the screen, so wherever a finger lands it
 * scrolls the page inside the preview while the chat or comment list around it
 * stays put — two scroll areas fighting over one gesture. Below the mobile
 * breakpoint the preview shrinks to a short thumbnail and this overlay covers
 * it: the iframe never receives touch input, the surrounding list scrolls
 * wherever the finger lands, and a tap opens the full-screen preview, which is
 * then the only thing that scrolls.
 */

import { useT } from "../i18n";

/** Thumbnail height on phones. Keep the two in sync. */
export const PREVIEW_POSTER_HEIGHT = "h-[200px]";
export const PREVIEW_POSTER_HEIGHT_PX = 200;

export function PreviewPoster({ onOpen }: { onOpen: () => void }) {
  const { t } = useT("editor");
  const label = t(($) => $.attachment.tap_to_view);
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label={label}
      className="absolute inset-0 flex items-end justify-center rounded-md bg-gradient-to-b from-transparent from-50% to-background pb-3"
    >
      <span className="rounded-full bg-foreground px-3.5 py-1 text-xs font-medium text-background shadow-sm">
        {label}
      </span>
    </button>
  );
}
