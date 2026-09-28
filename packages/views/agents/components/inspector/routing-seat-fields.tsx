"use client";

import { useState } from "react";
import {
  ROUTING_TIER_KEYS,
  ROUTING_USAGE_KEYS,
  routingTierKeyOf,
  routingUsageKeyOf,
  type RoutingTierKey,
  type RoutingUsageKey,
} from "@multica/core/agents";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

/** Weakest first, then off: the order the segmented rows read left to right. */
export const ROUTING_TIER_CHOICES: readonly ("" | RoutingTierKey)[] = [
  "",
  ...[...ROUTING_TIER_KEYS].reverse(),
];

export function routingTierLabel(t: AgentsT, key: RoutingTierKey | ""): string {
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
      return t(($) => $.pickers.routing_tier_none);
  }
}

export function routingUsageLabel(t: AgentsT, key: RoutingUsageKey): string {
  switch (key) {
    case "tight":
      return t(($) => $.pickers.routing_usage_tight);
    case "ample":
      return t(($) => $.pickers.routing_usage_ample);
    default:
      return t(($) => $.pickers.routing_usage_normal);
  }
}

/**
 * A row of mutually exclusive buttons that saves on click. Used for the two
 * routing tags because both have a handful of values that fit on one line,
 * and seeing every value at once is what makes the current one legible.
 */
function Segmented<K extends string>({
  label,
  options,
  value,
  canEdit,
  onChange,
}: {
  label: string;
  options: readonly { key: K; label: string }[];
  value: K;
  canEdit: boolean;
  onChange: (next: K) => Promise<void> | void;
}) {
  // Holds the clicked value while the write is in flight, so the row shows
  // the choice immediately and falls back to the saved one if it fails.
  const [pending, setPending] = useState<K | null>(null);
  const shown = pending ?? value;

  const pick = async (next: K) => {
    if (next === shown || !canEdit) return;
    setPending(next);
    try {
      await onChange(next);
    } finally {
      setPending(null);
    }
  };

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex max-w-full overflow-hidden rounded-md border border-input"
    >
      {options.map((option, index) => {
        const on = option.key === shown;
        return (
          <button
            key={option.key || "none"}
            type="button"
            role="radio"
            aria-checked={on}
            disabled={!canEdit}
            onClick={() => void pick(option.key)}
            className={cn(
              "px-2.5 py-1 text-caption whitespace-nowrap transition-colors focus-visible:outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed",
              index > 0 && "border-l border-input",
              on
                ? "bg-accent font-medium text-foreground"
                : "text-muted-foreground enabled:hover:bg-muted",
            )}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

/** The seat's rung on the dispatch ladder, or off it. */
export function RoutingTierSegmented({
  value,
  canEdit,
  onChange,
}: {
  value: string | undefined;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  return (
    <Segmented
      label={t(($) => $.inspector.prop_routing_tier)}
      options={ROUTING_TIER_CHOICES.map((key) => ({
        key,
        label: routingTierLabel(t, key),
      }))}
      value={routingTierKeyOf(value)}
      canEdit={canEdit}
      onChange={onChange}
    />
  );
}

/** How much account headroom the seat has; orders seats inside a rung. */
export function RoutingUsageSegmented({
  value,
  canEdit,
  onChange,
}: {
  value: string | undefined;
  canEdit: boolean;
  onChange: (next: RoutingUsageKey) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  return (
    <Segmented
      label={t(($) => $.inspector.prop_routing_usage)}
      options={ROUTING_USAGE_KEYS.map((key) => ({
        key,
        label: routingUsageLabel(t, key),
      }))}
      value={routingUsageKeyOf(value)}
      canEdit={canEdit}
      onChange={onChange}
    />
  );
}
