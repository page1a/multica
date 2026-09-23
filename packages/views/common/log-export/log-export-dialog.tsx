"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import {
  AlertTriangle,
  Check,
  Copy,
  Download,
  FileJson,
  Loader2,
  ShieldAlert,
  ShieldCheck,
  ShieldQuestion,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  Progress,
} from "@multica/ui/components/ui/progress";
import { copyText } from "@multica/ui/lib/clipboard";
import type { TaskLogExportProgress } from "@multica/core/api/client";
import {
  LogExportCommentError,
  useExportTaskLogs,
  useReportTaskLogExport,
} from "@multica/core/logs";
import type {
  TaskLogExport,
  TaskLogExportReport,
  TaskLogExportScope,
} from "@multica/core/types";
import { SegmentedToggle } from "../segmented-toggle";
import { useT } from "../../i18n";

// The 导出日志 dialog (DENE-599, variant A).
//
// Shape, and why it is this shape: the bundle is a document, so the dialog is
// a small form around one document — pick a range, get the bundle, then do one
// of two things with it (report it on the issue, or copy the summary into an
// AI chat). Everything the reader needs to judge the document before sending
// it lives on the card: what it covers, how big it is, and whether the masking
// is complete.
//
// The export itself is one server request. There is no incremental collection
// to watch, so "正在收集" reports measured progress — bytes off the wire plus
// the entries already decoded — rather than a scripted percentage.

/** Default lookback for the "最近 N 小时" range. */
const DEFAULT_HOURS = 6;
const MIN_HOURS = 1;
const MAX_HOURS = 24 * 30;

interface LogExportDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The run being exported. */
  taskId: string;
  /** Issue the report lands on; falls back to the bundle's own issue. */
  issueId?: string;
  issueIdentifier?: string;
  /** What the range picker calls "this run" — a short id or a timestamp. */
  runLabel?: string;
}

