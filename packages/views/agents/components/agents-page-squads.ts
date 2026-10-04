import type { Squad, SquadMemberPreview } from "@multica/core/types";

/** Filter / group sentinel for agents that belong to no active squad. */
export const NO_SQUAD_ID = "__none__";

/** Group title row height — matches `h-9` on the list column header. */
export const GROUP_HEADER_HEIGHT = 36;

export type SquadRoster =
  | { kind: "known"; members: readonly SquadMemberPreview[] }
  | { kind: "unknown" };

export type FetchedSquadMembers = {
  status: "pending" | "error" | "success";
  members?: readonly {
    member_type: string;
    member_id: string;
    role?: string;
  }[];
};

function asPreview(
  members: readonly {
    member_type: string;
    member_id: string;
    role?: string;
  }[],
): SquadMemberPreview[] {
  return members.map((member) => ({
    member_type: member.member_type === "member" ? "member" : "agent",
    member_id: member.member_id,
    role: member.role ?? "",
  }));
}

export function isActiveSquad(squad: Squad): boolean {
  return squad.archived_at == null;
}

/** Fork backends still send the full roster on the list payload. */
export function hasInlineSquadRoster(squad: Squad): boolean {
  return Array.isArray(squad.members) && squad.members.length > 0;
}

/**
 * Official cloud omits `members` (and an empty array is equally unusable as a
 * full roster). Fetch GET /api/squads/:id/members for those active squads.
 */
export function needsSquadMembersFetch(squad: Squad): boolean {
  return isActiveSquad(squad) && !hasInlineSquadRoster(squad);
}

export function resolveSquadRoster(
  squad: Squad,
  fetched?: FetchedSquadMembers,
): SquadRoster {
  if (hasInlineSquadRoster(squad) && squad.members) {
    return { kind: "known", members: squad.members };
  }
  if (fetched?.status === "success") {
    return { kind: "known", members: asPreview(fetched.members ?? []) };
  }
  return { kind: "unknown" };
}

export function resolveSquadRosters(
  squads: readonly Squad[],
  fetched: ReadonlyMap<string, FetchedSquadMembers> = new Map(),
): Map<string, SquadRoster> {
  const map = new Map<string, SquadRoster>();
  for (const squad of squads) {
    if (!isActiveSquad(squad)) continue;
    map.set(squad.id, resolveSquadRoster(squad, fetched.get(squad.id)));
  }
  return map;
}

function rosterOf(
  squad: Squad,
  rosters?: ReadonlyMap<string, SquadRoster>,
): SquadRoster {
  if (rosters) return rosters.get(squad.id) ?? { kind: "unknown" };
  if (squad.members != null) return { kind: "known", members: squad.members };
  return { kind: "unknown" };
}

export function squadAgentMemberIds(
  squad: Squad,
  roster?: SquadRoster,
): string[] {
  const resolved = roster ?? rosterOf(squad);
  if (resolved.kind !== "known") return [];
  const ids: string[] = [];
  for (const member of resolved.members) {
    if (member.member_type === "agent") ids.push(member.member_id);
  }
  return ids;
}

/** Active squads that contain a given agent, in list order. */
export function buildAgentSquadsMap(
  squads: readonly Squad[],
  rosters?: ReadonlyMap<string, SquadRoster>,
): Map<string, Squad[]> {
  const map = new Map<string, Squad[]>();
  for (const squad of squads) {
    if (!isActiveSquad(squad)) continue;
    const roster = rosterOf(squad, rosters);
    if (roster.kind !== "known") continue;
    for (const agentId of squadAgentMemberIds(squad, roster)) {
      const existing = map.get(agentId);
      if (existing) existing.push(squad);
      else map.set(agentId, [squad]);
    }
  }
  return map;
}

/**
 * Squad filter is OR across selected ids, including `__none__` for
 * unassigned agents. An empty selection is inactive (the row passes).
 * Unknown membership must not be treated as "no squad".
 */
