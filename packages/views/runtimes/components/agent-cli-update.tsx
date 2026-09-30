"use client";

import { useState } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import type { AgentRuntime } from "@multica/core/types";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { rowLinkInteractiveProps } from "../../navigation/use-row-link";
import { useT } from "../../i18n";

export interface AgentCLIUpdateView {
  current: string | null;
  latest: string | null;
  autoFollow: boolean;
  phase: string;
  error: string | null;
  note: string | null;
  binaryPath: string | null;
  checkedAt: string | null;
  /** While waiting: this CLI's tasks still running (0 when not reported). */
  waitingTasks: number;
  /** True while new tasks of this CLI are held back for an "update now". */
  claimsPaused: boolean;
  waitReason: string | null;
  /** Set on the report right after a successful upgrade. */
  updatedAt: string | null;
}

export function readAgentCLIUpdate(
  metadata: Record<string, unknown> | null,
): AgentCLIUpdateView | null {
  const raw = metadata?.cli_update;
  if (!raw || typeof raw !== "object") return null;
  const doc = raw as Record<string, unknown>;
  const text = (key: string) =>
    typeof doc[key] === "string" && doc[key] ? (doc[key] as string) : null;
  return {
    current: text("current_version"),
    latest: text("latest_version"),
    autoFollow: doc.auto_follow !== false,
    phase: text("phase") ?? "unknown",
    error: text("error"),
    note: text("note"),
    binaryPath: text("binary_path"),
    checkedAt: text("checked_at"),
    waitingTasks:
      typeof doc.waiting_tasks === "number" && doc.waiting_tasks > 0
        ? doc.waiting_tasks
        : 0,
    claimsPaused: doc.claims_paused === true,
    waitReason: text("wait_reason"),
    updatedAt: text("updated_at"),
  };
}

function cliLabel(provider: string): string {
  switch (provider) {
    case "claude":
      return "Claude";
    case "codex":
      return "Codex";
    case "opencode":
      return "OpenCode";
    default:
      return provider;
  }
}

type StatusTone = "muted" | "default" | "success" | "error";

