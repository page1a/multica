import type { Issue } from "../types";

/** Metadata key for what a backlog ticket waits for (DENE-1638). */
export const BACKLOG_WAITING_FOR_KEY = "backlog.waiting_for";

/**
 * The one line a parked ticket waits on, or "" when there is none to show.
 *
 * The server drops the key when a ticket leaves backlog; this only guards the
 * render against a stale copy. The client schema folds backlog and todo into
 * the `unstarted` category, so a custom backlog status is told apart from todo
 * by key.
 */
export function backlogWaitingFor(
  issue: Pick<Issue, "status" | "status_category" | "metadata">,
): string {
  const reason = issue.metadata?.[BACKLOG_WAITING_FOR_KEY];
  if (typeof reason !== "string" || !reason.trim()) return "";
  if (issue.status === "backlog") return reason;
  const category: string | undefined = issue.status_category;
  if (issue.status !== "todo" && (category === "unstarted" || category === "backlog")) return reason;
  return "";
}
