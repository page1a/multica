"use client";

import { useCallback } from "react";
import type { Attachment } from "@multica/core/types";
import {
  useAttachmentActions,
  useAttachmentPreview,
  usePreviewSequence,
} from "../../../editor";

/**
 * Open a file in the issue's viewer at its place in the page's sequence. A
 * file the sequence doesn't hold opens on its own, and one the viewer can't
 * show downloads. Render `modal` for the on-its-own case.
 */
export function useOpenAttachment() {
  const sequence = usePreviewSequence();
  const preview = useAttachmentPreview();
  const { download } = useAttachmentActions();
  const { openAt } = sequence;
  const { tryOpen } = preview;
  const open = useCallback(
    (attachment: Attachment) => {
      if (openAt(attachment.id)) return;
      if (tryOpen({ kind: "full", attachment })) return;
      download({ attachmentId: attachment.id });
    },
    [openAt, tryOpen, download],
  );
  return { open, modal: preview.modal };
}
