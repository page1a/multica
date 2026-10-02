"use client";

import { useMemo, useState } from "react";
import { Check, CircleHelp, Plus, Target } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useOpenIssueGoal } from "@multica/core/issues/mutations";
import { useT } from "../../i18n";

const DEFAULT_CHECKS = ["验收标准已经满足", "相关测试或证据已准备好"];

/** The one completion-line surface shared by goal entry points. */
export function GoalCompletionPanel({
  issueId,
  title,
  initialChecks,
  onConfirmed,
}: {
  issueId: string;
  title?: string;
  initialChecks?: string[];
  onConfirmed?: () => void;
}) {
  const { t } = useT("issues");
  const [checks, setChecks] = useState(() => {
    const values = (initialChecks ?? DEFAULT_CHECKS).filter(Boolean);
    return values.length > 0 ? values : [...DEFAULT_CHECKS];
  });
  const [active, setActive] = useState(0);
  const openGoal = useOpenIssueGoal();
  const canConfirm = useMemo(
    () => checks.length > 0 && checks.every((check) => check.trim().length > 0),
    [checks],
  );

  const updateCheck = (value: string) => {
    setChecks((current) => current.map((check, index) => (index === active ? value : check)));
  };

  const confirm = async () => {
    if (!canConfirm || openGoal.isPending) return;
    await openGoal.mutateAsync({
      issueId,
      input: {
        checks: checks.map((description) => ({ description: description.trim(), method: "acceptance" })),
      },
    });
    onConfirmed?.();
  };

  return (
    <section aria-labelledby="goal-completion-title" className="space-y-5">
      <div className="flex items-start gap-3">
        <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <Target className="size-5" aria-hidden="true" />
        </span>
        <div className="min-w-0">
          <h2 id="goal-completion-title" className="text-title-sm font-semibold">
            {t(($) => $.detail.goal.completion_title)}
          </h2>
          <p className="mt-1 text-caption leading-5 text-muted-foreground">
            {title ? `${title} · ` : ""}{t(($) => $.detail.goal.completion_hint)}
          </p>
        </div>
      </div>

      <div className="rounded-xl border bg-card/50 p-4">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-caption font-medium">
            <CircleHelp className="size-4 text-primary" aria-hidden="true" />
            {t(($) => $.detail.goal.question_label)}
          </div>
          {checks.length > 1 && (
            <div className="flex items-center gap-1" aria-label={t(($) => $.detail.goal.question_tabs)}>
              {checks.map((_, index) => (
                <button
                  key={index}
                  type="button"
                  aria-label={t(($) => $.detail.goal.question_number, { n: index + 1 })}
                  aria-current={index === active ? "step" : undefined}
                  onClick={() => setActive(index)}
                  className={cn(
                    "flex size-7 items-center justify-center rounded-full text-micro",
                    index === active ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground",
                  )}
                >
                  {index + 1}
                </button>
              ))}
            </div>
          )}
        </div>
        <p className="mt-3 text-body font-medium">{t(($) => $.detail.goal.question_prompt)}</p>
        <div className="mt-3 space-y-2">
          <Input value={checks[active] ?? ""} onChange={(event) => updateCheck(event.target.value)} />
          <p className="text-caption text-muted-foreground">{t(($) => $.detail.goal.other_option)}</p>
        </div>
        <button
          type="button"
          className="mt-3 inline-flex items-center gap-1.5 text-caption text-primary hover:underline"
          onClick={() => {
            setChecks((current) => [...current, ""]);
            setActive(checks.length);
          }}
        >
          <Plus className="size-3.5" aria-hidden="true" />
          {t(($) => $.detail.goal.add_question)}
        </button>
      </div>

      <div className="rounded-lg bg-muted/40 px-3 py-2 text-caption text-muted-foreground">
        <Check className="mr-1 inline size-3.5 text-success" aria-hidden="true" />
        {t(($) => $.detail.goal.lock_hint)}
      </div>
      <Button className="w-full" onClick={() => void confirm()} disabled={!canConfirm || openGoal.isPending}>
        {openGoal.isPending ? t(($) => $.detail.goal.confirming) : t(($) => $.detail.goal.confirm)}
      </Button>
    </section>
  );
}
