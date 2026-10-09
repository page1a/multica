"use client";

import { RotateCcw, ShieldCheck } from "lucide-react";
import type { InboxItem } from "@multica/core/types";
import { useKeepIssueStall, useUndoIssueStall } from "@multica/core/issues";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import { useT } from "../../i18n";

/** Inbox keeps the server-owned actions available without opening the issue. */
export function StallActionNotice({ item }: { item: InboxItem }) {
  const { t } = useT("inbox");
  const keep = useKeepIssueStall();
  const undo = useUndoIssueStall();
  if (!item.issue_id) return null;
  const rawActions = (item.details as { actions?: unknown } | null)?.actions;
  const actions = Array.isArray(rawActions) ? rawActions.filter((action): action is string => typeof action === "string") : [];
  const canKeep = actions.includes("keep");
  const canUndo = actions.includes("undo");
  const pending = keep.isPending || undo.isPending;
  const onError = (err: Error) => toast.error(err.message || t(($) => $.detail.stall_action_failed));
  return (
    <div className="mx-4 mt-3 rounded-lg border border-amber-500/40 bg-amber-500/5 p-4">
      <p className="text-body font-medium">{item.title}</p>
      {item.body ? <p className="mt-2 whitespace-pre-wrap text-caption text-muted-foreground">{item.body}</p> : null}
      <div className="mt-3 flex flex-wrap gap-2">
        {canKeep ? (
          <Button size="sm" variant="outline" disabled={pending} onClick={() => keep.mutate(item.issue_id!, { onError })}>
            <ShieldCheck className="mr-1 size-3.5" />{t(($) => $.detail.stall_keep)}
          </Button>
        ) : null}
        {canUndo ? (
          <Button size="sm" variant="outline" disabled={pending} onClick={() => undo.mutate(item.issue_id!, { onError })}>
            <RotateCcw className="mr-1 size-3.5" />{t(($) => $.detail.stall_undo)}
          </Button>
        ) : null}
      </div>
    </div>
  );
}
