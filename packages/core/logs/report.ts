import type { TaskLogExport, TaskLogExportBundle } from "../types";

/** Who a reported export should notify. */
export type LogExportMentionKind = "member" | "agent" | "squad";

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
 * Compose the comment body for a reported export.
 *
 * The body is the bundle's own AI summary — already redacted server-side —
 * plus the owner mention. Reusing the summary rather than re-describing the
 * export client-side keeps the comment, the artifact, and the CLI's report
 * saying the same thing.
 */
export function buildLogExportReportComment(
  exported: Pick<TaskLogExport, "bundle"> | Pick<TaskLogExportBundle, "task" | "summary_markdown">,
  mention?: string,
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

  const target = mention?.trim();
  if (target) {
    parts.push(target);
  }
  return parts.join("\n\n");
}