export function LogExportDialog({
  open,
  onOpenChange,
  taskId,
  issueId,
  issueIdentifier,
  runLabel,
}: LogExportDialogProps) {
  const { t } = useT("logExport");
  const [scope, setScope] = useState<TaskLogExportScope>("run");
  const [hours, setHours] = useState(DEFAULT_HOURS);
  const [progress, setProgress] = useState<TaskLogExportProgress | null>(null);
  const [exported, setExported] = useState<TaskLogExport | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [restored, setRestored] = useState(false);
  const [report, setReport] = useState<TaskLogExportReport | null>(null);
  const [pendingComment, setPendingComment] = useState<LogExportCommentError | null>(null);
  const [copied, setCopied] = useState(false);

  const exportMutation = useExportTaskLogs();
  const reportMutation = useReportTaskLogExport();

  // Which range the dialog is currently asking for. Every edit to the range —
  // the scope toggle, the hours number, closing the dialog — bumps this, and a
  // response may only land on screen if the range it was requested for is
  // still the current one. Without it, an export started for "the last 6h"
  // still resolves after the reader has typed 24, and the card, summary and
  // download would describe 6h while a report sends the new range: the server
  // rebuilds from the range, so the comment would carry a document the reader
  // never saw.
  const rangeVersionRef = useRef(0);

  // A dialog reopened on another run must not show the previous run's bundle.
  useEffect(() => {
    if (open) return;
    // Closing also abandons anything still in flight: it belongs to a range
    // nobody is looking at any more.
    rangeVersionRef.current += 1;
    setProgress(null);
    setExported(null);
    setFailure(null);
    setRestored(false);
    setReport(null);
    setPendingComment(null);
    setCopied(false);
    setScope("run");
    setHours(DEFAULT_HOURS);
  }, [open]);

  const bundle = exported?.bundle;
  const issue = issueId ?? bundle?.task.issue_id ?? "";
  const identifier = issueIdentifier ?? bundle?.task.issue_identifier ?? "";
  const entries = bundle?.entry_count ?? 0;
  const isEmpty = !!exported && entries === 0 && (bundle?.entries.length ?? 0) === 0;
  const collecting = exportMutation.isPending;

  const runExport = (nextScope: TaskLogExportScope) => {
    // The range this request answers, frozen at the moment it is sent. A
    // request carries `hours` by value, so a later edit must not let its
    // result be treated as the current range's bundle.
    const requestedRange = rangeVersionRef.current;
    setFailure(null);
    setRestored(false);
    setReport(null);
    setPendingComment(null);
    setCopied(false);
    setProgress(null);
    exportMutation.mutate(
      {
        taskId,
        scope: nextScope,
        hours: nextScope === "hours" ? hours : undefined,
        onProgress: setProgress,
      },
      {
        onSuccess: (result) => {
          if (requestedRange !== rangeVersionRef.current) return;
          setExported(result);
        },
        onError: (error) => {
          // A failure for a range the reader has already left is noise, not a
          // state to show.
          if (requestedRange !== rangeVersionRef.current) return;
          setFailure(error instanceof Error ? error.message : String(error));
        },
      },
    );
  };

  // A bundle answers exactly one range, so anything that changes the range
  // drops it rather than leaving a card that describes a different export than
  // a report would send. The scope toggle and the hours number are both range
  // inputs and share this. Editing the range also invalidates whatever is
  // still in flight: a response that arrives afterwards describes the range
  // the reader just left, so it must not be installed as if it answered the
  // current one.
  const discardExported = () => {
    rangeVersionRef.current += 1;
    setExported(null);
    setFailure(null);
    setRestored(false);
    setReport(null);
    setPendingComment(null);
    setCopied(false);
  };

  const changeScope = (next: TaskLogExportScope) => {
    setScope(next);
    discardExported();
  };

  const changeHours = (raw: string) => {
    const parsed = Number(raw);
    if (!Number.isFinite(parsed)) return;
    const next = clampHours(parsed);
    // The input fires only on a real edit; clamping can round back to the
    // current number, and an unchanged range keeps its bundle.
    if (next === hours) return;
    setHours(next);
    discardExported();
  };

  const runReport = () => {
    if (!exported || !issue) return;
    reportMutation.mutate(
      {
        exported,
        issueId: issue,
        taskId,
        scope,
        hours: scope === "hours" ? hours : undefined,
        // A push that already landed is not repeated: the retry sends only
        // the comment that links it.
        pushed: pendingComment?.push,
      },
      {
        onSuccess: (result) => {
          setPendingComment(null);
          setReport(result);
        },
        onError: (error) => {
          if (error instanceof LogExportCommentError) {
            // The bundle is committed, so nothing is uploaded here: the link
            // and the reason stay on screen and the action becomes a
            // comment-only retry.
            setPendingComment(error);
            return;
          }
          toast.error(
            error instanceof Error
              ? error.message
              : t(($) => $.error.report_failed),
          );
        },
      },
    );
  };

  const copySummary = async () => {
    if (!bundle) return;
    const ok = await copyText(bundle.summary_markdown);
    setCopied(ok);
    if (!ok) toast.error(t(($) => $.error.export_failed));
  };

  const rangeLabel = useMemo(() => {
    if (scope === "run") return t(($) => $.range.run);
    if (scope === "hours") return t(($) => $.range.hours, { hours });
    return t(($) => $.range.task);
  }, [scope, hours, t]);

  const runHint = runLabel ?? shortId(taskId);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="sm:!max-w-2xl"
        showCloseButton={false}
        data-testid="log-export-dialog"
      >
        <DialogHeader>
          <DialogTitle>{t(($) => $.dialog.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.dialog.subtitle)}</DialogDescription>
        </DialogHeader>

        <section className="space-y-2">
          <div className="text-caption font-medium text-muted-foreground">
            {t(($) => $.range.section)}
          </div>
          <div role="group" aria-label={t(($) => $.range.section)}>
            <SegmentedToggle<TaskLogExportScope>
              value={scope}
              onChange={changeScope}
              options={[
                [
                  "run",
                  <RangeOption
                    key="run"
                    title={t(($) => $.range.run)}
                    hint={t(($) => $.range.run_hint, { id: runHint })}
                  />,
                ],
                [
                  "hours",
                  <RangeOption
                    key="hours"
                    title={t(($) => $.range.hours, { hours })}
                    hint={t(($) => $.range.hours_hint, {
                      from: hoursFrom(hours),
                    })}
                  />,
                ],
                [
                  "task",
                  <RangeOption
                    key="task"
                    title={t(($) => $.range.task)}
                    hint={t(($) => $.range.task_hint)}
                  />,
                ],
              ]}
            />
          </div>
          {scope === "hours" && (
            <label className="flex items-center gap-2 text-caption text-muted-foreground">
              {t(($) => $.range.hours_input)}
              <input
                type="number"
                min={MIN_HOURS}
                max={MAX_HOURS}
                value={hours}
                onChange={(event) => changeHours(event.target.value)}
                className="h-7 w-20 rounded-md border border-input bg-transparent px-2 text-caption tabular-nums outline-none focus-visible:border-ring"
              />
            </label>
          )}
        </section>

        <section className="space-y-2">
          <div className="text-caption font-medium text-muted-foreground">
            {t(($) => $.state.section)}
          </div>
          <div
            className="rounded-lg border bg-muted/30 p-3"
            aria-live="polite"
            data-testid="log-export-state"
          >
            {collecting ? (
              <CollectingPanel progress={progress} />
            ) : failure ? (
              <FailedPanel
                reason={failure}
                canRestore={!!exported}
                onRetry={() => runExport(scope)}
                onRestore={() => {
                  setFailure(null);
                  setRestored(true);
                }}
              />
            ) : isEmpty ? (
              <EmptyPanel
                rangeLabel={rangeLabel}
                onWiden={() => {
                  setScope("task");
                  runExport("task");
                }}
              />
            ) : exported && bundle ? (
              <>
                <ReadyPanel
                  exported={exported}
                  issueIdentifier={identifier}
                />
                {restored && (
                  <p className="mt-2 text-micro text-warning">
                    {t(($) => $.state.restored)}
                  </p>
                )}
              </>
            ) : (
              <IdlePanel actionLabel={t(($) => $.action.export)} />
            )}
          </div>
        </section>

        {/* The dialog's own actions. The export button is here rather than on
            the card so the primary target never moves between states; the two
            document actions live on the card, where the document is.

            Exactly one action is a filled brand button at a time, and it is
            whichever one moves the reader forward: as long as there is no
            bundle on screen that is this button, and once there is — or once
            the result panel is offering a retry — it becomes the outline
            alternative to the panel's own action. Two filled buttons in one
            dialog leave the eye with no answer to "what now". */}
        <div className="flex items-center justify-end gap-2">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t(($) => $.dialog.cancel)}
          </Button>
          <Button
            variant={exported || failure ? "outline" : "brand"}
            disabled={collecting}
            onClick={() => runExport(scope)}
          >
            {collecting && <Loader2 className="animate-spin" />}
            {exported
              ? t(($) => $.action.reexport)
              : t(($) => $.action.export)}
          </Button>
        </div>

        {exported && bundle && !collecting && !failure && !isEmpty && (
          <div className="flex flex-wrap items-center gap-2 border-t pt-3">
            {issue ? (
              <Button
                variant="brand"
                disabled={reportMutation.isPending || !exported}
                onClick={runReport}
                className="min-w-0 flex-1 sm:flex-none"
              >
                {reportMutation.isPending ? (
                  <Loader2 className="animate-spin" />
                ) : (
                  <FileJson />
                )}
                <span className="truncate">
                  {pendingComment
                    ? t(($) => $.action.retry_comment)
                    : t(($) => $.action.report)}
                </span>
              </Button>
            ) : (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.error.no_issue)}
              </p>
            )}
            <Button variant="outline" onClick={() => void copySummary()}>
              {copied ? <Check /> : <Copy />}
              {copied ? t(($) => $.action.copied) : t(($) => $.action.copy_summary)}
            </Button>
            <p className="basis-full text-micro text-faint-foreground">
              {t(($) => $.report.hint)}
            </p>
            {report && (
              <p className="basis-full text-caption text-muted-foreground">
                <ReportSummary report={report} identifier={identifier} />
              </p>
            )}
            {pendingComment && (
              <p className="basis-full break-words text-caption text-warning">
                <PushLandedNote error={pendingComment} identifier={identifier} />
              </p>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ─── Range picker label ────────────────────────────────────────────────────

function RangeOption({ title, hint }: { title: string; hint: string }) {
  return (
    <span className="flex min-w-0 flex-col items-start leading-tight">
      <span className="truncate">{title}</span>
      <span className="truncate text-micro font-normal text-faint-foreground">
        {hint}
      </span>
    </span>
  );
}

// ─── States ────────────────────────────────────────────────────────────────

function IdlePanel({ actionLabel }: { actionLabel: string }) {
  const { t } = useT("logExport");
  return (
    <div className="space-y-1">
      <p className="text-caption font-medium">{t(($) => $.state.idle_title)}</p>
      <p className="text-caption text-muted-foreground">
        {t(($) => $.state.idle_body, { action: actionLabel })}
      </p>
    </div>
  );
}

/**
 * "Collecting" is measured, never scripted: the bar tracks bytes received
 * against the server's declared length, and the count is the entries decoded
 * from those bytes. A chunked response carries no length, so the bar goes
 * indeterminate rather than inventing a denominator.
 */
function CollectingPanel({ progress }: { progress: TaskLogExportProgress | null }) {
  const { t } = useT("logExport");
  const known = !!progress && progress.totalBytes > 0;
  const percent = known
    ? Math.min(100, Math.round((progress!.receivedBytes / progress!.totalBytes) * 100))
    : 0;

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground" />
        <p className="text-caption font-medium">
          {t(($) => $.state.collecting_title)}
        </p>
      </div>
      <p className="text-caption text-muted-foreground">
        {t(($) => $.state.collecting_body)}
      </p>
      <Progress value={known ? percent : null} aria-label={t(($) => $.state.collecting_title)} />
      <div className="flex items-center justify-between text-micro tabular-nums text-muted-foreground">
        <span>{t(($) => $.state.collected, { n: progress?.entries ?? 0 })}</span>
        {known && (
          <span>
            {t(($) => $.state.received, {
              received: formatBytes(progress!.receivedBytes),
              total: formatBytes(progress!.totalBytes),
            })}
          </span>
        )}
      </div>
    </div>
  );
}

function EmptyPanel({
  rangeLabel,
  onWiden,
}: {
  rangeLabel: string;
  onWiden: () => void;
}) {
  const { t } = useT("logExport");
  return (
    <div className="space-y-2">
      <p className="text-caption font-medium">{t(($) => $.state.empty_title)}</p>
      <p className="text-caption text-muted-foreground">
        {t(($) => $.state.empty_body, { range: rangeLabel })}
      </p>
      <Button variant="outline" size="sm" onClick={onWiden}>
        {t(($) => $.action.widen)}
      </Button>
    </div>
  );
}

/**
 * Failure, with the two honest ways out: retry, or go back to the last bundle
 * that did finish. "只导出已收集部分" cannot mean "hand over the partial
 * response" — the server builds the artifact in one piece, so a broken
 * transfer leaves nothing valid to forward. When no earlier bundle exists the
 * button is absent rather than present-and-useless.
 */
function FailedPanel({
  reason,
  canRestore,
  onRetry,
  onRestore,
}: {
  reason: string;
  canRestore: boolean;
  onRetry: () => void;
  onRestore: () => void;
}) {
  const { t } = useT("logExport");
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <AlertTriangle className="size-3.5 shrink-0 text-destructive" />
        <p className="text-caption font-medium">
          {t(($) => $.state.failed_title)}
        </p>
      </div>
      <p className="text-caption break-words text-muted-foreground">
        {isNetworkFailure(reason) ? t(($) => $.error.export_failed) : reason}
      </p>
      {!canRestore && (
        <p className="text-micro text-faint-foreground">
          {t(($) => $.state.partial_note)}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button variant="brand" size="sm" onClick={onRetry}>
          {t(($) => $.action.retry)}
        </Button>
        {canRestore && (
          <Button variant="outline" size="sm" onClick={onRestore}>
            {t(($) => $.action.partial)}
          </Button>
        )}
      </div>
    </div>
  );
}

/**
 * Is this the browser's own sentence rather than the server's?
 *
 * A dropped connection surfaces as an English "Failed to fetch" (or Safari's
 * "Load failed"), which is not something to show a reader in a localized
 * panel, so those become the translated generic failure. Anything the server
 * actually said passes through untouched: a specific reason is the only thing
 * that tells the reader whether retrying is worth it.
 */
function isNetworkFailure(reason: string): boolean {
  return /failed to fetch|networkerror|load failed|network request failed|err_/i.test(reason);
}

/** The bundle card: what it covers, how big it is, and how clean it is. */
function ReadyPanel({
  exported,
  issueIdentifier,
}: {
  exported: TaskLogExport;
  issueIdentifier: string;
}) {
  const { t } = useT("logExport");
  const { bundle, filename, artifact } = exported;
  const size = useMemo(() => new Blob([artifact]).size, [artifact]);
  const downloadUrl = useObjectUrl(artifact, filename);

  return (
    <div className="space-y-3">
      <div className="flex min-w-0 items-start gap-2">
        <FileJson className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1 space-y-1">
          <p className="truncate text-caption font-medium" title={filename}>
            {filename}
          </p>
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-micro text-muted-foreground">
            <span className="tabular-nums">{formatBytes(size)}</span>
            <span aria-hidden>·</span>
            <span>{t(($) => $.card.runs, { n: bundle.run_count })}</span>
            <span aria-hidden>·</span>
            <span>{t(($) => $.card.entries, { n: bundle.entry_count })}</span>
            {issueIdentifier && (
              <>
                <span aria-hidden>·</span>
                <span>{issueIdentifier}</span>
              </>
            )}
          </p>
          <RedactionBadge exported={exported} />
        </div>
        {downloadUrl && (
          <a
            href={downloadUrl}
            download={filename}
            className="flex shrink-0 items-center gap-1 rounded-md px-1.5 py-1 text-caption text-muted-foreground transition-colors hover:bg-accent/60 hover:text-foreground"
            title={t(($) => $.action.download)}
          >
            <Download className="size-3.5" />
            <span className="sr-only sm:not-sr-only">
              {t(($) => $.action.download)}
            </span>
          </a>
        )}
      </div>

      {bundle.truncated && (
        <p className="text-micro text-warning">{t(($) => $.card.truncated)}</p>
      )}

      <div className="space-y-1">
        <div className="text-micro font-medium text-muted-foreground">
          {t(($) => $.card.summary)}
        </div>
        <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md border bg-background/60 p-2 font-mono text-micro text-muted-foreground">
          {bundle.summary_markdown || t(($) => $.card.no_summary)}
        </pre>
      </div>
    </div>
  );
}

/**
 * The masking statement.
 *
 * `redaction` missing means an older server that never stated it, and
 * `complete: false` means a bundle that may still carry a credential. Both
 * read as "do not forward as is" — only an explicit `complete: true` earns the
 * green line. Fail-open here would be the whole redaction feature undone at
 * the last step.
 */
function RedactionBadge({ exported }: { exported: TaskLogExport }) {
  const { t } = useT("logExport");
  const redaction = exported.bundle.redaction;
  const complete = redaction?.complete === true;
  const known = !!redaction;
  const Icon = complete ? ShieldCheck : known ? ShieldAlert : ShieldQuestion;
  const label = complete
    ? t(($) => $.card.redacted)
    : known
      ? t(($) => $.card.redaction_incomplete)
      : t(($) => $.card.redaction_unknown);

  return (
    <p
      className={
        complete
          ? "flex items-center gap-1 text-micro text-success"
          : "flex items-center gap-1 text-micro text-warning"
      }
      title={redaction?.note || undefined}
    >
      <Icon className="size-3 shrink-0" />
      {label}
    </p>
  );
}

function ReportSummary({
  report,
  identifier,
}: {
  report: TaskLogExportReport;
  identifier: string;
}) {
  const { t } = useT("logExport");
  const issue = identifier || report.issueId;
  if (report.channel === "git") {
    return (
      <>
        {t(($) => $.report.success_git, { issue, repo: report.url ?? "" })}{" "}
        {report.url && (
          <a
            href={report.url}
            target="_blank"
            rel="noreferrer"
            className="underline underline-offset-2"
          >
            {t(($) => $.report.link)}
          </a>
        )}
      </>
    );
  }
  return (
    <>
      {t(($) => $.report.success_attachment, { issue })}
      {report.fallbackReason && (
        <>
          {" "}
          <span title={report.fallbackReason}>
            {t(($) => $.report.fallback, { reason: compactReason(report.fallbackReason) })}
          </span>
        </>
      )}
    </>
  );
}

/**
 * The push landed but the comment did not.
 *
 * The artifact is already in the repository, so the reader gets the link and
 * the reason and the action above becomes a comment-only retry. Nothing here
 * offers the attachment fallback: re-POSTing the bundle is the request this
 * channel exists to avoid, and the repository copy would still need a link.
 */
function PushLandedNote({
  error,
  identifier,
}: {
  error: LogExportCommentError;
  identifier: string;
}) {
  const { t } = useT("logExport");
  const issue = identifier || error.issueId;
  return (
    <>
      {t(($) => $.report.push_landed, {
        issue,
        reason: compactReason(error.reason),
      })}{" "}
      {error.push.url && (
        <a
          href={error.push.url}
          target="_blank"
          rel="noreferrer"
          className="underline underline-offset-2"
        >
          {t(($) => $.report.link)}
        </a>
      )}
    </>
  );
}

/**
 * The fallback reason is raw git stderr, and git is generous: a failed clone
 * quotes its temporary directory, the remote URL, and a multi-line fatal.
 * The panel needs the clause that says what went wrong — git's `fatal:` line
 * when there is one, otherwise the first line — not the transcript. The full
 * text stays available as the element's tooltip.
 */
function compactReason(reason: string): string {
  const lines = reason.split("\n").map((l) => l.trim()).filter((l) => l !== "");
  const fatal = lines.filter((l) => /^(fatal|error|ERROR):/.test(l)).pop();
  const chosen = fatal ?? lines[0] ?? "";
  const firstSentence = chosen.split(/\.\s/)[0] ?? chosen;
  return firstSentence.length > 140 ? `${firstSentence.slice(0, 140)}…` : firstSentence;
}

// ─── helpers ───────────────────────────────────────────────────────────────

function clampHours(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_HOURS;
  return Math.min(MAX_HOURS, Math.max(MIN_HOURS, Math.round(value)));
}

/** The wall-clock instant a lookback window opens at, in the reader's zone. */
function hoursFrom(hours: number): string {
  const from = new Date(Date.now() - hours * 60 * 60 * 1000);
  return from.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function shortId(id: string): string {
  return id.slice(0, 8);
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * An object URL for the artifact, so the card's download hands over the exact
 * bytes the server produced rather than a re-encoded copy of the parsed
 * bundle. Revoked when the card unmounts; a data: URL would be simpler but
 * browsers refuse to navigate to a multi-megabyte one.
 */
function useObjectUrl(artifact: string, filename: string): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (typeof URL.createObjectURL !== "function") return;
    const blob = new Blob([artifact], { type: "application/json" });
    const created = URL.createObjectURL(blob);
    setUrl(created);
    return () => {
      URL.revokeObjectURL(created);
      setUrl(null);
    };
  }, [artifact, filename]);
  return url;
}