export function rowMatchesSquadFilter(
  squadIds: readonly string[],
  selected: readonly string[],
  membershipKnown = true,
): boolean {
  if (selected.length === 0) return true;
  const unassigned = squadIds.length === 0 && membershipKnown;
  for (const value of selected) {
    if (value === NO_SQUAD_ID) {
      if (unassigned) return true;
      continue;
    }
    if (squadIds.includes(value)) return true;
  }
  return false;
}

export function squadFilterOptionCounts(
  rows: readonly { squadIds: readonly string[] }[],
  squads: readonly Squad[],
  rosters?: ReadonlyMap<string, SquadRoster>,
): { bySquadId: Map<string, number | null>; noSquadCount: number | null } {
  const bySquadId = new Map<string, number | null>();
  let allKnown = true;
  for (const squad of squads) {
    if (!isActiveSquad(squad)) continue;
    const roster = rosterOf(squad, rosters);
    if (roster.kind !== "known") {
      bySquadId.set(squad.id, null);
      allKnown = false;
      continue;
    }
    let count = 0;
    for (const row of rows) {
      if (row.squadIds.includes(squad.id)) count += 1;
    }
    bySquadId.set(squad.id, count);
  }
  if (!allKnown) {
    return { bySquadId, noSquadCount: null };
  }
  let noSquadCount = 0;
  for (const row of rows) {
    if (row.squadIds.length === 0) noSquadCount += 1;
  }
  return { bySquadId, noSquadCount };
}

export interface AgentSquadGroup<
  T extends { agent: { id: string }; squadIds: readonly string[] },
> {
  id: string;
  squad: Squad | null;
  rows: T[];
}

/**
 * One group per active squad that still has a matching agent after filters,
 * then a trailing "no squad" group for unassigned agents. Multi-squad agents
 * appear in every matching group. Row order inside a group follows `rows`.
 * Squads whose roster is unknown are omitted — never rendered as empty/0.
 */
export function groupRowsBySquad<
  T extends { agent: { id: string }; squadIds: readonly string[] },
>(
  rows: readonly T[],
  squads: readonly Squad[],
  rosters?: ReadonlyMap<string, SquadRoster>,
): AgentSquadGroup<T>[] {
  const groups: AgentSquadGroup<T>[] = [];
  const active = squads
    .filter(isActiveSquad)
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name, "en"));

  let allKnown = true;
  for (const squad of active) {
    const roster = rosterOf(squad, rosters);
    if (roster.kind !== "known") {
      allKnown = false;
      continue;
    }
    const memberIds = new Set(squadAgentMemberIds(squad, roster));
    const memberRows = rows.filter((row) => memberIds.has(row.agent.id));
    if (memberRows.length === 0) continue;
    groups.push({ id: squad.id, squad, rows: memberRows });
  }

  if (allKnown) {
    const unassigned = rows.filter((row) => row.squadIds.length === 0);
    if (unassigned.length > 0) {
      groups.push({ id: NO_SQUAD_ID, squad: null, rows: [...unassigned] });
    }
  }
  return groups;
}

export type AgentListVirtualItem<
  T extends { agent: { id: string }; squadIds: readonly string[] },
> =
  | { kind: "header"; key: string; group: AgentSquadGroup<T> }
  | { kind: "row"; key: string; row: T; groupId: string };

export function flattenAgentListItems<
  T extends { agent: { id: string }; squadIds: readonly string[] },
>(
  rows: readonly T[],
  squads: readonly Squad[],
  grouping: "none" | "squad",
  rosters?: ReadonlyMap<string, SquadRoster>,
): AgentListVirtualItem<T>[] {
  if (grouping !== "squad") {
    return rows.map((row) => ({
      kind: "row" as const,
      key: row.agent.id,
      row,
      groupId: "",
    }));
  }
  const items: AgentListVirtualItem<T>[] = [];
  for (const group of groupRowsBySquad(rows, squads, rosters)) {
    items.push({ kind: "header", key: `header:${group.id}`, group });
    for (const row of group.rows) {
      items.push({
        kind: "row",
        key: `${group.id}:${row.agent.id}`,
        row,
        groupId: group.id,
      });
    }
  }
  return items;
}
