"use client";

import { useMemo, useState } from "react";
import { RotateCcw } from "lucide-react";
import type { AgentTask } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useActorName } from "@multica/core/workspace/hooks";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import {
  collectUnmappedModels,
  formatTokens,
  formatUsd,
  summarizeTaskUsage,
  summarizeTaskUsageAcross,
  type TaskUsageSummary,
} from "../../runtimes/utils";
import { KpiCard } from "../../runtimes/components/shared";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { RunCostBreakdown } from "./run-cost-breakdown";
import { defaultSelectedRunId } from "./run-cost";
import { useStatusLabel, useTriggerText } from "./task-run-labels";
import { TaskStatusIcon } from "./task-status-icon";

// Per-run cost breakdown for one issue — the surface the execution log's
// header total opens.
//
// The execution log answers "how much did this run cost" one row at a time;
// this answers "which run cost the most, and why". That comparison needs the
// input / output / cache split side by side, which the 288px sidebar cannot
// hold, so it lives here instead of expanding the rows.
//
// Every figure comes from the same `summarizeTaskUsage` helpers the sidebar
// uses, so the total here can never disagree with the total that opened it.

export function IssueUsageDialog({
  open,
  onOpenChange,
  identifier,
  tasks,
  isPending = false,
  isError = false,
  onRetry,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  identifier: string;
  tasks: AgentTask[];
  /** The run list is still loading — render the skeleton, not "no usage". */
  isPending?: boolean;
  /** The run list failed to load — render the error, not "no usage". */
  isError?: boolean;
  onRetry?: () => void;
}) {
  const { t } = useT("issues");
  // `estimateCost` reads custom rates imperatively out of the Zustand store,
  // so nothing re-renders this dialog when the user saves a new rate. Subscribe
  // to the snapshot and carry it into every memo that prices usage — otherwise
  // the totals, the per-run costs, and the "unmapped model" notice all keep
  // showing the old price until the task list happens to refetch. Same reason
  // the runtime usage page subscribes in usage-section.tsx.
  const pricings = useCustomPricingStore((s) => s.pricings);
  // `null` means "no explicit pick yet" and defers to the most expensive run,
  // which is recomputed as the list refreshes. Seeding state from the tasks
  // instead would pin the panel to whichever run was dearest when the dialog
  // first opened, and a run that finishes while it is open would never take
  // the slot it just earned.
  const [pickedRunId, setPickedRunId] = useState<string | null>(null);

  // Only runs that actually recorded usage earn a row: a run with no figure
  // contributes nothing to compare and would just add an all-em-dash line.
  // Their existence is still accounted for in the footnote below.
  const priced = useMemo(
    () => tasks.filter((task) => (task.usage?.length ?? 0) > 0),
    [tasks],
  );
  const unpricedCount = tasks.length - priced.length;

  const total = useMemo(
    () => summarizeTaskUsageAcross(priced.map((task) => task.usage)),
    [priced, pricings],
  );

  const agentIds = useMemo(
    () => Array.from(new Set(priced.map((task) => task.agent_id).filter(Boolean))),
    [priced],
  );

  // Models with no rate-table entry and no provider-reported cost: their
  // tokens are counted but their spend is not, so the totals below understate
  // reality. Saying so is the difference between an estimate and a wrong number.
  const unmapped = useMemo(
    () => collectUnmappedModels(priced.flatMap((task) => task.usage ?? [])),
    [priced, pricings],
  );

  // Floor, not round: on a cache-heavy issue 99.55% rounds to "100% hit rate",
  // which claims every single token came from cache. Flooring only ever says
  // 100% when it is actually 100%, and one percentage point of pessimism is
  // cheaper than an impossible-looking number.
  const cacheHitRate =
    total && total.input + total.cacheRead > 0
      ? Math.floor((total.cacheRead / (total.input + total.cacheRead)) * 100)
      : 0;

  // Whichever run the reader picked, falling back to the dearest one. A pick
  // that is no longer in the list (the run list refreshed past it) falls back
  // too, rather than leaving the panel blank under a table that still has rows.
  const selectedId = useMemo(() => {
    if (pickedRunId && priced.some((task) => task.id === pickedRunId)) {
      return pickedRunId;
    }
    return defaultSelectedRunId(priced);
  }, [pickedRunId, priced, pricings]);
  const selected = priced.find((task) => task.id === selectedId) ?? null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* DialogContent's base is `sm:max-w-sm`, which the same-specificity
          `max-w-4xl` does not beat — the table then overflows the box instead
          of the box growing. `!` wins it, matching the transcript dialog.
          5xl rather than 4xl because nine columns plus the token bar need
          ~920px: at 4xl the Cost column — the one people open this for —
          landed outside the scroll viewport. */}
      <DialogContent className="!max-w-5xl !w-[calc(100vw-4rem)]">
        <DialogHeader>
          <DialogTitle>{t(($) => $.usage_detail.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.usage_detail.subtitle, {
              identifier,
              count: priced.length,
            })}
          </DialogDescription>
        </DialogHeader>

        {isError ? (
          <UsageLoadFailed onRetry={onRetry} />
        ) : isPending ? (
          <UsageSkeleton />
        ) : total == null ? (
          <UsageEmpty />
        ) : (
          /* `min-w-0`: DialogContent is a grid, and a grid item defaults to
             `min-width: auto` — it sizes to its content's minimum rather than
             to the track. Without this the wide run table pushes this column
             past the dialog's own max-width, and every sibling (KPI cards,
             by-agent bars, footnotes) stretches with it and paints outside
             the box. */
          <div className="flex min-w-0 flex-col gap-5">
            <div className="grid grid-cols-3 divide-x rounded-lg border bg-card">
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cost)}
                value={formatUsd(total.cost)}
                hint={<CostConcentrationHint tasks={priced} total={total} />}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cache)}
                value={formatUsd(total.cacheSavings)}
                accent={total.cacheSavings > 0 ? "success" : "default"}
                hint={t(($) => $.usage_detail.kpi_cache_hint, {
                  pct: cacheHitRate,
                  reads: formatTokens(total.cacheRead),
                })}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_tokens)}
                value={formatTokens(total.tokens)}
                hint={t(($) => $.usage_detail.kpi_tokens_hint, {
                  input: formatTokens(total.input),
                  output: formatTokens(total.output),
                })}
              />
            </div>

            {agentIds.length > 1 && (
              <CostByAgent tasks={priced} agentIds={agentIds} total={total} />
            )}

            <RunTable
              tasks={priced}
              total={total}
              selectedId={selectedId}
              onSelect={setPickedRunId}
            />

            {selected && (
              <RunCostBreakdown task={selected} issueCost={total.cost} />
            )}

            <div className="space-y-1 text-micro text-muted-foreground">
              {unpricedCount > 0 && (
                <p>{t(($) => $.usage_detail.note_unpriced, { count: unpricedCount })}</p>
              )}
              {unmapped.length > 0 && (
                <p>
                  {t(($) => $.usage_detail.note_unmapped, { models: unmapped.join(", ") })}
                </p>
              )}
              <p>{t(($) => $.usage_detail.note_estimate)}</p>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ─── The three load states ─────────────────────────────────────────────────

