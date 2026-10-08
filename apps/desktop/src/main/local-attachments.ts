import path from "node:path";
import { ipcMain, shell } from "electron";
import { activeDaemonHealthPort } from "./daemon-manager";

/**
 * Open attachments an agent on this machine uploaded, in place (DENE-1549).
 *
 * The local daemon keeps a copy of every upload its tasks make, keyed by
 * attachment id. The renderer only ever hands over an attachment id: this
 * module asks the daemon where the copy is and opens it with the OS. The path
 * stays in the main process — it never reaches the renderer and never leaves
 * this machine.
 */

const ATTACHMENT_ID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Only attachment ids cross the bridge; anything else is refused. */
export function isAttachmentId(value: unknown): value is string {
  return typeof value === "string" && ATTACHMENT_ID.test(value);
}

/** What the renderer may learn about a local copy: whether there is one. */
export interface LocalAttachmentStatus {
  available: boolean;
}

export type LocalAttachmentActionResult =
  | { ok: true }
  | { ok: false; reason: "invalid_id" | "not_found" | "open_failed" };

interface Deps {
  daemonPort: () => Promise<number | null>;
  fetch: typeof fetch;
  openPath: (path: string) => Promise<string>;
  showItemInFolder: (path: string) => void;
}

const REQUEST_TIMEOUT_MS = 5_000;

export function createLocalAttachments(deps: Deps) {
  // Where the daemon says the copy for `id` is, or null. A copy that was
  // deleted or edited since the upload is a 404 from the daemon.
  async function locate(id: string): Promise<string | null> {
    const port = await deps.daemonPort();
    if (port === null) return null;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
    try {
      const res = await deps.fetch(
        `http://127.0.0.1:${port}/outputs?attachment_id=${encodeURIComponent(id)}`,
        { method: "GET", signal: controller.signal },
      );
      if (!res.ok) return null;
      const body = (await res.json()) as { path?: unknown };
      const found = typeof body.path === "string" ? body.path : "";
      // The copy has to sit in a directory named after this very id: a
      // daemon answer that points anywhere else is not opened.
      if (
        !path.isAbsolute(found) ||
        path.basename(path.dirname(found)).toLowerCase() !== id.toLowerCase()
      ) {
        return null;
      }
      return found;
    } catch {
      return null;
    } finally {
      clearTimeout(timer);
    }
  }

  return {
    async status(id: unknown): Promise<LocalAttachmentStatus> {
      if (!isAttachmentId(id)) return { available: false };
      return { available: (await locate(id)) !== null };
    },
    async open(id: unknown): Promise<LocalAttachmentActionResult> {
      if (!isAttachmentId(id)) return { ok: false, reason: "invalid_id" };
      const found = await locate(id);
      if (!found) return { ok: false, reason: "not_found" };
      const error = await deps.openPath(found);
      return error ? { ok: false, reason: "open_failed" } : { ok: true };
    },
    async reveal(id: unknown): Promise<LocalAttachmentActionResult> {
      if (!isAttachmentId(id)) return { ok: false, reason: "invalid_id" };
      const found = await locate(id);
      if (!found) return { ok: false, reason: "not_found" };
      deps.showItemInFolder(found);
      return { ok: true };
    },
  };
}

export function setupLocalAttachments(): void {
  const local = createLocalAttachments({
    daemonPort: activeDaemonHealthPort,
    fetch: (input, init) => fetch(input, init),
    openPath: (p) => shell.openPath(p),
    showItemInFolder: (p) => shell.showItemInFolder(p),
  });
  ipcMain.handle("local-attachment:status", (_event, id: unknown) =>
    local.status(id),
  );
  ipcMain.handle("local-attachment:open", (_event, id: unknown) =>
    local.open(id),
  );
  ipcMain.handle("local-attachment:reveal", (_event, id: unknown) =>
    local.reveal(id),
  );
}
