import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { buildLogExportReportComment } from "./report";
import type { Comment, TaskLogExport } from "../types";

export interface ReportTaskLogExportVars {
  /** The fetched export. Its `artifact` is uploaded verbatim. */
  exported: TaskLogExport;
  /** The issue the export is reported on — normally `exported.bundle.task.issue_id`. */
  issueId: string;
  /** Ready-made mention link, e.g. from `logExportOwnerMention`. */
  mention?: string;
}

/**
 * Report a fetched log export on its issue.
 *
 * Deliberately composed from the two calls the ordinary comment composer
 * already makes — `uploadFile`, then `createComment` with the attachment — so
 * a reported export is an ordinary comment attachment: it reuses the existing
 * upload, permission, and realtime paths, and needs no bespoke endpoint. Any
 * export surface (dialog, drawer, command card) can call this hook with
 * whatever the export mutation produced.
 */
export function useReportTaskLogExport() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (vars: ReportTaskLogExportVars): Promise<Comment> => {
      const file = new File([vars.exported.artifact], vars.exported.filename, {
        type: "application/json",
      });
      const attachment = await api.uploadFile(file, { issueId: vars.issueId });
      if (!attachment.id) {
        throw new Error("Upload did not return an attachment id");
      }
      return api.createComment(
        vars.issueId,
        buildLogExportReportComment(vars.exported, vars.mention),
        undefined,
        undefined,
        [attachment.id],
      );
    },
    onSuccess: (_comment, vars) => {
      // The comment lands in the issue timeline; refresh it so the reported
      // attachment shows without a manual reload.
      queryClient.invalidateQueries({ queryKey: issueKeys.timeline(vars.issueId) });
    },
  });
}