// Nothing on this issue has a usage figure. Two very different situations
// share this branch — every run predates usage reporting, or the daemon is
// not reporting — and the reader can act on neither without being told which
// way to look, so the empty state says where reporting comes from instead of
// just stating the absence.
function UsageEmpty() {
  const { t } = useT("issues");
  return (
    <div className="py-10 text-center">
      <p className="text-body text-muted-foreground">
        {t(($) => $.usage_detail.empty)}
      </p>
      <p className="mx-auto mt-2 max-w-md text-caption text-muted-foreground">
        {t(($) => $.usage_detail.empty_hint)}
      </p>
    </div>
  );
}

// Shaped like the content it replaces — three KPI tiles over a run table — so
// the dialog does not resize the instant the data lands. A spinner here would
// be less work and would make the box jump.
function UsageSkeleton() {
  const { t } = useT("issues");
  return (
    <div className="flex min-w-0 flex-col gap-5" aria-busy="true">
      <span className="sr-only">{t(($) => $.usage_detail.loading)}</span>
      <div className="grid grid-cols-3 divide-x rounded-lg border bg-card">
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex flex-col gap-2 p-5">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-8 w-24" />
            <Skeleton className="h-3 w-28" />
          </div>
        ))}
      </div>
      <div className="space-y-2">
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-7 w-full" />
        ))}
      </div>
      <Skeleton className="h-40 w-full rounded-lg" />
    </div>
  );
}

// The run list failed to load. Retry is the whole point of this branch: the
// dialog has no other way back to the data, and closing and reopening it
// would hit the same cached rejection.
function UsageLoadFailed({ onRetry }: { onRetry?: () => void }) {
  const { t } = useT("issues");
  return (
    <div className="py-10 text-center">
      <p className="text-body text-muted-foreground">
        {t(($) => $.usage_detail.load_failed)}
      </p>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="mt-3 inline-flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-caption transition-colors hover:bg-accent"
        >
          <RotateCcw className="!size-3.5" />
          {t(($) => $.usage_detail.retry)}
        </button>
      )}
    </div>
  );
}

