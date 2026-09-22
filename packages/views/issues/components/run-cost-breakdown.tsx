"use client";

import { useMemo } from "react";
import type { AgentTask } from "@multica/core/types";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { useT } from "../../i18n";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import { formatTokens, formatUsd, summarizeTaskUsage } from "../../runtimes/utils";
import {
  formatMillis,
  runCacheHitRate,
  runCostComposition,
  runMetadata,
  runShareOfIssue,
  type RunCostSegmentKey,
} from "./run-cost";
import { useTriggerText } from "./task-run-labels";

// The per-run half of the Token cost view: one selected run, what its tokens
// were spent on, and how much of the issue's bill it is.
//
// The table above it answers "which run"; this answers "and why that one".
// Those are different questions with different shapes — a table row cannot
// carry a proportion you can read at a glance, and a chart cannot let you
// compare nine runs — so they sit one above the other rather than one
// replacing the other.
//
// Everything here is derived from `task.usage`, the same array the table
// prices, through the same `summarizeTaskUsage` / `estimateCostBreakdown`
// helpers. There is no second accounting path, so the panel cannot disagree
// with the row that opened it.

// One colour per billing segment, in the same order run-cost.ts declares them.
// Chart tokens rather than literal colours so the bar reads correctly in both
// themes; `bg-chart-1` is the same ramp the by-agent bars and the runtime
// usage charts already use, which is what makes "input" the same colour here
// as it is on the usage page.
const SEGMENT_COLOR: Record<RunCostSegmentKey, string> = {
  input: "bg-chart-1",
  cacheWrite: "bg-chart-2",
  cacheRead: "bg-chart-3",
  output: "bg-chart-4",
};

export function RunCostBreakdown({
  task,
  issueCost,
}: {
  task: AgentTask;
  /** The issue's whole spend, for the share-of-total figure. */
  issueCost: number;
}) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  // Same subscription the dialog and the header total make: `estimateCost`
  // reads custom rates imperatively, so without this the panel keeps quoting
  // the old rate after the user saves a new one.
  const pricings = useCustomPricingStore((s) => s.pricings);

  const composition = useMemo(() => runCostComposition(task.usage), [task.usage, pricings]);
  const summary = useMemo(() => summarizeTaskUsage(task.usage), [task.usage, pricings]);
  const meta = useMemo(() => runMetadata(task.usage), [task.usage]);
  const hitRate = useMemo(() => runCacheHitRate(task.usage), [task.usage]);

  if (!composition || !summary) return null;

  const share = runShareOfIssue(composition.cost, issueCost);
  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : formatMillis(meta.totalMs);

  const segmentLabel = (key: RunCostSegmentKey) =>
    t(($) => $.run_cost.segment[key]);

  return (
    <section
      aria-label={t(($) => $.run_cost.section)}
      className="rounded-lg border bg-card p-4"
    >
      <header className="mb-3 flex min-w-0 items-baseline gap-2">
        <h3 className="min-w-0 truncate text-caption font-medium">{trigger}</h3>
        <span className="shrink-0 text-micro text-muted-foreground tabular-nums">
          {[summary.models.join(", "), duration].filter(Boolean).join(" · ")}
        </span>
      </header>

      <div className="mb-4 grid grid-cols-3 gap-3">
        <Figure
          label={t(($) => $.run_cost.figure_cost)}
          value={formatUsd(composition.cost)}
          hint={
            share === null
              ? t(($) => $.run_cost.figure_cost_hint_unknown)
              : t(($) => $.run_cost.figure_cost_hint, {
                  pct: Math.round(share * 100),
                })
          }
        />
        <Figure
          label={t(($) => $.run_cost.figure_cache)}
          value={hitRate === null ? "—" : `${hitRate}%`}
          hint={t(($) => $.run_cost.figure_cache_hint, {
            reads: formatTokens(summary.cacheRead),
            writes: formatTokens(summary.cacheWrite),
          })}
        />
        <Figure
          label={t(($) => $.run_cost.figure_largest)}
          value={segmentLabel(composition.largest.key)}
          hint={t(($) => $.run_cost.figure_largest_hint, {
            pct: Math.round(composition.largest.tokenShare * 100),
            tokens: formatTokens(composition.largest.tokens),
          })}
        />
      </div>

      {/* The composition bar. `flex` with per-segment percentage widths rather
          than a chart library: four stacked proportions of one total is a
          layout, and a charting dependency here would buy axes and tooltips
          nothing on screen needs. A zero-token segment contributes a 0% child,
          which paints nothing — no hairline for a segment that isn't there. */}
      <div
        className="flex h-2.5 w-full overflow-hidden rounded-full bg-muted"
        role="img"
        aria-label={composition.segments
          .map(
            (s) =>
              `${segmentLabel(s.key)} ${Math.round(s.tokenShare * 100)}%`,
          )
          .join(", ")}
      >
        {composition.segments.map((segment) => (
          <span
            key={segment.key}
            className={SEGMENT_COLOR[segment.key]}
            style={{ width: `${segment.tokenShare * 100}%` }}
          />
        ))}
      </div>

      <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-1.5 sm:grid-cols-4">
        {composition.segments.map((segment) => (
          <div key={segment.key} className="flex items-baseline gap-2">
            <span
              className={`size-2 shrink-0 rounded-xs ${SEGMENT_COLOR[segment.key]}`}
              aria-hidden
            />
            <dt className="min-w-0 flex-1 truncate text-caption text-muted-foreground">
              {segmentLabel(segment.key)}
            </dt>
            <dd className="shrink-0 text-caption tabular-nums">
              {formatTokens(segment.tokens)}
              <span className="ml-1.5 text-micro text-muted-foreground">
                {formatUsd(segment.cost)}
              </span>
            </dd>
          </div>
        ))}
      </dl>

      <RunFacts meta={meta} />

      {/* Says what this view does NOT measure, on the view itself rather than
          in a ticket. `task_usage` records the provider's billing counters;
          nothing in the pipeline attributes prompt tokens to the runtime
          brief, the tool schemas, the conversation history or the tool
          results. Splitting the bar those four ways would mean inventing the
          split, and an invented number in a cost view is the one thing it
          must not contain. */}
      <p className="mt-3 border-t pt-3 text-micro text-muted-foreground">
        {t(($) => $.run_cost.note_no_prompt_split)}
      </p>
    </section>
  );
}

