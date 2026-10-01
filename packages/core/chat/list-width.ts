/**
 * Width rules for the chat page's list pane (the divider between the chat
 * list and the conversation).
 *
 * The list can take whatever the conversation does not need: the only upper
 * bound is that the conversation keeps `CHAT_DETAIL_MIN_WIDTH`. `fitWidth` is
 * the list width at which every project chip fits on the two-row project bar;
 * a drag snaps to it, and a double-click on the divider jumps to it.
 */

export const CHAT_LIST_MIN_WIDTH = 240;
export const CHAT_LIST_DEFAULT_WIDTH = 300;
export const CHAT_DETAIL_MIN_WIDTH = 360;
/** A drag this close to the fit width lands on it. */
export const CHAT_LIST_SNAP_DISTANCE = 12;
/** The fit guide shows once a drag is this close. */
export const CHAT_LIST_FIT_HINT_DISTANCE = 60;
/** A double-click at or past (fit − this) goes back to the default. */
const FIT_TOLERANCE = 4;

/** `containerWidth <= 0` means not measured yet: no upper bound. */
export function chatListMaxWidth(containerWidth: number): number {
  if (!(containerWidth > 0)) return Number.POSITIVE_INFINITY;
  return Math.max(CHAT_LIST_MIN_WIDTH, containerWidth - CHAT_DETAIL_MIN_WIDTH);
}

export function clampChatListWidth(width: number, containerWidth: number): number {
  const wanted = Number.isFinite(width) ? Math.round(width) : CHAT_LIST_DEFAULT_WIDTH;
  return Math.max(CHAT_LIST_MIN_WIDTH, Math.min(chatListMaxWidth(containerWidth), wanted));
}

/** The fit width when the window leaves room for it, else null. */
function reachableFitWidth(fitWidth: number | null, containerWidth: number): number | null {
  if (fitWidth == null || !(fitWidth > 0)) return null;
  const fit = Math.max(CHAT_LIST_MIN_WIDTH, Math.ceil(fitWidth));
  return fit < chatListMaxWidth(containerWidth) ? fit : null;
}

export interface ChatListDrag {
  width: number;
  /** The pointer asked for more than the conversation can give up. */
  atMax: boolean;
  /** The width landed exactly on the fit width. */
  atFit: boolean;
  /** Where to draw the fit guide, or null when the drag is not near it. */
  fitGuide: number | null;
}

export function resolveChatListDrag({
  wanted,
  containerWidth,
  fitWidth,
}: {
  wanted: number;
  containerWidth: number;
  fitWidth: number | null;
}): ChatListDrag {
  const max = chatListMaxWidth(containerWidth);
  const fit = reachableFitWidth(fitWidth, containerWidth);
  let width = clampChatListWidth(wanted, containerWidth);
  // Pushing into the limit wins over a fit width that sits just short of it.
  const atMax = wanted >= max;
  if (fit != null && !atMax && Math.abs(width - fit) < CHAT_LIST_SNAP_DISTANCE) width = fit;
  return {
    width,
    atMax,
    atFit: fit != null && width === fit,
    fitGuide: fit != null && Math.abs(wanted - fit) < CHAT_LIST_FIT_HINT_DISTANCE ? fit : null,
  };
}

export interface ChatListToggle {
  width: number;
  /**
   * `fit`: every project now fits. `max`: the window is too narrow for that,
   * so the list went as wide as it may. `default`: back to the default width.
   */
  reason: "fit" | "max" | "default";
}

/** Double-click on the divider: expand to the fit width, or go back to default. */
export function toggleChatListWidth({
  width,
  containerWidth,
  fitWidth,
}: {
  width: number;
  containerWidth: number;
  fitWidth: number | null;
}): ChatListToggle {
  const back: ChatListToggle = {
    width: clampChatListWidth(CHAT_LIST_DEFAULT_WIDTH, containerWidth),
    reason: "default",
  };
  if (fitWidth == null || !(fitWidth > 0)) return back;
  const fit = reachableFitWidth(fitWidth, containerWidth);
  const target = fit ?? chatListMaxWidth(containerWidth);
  if (!Number.isFinite(target) || width >= target - FIT_TOLERANCE) return back;
  return { width: target, reason: fit != null ? "fit" : "max" };
}
