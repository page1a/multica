"use client";

import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { issueTasksOptions } from "@multica/core/issues/queries";
import { isSessionBreak, runSessionLineage, type RunSessionLineage } from "@multica/core/issues/session-lineage";
import type { AgentTask } from "@multica/core/types";
import { useActorName } from "@multica/core/workspace/hooks";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// How each run's CLI session relates to the runs before it (DENE-1345): a grey
// line per run, and a divider where the session broke. No badges, no colour.

/** This run's lineage, read from the issue's run list the page already holds. */
export function useRunSessionLineage(issueId: string, taskId: string): RunSessionLineage | undefined {
  const select = useCallback((tasks: AgentTask[]) => runSessionLineage(tasks).get(taskId), [taskId]);
  const { data } = useQuery({ ...issueTasksOptions(issueId), select });
  return data;
}

function useLineageText() {
  const { t } = useT("issues");
  const reason = (key: string | undefined): string | null => {
    switch (key) {
      case "agent_changed": return t(($) => $.session_lineage.reason_agent_changed);
      case "runtime_changed": return t(($) => $.session_lineage.reason_runtime_changed);
      case "session_lost": return t(($) => $.session_lineage.reason_session_lost);
      case "fresh_requested": return t(($) => $.session_lineage.reason_fresh_requested);
      default: return null;
    }
  };
  const session = (l: RunSessionLineage): string =>
    l.mode === "resumed"
      ? l.resumedFromRound
        ? t(($) => $.session_lineage.resumed, { round: l.resumedFromRound })
        : t(($) => $.session_lineage.resumed_earlier)
      : t(($) => $.session_lineage.new);
  const round = (l: RunSessionLineage): string | null =>
    l.round ? t(($) => $.session_lineage.round, { round: l.round }) : null;
  const divider = (name: string, l: RunSessionLineage): string =>
    [
      l.round ? t(($) => $.session_lineage.divider, { name, round: l.round }) : name,
      t(($) => $.session_lineage.new),
      reason(l.breakReason),
    ].filter(Boolean).join(" · ");
  return { reason, session, round, divider };
}

/** 「第 2 轮 · 接着第 1 轮的会话」. Nothing for runs recorded before lineage. */
export function RunSessionCaption({ lineage, withReason = false, className }: {
  lineage: RunSessionLineage | undefined;
  /** Name why a new session started; off where a divider already says it. */
  withReason?: boolean;
  className?: string;
}) {
  const text = useLineageText();
  if (!lineage?.mode) return null;
  const reason = withReason && isSessionBreak(lineage) ? text.reason(lineage.breakReason) : null;
  const line = [text.round(lineage), text.session(lineage), reason].filter(Boolean).join(" · ");
  return <p className={cn("text-caption text-muted-foreground", className)} data-run-session>{line}</p>;
}

/** A rule across the feed where a run started over: who, which round, why. */
export function RunSessionDivider({ agentId, lineage }: { agentId: string; lineage: RunSessionLineage | undefined }) {
  const text = useLineageText();
  const { getActorName } = useActorName();
  if (!isSessionBreak(lineage)) return null;
  return (
    <div role="separator" aria-label={text.divider(getActorName("agent", agentId), lineage!)}
      className="flex items-center gap-3 py-2 text-caption text-muted-foreground" data-run-session-break>
      <span aria-hidden className="h-px min-w-4 flex-1 bg-border" />
      <span className="min-w-0 text-center">{text.divider(getActorName("agent", agentId), lineage!)}</span>
      <span aria-hidden className="h-px min-w-4 flex-1 bg-border" />
    </div>
  );
}
