"use client";

import { memo, useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueProgressHistoryOptions } from "@multica/core/issues/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import type { AgentTask, Issue, Progress } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { ProgressDotMark, ProgressLine, progressDot, type ProgressDot } from "../../common/progress-line";
import { selectIssueLiveState, type IssueLiveState } from "../surface/activity";
import { useT, useTimeAgo } from "../../i18n";

const LIVE_DOT: Record<Exclude<IssueLiveState, null>, ProgressDot> = {
  running: "working",
  queued: "waiting",
  failed: "stuck",
};

function useIssueLiveState(issueId: string, since?: string | null): IssueLiveState {
  const wsId = useWorkspaceId();
  const select = useCallback(
    (snapshot: AgentTask[]) => selectIssueLiveState(snapshot, issueId, since),
    [issueId, since],
  );
  const { data = null } = useQuery({ ...agentTaskSnapshotOptions(wsId), select });
  return data;
}

function useLiveLabel(live: IssueLiveState): string | undefined {
  const { t } = useT("issues");
  if (live === "running") return t(($) => $.progress_line.live_running);
  if (live === "queued") return t(($) => $.progress_line.live_queued);
  if (live === "failed") return t(($) => $.progress_line.live_failed);
  return undefined;
}

/**
 * Board / list second line (DENE-1037): live agent state first, then the
 * latest progress line. Renders nothing when there is neither, so the board
 * card can fall back to the description.
 */
export const IssueProgressLine = memo(function IssueProgressLine({
  issue,
  className,
}: {
  issue: Pick<Issue, "id" | "progress">;
  className?: string;
}) {
  const live = useIssueLiveState(issue.id, issue.progress?.updated_at);
  const status = useLiveLabel(live);
  return <ProgressLine progress={issue.progress} status={status} dot={live ? LIVE_DOT[live] : undefined} className={className} />;
});

export function useIssueHasProgressLine(issue: Pick<Issue, "id" | "progress">): boolean {
  const live = useIssueLiveState(issue.id, issue.progress?.updated_at);
  return !!live || !!issue.progress?.text?.trim();
}

function ProgressMeta({ entry }: { entry: Progress }) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const { getActorName } = useActorName();
  const author = entry.author_id && entry.author_type !== "system" ? getActorName(entry.author_type, entry.author_id) : null;
  const sourceKey = ["agent", "close", "parking", "model", "reply"].includes(entry.source) ? entry.source : null;
  const source = sourceKey
    ? t(($) => $.progress_line[`source_${sourceKey}` as "source_agent"])
    : entry.source;
  return (
    <span className="shrink-0 text-micro text-muted-foreground">
      {[author, source, timeAgo(entry.updated_at)].filter(Boolean).join(" · ")}
    </span>
  );
}

/**
 * Detail progress bar (DENE-1037): live state, the latest line with who
 * reported it and when, and an expandable history from GET /progress. The
 * history is fetched only while expanded; issue:updated with
 * progress_changed invalidates it.
 */
export function IssueProgressBar({ issue, className }: { issue: Pick<Issue, "id" | "progress">; className?: string }) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  const live = useIssueLiveState(issue.id, issue.progress?.updated_at);
  const status = useLiveLabel(live);
  const progress = issue.progress?.text?.trim() ? issue.progress : null;
  const history = useQuery({ ...issueProgressHistoryOptions(wsId, issue.id), enabled: open });
  if (!progress && !status) return null;
  const dot = live ? LIVE_DOT[live] : progressDot(progress!);
  // The first history row is the current line itself.
  const earlier = (history.data ?? []).slice(progress ? 1 : 0);

  return (
    <div className={cn("rounded-md border border-border/60 bg-muted/30 px-2.5 py-1.5", className)} aria-label={t(($) => $.progress_line.label)}>
      <div className="flex min-w-0 items-center gap-2">
        <ProgressLine progress={progress} status={status} dot={dot} className="flex-1 text-body" />
        {progress && <ProgressMeta entry={progress} />}
        {progress && (
          <button
            type="button"
            className="shrink-0 text-micro text-muted-foreground hover:text-foreground"
            aria-expanded={open}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? t(($) => $.progress_line.history_hide) : t(($) => $.progress_line.history_show)}
          </button>
        )}
      </div>
      {open && (
        <ol className="mt-1.5 space-y-1 border-t border-border/60 pt-1.5">
          {history.isLoading ? (
            <li className="text-caption text-muted-foreground">{t(($) => $.progress_line.history_loading)}</li>
          ) : earlier.length === 0 ? (
            <li className="text-caption text-muted-foreground">{t(($) => $.progress_line.history_empty)}</li>
          ) : (
            earlier.map((entry, i) => (
              <li key={`${entry.updated_at}-${i}`} className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
                <ProgressDotMark dot={progressDot(entry)} />
                <span className="min-w-0 flex-1 truncate" title={entry.text}>{entry.text}</span>
                <ProgressMeta entry={entry} />
              </li>
            ))
          )}
        </ol>
      )}
    </div>
  );
}
