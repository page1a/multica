"use client";

import { useCallback, useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { getShortcutPlatform } from "@multica/core/shortcuts";
import { useT } from "../i18n";
import { openExternal } from "../platform";
import {
  canOpenLocalAttachments,
  localAttachmentAvailable,
  openLocalAttachment,
  revealLocalAttachment,
} from "../platform/local-attachments";
import { useDownloadAttachment } from "./use-download-attachment";

/** What a surface knows about the file the reader asked for. */
export interface AttachmentTarget {
  /** Attachment id once the file resolves to a record. */
  attachmentId?: string | null;
  /** The URL as rendered, for files with no record (external links). */
  url?: string;
}

export interface AttachmentActions {
  /**
   * Hand the file to the reader: a known attachment is re-signed and
   * downloaded; a bare URL opens externally.
   */
  download: (target: AttachmentTarget) => void;
}

/**
 * The one place that decides what happens when a reader asks for a file's
 * bytes. File cards, the preview viewer, deliverables and markdown links all
 * come through here instead of calling the download hook or `openExternal`
 * themselves.
 */
export function useAttachmentActions(): AttachmentActions {
  const downloadById = useDownloadAttachment();
  const download = useCallback(
    ({ attachmentId, url }: AttachmentTarget) => {
      if (attachmentId) {
        void downloadById(attachmentId);
        return;
      }
      if (url) openExternal(url);
    },
    [downloadById],
  );
  return useMemo(() => ({ download }), [download]);
}

/** Open / reveal for a file whose copy is on this computer. */
export interface LocalAttachment {
  open: () => void;
  reveal: () => void;
  /** "Show in Finder" on macOS, "Show in folder" elsewhere. */
  revealLabel: string;
}

// Machine-local, not workspace data: an attachment id names one file
// everywhere, and the answer depends only on this computer's daemon.
const localAttachmentKey = (attachmentId: string) =>
  ["local-attachment", attachmentId] as const;

/**
 * The desktop app's in-place actions for a file an agent on this computer
 * uploaded (DENE-1549). Null on web, in a desktop build without the bridge,
 * and whenever the copy is gone or was changed — the surface then offers the
 * download it always did. An action that finds the copy gone in the meantime
 * downloads instead, so the reader always gets the file.
 */
export function useLocalAttachment(
  attachmentId?: string | null,
): LocalAttachment | null {
  const { t } = useT("editor");
  const queryClient = useQueryClient();
  const { download } = useAttachmentActions();
  const enabled = !!attachmentId && canOpenLocalAttachments();
  const { data: available = false } = useQuery({
    queryKey: localAttachmentKey(attachmentId ?? ""),
    queryFn: () => localAttachmentAvailable(attachmentId!),
    enabled,
    staleTime: 30_000,
  });

  return useMemo(() => {
    if (!enabled || !available || !attachmentId) return null;
    const run = (action: (id: string) => Promise<boolean>) => () => {
      void action(attachmentId).then((ok) => {
        if (ok) return;
        toast(t(($) => $.attachment.local_copy_gone));
        void queryClient.invalidateQueries({
          queryKey: localAttachmentKey(attachmentId),
        });
        download({ attachmentId });
      });
    };
    return {
      open: run(openLocalAttachment),
      reveal: run(revealLocalAttachment),
      revealLabel:
        getShortcutPlatform() === "macos"
          ? t(($) => $.attachment.reveal_local_finder)
          : t(($) => $.attachment.reveal_local),
    };
  }, [enabled, available, attachmentId, queryClient, download, t]);
}
