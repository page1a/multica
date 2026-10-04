"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ROUTING_USAGE_KEYS,
  routingTierKeyOf,
  routingUsageKeyOf,
  useBulkUpdateAgentRouting,
  type RoutingTierKey,
  type RoutingUsageKey,
} from "@multica/core/agents";
import { paths, useCurrentWorkspace } from "@multica/core/paths";
import { runtimeListOptions } from "@multica/core/runtimes";
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
import { AppLink } from "../../navigation";
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

/**
 * Fixed position inside a runtime: creation order, never tier, so a row stays
 * put while its tier is being edited.
 */
function seatOrder(a: Agent, b: Agent): number {
  return a.created_at.localeCompare(b.created_at) || a.name.localeCompare(b.name);
}

type SeatRow = { agent: Agent; depth: 0 | 1 };
type SeatGroup = { runtimeId: string; runtimeName: string; rows: SeatRow[] };

/**
 * One group per runtime, groups by runtime name. Inside a group, base roles in
 * creation order, each followed by its specialisations. A specialisation whose
 * base role is not in the live list stays a root, so archiving the parent does
 * not hide it.
 */
function groupSeats(
  agents: Agent[],
  runtimeName: (runtimeId: string) => string,
): SeatGroup[] {
  const live = agents.filter((agent) => !agent.archived_at);
  const liveIds = new Set(live.map((agent) => agent.id));
  const children = new Map<string, Agent[]>();
  const roots: Agent[] = [];
  for (const agent of live) {
    const parentId = agent.parent_agent_id;
    if (parentId && liveIds.has(parentId)) {
      const list = children.get(parentId);
      if (list) list.push(agent);
      else children.set(parentId, [agent]);
    } else {
      roots.push(agent);
    }
  }
  roots.sort(seatOrder);
  for (const list of children.values()) {
    list.sort((a, b) => a.name.localeCompare(b.name));
  }
  const groups = new Map<string, SeatRow[]>();
  for (const root of roots) {
    const rows = groups.get(root.runtime_id) ?? [];
    rows.push({ agent: root, depth: 0 });
    for (const child of children.get(root.id) ?? []) {
      rows.push({ agent: child, depth: 1 });
    }
    groups.set(root.runtime_id, rows);
  }
  return [...groups.entries()]
    .map(([runtimeId, rows]) => ({ runtimeId, runtimeName: runtimeName(runtimeId), rows }))
    .sort((a, b) => a.runtimeName.localeCompare(b.runtimeName));
}

/** Work switched off on the agent page: routing skips it, so its tier is moot here. */
function workOff(agent: Agent): boolean {
  return agent.work_enabled === false;
}

/** Open follow: tier and usage are the base role's, and this row cannot edit them. */
function followsParent(agent: Agent): boolean {
  return agent.runtime_inherited === true;
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
  const runtimesQuery = useQuery({ ...runtimeListOptions(wsId), enabled: !!wsId });
  const write = useBulkUpdateAgentRouting(wsId);
  const slug = useCurrentWorkspace()?.slug ?? "";
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());

  const unknownRuntime = t(($) => $.routing.seats_runtime_unknown);
  const groups = useMemo(() => {
    const names = new Map(
      (runtimesQuery.data ?? []).map((runtime) => [runtime.id, runtime.name]),
    );
    return groupSeats(
      agentsQuery.data ?? [],
      (runtimeId) => names.get(runtimeId) ?? unknownRuntime,
    );
  }, [agentsQuery.data, runtimesQuery.data, unknownRuntime]);
  const seats = groups.flatMap((group) => group.rows);
  // Followers and work-off seats are read-only, so they never join a bulk
  // write: the server would refuse a follower, and a work-off tier is moot.
  const editable = seats.filter(
    (row) => !followsParent(row.agent) && !workOff(row.agent),
  );
  // Drop ids that left the list (archived elsewhere) so the count stays true.
  const selectedIds = editable
    .filter((row) => selected.has(row.agent.id))
    .map((row) => row.agent.id);
  const allSelected = editable.length > 0 && selectedIds.length === editable.length;

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
                    setSelected(
                      on ? new Set(editable.map((row) => row.agent.id)) : new Set(),
                    )
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
          {groups.map((group) => [
            <TableRow key={`runtime-${group.runtimeId}`} className="hover:bg-transparent">
              <TableCell
                colSpan={canManage ? 5 : 4}
                className="bg-muted/40 px-4 py-1.5 text-caption font-medium text-muted-foreground"
              >
                {group.runtimeName}
              </TableCell>
            </TableRow>,
            ...group.rows.map(({ agent: seat, depth }) => {
            const tier = tierChoiceOf(seat);
            const usage = routingUsageKeyOf(seat.routing_usage);
            const follows = followsParent(seat);
            const off = workOff(seat);
            // Work-off wins over follow: it is the reason routing skips the row.
            const note = off
              ? t(($) => $.routing.seats_work_off)
              : follows
                ? seat.parent_agent_name
                  ? t(($) => $.routing.seats_follows, { name: seat.parent_agent_name })
                  : t(($) => $.routing.seats_follows_base)
                : "";
            const followLabel = follows && !off
              ? seat.parent_agent_name
                ? t(($) => $.routing.seats_follows, { name: seat.parent_agent_name })
                : t(($) => $.routing.seats_follows_base)
              : "";
            const locked = !canManage || follows || off || write.isPending;
            return (
              <TableRow
                key={seat.id}
                data-state={selected.has(seat.id) ? "selected" : undefined}
                data-seat-depth={depth}
              >
                {canManage ? (
                  <TableCell className="pl-4">
                    <Checkbox
                      checked={selected.has(seat.id)}
                      disabled={follows || off}
                      onCheckedChange={(on) => toggle(seat.id, on)}
                      aria-label={t(($) => $.routing.seats_select_row, {
                        name: seat.name,
                      })}
                    />
                  </TableCell>
                ) : null}
                <TableCell
                  className={cn(
                    "max-w-56 truncate font-medium",
                    !canManage && depth === 0 && "pl-4",
                    depth === 1 && "pl-10",
                    (tier === TIER_OFF || off) && "text-muted-foreground",
                  )}
                >
                  {follows ? (
                    <span className="block truncate">{seat.name}</span>
                  ) : (
                    seat.name
                  )}
                  {follows ? (
                    <span className="block truncate text-caption font-normal text-muted-foreground">
                      <span>{followLabel}</span>
                      {slug ? (
                        <>
                          {" · "}
                          <AppLink
                            href={paths.workspace(slug).agentDetail(seat.id)}
                            className="underline underline-offset-2 hover:text-foreground"
                          >
                            {t(($) => $.routing.seats_follows_unlock)}
                          </AppLink>
                        </>
                      ) : null}
                    </span>
                  ) : null}
                </TableCell>
                <TableCell className="max-w-48 truncate font-mono text-caption text-muted-foreground">
                  {seat.model || "—"}
                </TableCell>
                <TableCell>
                  <Select
                    items={tierItems}
                    value={tier}
                    disabled={locked}
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
                  <div className="flex items-center gap-2">
                    <Select
                      items={usageItems}
                      value={usage}
                      disabled={locked}
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
                    {off ? (
                      <span className="whitespace-nowrap text-caption text-muted-foreground">
                        {note}
                      </span>
                    ) : null}
                  </div>
                </TableCell>
              </TableRow>
            );
            }),
          ])}
        </TableBody>
      </Table>
    </div>
  );
}
