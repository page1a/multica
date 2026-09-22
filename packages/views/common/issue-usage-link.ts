// Deep link for one issue's Token cost view.
//
// The workspace usage page links with it; the issue detail route opens the
// usage dialog from it and mirrors the dialog's open state back into the URL,
// so a link is copyable and a refresh lands on the same view.
//
// A dedicated `usage` param rather than the agent/skill pages' `?view=`: the
// issue detail can also be rendered inside the inbox side panel, which already
// owns `?view=` for its archived/inbox toggle. Sharing that name would let a
// dialog toggle in the panel rewrite the inbox's own view state.
export const ISSUE_USAGE_QUERY_PARAM = "usage";

/** The link from the workspace usage page into an issue's Token cost view. */
export function issueUsageHref(issueDetailPath: string): string {
  return `${issueDetailPath}?${ISSUE_USAGE_QUERY_PARAM}=1`;
}

/**
 * Whether the current location asks for the issue's Token cost view. Only the
 * exact value `1` counts: a typo or a future value must fall back to the
 * closed dialog rather than opening a view the link never named.
 */
export function hasIssueUsageDeepLink(searchParams: URLSearchParams): boolean {
  return searchParams.get(ISSUE_USAGE_QUERY_PARAM) === "1";
}
