"use client";

import { useRef } from "react";

/**
 * How many entries of `keys` (oldest → newest) arrived after the reader left
 * the live end. Goes back to 0 the moment they return.
 *
 * The baseline is the newest key at the moment `away` turns true, so entries
 * prepended at the front (older history loading in) never count, and a
 * baseline that vanishes (deleted, session swapped) counts as nothing rather
 * than everything.
 */
export function useUnseenCount(keys: readonly string[], away: boolean): number {
  const baselineRef = useRef<string | null>(null);
  if (!away) {
    baselineRef.current = null;
    return 0;
  }
  if (baselineRef.current === null) {
    baselineRef.current = keys[keys.length - 1] ?? "";
    return 0;
  }
  const at = keys.lastIndexOf(baselineRef.current);
  return at < 0 ? 0 : keys.length - 1 - at;
}