function Figure({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint: string;
}) {
  return (
    <div className="min-w-0">
      <div className="text-micro font-medium uppercase tracking-wider text-muted-foreground">
        {label}
      </div>
      <div className="mt-1 truncate text-title font-semibold leading-none tabular-nums">
        {value}
      </div>
      <div className="mt-1 truncate text-micro text-muted-foreground">{hint}</div>
    </div>
  );
}

// Run facts the daemon reports beside the token counters (DENE-666): how many
// turns it took, whether it resumed a session instead of starting cold, and
// where the wall-clock went. They belong next to the cost because they are
// what explains it — a resumed run reads cache where a cold one pays for the
// whole prompt again.
//
// Only facts the daemon actually reported are rendered. A missing figure is
// dropped rather than shown as a dash: this is a variable-length list of
// evidence, and eight em dashes for a pre-DENE-666 run would be a wall of
// nothing where "we have no timing for this run" is the whole message.
function RunFacts({ meta }: { meta: ReturnType<typeof runMetadata> }) {
  const { t } = useT("issues");

  const facts: { key: string; label: string; value: string }[] = [];
  if (meta.numTurns !== undefined) {
    facts.push({
      key: "turns",
      label: t(($) => $.run_cost.fact_turns),
      value: String(meta.numTurns),
    });
  }
  if (meta.resumed !== undefined) {
    facts.push({
      key: "resumed",
      label: t(($) => $.run_cost.fact_start),
      value: meta.resumed
        ? t(($) => $.run_cost.fact_start_resumed)
        : t(($) => $.run_cost.fact_start_cold),
    });
  }
  if (meta.lastContextTokens !== undefined) {
    facts.push({
      key: "context",
      label: t(($) => $.run_cost.fact_last_context),
      value: formatTokens(meta.lastContextTokens),
    });
  }
  for (const [key, ms, label] of [
    ["queue", meta.queueToClaimMs, t(($) => $.run_cost.fact_queue)],
    ["prepare", meta.prepareMs, t(($) => $.run_cost.fact_prepare)],
    ["first", meta.spawnToFirstOutputMs, t(($) => $.run_cost.fact_first_output)],
  ] as const) {
    const value = formatMillis(ms);
    if (value !== null) facts.push({ key, label, value });
  }
  if (meta.attributionSource) {
    facts.push({
      key: "source",
      label: t(($) => $.run_cost.fact_trigger),
      value: meta.attributionSource,
    });
  }

  if (facts.length === 0) {
    return (
      <p className="mt-3 border-t pt-3 text-micro text-muted-foreground">
        {t(($) => $.run_cost.facts_unavailable)}
      </p>
    );
  }

  return (
    <dl className="mt-3 flex flex-wrap gap-x-5 gap-y-1 border-t pt-3">
      {facts.map((fact) => (
        <div key={fact.key} className="flex items-baseline gap-1.5">
          <dt className="text-micro text-muted-foreground">{fact.label}</dt>
          <dd className="text-micro tabular-nums">{fact.value}</dd>
        </div>
      ))}
    </dl>
  );
}
