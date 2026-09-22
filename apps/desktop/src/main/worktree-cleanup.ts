import { ipcMain } from "electron";
import { activeDaemonHealthPort } from "./daemon-manager";

/**
 * The renderer's route to this machine's parallel-copy cleanup (DENE-617).
 *
 * Everything the storage screen shows — directories, their sizes, their git
 * state — exists only on this computer. The backend has never seen any of it,
 * and shipping absolute paths to a server to render a local disk report would
 * put them somewhere they do not need to be. So the screen talks to the local
 * daemon's loopback endpoint, through this bridge.
 *
 * The bridge itself decides nothing. Which copy may be removed is the daemon's
 * rule, re-checked against the live filesystem at the moment of removal; this
 * file moves JSON.
 */

/** One working copy, with the verdict the daemon's policy reached on it. */
export interface WorktreeCleanupItem {
  path: string;
  git_root: string;
  branch: string;
  multica_created: boolean;
  in_use: boolean;
  last_run_at: string;
  dirty: boolean;
  merged: boolean;
  /**
   * How the branch was found to be delivered (DENE-647). "ancestor" is a
   * plain merge; "squash" means its content is in trunk while its commits are
   * not, which is what this repository's squash merges leave behind. Absent on
   * a copy that is not delivered, and on daemons that predate the distinction.
   */
  merged_via?: "ancestor" | "squash";
  unknown: boolean;
  size_bytes: number;
  /** Absent means the copy qualifies for removal. */
  keep_reason?:
    | "not_multica_created"
    | "in_use"
    | "too_recent"
    | "uncommitted_changes"
    | "branch_not_merged"
    | "status_unknown";
}

export interface WorktreeCleanupSettings {
  enabled: boolean;
  min_age_days?: number;
  trunk_branch?: string;
}

export interface WorktreeCleanupReport {
  settings: WorktreeCleanupSettings;
  items: WorktreeCleanupItem[];
  /** Roots that could not be scanned, so a partial report is visibly partial. */
  errors?: string[];
}

export type WorktreeCleanupResult =
  | { ok: true; report: WorktreeCleanupReport }
  /**
   * `reason` separates "the daemon is not up" from "the daemon refused".
   * The first is a state the user fixes by starting the daemon; the second
   * carries a rule they need to read.
   */
  | { ok: false; reason: "daemon_unavailable" | "error"; error?: string };

const REQUEST_TIMEOUT_MS = 30_000;

async function callDaemon(
  path: string,
  init: RequestInit,
): Promise<WorktreeCleanupResult> {
  const port = await activeDaemonHealthPort();
  if (port === null) return { ok: false, reason: "daemon_unavailable" };
  const controller = new AbortController();
  // The scan sizes directories and runs git per copy; it is bounded but not
  // instant. A hung daemon must not hang the settings screen either.
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      ...init,
      signal: controller.signal,
    });
    if (!res.ok) {
      return { ok: false, reason: "error", error: (await res.text()).trim() };
    }
    return { ok: true, report: (await res.json()) as WorktreeCleanupReport };
  } catch (err) {
    // A connection refused here means the daemon is not listening — the same
    // user-visible situation as having no port at all.
    const message = err instanceof Error ? err.message : String(err);
    if (/ECONNREFUSED|fetch failed|aborted/i.test(message)) {
      return { ok: false, reason: "daemon_unavailable", error: message };
    }
    return { ok: false, reason: "error", error: message };
  } finally {
    clearTimeout(timer);
  }
}

export function setupWorktreeCleanup(): void {
  ipcMain.handle("worktree-cleanup:report", () =>
    callDaemon("/worktrees/cleanup", { method: "GET" }),
  );

  ipcMain.handle(
    "worktree-cleanup:save-settings",
    (_event, settings: WorktreeCleanupSettings) =>
      callDaemon("/worktrees/cleanup", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(settings),
      }),
  );

  ipcMain.handle("worktree-cleanup:remove", (_event, path: string) =>
    callDaemon(`/worktrees/cleanup?path=${encodeURIComponent(path ?? "")}`, {
      method: "DELETE",
    }),
  );
}