// "N failed runs account for X%" — the single most useful thing the total can
// say about itself, and the reason this dialog exists. Rendered only when
// there IS a failed run with a cost, so a healthy issue gets no scolding hint.
function CostConcentrationHint({
  tasks,
  total,
}: {
  tasks: AgentTask[];
  total: TaskUsageSummary;
}) {
  const { t } = useT("issues");
  const failed = tasks.filter((task) => task.status === "failed");
  if (failed.length === 0 || total.cost <= 0) return null;

  const failedCost = failed.reduce(
    (sum, task) => sum + (summarizeTaskUsage(task.usage)?.cost ?? 0),
    0,
  );
  if (failedCost <= 0) return null;

  return (
    <span className="text-warning">
      {t(($) => $.usage_detail.kpi_cost_hint, {
        count: failed.length,
        pct: Math.round((failedCost / total.cost) * 100),
      })}
    </span>
  );
}

// Same bar language as the runtime usage page's "Cost by agent" block: one row
// per agent, bar scaled to the biggest spender. Hidden entirely for a
// single-agent issue, where a one-bar chart says nothing the total didn't.
function CostByAgent({
  tasks,
  agentIds,
  total,
}: {
  tasks: AgentTask[];
  agentIds: string[];
  total: TaskUsageSummary;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const pricings = useCustomPricingStore((s) => s.pricings);

  const rows = useMemo(() => {
    return agentIds
      .map((agentId) => {
        const own = tasks.filter((task) => task.agent_id === agentId);
        const summary = summarizeTaskUsageAcross(own.map((task) => task.usage));
        return { agentId, cost: summary?.cost ?? 0, tokens: summary?.tokens ?? 0 };
      })
      .toSorted((a, b) => b.cost - a.cost);
  }, [agentIds, tasks, pricings]);

  const maxCost = rows.reduce((m, r) => Math.max(m, r.cost), 0);

  return (
    <div>
      <div className="mb-2 text-caption font-medium">
        {t(($) => $.usage_detail.by_agent)}
      </div>
      <div className="space-y-2">
        {rows.map((row) => (
          <div
            key={row.agentId}
            className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)_5rem_4rem] items-center gap-3"
          >
            <div className="flex min-w-0 items-center gap-2">
              <ActorAvatar actorType="agent" actorId={row.agentId} size="sm" enableHoverCard />
              <span className="truncate text-caption">
                {getActorName("agent", row.agentId)}
              </span>
            </div>
            <div className="relative h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-chart-1"
                style={{ width: `${maxCost > 0 ? (row.cost / maxCost) * 100 : 0}%` }}
              />
            </div>
            <div className="text-right text-caption tabular-nums text-muted-foreground">
              {formatTokens(row.tokens)}
            </div>
            <div className="text-right text-caption font-medium tabular-nums">
              {formatUsd(row.cost)}
            </div>
          </div>
        ))}
      </div>
      <div className="sr-only">
        {t(($) => $.usage_detail.by_agent_total, { cost: formatUsd(total.cost) })}
      </div>
    </div>
  );
}

function RunTable({
  tasks,
  total,
  selectedId,
  onSelect,
}: {
  tasks: AgentTask[];
  total: TaskUsageSummary;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  const { t } = useT("issues");
  const maxTokens = tasks.reduce(
    (m, task) => Math.max(m, summarizeTaskUsage(task.usage)?.tokens ?? 0),
    0,
  );

  return (
    // Nine columns of numbers have a floor width; on a narrow window they
    // scroll sideways rather than squeezing the trigger text to nothing or
    // pushing the totals outside the dialog.
    //
    // `min-w-0` is what makes `overflow-auto` actually clip: as a flex item
    // this box defaults to `min-width: auto`, so it grows to the table's
    // min-content width and never scrolls. Removing it puts the table back
    // outside the dialog.
    <div className="max-h-[45vh] min-w-0 overflow-auto">
      <table className="w-full min-w-[46rem]">
        <thead className="sticky top-0 bg-popover">
          <tr className="text-micro text-muted-foreground [&>th]:whitespace-nowrap [&>th]:px-2 [&>th]:pb-1.5 [&>th]:text-right [&>th]:font-normal">
            <th className="!pl-0 !text-left">{t(($) => $.usage_detail.col_run)}</th>
            <th>{t(($) => $.usage_detail.col_model)}</th>
            <th>{t(($) => $.usage_detail.col_duration)}</th>
            <th>{t(($) => $.usage_detail.col_input)}</th>
            <th>{t(($) => $.usage_detail.col_output)}</th>
            <th>{t(($) => $.usage_detail.col_cache_read)}</th>
            <th>{t(($) => $.usage_detail.col_cache_write)}</th>
            <th className="!pr-16">{t(($) => $.usage_detail.col_tokens)}</th>
            <th className="!pr-0">{t(($) => $.usage_detail.col_cost)}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <RunRow
              key={task.id}
              task={task}
              maxTokens={maxTokens}
              selected={task.id === selectedId}
              onSelect={onSelect}
            />
          ))}
        </tbody>
        <tfoot>
          <tr className="text-caption font-medium tabular-nums [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:border-input [&>td]:px-2 [&>td]:py-2 [&>td]:text-right">
            <td className="!pl-0 !text-left">{t(($) => $.usage_detail.total)}</td>
            <td colSpan={2} />
            <td>{formatTokens(total.input)}</td>
            <td>{formatTokens(total.output)}</td>
            <td>{formatTokens(total.cacheRead)}</td>
            <td>{formatTokens(total.cacheWrite)}</td>
            <td className="!pr-16">{formatTokens(total.tokens)}</td>
            <td className="!pr-0">{formatUsd(total.cost)}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  );
}

