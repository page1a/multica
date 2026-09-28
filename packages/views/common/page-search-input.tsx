"use client";

import { useState } from "react";
import { Search, X } from "lucide-react";
import { useShortcut } from "@multica/core/shortcuts";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { ShortcutKeycaps } from "./shortcut-keycaps";

/** Marks the one input the `focusPageSearch` shortcut jumps into. */
export const PAGE_SEARCH_ATTRIBUTE = "data-page-search";

/**
 * The in-page search box shared by the chat list and the issues toolbar.
 * Filters the page it sits on; Cmd+K stays the global search. Shows the
 * (user-editable) focus shortcut while idle; Esc clears and leaves.
 */
export function PageSearchInput({
  value,
  onChange,
  placeholder,
  clearLabel,
  className,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  clearLabel: string;
  className?: string;
}) {
  const shortcut = useShortcut("focusPageSearch");
  const [focused, setFocused] = useState(false);
  const showKeycaps = !value && !focused && shortcut !== null;

  return (
    <div className={cn("relative w-56 shrink-0", className)}>
      <Search
        aria-hidden
        className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
      />
      <Input
        type="text"
        role="searchbox"
        inputMode="search"
        {...{ [PAGE_SEARCH_ATTRIBUTE]: "" }}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          event.preventDefault();
          onChange("");
          event.currentTarget.blur();
        }}
        aria-label={placeholder}
        placeholder={placeholder}
        className="h-7 pl-7 pr-7 text-caption"
      />
      {value ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label={clearLabel}
          onClick={() => onChange("")}
          className="absolute right-0.5 top-0.5 text-muted-foreground"
        >
          <X className="size-3" />
        </Button>
      ) : showKeycaps ? (
        <ShortcutKeycaps
          shortcut={shortcut}
          decorative
          className="pointer-events-none absolute right-1.5 top-1/2 -translate-y-1/2"
        />
      ) : null}
    </div>
  );
}
