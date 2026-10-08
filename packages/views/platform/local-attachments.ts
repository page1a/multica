// Desktop-only bridge to copies of attachments an agent on this machine
// uploaded (DENE-1549). Only an attachment id crosses the bridge; where the
// copy lives stays in the desktop main process. On web there is no bridge and
// every call answers "no local copy".

export type LocalAttachmentActionResult =
  | { ok: true }
  | { ok: false; reason: "invalid_id" | "not_found" | "open_failed" };

interface LocalAttachmentBridge {
  status: (attachmentId: string) => Promise<{ available: boolean }>;
  open: (attachmentId: string) => Promise<LocalAttachmentActionResult>;
  reveal: (attachmentId: string) => Promise<LocalAttachmentActionResult>;
}

function readBridge(): LocalAttachmentBridge | undefined {
  if (typeof window === "undefined") return undefined;
  const api = (
    window as unknown as {
      desktopAPI?: { localAttachment?: Partial<LocalAttachmentBridge> };
    }
  ).desktopAPI?.localAttachment;
  if (
    typeof api?.status !== "function" ||
    typeof api.open !== "function" ||
    typeof api.reveal !== "function"
  ) {
    return undefined;
  }
  return api as LocalAttachmentBridge;
}

/** True in a desktop build that can open local copies. */
export function canOpenLocalAttachments(): boolean {
  return readBridge() !== undefined;
}

export async function localAttachmentAvailable(
  attachmentId: string,
): Promise<boolean> {
  const bridge = readBridge();
  if (!bridge) return false;
  try {
    return (await bridge.status(attachmentId)).available === true;
  } catch {
    return false;
  }
}

export async function openLocalAttachment(
  attachmentId: string,
): Promise<boolean> {
  const bridge = readBridge();
  if (!bridge) return false;
  try {
    return (await bridge.open(attachmentId)).ok;
  } catch {
    return false;
  }
}

export async function revealLocalAttachment(
  attachmentId: string,
): Promise<boolean> {
  const bridge = readBridge();
  if (!bridge) return false;
  try {
    return (await bridge.reveal(attachmentId)).ok;
  } catch {
    return false;
  }
}
