import type { Issue } from "../types";

/** Metadata key the server writes when a ticket skipped acceptance (DENE-1678). */
export const REVIEW_SKIP_KEY = "review_skip";

/**
 * Why a done ticket had no acceptance seat — its PR was merged and already
 * reviewed — or "" when there is none to show. The server clears the key when
 * the ticket goes back to review; this only guards against a stale copy.
 */
export function reviewSkipReason(issue: Pick<Issue, "status" | "metadata">): string {
  const reason = issue.metadata?.[REVIEW_SKIP_KEY];
  if (issue.status !== "done" || typeof reason !== "string") return "";
  return reason.trim();
}
