import type { TaskUsage } from "@multica/core/types";
import { estimateCostBreakdown, summarizeTaskUsage } from "../../runtimes/utils";

// Pure arithmetic behind the per-run Token cost panel. Lives apart from the
// component so the boundary cases — a run with no usage, a run whose slices
// carry different metadata, a zero-cost issue total — are pinned in a node
// test instead of through a DOM mount. The component test keeps the happy
// path and the wiring; see run-cost.test.ts for the matrix.

/**
 * What a run's tokens were, split by how they are BILLED.
 *
 * These four are the only composition `task_usage` records: the daemon
 * reports the provider's aggregate counters per (provider, model), and no
 * provider attributes tokens to the parts of a prompt (runtime brief, tool
 * schemas, history, tool results). A split by prompt segment would have to be
 * invented here, and an invented number in a cost view is worse than a
 * missing one — so the panel shows these four and says plainly that the
 * prompt-segment split is not metered.
 *
 * Order is the billing story, cheapest last: fresh input paid at full rate,
 * the write that built the cache, the reads that got the discount, the
 * output.
 */
export const RUN_COST_SEGMENTS = ["input", "cacheWrite", "cacheRead", "output"] as const;

export type RunCostSegmentKey = (typeof RUN_COST_SEGMENTS)[number];

export interface RunCostSegment {
  key: RunCostSegmentKey;
  tokens: number;
  cost: number;
  /** Share of the run's total tokens, 0–1. 0 when the run recorded no tokens. */
  tokenShare: number;
  /** Share of the run's total cost, 0–1. 0 when the run cost nothing. */
  costShare: number;
}

export interface RunCostComposition {
  segments: RunCostSegment[];
  tokens: number;
  cost: number;
  /** The segment with the most tokens — "the biggest block", the thing the panel exists to name. */
  largest: RunCostSegment;
}

/**
 * Split one run's usage into the four billing segments.
 *
 * Cost per segment comes from `estimateCostBreakdown` summed slice by slice,
 * the same helper the runtime usage charts use, so the segments always add up
 * to the `summarizeTaskUsage` cost rendered beside them — including for rows
 * the provider priced itself.
 *
 * Returns `null` for a run with no usage recorded. That is not "this run was
 * free"; the caller renders an em dash or the empty state.
 */
export function runCostComposition(
  usage: readonly TaskUsage[] | undefined,
): RunCostComposition | null {
  const summary = summarizeTaskUsage(usage);
  if (!summary) return null;

  const cost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
  for (const slice of usage ?? []) {
    const part = estimateCostBreakdown(slice);
    cost.input += part.input;
    cost.output += part.output;
    cost.cacheRead += part.cacheRead;
    cost.cacheWrite += part.cacheWrite;
  }

  const totalCost = cost.input + cost.output + cost.cacheRead + cost.cacheWrite;
  const segments = RUN_COST_SEGMENTS.map<RunCostSegment>((key) => ({
    key,
    tokens: summary[key],
    cost: cost[key],
    tokenShare: summary.tokens > 0 ? summary[key] / summary.tokens : 0,
    costShare: totalCost > 0 ? cost[key] / totalCost : 0,
  }));

  // `reduce` rather than a sort: ties keep the declaration order above, so a
  // run that split evenly names the same segment every render instead of
  // flipping with the sort's stability.
  const largest = segments.reduce((a, b) => (b.tokens > a.tokens ? b : a));

  return { segments, tokens: summary.tokens, cost: totalCost, largest };
}

/**
 * Cache hit rate as a whole percentage: reads served from cache over every
 * token that entered the model as prompt.
 *
 * Floored, never rounded, for the reason IssueUsageDialog floors its own:
 * 99.6% rounds to "100%", which claims not one fresh token was read. Returns
 * `null` when the run sent no prompt tokens at all — 0% would assert a total
 * cache miss on a run that never asked.
 */
export function runCacheHitRate(
  usage: readonly TaskUsage[] | undefined,
): number | null {
  const summary = summarizeTaskUsage(usage);
  if (!summary) return null;
  const prompt = summary.input + summary.cacheRead;
  if (prompt <= 0) return null;
  return Math.floor((summary.cacheRead / prompt) * 100);
}

/**
 * This run's share of the issue's whole spend, 0–1.
 *
 * Measured in money, not tokens: two runs with identical token counts on
 * different models are not equally responsible for the bill. Returns `null`
 * when the issue total is zero — a share of nothing is not 0%, it is
 * undefined, and drawing a 0% bar would read as "this run was free".
 */
export function runShareOfIssue(
  runCost: number,
  issueCost: number,
): number | null {
  if (issueCost <= 0) return null;
  return runCost / issueCost;
}

