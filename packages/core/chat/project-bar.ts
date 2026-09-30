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
 * How many leading chip widths fit in `available` px with `gap` px between
 * them. A chip that would pass the end of the row is not counted — the row
 * never keeps a partial chip. Widths that are not yet measured (`<= 0`)
 * stop the scan.
 */
export function leadingFitCount(
  widths: readonly number[],
  available: number,
  gap: number,
): number {
  if (!(available > 0)) return 0;
  let used = 0;
  let count = 0;
  for (let i = 0; i < widths.length; i++) {
    const width = widths[i] ?? 0;
    if (!(width > 0)) break;
    const next = used + (count > 0 ? gap : 0) + width;
    if (next > available) break;
    used = next;
    count += 1;
  }
  return count;
}

/**
 * Chips to paint for this row width, in the person's pin order.
 * Overflow comes off the end. `promotedId` is the current filter: when its
 * own width fits, it stays on the row (taking the last slot and pushing the
 * previous tail into overflow). When it cannot fit even by itself, it is
 * left off the row so the caller can mark the overflow trigger selected
 * instead of clipping the chip.
 *
 * `widths` lines up with `ids`. `promotedWidth` is only used when
 * `promotedId` is not one of `ids` (the "no project" chip, or a project
 * opened from the menu that is not otherwise on the bar).
 */
export function visibleBarProjectIdsForWidths(
  ids: readonly string[],
  widths: readonly number[],
  available: number,
  gap: number,
  promotedId: string | null,
  promotedWidth?: number,
): string[] {
  const fitted = ids.slice(0, leadingFitCount(widths, available, gap));
  if (!promotedId || fitted.includes(promotedId)) return fitted;

  const index = ids.indexOf(promotedId);
  const width = index >= 0 ? (widths[index] ?? 0) : (promotedWidth ?? 0);
  if (!(width > 0) || width > available) return fitted;

  let used = 0;
  const kept: string[] = [];
  for (let i = 0; i < fitted.length; i++) {
    const chip = widths[i] ?? 0;
    const next = used + (kept.length > 0 ? gap : 0) + chip;
    if (next + gap + width > available) break;
    used = next;
    kept.push(fitted[i]!);
  }
  return [...kept, promotedId];
}
