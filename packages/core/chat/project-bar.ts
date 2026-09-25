/**
 * Ordering for the chat-list project bar.
 *
 * The bar is: pinned projects in the person's own order, then every other
 * project that already has a chat, most recently chatted first. Projects
 * with no chats stay out of the bar (they still appear in "more"). Pin
 * order is a list of project ids supplied by the caller — that list is a
 * per-user preference, not a workspace setting.
 */

export interface ChatProjectBarSession {
  projectIds: readonly string[];
  /** ISO timestamp of the session's last activity. */
  updatedAt: string;
  status: "active" | "archived";
  hasUnread: boolean;
}

export interface RankedChatProject {
  id: string;
  /** Non-archived chats that include this project. */
  chatCount: number;
  hasUnread: boolean;
  /** Epoch ms of the newest non-archived chat, or 0 when there is none. */
  recentAt: number;
}

export type ChatProjectFilter =
  | { type: "all" }
  | { type: "none" }
  | { type: "project"; id: string };

export type ChatProjectBarItem =
  | { type: "project"; id: string }
  | { type: "none" };

export function sessionMatchesChatProjectFilter(
  projectIds: readonly string[],
  filter: ChatProjectFilter,
): boolean {
  if (filter.type === "all") return true;
  if (filter.type === "none") return projectIds.length === 0;
  return projectIds.includes(filter.id);
}

export function rankChatProjects(
  projectIds: readonly string[],
  pinnedIds: readonly string[],
  sessions: readonly ChatProjectBarSession[],
): {
  /** Pinned projects that still exist, in pin order. Includes empty ones. */
  pinned: RankedChatProject[];
  /** Unpinned projects, most recent chat first. Includes empty ones. */
  rest: RankedChatProject[];
  /**
   * What the one-row bar is allowed to show, before width fitting: every
   * pin, then unpinned projects that have at least one chat.
   */
  bar: RankedChatProject[];
} {
  const known = new Set(projectIds);
  const stats = new Map<string, RankedChatProject>();
  for (const id of projectIds) {
    stats.set(id, { id, chatCount: 0, hasUnread: false, recentAt: 0 });
  }
  for (const session of sessions) {
    if (session.status === "archived") continue;
    const at = Date.parse(session.updatedAt);
    const recentAt = Number.isFinite(at) ? at : 0;
    for (const id of session.projectIds) {
      const row = stats.get(id);
      if (!row) continue;
      row.chatCount += 1;
      if (session.hasUnread) row.hasUnread = true;
      if (recentAt > row.recentAt) row.recentAt = recentAt;
    }
  }

  const pinned: RankedChatProject[] = [];
  const pinnedSet = new Set<string>();
  for (const id of pinnedIds) {
    if (!known.has(id) || pinnedSet.has(id)) continue;
    pinnedSet.add(id);
    pinned.push(stats.get(id)!);
  }

  const rest = projectIds
    .filter((id) => !pinnedSet.has(id))
    .map((id) => stats.get(id)!)
    .sort((a, b) => b.recentAt - a.recentAt || a.id.localeCompare(b.id));

  return {
    pinned,
    rest,
    bar: [...pinned, ...rest.filter((row) => row.chatCount > 0)],
  };
}

/**
 * The chips that actually render once the row has run out of room.
 * `fitCount` is how many of `orderedIds` fit. A project (or the "no
 * project" filter) chosen from the overflow menu stays on the row by
 * taking the last slot, so the selection never disappears into "more".
 */
export function visibleBarProjectIds(
  orderedIds: readonly string[],
  fitCount: number,
  promotedId: string | null,
): string[] {
  const fitted = orderedIds.slice(0, Math.max(0, Math.min(fitCount, orderedIds.length)));
  if (!promotedId || fitted.includes(promotedId)) return fitted;
  if (fitted.length === 0) return [promotedId];
  return [...fitted.slice(0, -1), promotedId];
}
