import { Check, ChevronDown, CircleDashed, CirclePause, ExternalLink, Target } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { issueGoalOptions } from "@multica/core/issues/queries";
import type { IssueGoal, IssueGoalCheck, IssueGoalEvidence } from "@multica/core/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

type GoalSectionProps = { wsId: string; issueId: string };
type GoalTranslator = (key: string, options?: Record<string, unknown>) => string;

function goalLabel(t: ReturnType<typeof useT<"issues">>["t"], key: string, options?: Record<string, unknown>): string {
  return (t as unknown as GoalTranslator)(key, options);
}

function statusText(status: IssueGoal["status"], t: ReturnType<typeof useT<"issues">>["t"]): string {
  switch (status) {
    case "draft":
      return goalLabel(t, "goal.status.draft");
    case "locked":
    case "active":
      return goalLabel(t, "goal.status.locked");
    case "stopped":
      return goalLabel(t, "goal.status.stopped");
    case "achieved":
      return goalLabel(t, "goal.status.achieved");
    default:
      return status;
  }
}

function statusClass(status: IssueGoal["status"]): string {
  if (status === "achieved") return "border-success/30 bg-success/10 text-success";
  if (status === "stopped") return "border-warning/30 bg-warning/10 text-warning-foreground";
  if (status === "locked" || status === "active") return "border-primary/30 bg-primary/10 text-primary";
  return "border-border bg-muted/60 text-muted-foreground";
}

function isPassed(check: IssueGoalCheck): boolean {
  return check.passed === true || check.status === "passed" || check.status === "done" || check.status === "achieved";
}

function budgetValue(budget: Record<string, unknown> | null | undefined, keys: string[]): number | null {
  for (const key of keys) {
    const value = budget?.[key];
    if (typeof value === "number" && Number.isFinite(value)) return value;
  }
  return null;
}

function formatCount(value: number | null): string {
  return value == null ? "—" : new Intl.NumberFormat().format(value);
}

