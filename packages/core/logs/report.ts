import type { Issue, TaskLogExport, TaskLogExportBundle } from "../types";

/** Who a reported export should notify. */
export type LogExportMentionKind = "member" | "agent" | "squad";

/** The issue fields the default mention rule reads. */
export type LogExportMentionSubject = Pick<
  Issue,
  "assignee_type" | "assignee_id" | "creator_type" | "creator_id"
>;

/**
 * Build a mention link for the person or agent who owns the run's issue.
 *
 * Kept here rather than inline in a component because the CLI's `--report`
 * emits the same link shape; the two paths must agree or the same "一键上报"
 * notifies different people depending on which surface was used.
 */
export function logExportOwnerMention(
  kind: LogExportMentionKind,
  id: string,
  label = "负责人",
): string {
  return `[@${label}](mention://${kind}/${id})`;
}

/**
 * Resolve who a default report should mention, or `undefined` for nobody.
 *
 * A default mention must never name an agent or a squad: `mention://agent/<id>`
 * enqueues a paid run, and the archetypal report is the log of a run that just
 * died — usually the run of the very agent the issue is assigned to. Waking it
 * back up is the opposite of what a post-mortem should do. So a non-member
 * assignee falls back to the issue's human creator, and an issue with no human
 * on it is mentioned to nobody rather than to a guess.
 *
 * This mirrors the CLI's `exportMention` (server/cmd/multica/cmd_logs.go) field
 * for field; change one and change the other.
 */
export function resolveLogExportMention(
  issue: LogExportMentionSubject | null | undefined,
): string | undefined {
  if (!issue?.assignee_type) return undefined;
  if (issue.assignee_type === "member") {
    return issue.assignee_id
      ? logExportOwnerMention("member", issue.assignee_id, "负责人")
      : undefined;
  }
  if (issue.creator_type === "member" && issue.creator_id) {
    return logExportOwnerMention("member", issue.creator_id, "创建人");
  }
  return undefined;
}

/**
 * Compose the comment body for a reported export.
 *
 * The body is the bundle's own AI summary — already redacted server-side —
 * plus the owner mention and, when the bundle went to the workspace log
 * repository instead of the issue, the link to the committed file. Reusing the
 * summary rather than re-describing the export client-side keeps the comment,
 * the artifact, and the CLI's report saying the same thing.
 */
export function buildLogExportReportComment(
  exported: Pick<TaskLogExport, "bundle"> | Pick<TaskLogExportBundle, "task" | "summary_markdown">,
  mention?: string,
  link?: string,
): string {
  const bundle = "bundle" in exported ? exported.bundle : exported;
  const parts: string[] = [];

  const summary = bundle.summary_markdown.trim();
  if (summary) {
    parts.push(summary);
  } else {
    const subject = bundle.task?.issue_identifier?.trim() || bundle.task?.id?.trim();
    parts.push(subject ? `运行日志导出 · ${subject}` : "运行日志导出");
  }

  const href = link?.trim();
  if (href) {
    parts.push(`日志包：[${href}](${href})`);
  }

  const target = mention?.trim();
  if (target) {
    parts.push(target);
  }
  return parts.join("\n\n");
}
