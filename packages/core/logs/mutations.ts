import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { TaskLogExportProgress } from "../api/client";
import { issueKeys } from "../issues/queries";
import { buildLogExportReportComment, resolveLogExportMention } from "./report";
import type {
  Comment,
  TaskLogExport,
  TaskLogExportPush,
  TaskLogExportReport,
  TaskLogExportScope,
} from "../types";

export interface ExportTaskLogsVars {
  taskId: string;
  scope: TaskLogExportScope;
  /** Only meaningful with `scope: "hours"`. */
  hours?: number;
  /** Live readout of the streamed artifact; see `api.exportTaskLogs`. */
  onProgress?: (progress: TaskLogExportProgress) => void;
}

/**
 * Fetch a log export bundle for a run.
 *
 * A mutation rather than a query: an export is a document the reader asks for
 * once, at a chosen scope, and `generated_at` moves on every call. Caching it
 * as server state would either serve a stale artifact or refetch a
 * multi-megabyte body behind the user's back.
 */
export function useExportTaskLogs() {
  return useMutation({
    mutationFn: (vars: ExportTaskLogsVars): Promise<TaskLogExport> =>
      api.exportTaskLogs(vars.taskId, {
        scope: vars.scope,
        hours: vars.hours,
        onProgress: vars.onProgress,
      }),
  });
}

export interface ReportTaskLogExportVars {
  /** The fetched export. Its `artifact` is uploaded verbatim on the fallback. */
  exported: TaskLogExport;
  /** The issue the export is reported on — normally `exported.bundle.task.issue_id`. */
  issueId: string;
  /** The run being reported; defaults to the bundle's own `task.id`. */
  taskId?: string;
  /** Ready-made mention link, e.g. from `logExportOwnerMention`. */
  mention?: string;
  /** The scope the export was fetched with, so the push rebuilds the same document. */
  scope?: TaskLogExportScope;
  hours?: number;
  /**
   * `"attachment"` skips the workspace git repository and always uploads the
   * artifact, which is what the dialog uses when the operator asks for the
   * old behavior. Defaults to `"auto"`: try the repository, fall back to the
   * attachment.
   */
  channel?: "auto" | "attachment";
  /**
   * A push that already landed, supplied to retry only the comment. When set,
   * the repository is not contacted again and the artifact is never uploaded:
   * the bundle is committed, so the only step left is the link comment.
   */
  pushed?: TaskLogExportPush;
}

/**
 * The push landed but the comment that would link it did not.
 *
 * Kept distinct from every other report failure because the recovery differs:
 * the artifact is already committed, so the useful next step is to send the
 * comment again — never to upload the bundle as an attachment, which is the
 * multi-megabyte request the repository channel exists to avoid. It carries
 * the link so the dialog can hand the reader the committed file even when the
 * comment keeps failing.
 */
export class LogExportCommentError extends Error {
  readonly push: TaskLogExportPush;
  readonly issueId: string;
  readonly reason: string;

  constructor(push: TaskLogExportPush, issueId: string, cause: unknown) {
    const reason = cause instanceof Error ? cause.message : String(cause);
    super(reason);
    this.name = "LogExportCommentError";
    this.push = push;
    this.issueId = issueId;
    this.reason = reason;
  }
}

/**
 * Report a fetched log export on its issue.
 *
 * Two channels, in order:
 *
 *  1. The workspace's configured log repository. The server rebuilds the
 *     bundle with the same generator and commits it, so the comment carries a
 *     link instead of a multi-megabyte attachment — the upload path on this
 *     instance dies on large bodies long before GitHub would.
 *  2. The ordinary comment attachment, composed from the two calls the normal
 *     comment composer already makes (`uploadFile`, then `createComment` with
 *     the attachment). The fetched `artifact` is uploaded byte for byte; it is
 *     never re-encoded from the parsed bundle.
 *
 * Falling back is not an error path: a workspace with no repository configured
 * reports exactly the way it did before the repository existed. The reason is
 * returned so the dialog can say which way it went.
 *
 * The fallback only covers a push that FAILED. A push that succeeded and a
 * comment that then failed is a `LogExportCommentError` carrying the link, not
 * a reason to upload the artifact: the bytes are already committed, and the
 * caller retries the comment (or shows the link and the reason).
 */
