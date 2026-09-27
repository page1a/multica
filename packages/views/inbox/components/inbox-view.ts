/**
 * Which of the inbox's two lists is showing.
 *
 * "inbox" is the default list of active notifications; "archived" is the
 * sub-view reached from the entry at the bottom of that list. The two are
 * mutually exclusive per issue — the server decides which list an issue
 * belongs to (see ListArchivedInboxItems) — so a view is a complete swap of
 * the list's data source and row action, not a filter layered on one list.
 *
 * Persisted in the URL as `?view=archived` so a refresh, a back/forward step,
 * or a mobile detail-back returns to the list the user was actually in.
 */
export type InboxView = "inbox" | "archived";

export const ARCHIVED_VIEW_PARAM = "archived";

/**
 * The inbox has two layers (DENE-882): the one-row-per-issue board on top, and
 * the full notification list below it, opened as `?layer=activity`. A link that
 * already points into the list — `?issue=` or `?view=archived` — lands there too.
 */
export const LAYER_PARAM = "layer";
export const ACTIVITY_LAYER_PARAM = "activity";

export function isActivityLayer(searchParams: URLSearchParams): boolean {
  return (
    searchParams.get(LAYER_PARAM) === ACTIVITY_LAYER_PARAM ||
    searchParams.has("issue") ||
    searchParams.get("view") === ARCHIVED_VIEW_PARAM
  );
}
