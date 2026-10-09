import { RotateCcw, ShieldCheck, Timer } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { toast } from "sonner";
import type { Issue } from "@multica/core/types";
import { useKeepIssueStall, useUndoIssueStall } from "@multica/core/issues";
import { useT } from "../../i18n";

/** Shared ticket-level affordances for the server-owned stall protocol. */
export function IssueStallActionBanner({ issue }: { issue: Issue }) {
  const { t } = useT("inbox");
  const action = typeof issue.metadata?.["stall.action"] === "string" ? issue.metadata["stall.action"] : "";
  const reason = typeof issue.metadata?.["stall.reason"] === "string" ? issue.metadata["stall.reason"] : "";
  const keep = useKeepIssueStall();
  const undo = useUndoIssueStall();
  if (!action || (action !== "announced" && action !== "parent_completed" && action !== "cancelled")) return null;
  const announced = action === "announced";
  const pending = keep.isPending || undo.isPending;
  const onError = (err: Error) => toast.error(err.message || t(($) => $.detail.stall_action_failed));
  return (
    <div className="mb-3 flex items-start gap-3 rounded-lg border border-amber-300/60 bg-amber-50/60 p-3 text-caption dark:bg-amber-950/20">
      {announced ? <Timer className="mt-0.5 size-4 shrink-0 text-amber-600" /> : <ShieldCheck className="mt-0.5 size-4 shrink-0 text-emerald-600" />}
      <div className="min-w-0 flex-1">
        <div className="font-medium">{announced ? t(($) => $.detail.stall_review_title) : t(($) => $.detail.stall_processed_title)}</div>
        {reason && <div className="mt-1 text-muted-foreground">{reason}</div>}
        <div className="mt-2 flex gap-2">
          {announced ? (
            <Button size="sm" variant="outline" disabled={pending} onClick={() => keep.mutate(issue.id, { onError })}><ShieldCheck className="mr-1 size-3.5" />{t(($) => $.detail.stall_keep)}</Button>
          ) : (
            <Button size="sm" variant="outline" disabled={pending} onClick={() => undo.mutate(issue.id, { onError })}><RotateCcw className="mr-1 size-3.5" />{t(($) => $.detail.stall_undo)}</Button>
          )}
        </div>
      </div>
    </div>
  );
}
