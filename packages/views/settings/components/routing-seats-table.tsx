"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ROUTING_TIER_KEYS,
  ROUTING_USAGE_KEYS,
  routingTierKeyOf,
  routingUsageKeyOf,
  useBulkUpdateAgentRouting,
  type RoutingTierKey,
  type RoutingUsageKey,
} from "@multica/core/agents";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { Agent } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import {
  ROUTING_TIER_CHOICES,
  routingTierLabel,
  routingUsageLabel,
} from "../../agents/components/inspector/routing-seat-fields";

// A Select item cannot carry an empty value, so "off the ladder" travels as
// this sentinel inside the table and becomes "" on the wire.
const TIER_OFF = "off";

type TierChoice = RoutingTierKey | typeof TIER_OFF;

function tierChoiceOf(agent: Agent): TierChoice {
  return routingTierKeyOf(agent.routing_tier) || TIER_OFF;
}

function tierWire(choice: TierChoice): string {
  return choice === TIER_OFF ? "" : choice;
}

/** Laddered seats first, strongest to weakest, then off-ladder; by name inside. */
function seatOrder(a: Agent, b: Agent): number {
  const rank = (agent: Agent) => {
    const key = routingTierKeyOf(agent.routing_tier);
    return key ? ROUTING_TIER_KEYS.indexOf(key) : ROUTING_TIER_KEYS.length;
  };
  return rank(a) - rank(b) || a.name.localeCompare(b.name);
}

/**
 * The routing seats table (DENE-922): every live agent with its tier and
 * usage, editable in place, and a checkbox selection that applies one tier or
 * one usage to many seats in a single all-or-nothing write.
 */