export function AgentCLIUpdateControls({
  runtime,
  canManage,
  compact = false,
}: {
  runtime: AgentRuntime;
  canManage: boolean;
  compact?: boolean;
}) {
  const { t } = useT("runtimes");
  const reported = readAgentCLIUpdate(
    runtime.metadata as Record<string, unknown> | null,
  );
  const [followOverride, setFollowOverride] = useState<boolean | null>(null);
  const [busy, setBusy] = useState<"follow" | "update" | null>(null);
  const [localError, setLocalError] = useState("");
  // The report the page had when "update now" was accepted. Until the
  // daemon sends a different one, the click is shown as queued rather than
  // falling back to "update available".
  const [requestedAgainst, setRequestedAgainst] = useState<{
    checkedAt: string | null;
    phase: string;
  } | null>(null);

  if (!reported || runtime.runtime_mode !== "local" || runtime.profile_id) {
    return null;
  }

  const follow = followOverride ?? reported.autoFollow;
  const phase = reported.phase;
  const requested =
    busy === "update" ||
    (requestedAgainst !== null &&
      requestedAgainst.checkedAt === reported.checkedAt &&
      requestedAgainst.phase === reported.phase);
  const updating = phase === "updating";
  const cli = cliLabel(runtime.provider);
  const versions =
    reported.current && reported.latest
      ? t(($) => $.agent_cli.versions, {
          current: reported.current,
          latest: reported.latest,
        })
      : (reported.current ?? reported.latest ?? "");
  // Waiting is not an error. An older daemon still put its waiting note in
  // `error`; that one is not shown in red.
  const detail =
    localError || (phase === "waiting" ? null : reported.error);
  const queuedText =
    reported.waitReason === "daemon_update"
      ? t(($) => $.agent_cli.queued_daemon)
      : reported.waitReason === "hold_expired"
        ? t(($) => $.agent_cli.queued_hold_expired, {
            cli,
            count: reported.waitingTasks,
          })
        : reported.waitingTasks > 0
          ? t(($) => $.agent_cli.queued_tasks, {
              cli,
              count: reported.waitingTasks,
            })
          : t(($) => $.agent_cli.waiting);
  const pausedText = reported.claimsPaused
    ? t(($) => $.agent_cli.queued_paused, { cli })
    : null;
  const status: { text: string; tone: StatusTone; spinning: boolean } | null =
    updating
      ? { text: t(($) => $.agent_cli.updating), tone: "default", spinning: true }
      : requested && !localError
        ? {
            text: t(($) => $.agent_cli.queued_request),
            tone: "muted",
            spinning: true,
          }
        : phase === "waiting"
          ? { text: queuedText, tone: "muted", spinning: false }
          : phase === "failed"
            ? {
                text: detail || t(($) => $.agent_cli.update_failed),
                tone: "error",
                spinning: false,
              }
            : phase === "check_failed"
              ? {
                  text: t(($) => $.agent_cli.check_failed),
                  tone: "error",
                  spinning: false,
                }
              : phase === "current" && reported.updatedAt && reported.current
                ? {
                    text: t(($) => $.agent_cli.updated_to, {
                      version: reported.current,
                    }),
                    tone: "success",
                    spinning: false,
                  }
                : phase === "current" && reported.latest
                  ? {
                      text: t(($) => $.agent_cli.up_to_date),
                      tone: "success",
                      spinning: false,
                    }
                  : phase === "available"
                    ? {
                        text: t(($) => $.agent_cli.update_available),
                        tone: "default",
                        spinning: false,
                      }
                    : phase === "unsupported"
                      ? {
                          text: t(($) => $.agent_cli.unsupported),
                          tone: "muted",
                          spinning: false,
                        }
                      : null;
  // A failure's reason is the status line itself; any other detail
  // (a local request error, an unsupported copy) is shown under it.
  const extraDetail = status?.tone === "error" && phase === "failed" ? null : detail;
  const disabled =
    !canManage || updating || requested || phase === "unsupported";

  const onFollow = async (next: boolean) => {
    setFollowOverride(next);
    setLocalError("");
    setBusy("follow");
    try {
      await api.setAgentCLIFollow(runtime.id, next);
    } catch {
      setFollowOverride(null);
      setLocalError(t(($) => $.agent_cli.follow_failed));
    } finally {
      setBusy(null);
    }
  };

  const onUpdate = async () => {
    setLocalError("");
    setBusy("update");
    try {
      await api.requestAgentCLIUpdate(runtime.id);
      // Queueing the upgrade is not the upgrade. Waiting, updating,
      // failure, and "already current" arrive on the daemon's next report;
      // until then the cell says the request is queued.
      setRequestedAgainst({
        checkedAt: reported.checkedAt,
        phase: reported.phase,
      });
    } catch {
      setLocalError(t(($) => $.agent_cli.update_request_failed));
    } finally {
      setBusy(null);
    }
  };

  const updateTitle = canManage
    ? (reported.binaryPath ?? undefined)
    : t(($) => $.agent_cli.read_only);
  const toneClass: Record<StatusTone, string> = {
    muted: "text-caption text-muted-foreground",
    default: "text-caption text-foreground",
    success: "text-caption text-success",
    error: "text-caption text-destructive",
  };
  const statusBody = status && (
    <>
      {status.spinning && (
        <Loader2 className="h-3 w-3 shrink-0 animate-spin text-muted-foreground" />
      )}
      {status.tone === "error" && (
        <AlertTriangle className="h-3 w-3 shrink-0 text-destructive" />
      )}
      <span className={`truncate ${toneClass[status.tone]}`}>{status.text}</span>
    </>
  );
  const updateLabel =
    updating || requested
      ? t(($) => $.agent_cli.updating)
      : t(($) => $.agent_cli.update_now);

  if (compact) {
    // The runtimes list row is a fixed h-12 track, so the compact cell must
    // stay two text lines tall: version over status on the left, the
    // controls on the right. The status line itself says what is happening
    // (queued, updating, updated, or the failure reason); the binary path
    // and longer notes also go into the tooltip.
    const hover = [
      reported.binaryPath,
      phase === "waiting" ? queuedText : null,
      pausedText,
      status?.tone === "error" ? status.text : null,
      extraDetail,
    ]
      .filter(Boolean)
      .join("\n");
    const line = status ?? (extraDetail
      ? { text: extraDetail, tone: "error" as const, spinning: false }
      : null);
    return (
      <div
        className="flex w-full min-w-0 items-center gap-2"
        {...rowLinkInteractiveProps}
      >
        <div className="flex min-w-0 flex-1 flex-col" title={hover || undefined}>
          <span className="truncate font-mono text-caption text-muted-foreground">
            {versions || "—"}
          </span>
          {line && (
            <span className="flex min-w-0 items-center gap-1">
              {status ? (
                statusBody
              ) : (
                <>
                  <AlertTriangle className="h-3 w-3 shrink-0 text-destructive" />
                  <span className="truncate text-caption text-destructive">
                    {line.text}
                  </span>
                </>
              )}
            </span>
          )}
        </div>
        {phase !== "unsupported" && (
          <div className="flex shrink-0 items-center gap-1.5">
            <Switch
              size="sm"
              checked={follow}
              disabled={!canManage || busy === "follow"}
              onCheckedChange={(checked) => {
                void onFollow(checked);
              }}
              aria-label={t(($) => $.agent_cli.follow)}
              title={t(($) => $.agent_cli.follow)}
            />
            <Button
              type="button"
              size="xs"
              variant="outline"
              disabled={disabled}
              title={updateTitle}
              onClick={() => {
                void onUpdate();
              }}
            >
              {updateLabel}
            </Button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div
      className="space-y-3 rounded-xl border bg-card p-4"
      {...rowLinkInteractiveProps}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        {versions && (
          <span className="truncate font-mono text-caption text-muted-foreground">
            {versions}
          </span>
        )}
        {status && (
          <span className="inline-flex min-w-0 items-center gap-1">
            {statusBody}
          </span>
        )}
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <label className="inline-flex items-center gap-1.5 text-caption text-muted-foreground">
          <Switch
            size="sm"
            checked={follow}
            disabled={!canManage || busy === "follow" || phase === "unsupported"}
            onCheckedChange={(checked) => {
              void onFollow(checked);
            }}
          />
          {t(($) => $.agent_cli.follow)}
        </label>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={disabled}
          title={updateTitle}
          onClick={() => {
            void onUpdate();
          }}
        >
          {updateLabel}
        </Button>
      </div>
      {pausedText && phase === "waiting" && (
        <p className="text-caption text-muted-foreground">{pausedText}</p>
      )}
      {reported.binaryPath && (
        <p
          className="truncate font-mono text-caption text-muted-foreground"
          title={reported.binaryPath}
        >
          {t(($) => $.agent_cli.binary, { path: reported.binaryPath })}
        </p>
      )}
      {extraDetail && (
        <p className="text-caption text-destructive" title={extraDetail}>
          {extraDetail}
        </p>
      )}
    </div>
  );
}