function RunRow({
  task,
  maxTokens,
  selected,
  onSelect,
}: {
  task: AgentTask;
  maxTokens: number;
  selected: boolean;
  onSelect: (id: string) => void;
}) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  const statusLabel = useStatusLabel(task.status);
  const summary = summarizeTaskUsage(task.usage);
  if (!summary) return null;

  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : "";

  return (
    // Selecting a row drives the breakdown panel below, so the row is a
    // control: keyboard-reachable, and `aria-selected` rather than colour
    // alone. Selection is carried by font WEIGHT as well as background, and
    // the selected row pins its own hover background — hover only changes the
    // background, so without both the pointer landing on the selected row
    // would visually demote it to "just hovered".
    <tr
      role="button"
      tabIndex={0}
      aria-selected={selected}
      onClick={() => onSelect(task.id)}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onSelect(task.id);
        }
      }}
      className={`cursor-pointer text-caption tabular-nums outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:px-2 [&>td]:py-1.5 [&>td]:text-right ${
        selected
          ? "bg-accent/70 font-medium text-foreground hover:bg-accent/70"
          : "hover:bg-accent/40"
      }`}
    >
      <td className="!pl-0 !text-left">
        <div className="flex items-center gap-2">
          <ActorAvatar actorType="agent" actorId={task.agent_id} size="sm" enableHoverCard />
          <span className="max-w-[13rem] truncate">{trigger}</span>
          {task.status === "running" ? (
            <span className="inline-flex shrink-0 items-center gap-1 text-micro text-info">
              <span className="h-1.5 w-1.5 rounded-full bg-info" />
              {t(($) => $.execution_log.status_running)}
            </span>
          ) : (
            <>
              {/* TaskStatusIcon is aria-hidden, so the glyph alone leaves a
                  screen reader with no way to tell a failed run from a
                  completed one. Same sr-only pairing the execution log rows
                  use. */}
              <TaskStatusIcon status={task.status} />
              <span className="sr-only">{statusLabel}</span>
            </>
          )}
        </div>
      </td>
      {/* A run that spilled across models lists them all, and real model ids
          are long (`claude-haiku-4-5-20251001, claude-opus-5[1m]`). Left
          uncapped that single cell sets the table's width and forces every
          other issue's dialog to scroll sideways, so cap it and keep the full
          list in the title. `whitespace-nowrap` comes from the row, so the
          cell needs its own width bound for `truncate` to have anything to
          truncate against. */}
      <td className="max-w-[11rem] text-micro text-muted-foreground">
        <span className="block truncate" title={summary.models.join(", ")}>
          {summary.models.join(", ") || "—"}
        </span>
      </td>
      <td className="text-muted-foreground">{duration || "—"}</td>
      <td>{formatTokens(summary.input)}</td>
      <td>{formatTokens(summary.output)}</td>
      <td>{formatTokens(summary.cacheRead)}</td>
      <td>{formatTokens(summary.cacheWrite)}</td>
      <td className="!pr-2">
        <div className="flex items-center justify-end gap-2">
          <span>{formatTokens(summary.tokens)}</span>
          <span className="relative h-1 w-14 overflow-hidden rounded-full bg-muted">
            <span
              className="absolute inset-y-0 left-0 rounded-full bg-chart-1"
              style={{ width: `${maxTokens > 0 ? (summary.tokens / maxTokens) * 100 : 0}%` }}
            />
          </span>
        </div>
      </td>
      <td className="!pr-0 font-medium">{formatUsd(summary.cost)}</td>
    </tr>
  );
}
