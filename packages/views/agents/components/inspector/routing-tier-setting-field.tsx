"use client";

import { useState, type ReactNode } from "react";
import { ChevronDown, Signal } from "lucide-react";
import {
  ROUTING_TIER_KEYS,
  routingTierKeyOf,
  type RoutingTierKey,
} from "@multica/core/agents";
import { PickerItem, PropertyPicker } from "../../../issues/components/pickers";
import { SettingsRow } from "../../../settings/components/settings-layout";
import { useT } from "../../../i18n";

/**
 * The seat's rung on the automatic-dispatch ladder.
 *
 * This is the one field on the agent page that routing reads, and it is here —
 * next to model and thinking level — because that is where the judgement is
 * made: strength is model AND thinking level AND prompt together, which is why
 * the server cannot derive it and a person tags it instead.
 */
export function RoutingTierSettingField({
  label,
  description,
  value,
  canEdit,
  onChange,
}: {
  label: ReactNode;
  description?: ReactNode;
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  return (
    <SettingsRow label={label} description={description} size="select-wide">
      <RoutingTierPicker value={value} canEdit={canEdit} onChange={onChange} />
    </SettingsRow>
  );
}

export function routingTierLabel(
  t: ReturnType<typeof useT<"agents">>["t"],
  key: RoutingTierKey,
): string {
  switch (key) {
    case "strongest":
      return t(($) => $.pickers.routing_tier_strongest);
    case "strong":
      return t(($) => $.pickers.routing_tier_strong);
    case "medium":
      return t(($) => $.pickers.routing_tier_medium);
    case "weak":
      return t(($) => $.pickers.routing_tier_weak);
    default:
      // Server-driven enum: an unknown rung is shown as "off the ladder"
      // rather than as a raw key.
      return t(($) => $.pickers.routing_tier_none);
  }
}

function RoutingTierPicker({
  value,
  canEdit,
  onChange,
}: {
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const current = routingTierKeyOf(value);
  const triggerLabel = current
    ? routingTierLabel(t, current)
    : t(($) => $.pickers.routing_tier_none);
  const triggerTitle = t(($) => $.pickers.routing_tier_tooltip, {
    value: triggerLabel,
  });

  const select = async (next: string) => {
    setOpen(false);
    if (next !== current) await onChange(next);
  };

  const display = (
    <div className="flex min-h-10 items-center gap-2 rounded-lg border border-input bg-input/50 px-3 text-body text-muted-foreground">
      <Signal className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 truncate">{triggerLabel}</span>
    </div>
  );
  if (!canEdit) return display;

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-[var(--anchor-width)] min-w-[14rem] max-w-md"
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className="flex min-h-10 w-full min-w-0 items-center gap-2 rounded-lg border border-input bg-transparent px-3 text-left text-body transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          aria-label={triggerTitle}
        />
      }
      trigger={
        <>
          <Signal
            className="h-4 w-4 shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          <span className="min-w-0 flex-1 truncate">{triggerLabel}</span>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-muted-foreground transition-transform ${
              open ? "rotate-180" : ""
            }`}
            aria-hidden="true"
          />
        </>
      }
    >
      {ROUTING_TIER_KEYS.map((key) => (
        <PickerItem
          key={key}
          selected={key === current}
          onClick={() => void select(key)}
        >
          <span className="block min-w-0 flex-1 text-left">
            <span className="truncate text-label font-medium">
              {routingTierLabel(t, key)}
            </span>
          </span>
        </PickerItem>
      ))}
      <button
        type="button"
        onClick={() => void select("")}
        className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-caption text-muted-foreground transition-colors hover:bg-accent/50"
        title={t(($) => $.pickers.routing_tier_none_title)}
      >
        {t(($) => $.pickers.routing_tier_none)}
      </button>
    </PropertyPicker>
  );
}