function formatDuration(seconds: number | null): string {
  if (seconds == null) return "—";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h ${minutes % 60}m`;
}

function EvidenceList({ evidence, t }: { evidence: IssueGoalEvidence[]; t: ReturnType<typeof useT<"issues">>["t"] }) {
  if (evidence.length === 0) return null;
  return (
    <div className="mt-2 space-y-1.5 border-t border-border/60 pt-2">
      {evidence.map((item, index) => (
        <div key={item.id ?? `${item.kind ?? "evidence"}-${index}`} className="flex items-start gap-2 text-caption text-muted-foreground">
          <span className="mt-1 size-1.5 shrink-0 rounded-full bg-muted-foreground/60" />
          <div className="min-w-0 flex-1">
            <span>{item.label ?? item.detail ?? item.kind ?? goalLabel(t, "goal.evidence_item")}</span>
            {item.url && (
              <a href={item.url} target="_blank" rel="noreferrer" className="ml-1 inline-flex items-center gap-1 text-primary hover:underline">
                {goalLabel(t, "goal.open_evidence")}
                <ExternalLink className="size-3" />
              </a>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

function CheckRow({ check, t }: { check: IssueGoalCheck; t: ReturnType<typeof useT<"issues">>["t"] }) {
  const passed = isPassed(check);
  const evidence = check.evidence ?? [];
  const hasDetail = !!check.description || evidence.length > 0;
  const content = (
    <>
      <span className={cn("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border", passed ? "border-success bg-success text-success-foreground" : "border-muted-foreground/40 text-transparent")}>
        <Check className="size-3" strokeWidth={3} />
      </span>
      <span className={cn("min-w-0 flex-1 text-body", passed && "text-muted-foreground line-through decoration-muted-foreground/50")}>{check.title ?? check.description}</span>
      {check.method && <span className="shrink-0 rounded-xs bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">{check.method}</span>}
      {hasDetail && <ChevronDown className="mt-0.5 size-4 shrink-0 text-muted-foreground" />}
    </>
  );

  if (!hasDetail) return <div className="flex items-start gap-2.5 py-2">{content}</div>;
  return (
    <details className="group/check py-2">
      <summary className="flex cursor-pointer list-none items-start gap-2.5 [&::-webkit-details-marker]:hidden">{content}</summary>
      {check.description && <p className="ml-6 mt-1 text-caption leading-relaxed text-muted-foreground">{check.description}</p>}
      <EvidenceList evidence={evidence} t={t} />
    </details>
  );
}

export function GoalSection({ wsId, issueId }: GoalSectionProps) {
  const { t } = useT("issues");
  const { data: goal, isLoading } = useQuery(issueGoalOptions(wsId, issueId));
  if (isLoading) {
    return <div className="mt-8 space-y-3"><Skeleton className="h-5 w-24" /><Skeleton className="h-28 w-full rounded-lg" /></div>;
  }
  // Ordinary issues have no goal and should retain the existing detail layout.
  if (!goal) return null;

  const checks = goal.checks ?? [];
  const completed = checks.filter(isPassed).length;
  const budget = goal.budget as Record<string, unknown> | null | undefined;
  const usage = goal.usage as Record<string, unknown> | null | undefined;
  const tokenBudget = budgetValue(budget, ["tokens", "token_budget", "token_limit"]);
  const tokenUsage = budgetValue(usage, ["tokens_used", "tokens", "token_usage", "total_tokens"]);
  const runBudget = budgetValue(budget, ["runs", "run_budget", "run_limit"]);
  const runUsage = budgetValue(usage, ["runs_used", "runs", "run_usage", "run_count"]);
  const durationBudget = budgetValue(budget, ["duration_seconds", "duration_budget_seconds", "duration_limit_seconds"]);
  const durationUsage = budgetValue(usage, ["duration_seconds_used", "duration_seconds", "duration_usage_seconds", "elapsed_seconds"]);

  return (
    <section aria-labelledby={`goal-heading-${issueId}`} className="mt-8 rounded-xl border bg-card/40 p-4 md:p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2.5">
          <Target className="mt-0.5 size-5 shrink-0 text-primary" />
          <div className="min-w-0">
            <h2 id={`goal-heading-${issueId}`} className="text-title-sm font-semibold">{goalLabel(t, "goal.title")}</h2>
            <p className="mt-0.5 text-caption text-muted-foreground">{goalLabel(t, "goal.progress", { completed, total: checks.length })}</p>
          </div>
        </div>
        <span className={cn("inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-caption font-medium", statusClass(goal.status))}>
          {goal.status === "achieved" ? <Check className="size-3.5" /> : goal.status === "stopped" ? <CirclePause className="size-3.5" /> : goal.status === "draft" ? <CircleDashed className="size-3.5" /> : null}
          {statusText(goal.status, t)}
        </span>
      </div>

      <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
        <div className="rounded-lg bg-muted/50 px-3 py-2"><div className="text-micro text-muted-foreground">{goalLabel(t, "goal.round")}</div><div className="mt-0.5 text-body font-medium tabular-nums">{goal.round ?? 0}</div></div>
        <div className="rounded-lg bg-muted/50 px-3 py-2"><div className="text-micro text-muted-foreground">{goalLabel(t, "goal.tokens")}</div><div className="mt-0.5 text-body font-medium tabular-nums">{formatCount(tokenUsage)}{tokenBudget != null ? ` / ${formatCount(tokenBudget)}` : ""}</div></div>
        <div className="rounded-lg bg-muted/50 px-3 py-2"><div className="text-micro text-muted-foreground">{goalLabel(t, "goal.runs")}</div><div className="mt-0.5 text-body font-medium tabular-nums">{formatCount(runUsage)}{runBudget != null ? ` / ${formatCount(runBudget)}` : ""}</div></div>
        <div className="rounded-lg bg-muted/50 px-3 py-2"><div className="text-micro text-muted-foreground">{goalLabel(t, "goal.duration")}</div><div className="mt-0.5 text-body font-medium tabular-nums">{formatDuration(durationUsage)}{durationBudget != null ? ` / ${formatDuration(durationBudget)}` : ""}</div></div>
      </div>

      {Boolean(goal.budget_warning_at) && (
        <div role="status" className="mt-3 rounded-lg border border-amber-300/60 bg-amber-50/70 px-3 py-2 text-caption text-amber-900 dark:border-amber-700/60 dark:bg-amber-950/30 dark:text-amber-100">
          {goalLabel(t, "goal.budget_warning")}
        </div>
      )}
      {goal.status === "stopped" && (
        <div role="status" className="mt-3 rounded-lg border border-border bg-muted/40 px-3 py-2 text-caption text-muted-foreground">
          {goalLabel(t, "goal.stopped_hint")}
        </div>
      )}

      <div className="mt-4 divide-y divide-border/60 border-t border-border/60">
        {checks.map((check) => <CheckRow key={check.id} check={check} t={t} />)}
      </div>
      {goal.evidence && <EvidenceList evidence={goal.evidence} t={t} />}
    </section>
  );
}