export function RoutingSeatsTable({
  wsId,
  canManage,
}: {
  wsId: string;
  canManage: boolean;
}) {
  const { t } = useT("settings");
  const { t: ta } = useT("agents");
  const agentsQuery = useQuery({ ...agentListOptions(wsId), enabled: !!wsId });
  const write = useBulkUpdateAgentRouting(wsId);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());

  const seats = useMemo(
    () =>
      (agentsQuery.data ?? [])
        .filter((agent) => !agent.archived_at)
        .sort(seatOrder),
    [agentsQuery.data],
  );
  // Drop ids that left the list (archived elsewhere) so the count stays true.
  const selectedIds = seats
    .filter((seat) => selected.has(seat.id))
    .map((seat) => seat.id);
  const allSelected = seats.length > 0 && selectedIds.length === seats.length;

  const toggle = (id: string, on: boolean) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });

  const tierLabel = (choice: TierChoice) =>
    routingTierLabel(ta, choice === TIER_OFF ? "" : choice);
  const tierItems = ROUTING_TIER_CHOICES.map((key) => ({
    value: (key || TIER_OFF) as TierChoice,
    label: routingTierLabel(ta, key),
  }));
  const usageItems = ROUTING_USAGE_KEYS.map((key) => ({
    value: key,
    label: routingUsageLabel(ta, key),
  }));
  const tierHeading = ta(($) => $.inspector.prop_routing_tier);
  const usageHeading = ta(($) => $.inspector.prop_routing_usage);

  if (seats.length === 0) {
    return (
      <p className="px-4 py-3 text-caption text-muted-foreground">
        {t(($) => $.routing.seats_empty)}
      </p>
    );
  }

  return (
    <div className="flex flex-col">
      {canManage && selectedIds.length > 0 ? (
        <div
          role="toolbar"
          aria-label={t(($) => $.routing.seats_selected, {
            count: selectedIds.length,
          })}
          className="flex flex-wrap items-center gap-2 border-b border-surface-border bg-muted/40 px-4 py-2"
        >
          <span className="text-caption font-medium">
            {t(($) => $.routing.seats_selected, { count: selectedIds.length })}
          </span>
          <Select
            items={tierItems}
            value={null}
            disabled={write.isPending}
            onValueChange={(value) => {
              if (value)
                write.mutate({
                  agent_ids: selectedIds,
                  routing_tier: tierWire(value as TierChoice),
                });
            }}
          >
            <SelectTrigger size="sm" className="w-28" aria-label={t(($) => $.routing.seats_bulk_tier)}>
              <SelectValue>{() => t(($) => $.routing.seats_bulk_tier)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {tierItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            items={usageItems}
            value={null}
            disabled={write.isPending}
            onValueChange={(value) => {
              if (value)
                write.mutate({
                  agent_ids: selectedIds,
                  routing_usage: value as RoutingUsageKey,
                });
            }}
          >
            <SelectTrigger size="sm" className="w-28" aria-label={t(($) => $.routing.seats_bulk_usage)}>
              <SelectValue>{() => t(($) => $.routing.seats_bulk_usage)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {usageItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setSelected(new Set())}
          >
            {t(($) => $.routing.seats_bulk_clear)}
          </Button>
        </div>
      ) : null}

      {write.isError ? (
        <p role="alert" className="px-4 pt-2 text-caption text-destructive">
          {t(($) => $.routing.seats_save_error)}
        </p>
      ) : null}

      <Table>
        <TableHeader>
          <TableRow>
            {canManage ? (
              <TableHead className="w-10 pl-4">
                <Checkbox
                  checked={allSelected}
                  indeterminate={selectedIds.length > 0 && !allSelected}
                  onCheckedChange={(on) =>
                    setSelected(on ? new Set(seats.map((s) => s.id)) : new Set())
                  }
                  aria-label={t(($) => $.routing.seats_select_all)}
                />
              </TableHead>
            ) : null}
            <TableHead className={canManage ? undefined : "pl-4"}>
              {t(($) => $.routing.seats_col_agent)}
            </TableHead>
            <TableHead>{t(($) => $.routing.seats_col_model)}</TableHead>
            <TableHead>{tierHeading}</TableHead>
            <TableHead className="pr-4">{usageHeading}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {seats.map((seat) => {
            const tier = tierChoiceOf(seat);
            const usage = routingUsageKeyOf(seat.routing_usage);
            return (
              <TableRow
                key={seat.id}
                data-state={selected.has(seat.id) ? "selected" : undefined}
              >
                {canManage ? (
                  <TableCell className="pl-4">
                    <Checkbox
                      checked={selected.has(seat.id)}
                      onCheckedChange={(on) => toggle(seat.id, on)}
                      aria-label={t(($) => $.routing.seats_select_row, {
                        name: seat.name,
                      })}
                    />
                  </TableCell>
                ) : null}
                <TableCell
                  className={cn(
                    "max-w-48 truncate font-medium",
                    !canManage && "pl-4",
                    tier === TIER_OFF && "text-muted-foreground",
                  )}
                >
                  {seat.name}
                </TableCell>
                <TableCell className="max-w-48 truncate font-mono text-caption text-muted-foreground">
                  {seat.model || "—"}
                </TableCell>
                <TableCell>
                  <Select
                    items={tierItems}
                    value={tier}
                    disabled={!canManage || write.isPending}
                    onValueChange={(value) => {
                      if (value && value !== tier)
                        write.mutate({
                          agent_ids: [seat.id],
                          routing_tier: tierWire(value as TierChoice),
                        });
                    }}
                  >
                    <SelectTrigger
                      size="sm"
                      className="w-24"
                      aria-label={`${seat.name} · ${tierHeading}`}
                    >
                      <SelectValue>{() => tierLabel(tier)}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {tierItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </TableCell>
                <TableCell className="pr-4">
                  <Select
                    items={usageItems}
                    value={usage}
                    disabled={!canManage || write.isPending}
                    onValueChange={(value) => {
                      if (value && value !== usage)
                        write.mutate({
                          agent_ids: [seat.id],
                          routing_usage: value as RoutingUsageKey,
                        });
                    }}
                  >
                    <SelectTrigger
                      size="sm"
                      className="w-24"
                      aria-label={`${seat.name} · ${usageHeading}`}
                    >
                      <SelectValue>{() => routingUsageLabel(ta, usage)}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      {usageItems.map((item) => (
                        <SelectItem key={item.value} value={item.value}>
                          {item.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
