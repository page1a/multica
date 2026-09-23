/**
 * Types for the server-rendered task log export bundle.
 *
 * The bundle is produced by the Go logexport package and handed to every
 * client verbatim. Nothing here recomputes the summary or the redaction:
 * `artifact` is the exact text the server wrote, and the typed `bundle` is
 * only a convenience view for rendering. Uploading `artifact` (never a
 * re-encoded `bundle`) is what keeps the web dialog, the desktop dialog, and
 * `multica logs export` handing the user the same file.
 */

/** Export range. `run` is the requested run, `hours` a lookback across the
 *  issue's runs, `task` the issue's whole run history. */
export type TaskLogExportScope = "run" | "hours" | "task";

export interface TaskLogExportRun {
  task_id: string;
  agent_id?: string;
  agent_name?: string;
  status: string;
  started_at?: string;
  completed_at?: string;
  /** Null when the run is not terminal, or when no daemon reported one. */
  exit_code: number | null;
  failure_reason?: string;
  error?: string;
}

export interface TaskLogExportEntry {
  seq: number;
  task_id: string;
  at: string;
  type: string;
  tool?: string;
  content?: string;
  input?: unknown;
  output?: string;
  output_truncated?: boolean;
}

export interface TaskLogExportTaskView {
  id: string;
  issue_id: string;
  issue_identifier: string;
  issue_title?: string;
  agent_id?: string;
  agent_name?: string;
  status?: string;
  exit_code: number | null;
  scope: {
    kind: TaskLogExportScope;
    /** Present only for the `hours` scope. */
    hours?: number;
  };
  window: {
    from?: string;
    to?: string;
  };
}

/** The artifact's statement about its own masking. */
export interface TaskLogExportRedaction {
  /** The shared token/password patterns ran; true in every built bundle. */
  pattern_rules: boolean;
  /** False when the runs' environment was unreadable, leaving known secret
   *  variable values unmasked. */
  env_deny_list: boolean;
  /**
   * Gate any "safe to forward" affordance on this. When false the bundle may
   * still contain a credential, and the summary says so in prose too.
   */
  complete: boolean;
  note?: string;
}

export interface TaskLogExportBundle {
  format: string;
  version: number;
  generated_at: string;
  task: TaskLogExportTaskView;
  /** Absent on a server older than the marker; treat that as "unknown". */
  redaction?: TaskLogExportRedaction;
  run_count: number;
  entry_count: number;
  /** The transcript hit the server's entry cap; this is a head, not the whole. */
  truncated: boolean;
  runs: TaskLogExportRun[];
  entries: TaskLogExportEntry[];
  /** Markdown an operator can paste straight into an AI chat. */
  summary_markdown: string;
}

/** A fetched export: the verbatim artifact plus its parsed view and name. */
export interface TaskLogExport {
  bundle: TaskLogExportBundle;
  /**
   * Verbatim server artifact. Upload or write this exact text — decoding and
   * re-encoding would break byte-for-byte equality with the CLI output.
   */
  artifact: string;
  /** Server-suggested file name, safe to use as an attachment name. */
  filename: string;
}

/**
 * A completed push of the artifact into the workspace's log repository.
 *
 * The request that produces this carries only the scope — the server rebuilds
 * the bundle with the same generator it would have served over
 * `GET .../logs/export` and commits those bytes. That is deliberate: this
 * instance's upload path through Cloudflare dies on multi-megabyte bodies
 * (100s origin timeout), so a bundle-sized request is exactly what the git
 * path exists to avoid. `summary_markdown` comes back so the comment reports
 * the document that was actually pushed, not a preview that may have aged.
 */
export interface TaskLogExportPush {
  pushed: boolean;
  /** File name inside the repository, e.g. `log-export-DENE-599-....json`. */
  filename: string;
  /** Repository-relative path, e.g. `logs/log-export-DENE-599-....json`. */
  path: string;
  /** Clickable link to the committed file. */
  url: string;
  branch: string;
  /** Web URL of the repository, credentials stripped. */
  repo: string;
  summary_markdown: string;
  entry_count: number;
  run_count: number;
  size_bytes: number;
  /**
   * Mirrors the artifact's own `redaction.complete`. `null` means the pushed
   * bundle did not state it — read that as "not proven clean", never as
   * "clean".
   */
  redaction_complete: boolean | null;
  redaction_note?: string;
  truncated: boolean;
}

/** Where an export landed when it was reported on its issue. */
export type TaskLogExportReportChannel = "git" | "attachment";

/** The outcome of reporting an export on its issue. */
export interface TaskLogExportReport {
  channel: TaskLogExportReportChannel;
  /** The issue the comment landed on. */
  issueId: string;
  commentId: string;
  /** Present for the `git` channel. */
  url?: string;
  /** Why the report fell back to an attachment; present for `attachment`. */
  fallbackReason?: string;
}
