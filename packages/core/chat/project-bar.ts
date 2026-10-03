/**
 * Ordering for the chat-list project bar.
 *
 * The bar is: pinned projects in the person's own order, then every other
 * project that already has a chat, most recently chatted first. Projects
 * with no chats stay out of the bar (they still appear in "more"). Pin
 * order is a list of project ids supplied by the caller from the server's
 * per-user sidebar pin list.
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
   * What the bar is allowed to show, before width fitting: every
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

/** The bar wraps onto this many rows before anything goes into More. */
export const CHAT_PROJECT_BAR_ROWS = 2;

/**
 * How many leading chips fit when the bar wraps onto `rows` rows of
 * `available` px with `gap` px between chips. `lead` px is already taken at
 * the start of the first row (the All chip). A chip that would start a row
 * past the last one is not counted — the bar never keeps a partial chip. A
 * chip wider than a whole row takes a row to itself (it is painted clipped).
 * Widths that are not yet measured (`<= 0`) stop the scan.
 */
export function wrappedFitCount(
  widths: readonly number[],
  available: number,
  gap: number,
  rows: number,
  lead = 0,
): number {
  if (!(available > 0) || rows < 1) return 0;
  let row = 1;
  let used = lead > 0 ? Math.min(lead, available) : 0;
  let rowHasChip = lead > 0;
  let count = 0;
  for (let i = 0; i < widths.length; i++) {
    const measured = widths[i] ?? 0;
    if (!(measured > 0)) break;
    const width = Math.min(measured, available);
    const next = used + (rowHasChip ? gap : 0) + width;
    if (next <= available) {
      used = next;
    } else {
      row += 1;
      if (row > rows) break;
      used = width;
    }
    rowHasChip = true;
    count += 1;
  }
  return count;
}

/**
 * Chips to paint for this bar width. `ids` is the bar order: the first
 * `pinnedCount` are the person's pins, which is what keeps them on the bar
 * when space runs out — overflow comes off the end.
 *
 * `promotedId` is the current filter. When it would fall into More it moves
 * to the slot right after the pins; if the pins alone fill the bar it takes
 * the last slot instead. When it cannot fit even by itself it is left off so
 * the caller can mark the More trigger selected instead of clipping the chip.
 * It does not have to be one of `ids` (the "no project" chip, or a project
 * with no chats opened from the menu) as long as `widthById` knows it.
 */
export function visibleBarProjectIds({
  ids,
  pinnedCount,
  widthById,
  available,
  gap,
  rows,
  lead = 0,
  promotedId,
}: {
  ids: readonly string[];
  pinnedCount: number;
  widthById: ReadonlyMap<string, number>;
  available: number;
  gap: number;
  rows: number;
  lead?: number;
  promotedId: string | null;
}): string[] {
  const fit = (order: readonly string[]) =>
    order.slice(
      0,
      wrappedFitCount(
        order.map((id) => widthById.get(id) ?? 0),
        available,
        gap,
        rows,
        lead,
      ),
    );

  const fitted = fit(ids);
  if (!promotedId || fitted.includes(promotedId)) return fitted;
  if (!((widthById.get(promotedId) ?? 0) > 0)) return fitted;

  const others = ids.filter((id) => id !== promotedId);
  const pins = Math.min(pinnedCount, others.length);
  const afterPins = fit([...others.slice(0, pins), promotedId, ...others.slice(pins)]);
  if (afterPins.includes(promotedId)) return afterPins;

  for (let keep = fitted.length - 1; keep >= 0; keep--) {
    const order = [...fitted.slice(0, keep), promotedId];
    if (fit(order).length === order.length) return order;
  }
  return fitted;
}

/**
 * The narrowest bar width at which every chip fits in `rows` rows without
 * clipping any of them, or null while a chip is still unmeasured.
 */
export function narrowestWidthFittingAll(
  widths: readonly number[],
  gap: number,
  rows: number,
  lead = 0,
): number | null {
  if (widths.some((width) => !(width > 0))) return null;
  let low = Math.max(lead, ...widths);
  let high = widths.reduce((sum, width) => sum + gap + width, lead);
  const fitsAll = (available: number) =>
    wrappedFitCount(widths, available, gap, rows, lead) === widths.length;
  if (fitsAll(low)) return low;
  while (high - low > 1) {
    const middle = Math.floor((low + high) / 2);
    if (fitsAll(middle)) high = middle;
    else low = middle;
  }
  return high;
}

/** Which projects the More menu lists. */
export type ChatProjectMenuFilter = "all" | "pinned" | "unpinned" | "unread";
/** How the More menu groups what it lists. */
export type ChatProjectMenuGrouping = "pin" | "status" | "none";

/** Project statuses in the order the More menu shows their groups. */
export const CHAT_PROJECT_MENU_STATUS_ORDER = [
  "in_progress",
  "planned",
  "paused",
  "completed",
  "cancelled",
] as const;

export interface ChatProjectMenuGroup {
  /** "pinned" / "unpinned" / a project status / "all". */
  key: string;
  rows: RankedChatProject[];
  /** Rows in this group are pins and can be dragged to reorder. */
  reorderable: boolean;
}

/**
 * The More menu: every project exactly once, pins first in pin order, then
 * the rest by most recent chat. `filter` and `matches` narrow the list,
 * `grouping` splits it. Empty groups are dropped.
 */
export function chatProjectMenuGroups({
  pinned,
  rest,
  statusById,
  filter,
  grouping,
  matches = () => true,
}: {
  pinned: readonly RankedChatProject[];
  rest: readonly RankedChatProject[];
  statusById: ReadonlyMap<string, string>;
  filter: ChatProjectMenuFilter;
  grouping: ChatProjectMenuGrouping;
  matches?: (id: string) => boolean;
}): ChatProjectMenuGroup[] {
  const keep = (row: RankedChatProject) =>
    matches(row.id) && (filter !== "unread" || row.hasUnread);
  const pinnedRows = filter === "unpinned" ? [] : pinned.filter(keep);
  const restRows = filter === "pinned" ? [] : rest.filter(keep);

  let groups: ChatProjectMenuGroup[];
  if (grouping === "pin") {
    groups = [
      { key: "pinned", rows: pinnedRows, reorderable: true },
      { key: "unpinned", rows: restRows, reorderable: false },
    ];
  } else if (grouping === "status") {
    const all = [...pinnedRows, ...restRows];
    const known: readonly string[] = CHAT_PROJECT_MENU_STATUS_ORDER;
    const keys = [
      ...known,
      ...new Set(all.map((row) => statusById.get(row.id) ?? "").filter((s) => !known.includes(s))),
    ];
    groups = keys.map((key) => ({
      key,
      rows: all.filter((row) => (statusById.get(row.id) ?? "") === key),
      reorderable: false,
    }));
  } else {
    groups = [{ key: "all", rows: [...pinnedRows, ...restRows], reorderable: false }];
  }
  return groups.filter((group) => group.rows.length > 0);
}
