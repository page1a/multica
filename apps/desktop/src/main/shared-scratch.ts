import { ipcMain } from "electron";
import { activeDaemonHealthPort } from "./daemon-manager";

/**
 * The renderer's route to this machine's shared session folder (DENE-622).
 *
 * Questions with no local directory and no repository leave their files
 * there, one subdirectory per conversation. The folder, its size and its
 * sessions exist only on this computer, so the screen talks to the local
 * daemon. This file moves JSON; which directory may be removed is decided
 * by the daemon at the moment of removal.
 */

export interface SharedScratchSession {
  path: string;
  workspace_id?: string;
  session_id?: string;
  last_used?: string;
  size_bytes: number;
  in_use: boolean;
  ours: boolean;
  expired: boolean;
}

export interface SharedScratchSettings {
  enabled: boolean;
  retention_days?: number;
}

export interface SharedScratchReport {
  root: string;
  size_bytes: number;
  settings: SharedScratchSettings;
  sessions: SharedScratchSession[];
}

export type SharedScratchResult =
  | { ok: true; report: SharedScratchReport }
  | { ok: false; reason: "daemon_unavailable" | "error"; error?: string };

const REQUEST_TIMEOUT_MS = 30_000;

async function callDaemon(
  path: string,
  init: RequestInit,
): Promise<SharedScratchResult> {
  const port = await activeDaemonHealthPort();
  if (port === null) return { ok: false, reason: "daemon_unavailable" };
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      ...init,
      signal: controller.signal,
    });
    if (!res.ok) {
      return { ok: false, reason: "error", error: (await res.text()).trim() };
    }
    return { ok: true, report: (await res.json()) as SharedScratchReport };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (/ECONNREFUSED|fetch failed|aborted/i.test(message)) {
      return { ok: false, reason: "daemon_unavailable", error: message };
    }
    return { ok: false, reason: "error", error: message };
  } finally {
    clearTimeout(timer);
  }
}

export function setupSharedScratch(): void {
  ipcMain.handle("shared-scratch:report", () =>
    callDaemon("/sessions/scratch", { method: "GET" }),
  );

  ipcMain.handle(
    "shared-scratch:save-settings",
    (_event, settings: SharedScratchSettings) =>
      callDaemon("/sessions/scratch", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      }),
  );

  ipcMain.handle("shared-scratch:remove", (_event, path: string) =>
    callDaemon(`/sessions/scratch?path=${encodeURIComponent(path ?? "")}`, {
      method: "DELETE",
    }),
  );

  ipcMain.handle("shared-scratch:clean-expired", () =>
    callDaemon("/sessions/scratch?expired=1", { method: "POST" }),
  );
}
