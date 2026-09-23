import type { SharedScratchSession } from "../../../main/shared-scratch";

/**
 * Reading rules for the shared session folder (DENE-622).
 *
 * The daemon decides what may be removed. This only orders the list and
 * adds up what the screen says out loud, so a change to either is a row in
 * the test table rather than a render to squint at.
 */

export function canRemove(session: SharedScratchSession): boolean {
  return session.ours && !session.in_use;
}

/** Largest first; path breaks ties so a click does not land on a moving row. */
export function sortSessions(
  sessions: SharedScratchSession[],
): SharedScratchSession[] {
  return [...sessions].sort((a, b) => {
    if (b.size_bytes !== a.size_bytes) return b.size_bytes - a.size_bytes;
    return a.path.localeCompare(b.path);
  });
}

export function scratchTotals(sessions: SharedScratchSession[]): {
  count: number;
  bytes: number;
  reclaimable: number;
} {
  return {
    count: sessions.length,
    bytes: sessions.reduce((sum, session) => sum + session.size_bytes, 0),
    reclaimable: sessions
      .filter((session) => session.expired && canRemove(session))
      .reduce((sum, session) => sum + session.size_bytes, 0),
  };
}
