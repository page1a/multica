"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { FileDown } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { issueTasksOptions } from "@multica/core/issues/queries";
import type { AgentTask } from "@multica/core/types/agent";
import { LogExportDialog } from "./log-export-dialog";
import { useT } from "../../i18n";

// Entry points for the log-export dialog (DENE-599).
//
// Two shapes over one dialog: a compact icon button for surfaces that already
// have a run in hand (the transcript header, an execution-log row), and a
// labelled button for the issue header, which owns no single run and exports
// the newest one.

interface TaskLogExportButtonProps {
  task: AgentTask;
  /** Issue the report lands on. */
  issueId?: string;
  issueIdentifier?: string;
  /** Show the label beside the icon instead of an icon-only button. */
  labelled?: boolean;
  className?: string;
}

/** 「导出日志」 for one run. */
export function TaskLogExportButton({
  task,
  issueId,
  issueIdentifier,
  labelled = false,
  className,
}: TaskLogExportButtonProps) {
  const { t } = useT("logExport");
  const [open, setOpen] = useState(false);
  const label = t(($) => $.button.label);

  return (
    <>
      {labelled ? (
        <Button
          variant="ghost"
          size="sm"
          aria-label={label}
          className={cn("text-muted-foreground", className)}
          onClick={() => setOpen(true)}
        >
          <FileDown />
          {/* Labelled on wide headers, icon-only once the title needs the
              room; `aria-label` above keeps the name stable either way. */}
          <span className="hidden sm:inline">{label}</span>
        </Button>
      ) : (
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={label}
                className={cn("text-muted-foreground", className)}
                onClick={() => setOpen(true)}
              />
            }
          >
            <FileDown className="h-3.5 w-3.5" />
          </TooltipTrigger>
          <TooltipContent>{t(($) => $.button.tooltip)}</TooltipContent>
        </Tooltip>
      )}
      <LogExportDialog
        open={open}
        onOpenChange={setOpen}
        taskId={task.id}
        issueId={issueId}
        issueIdentifier={issueIdentifier}
      />
    </>
  );
}

/**
 * 「导出日志」 for an issue.
 *
 * An issue owns a run history, not one run, so the dialog opens on the newest
 * run — the one a post-mortem is almost always about — and the range picker
 * inside is what reaches the rest. Renders nothing while the issue has no runs
 * at all: a button that can only produce an empty bundle is not an offer.
 */
export function IssueLogExportButton({
  issueId,
  issueIdentifier,
  labelled = true,
  className,
}: {
  issueId: string;
  issueIdentifier?: string;
  labelled?: boolean;
  className?: string;
}) {
  const { data: tasks } = useQuery(issueTasksOptions(issueId));
  const latest = useMemo(() => newestTask(tasks ?? []), [tasks]);
  if (!latest) return null;
  return (
    <TaskLogExportButton
      task={latest}
      issueId={issueId}
      issueIdentifier={issueIdentifier}
      labelled={labelled}
      className={className}
    />
  );
}

/**
 * The run a bare "export the logs" click means.
 *
 * Newest by the timestamp the row actually has: a queued run has no
 * `started_at`, and sorting on a field that is null for half the list would
 * rank by accident.
 */
function newestTask(tasks: AgentTask[]): AgentTask | undefined {
  let newest: AgentTask | undefined;
  let newestAt = -Infinity;
  for (const task of tasks) {
    const at = Date.parse(
      task.started_at ?? task.dispatched_at ?? task.created_at,
    );
    if (!Number.isFinite(at)) continue;
    if (at >= newestAt) {
      newest = task;
      newestAt = at;
    }
  }
  return newest ?? tasks[0];
}
