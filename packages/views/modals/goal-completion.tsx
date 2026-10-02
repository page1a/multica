"use client";

import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { GoalCompletionPanel } from "../issues/draft/goal-completion-panel";
import { useT } from "../i18n";

export function GoalCompletionModal({ onClose, data }: { onClose: () => void; data?: Record<string, unknown> | null }) {
  const { t } = useT("issues");
  const issueId = typeof data?.issue_id === "string" ? data.issue_id : "";
  const title = typeof data?.title === "string" ? data.title : undefined;
  const initialChecks = Array.isArray(data?.initial_checks)
    ? data.initial_checks.filter((value): value is string => typeof value === "string")
    : undefined;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85dvh] w-full max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="sr-only">{t(($) => $.detail.goal.completion_title)}</DialogTitle>
        </DialogHeader>
        {issueId ? (
          <GoalCompletionPanel issueId={issueId} title={title} initialChecks={initialChecks} onConfirmed={onClose} />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
