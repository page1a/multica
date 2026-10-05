import type { AgentTask } from "../types";

/**
 * How one run's CLI session relates to the runs before it (DENE-1345).
 *
 * `round` numbers an agent's started runs on the issue from 1 in start order,
 * so "接着第 N 轮的会话" names a run the reader can find in the same list.
 * Runs that never started have no round; runs from a server that predates
 * lineage have no `mode`, and the UI shows nothing for them.
 */
export interface RunSessionLineage {
  round?: number;
  mode?: "new" | "resumed";
  /** Round of the run whose session this one resumed, when it is in the list. */
  resumedFromRound?: number;
  /** Why a new session started. */
  breakReason?: string;
}

type LineageTask = Pick<
  AgentTask,
  "id" | "agent_id" | "started_at" | "session_mode" | "resumed_from_run" | "session_break_reason"
>;

export function runSessionLineage(tasks: readonly LineageTask[]): Map<string, RunSessionLineage> {
  const started = tasks
    .filter((t) => !!t.started_at)
    .slice()
    .sort((a, b) => Date.parse(a.started_at!) - Date.parse(b.started_at!));
  const perAgent = new Map<string, number>();
  const rounds = new Map<string, number>();
  for (const t of started) {
    const next = (perAgent.get(t.agent_id) ?? 0) + 1;
    perAgent.set(t.agent_id, next);
    rounds.set(t.id, next);
  }

  const out = new Map<string, RunSessionLineage>();
  for (const t of tasks) {
    const mode = t.session_mode === "new" || t.session_mode === "resumed" ? t.session_mode : undefined;
    out.set(t.id, {
      round: rounds.get(t.id),
      mode,
      resumedFromRound:
        mode === "resumed" && t.resumed_from_run ? rounds.get(t.resumed_from_run) : undefined,
      breakReason: mode === "new" ? t.session_break_reason || undefined : undefined,
    });
  }
  return out;
}

/**
 * A new session after the first is a break worth a divider: the run before
 * it carried context this one does not have. The first run on an issue is
 * not a break.
 */
export function isSessionBreak(lineage: RunSessionLineage | undefined): boolean {
  return lineage?.mode === "new" && lineage.breakReason !== "first_run";
}