export function useReportTaskLogExport() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (vars: ReportTaskLogExportVars): Promise<TaskLogExportReport> => {
      // Resolved here rather than required from the caller: the mention rule
      // needs the issue's assignee/creator, and the surfaces that open the
      // dialog (a run row, a transcript) do not all have the issue in hand.
      const mention = vars.mention ?? (await resolveMention(vars.issueId));
      const report = { ...vars, mention };

      // A comment-only retry: the push already landed, so the repository is
      // not contacted again and the artifact is not re-uploaded.
      if (vars.pushed) {
        return reportViaRepository(report, vars.pushed);
      }

      if (vars.channel !== "attachment") {
        // The push and the comment are separated on purpose. Once the push
        // succeeds the bundle is in the repository; a comment failure from
        // here must not drop into the attachment fallback, because that would
        // POST the whole multi-megabyte artifact over the very upload path
        // this channel exists to avoid — and if that upload also failed, the
        // committed file would be left unlinked.
        let push: TaskLogExportPush;
        try {
          push = await api.pushTaskLogExport(
            vars.taskId || vars.exported.bundle.task.id,
            {
              scope: vars.scope ?? "run",
              hours: vars.hours,
            },
          );
        } catch (error) {
          const fallbackReason =
            error instanceof Error ? error.message : String(error);
          return reportViaAttachment(report, fallbackReason);
        }

        try {
          return await reportViaRepository(report, push);
        } catch (error) {
          throw new LogExportCommentError(push, vars.issueId, error);
        }
      }

      return reportViaAttachment(report);
    },
    onSuccess: (_report, vars) => {
      // The comment lands in the issue timeline; refresh it so the reported
      // attachment shows without a manual reload.
      queryClient.invalidateQueries({ queryKey: issueKeys.timeline(vars.issueId) });
    },
  });
}

/**
 * Resolve the default mention for an issue, or `undefined` for nobody.
 *
 * A lookup failure is not a report failure: the comment still goes out, just
 * without a mention. Guessing a target would be worse than silence — the one
 * thing the rule exists to prevent is waking an agent that just died.
 */
async function resolveMention(issueId: string): Promise<string | undefined> {
  try {
    return resolveLogExportMention(await api.getIssue(issueId));
  } catch {
    return undefined;
  }
}

/** Comment body for a bundle that was committed to the workspace repository. */
async function reportViaRepository(
  vars: ReportTaskLogExportVars,
  push: TaskLogExportPush,
): Promise<TaskLogExportReport> {
  const comment = await api.createComment(
    vars.issueId,
    buildLogExportReportComment(
      { task: vars.exported.bundle.task, summary_markdown: push.summary_markdown },
      vars.mention,
      push.url,
    ),
  );
  return { channel: "git", issueId: vars.issueId, commentId: comment.id, url: push.url };
}

/** Comment body carrying the artifact itself, as before c4. */
async function reportViaAttachment(
  vars: ReportTaskLogExportVars,
  fallbackReason?: string,
): Promise<TaskLogExportReport> {
  const file = new File([vars.exported.artifact], vars.exported.filename, {
    type: "application/json",
  });
  const attachment = await api.uploadFile(file, { issueId: vars.issueId });
  if (!attachment.id) {
    throw new Error("Upload did not return an attachment id");
  }
  const comment: Comment = await api.createComment(
    vars.issueId,
    buildLogExportReportComment(vars.exported, vars.mention),
    undefined,
    undefined,
    [attachment.id],
  );
  return {
    channel: "attachment",
    issueId: vars.issueId,
    commentId: comment.id,
    fallbackReason,
  };
}
