"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueGoalOptions } from "@multica/core/issues/queries";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { GoalCompletionPanel } from "../issues/draft/goal-completion-panel";
import { useT } from "../i18n";

export function GoalCompletionModal({ onClose, data }: { onClose: () => void; data?: Record<string, unknown> | null }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const issueId = typeof data?.issueId === "string" ? data.issueId : "";
  const title = typeof data?.title === "string" ? data.title : undefined;
  const passedChecks = Array.isArray(data?.initialChecks)
    ? data.initialChecks.filter((value): value is string => typeof value === "string")
    : undefined;
  // Quick create and chat-to-goal leave a drafted line on the server; start
  // the panel from it so the human edits the drafted checks, not defaults.
  const { data: goal, isPending } = useQuery({
    ...issueGoalOptions(wsId, issueId),
    enabled: !!wsId && !!issueId && !passedChecks,
  });
  const draftChecks = goal?.status === "draft"
    ? goal.checks.map((check) => check.description ?? check.title ?? "").filter(Boolean)
    : undefined;
  const waitingForDraft = !passedChecks && !!issueId && isPending;
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85dvh] w-full max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="sr-only">{t(($) => $.detail.goal.completion_title)}</DialogTitle>
        </DialogHeader>
        {issueId && !waitingForDraft ? (
          <GoalCompletionPanel
            issueId={issueId}
            title={title}
            initialChecks={passedChecks ?? (draftChecks?.length ? draftChecks : undefined)}
            onConfirmed={onClose}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
