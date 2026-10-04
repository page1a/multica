"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { sharingAccessOptions, type SharingAccessKind } from "@multica/core/visibility";
import { copyText } from "@multica/ui/lib/clipboard";
import { ShareScopeDialog } from "../common/share-scope-dialog";
import { PrivateLinkPrompt } from "../common/sharing-guide";
import { useT } from "../i18n";

export interface PrivateLinkModalData {
  kind: SharingAccessKind;
  id: string;
  url: string;
  label?: string;
  projectId?: string | null;
}

/** Opens the prompt for a private issue or project link; copies directly otherwise. */
export async function copyResourceLink(
  open: (modal: "private-link", data: Record<string, unknown>) => void,
  link: PrivateLinkModalData & { visibility?: string },
  messages: { copied: string; failed: string },
) {
  if (link.visibility === "private") {
    const { visibility: _visibility, ...data } = link;
    open("private-link", { ...data });
    return;
  }
  if (await copyText(link.url)) toast.success(messages.copied);
  else toast.error(messages.failed);
}

/**
 * Step before a private issue or project link leaves the app: warn that the
 * recipient will not be able to open it, and offer the share dialog first.
 * After a scope change the link is copied, so the original intent still
 * completes.
 */
export function PrivateLinkModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("common");
  const wsId = useWorkspaceId();
  const link = data as unknown as PrivateLinkModalData | null;
  const [step, setStep] = useState<"prompt" | "share">("prompt");
  const { data: access } = useQuery({
    ...sharingAccessOptions(wsId, link?.kind ?? "issue", link?.id ?? ""),
    enabled: !!link?.id,
  });
  if (!link) return null;

  const copy = async () => {
    if (await copyText(link.url)) toast.success(t(($) => $.share_guide.private_link.copied));
    else toast.error(t(($) => $.share_guide.private_link.copy_failed));
  };

  if (step === "share") {
    return (
      <ShareScopeDialog
        open
        onOpenChange={(open) => {
          if (!open) onClose();
        }}
        target={
          link.kind === "issue"
            ? { kind: "issue", resourceId: link.id, currentScope: "private", projectId: link.projectId, resourceLabel: link.label }
            : { kind: "project", resourceId: link.id, currentScope: "private", resourceLabel: link.label }
        }
        onSaved={() => void copy()}
      />
    );
  }

  return (
    <PrivateLinkPrompt
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
      kind={link.kind}
      canChange={access?.can_change ?? false}
      onCopyAnyway={() => {
        onClose();
        void copy();
      }}
      onChangeScope={() => setStep("share")}
    />
  );
}
