"use client";

import { useState } from "react";
import { Loader2 } from "lucide-react";
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
  };
}

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

  if (!reported || runtime.runtime_mode !== "local" || runtime.profile_id) {
    return null;
  }

  const follow = followOverride ?? reported.autoFollow;
  const phase = busy === "update" ? "updating" : reported.phase;
  const updating = phase === "updating";
  const versions =
    reported.current && reported.latest
      ? t(($) => $.agent_cli.versions, {
          current: reported.current,
          latest: reported.latest,
        })
      : (reported.current ?? reported.latest ?? "");
  const badge =
    phase === "available" || phase === "waiting" || phase === "updating"
      ? t(($) => $.agent_cli.update_available)
      : phase === "current" && reported.latest
        ? t(($) => $.agent_cli.up_to_date)
        : phase === "unsupported"
          ? t(($) => $.agent_cli.unsupported)
          : phase === "check_failed"
            ? t(($) => $.agent_cli.check_failed)
            : null;
  const detail = localError || reported.error;
  const disabled = !canManage || updating || phase === "unsupported";

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
    } catch {
      setLocalError(t(($) => $.agent_cli.update_request_failed));
    } finally {
      // Queueing the upgrade is not the upgrade. Waiting, failure, and
      // "already current" arrive on the daemon's next report.
      setBusy(null);
    }
  };

  return (
    <div
      className={
        compact
          ? "flex min-w-0 flex-col gap-1"
          : "space-y-3 rounded-xl border bg-card p-4"
      }
      {...rowLinkInteractiveProps}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        {versions && (
          <span className="truncate font-mono text-caption text-muted-foreground">
            {versions}
          </span>
        )}
        {badge && (
          <span
            className={
              phase === "current"
                ? "text-caption text-success"
                : phase === "failed" || phase === "check_failed"
                  ? "text-caption text-destructive"
                  : "text-caption text-foreground"
            }
          >
            {badge}
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
            aria-label={compact ? t(($) => $.agent_cli.follow) : undefined}
          />
          {!compact && t(($) => $.agent_cli.follow)}
        </label>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={disabled || busy === "update"}
          title={
            canManage
              ? (reported.binaryPath ?? undefined)
              : t(($) => $.agent_cli.read_only)
          }
          onClick={() => {
            void onUpdate();
          }}
        >
          {updating && <Loader2 className="h-3 w-3 animate-spin" />}
          {updating
            ? t(($) => $.agent_cli.updating)
            : t(($) => $.agent_cli.update_now)}
        </Button>
      </div>
      {compact && (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.agent_cli.follow)}
        </span>
      )}
      {phase === "waiting" && (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.agent_cli.waiting)}
        </p>
      )}
      {reported.binaryPath && (
        <p
          className="truncate font-mono text-caption text-muted-foreground"
          title={reported.binaryPath}
        >
          {compact
            ? reported.binaryPath
            : t(($) => $.agent_cli.binary, { path: reported.binaryPath })}
        </p>
      )}
      {detail && (
        <p className="text-caption text-destructive" title={detail}>
          {detail}
        </p>
      )}
    </div>
  );
}
