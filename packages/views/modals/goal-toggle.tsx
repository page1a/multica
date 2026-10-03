"use client";

import { Target } from "lucide-react";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "../i18n";

/** Shared target-mode control used by both create faces. */
export function GoalToggle({
  checked,
  onCheckedChange,
  disabled,
}: {
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  const { t } = useT("issues");
  return (
    <label className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-brand/30 bg-brand/5 px-2 py-1 text-caption text-brand cursor-pointer select-none">
      <Target className="size-3.5" aria-hidden="true" />
      <span>{t(($) => $.detail.goal.goal_toggle)}</span>
      <Switch size="sm" checked={checked} onCheckedChange={onCheckedChange} disabled={disabled} />
    </label>
  );
}