/**
 * Run-level metadata, lifted off whichever slice carries it.
 *
 * The daemon writes the same run-level values onto every (provider, model)
 * row of a run, so summing would multiply a 12-turn run into 24 turns the
 * moment it spilled onto a second model. First slice that has the field wins;
 * the rest are the same value.
 */
export interface RunMetadata {
  numTurns?: number;
  resumed?: boolean;
  sessionId?: string;
  lastContextTokens?: number;
  queueToClaimMs?: number;
  prepareMs?: number;
  spawnToFirstOutputMs?: number;
  totalMs?: number;
  attributionSource?: string;
  triggerEvidenceKind?: string;
}

export function runMetadata(usage: readonly TaskUsage[] | undefined): RunMetadata {
  const meta: RunMetadata = {};
  for (const slice of usage ?? []) {
    // `num_turns` is NOT NULL DEFAULT 0 server-side, so a pre-DENE-666 row and
    // a genuinely turn-less run both arrive as 0. Treat 0 as "not reported":
    // a run that reached a model ran at least one turn, so printing "0 turns"
    // could only ever be a missing figure dressed up as a measurement.
    if (meta.numTurns === undefined && slice.num_turns) meta.numTurns = slice.num_turns;
    if (meta.resumed === undefined && slice.resumed !== undefined) {
      meta.resumed = slice.resumed;
    }
    if (meta.sessionId === undefined && slice.session_id) {
      meta.sessionId = slice.session_id;
    }
    if (meta.lastContextTokens === undefined && slice.last_context_tokens !== undefined) {
      meta.lastContextTokens = slice.last_context_tokens;
    }
    if (meta.queueToClaimMs === undefined && slice.queue_to_claim_ms !== undefined) {
      meta.queueToClaimMs = slice.queue_to_claim_ms;
    }
    if (meta.prepareMs === undefined && slice.prepare_ms !== undefined) {
      meta.prepareMs = slice.prepare_ms;
    }
    if (
      meta.spawnToFirstOutputMs === undefined &&
      slice.spawn_to_first_output_ms !== undefined
    ) {
      meta.spawnToFirstOutputMs = slice.spawn_to_first_output_ms;
    }
    if (meta.totalMs === undefined && slice.total_ms !== undefined) {
      meta.totalMs = slice.total_ms;
    }
    if (meta.attributionSource === undefined && slice.attribution_source) {
      meta.attributionSource = slice.attribution_source;
    }
    if (meta.triggerEvidenceKind === undefined && slice.trigger_evidence_kind) {
      meta.triggerEvidenceKind = slice.trigger_evidence_kind;
    }
  }
  // The server serialises `resumed` with `omitempty`, so a cold start never
  // arrives as `false` — it arrives as an absent field, exactly like a
  // pre-DENE-666 row. Tell them apart by the metadata that travels with it: a
  // run that reported turns, a session or a total duration came from a daemon
  // that reports `resumed` too, so its silence means "cold start".
  if (
    meta.resumed === undefined &&
    (meta.numTurns !== undefined ||
      meta.sessionId !== undefined ||
      meta.totalMs !== undefined)
  ) {
    meta.resumed = false;
  }
  return meta;
}

/**
 * Pick which run the panel opens on: the most expensive one.
 *
 * The question the view answers is "where did the money go", so the run that
 * spent the most is the one already on screen when it opens. Ties fall back to
 * list order, which the caller has already sorted newest-first.
 */
export function defaultSelectedRunId<T extends { id: string; usage?: TaskUsage[] }>(
  tasks: readonly T[],
): string | null {
  let best: { id: string; cost: number } | null = null;
  for (const task of tasks) {
    const summary = summarizeTaskUsage(task.usage);
    if (!summary) continue;
    if (best === null || summary.cost > best.cost) {
      best = { id: task.id, cost: summary.cost };
    }
  }
  return best?.id ?? null;
}

/**
 * A daemon-reported phase duration, in milliseconds, as a short label.
 *
 * Sub-second phases are the normal case for the queue and prepare marks, and
 * `formatDuration`'s second resolution collapses all of them to "0s" — which
 * reads as "not measured" next to a phase that genuinely was not. Returns
 * `null` for an absent figure so the caller renders an em dash.
 */
export function formatMillis(ms: number | undefined): string | null {
  if (ms === undefined || !Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1_000) return `${Math.round(ms)}ms`;
  const sec = ms / 1_000;
  if (sec < 60) return `${Number(sec.toFixed(1))}s`;
  const min = Math.floor(sec / 60);
  const remSec = Math.round(sec % 60);
  if (min < 60) return `${min}m ${remSec}s`;
  return `${Math.floor(min / 60)}h ${min % 60}m`;
}
