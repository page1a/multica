"use client";

import type { ReactNode } from "react";
import { useModalStore } from "@multica/core/modals";
import type { CreateMode } from "@multica/core/issues/stores";
import { CreateIssueDialog } from "./create-issue-dialog";
import { CreateProjectModal } from "./create-project";
import { CreateSquadModal } from "./create-squad";
import { FeedbackModal } from "./feedback";
import { SetParentIssueModal } from "./set-parent-issue";
import { MarkDuplicateIssueModal } from "./mark-duplicate-issue";
import { AddChildIssueModal } from "./add-child-issue";
import { DeleteIssueConfirmModal } from "./delete-issue-confirm";
import { CloseIssueDialog } from "./close-issue";
import { RunConfirmModal } from "./run-confirm";
import { IssueLimitUpgradeDialog } from "./issue-limit-upgrade-dialog";
import { GoalCompletionModal } from "./goal-completion";
import { PrivateLinkModal } from "./private-link";

/**
 * Which face the create-issue dialog opens on.
 *
 * `create-issue` is the manual face unless the opener asked for the alignment
 * face by name (`initial_mode: "align"`, see `openAlignIssue`). The alignment
 * entry used to be its own `create-issue-draft` modal mounting a second dialog;
 * it is now an initial mode of this one, so switching faces never remounts the
 * Popup and nothing about the alignment input duplicates "New issue".
 */
function createIssueInitialMode(
  data: Record<string, unknown> | null,
): CreateMode {
  return data?.initial_mode === "align" ? "align" : "manual";
}

export function ModalRegistry() {
  const modal = useModalStore((s) => s.modal);
  const data = useModalStore((s) => s.data);
  const close = useModalStore((s) => s.close);

  let activeModal: ReactNode = null;
  switch (modal) {
    // Both modal types open the same shell so the in-modal mode switch is
    // instant — only the inner panel swaps, the Dialog Root stays mounted.
    case "create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode={createIssueInitialMode(data)}
          data={data}
        />
      );
      break;
    case "quick-create-issue":
      activeModal = (
        <CreateIssueDialog
          onClose={close}
          initialMode="agent"
          data={data}
        />
      );
      break;
    case "create-project":
      activeModal = <CreateProjectModal onClose={close} />;
      break;
    case "create-squad":
      activeModal = <CreateSquadModal onClose={close} />;
      break;
    case "feedback":
      activeModal = <FeedbackModal onClose={close} data={data} />;
      break;
    case "issue-set-parent":
      activeModal = <SetParentIssueModal onClose={close} data={data} />;
      break;
    case "issue-mark-duplicate":
      activeModal = <MarkDuplicateIssueModal onClose={close} data={data} />;
      break;
    case "issue-add-child":
      activeModal = <AddChildIssueModal onClose={close} data={data} />;
      break;
    case "issue-delete-confirm":
      activeModal = <DeleteIssueConfirmModal onClose={close} data={data} />;
      break;
    case "issue-close":
      activeModal = <CloseIssueDialog onClose={close} data={data} />;
      break;
    case "issue-run-confirm":
      activeModal = <RunConfirmModal onClose={close} data={data} />;
      break;
    case "goal-completion":
      activeModal = <GoalCompletionModal onClose={close} data={data} />;
      break;
    case "private-link":
      activeModal = <PrivateLinkModal onClose={close} data={data} />;
      break;
  }

  return (
    <>
      {activeModal}
      <IssueLimitUpgradeDialog />
    </>
  );
}
