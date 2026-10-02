"use client";

import { Check } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

export type GoalFlowStep = "chat" | "align" | "issue";

/** The Goal flow is one path through existing surfaces, not a fourth mode. */
export function GoalFlowSteps({
  active,
  className,
}: {
  active: GoalFlowStep;
  className?: string;
}) {
  const { t } = useT("issues");
  const steps: Array<{ id: GoalFlowStep; label: string }> = [
    { id: "chat", label: t(($) => $.alignment.goal_flow_chat) },
    { id: "align", label: t(($) => $.alignment.goal_flow_align) },
    { id: "issue", label: t(($) => $.alignment.goal_flow_issue) },
  ];
  const currentIndex = steps.findIndex((step) => step.id === active);

  return (
    <nav
      aria-label={t(($) => $.alignment.goal_flow_aria)}
      className={cn("flex items-center gap-1.5 text-caption", className)}
    >
      {steps.map((step, index) => {
        const complete = index < currentIndex;
        const isActive = index === currentIndex;
        return (
          <div key={step.id} className="flex min-w-0 items-center gap-1.5">
            {index > 0 ? <span className="h-px w-3 bg-border" aria-hidden="true" /> : null}
            <span
              aria-current={isActive ? "step" : undefined}
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-1",
                isActive && "bg-primary/10 font-medium text-primary",
                complete && "text-muted-foreground",
                !isActive && !complete && "text-faint-foreground",
              )}
            >
              {complete ? <Check className="size-3" aria-hidden="true" /> : null}
              <span>{step.label}</span>
            </span>
          </div>
        );
      })}
    </nav>
  );
}
