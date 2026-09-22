import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { Agent, ModuleVisibility, Squad, Workspace } from "../types";

export const workspaceKeys = {
  all: (wsId: string) => ["workspaces", wsId] as const,
  list: () => ["workspaces", "list"] as const,
  members: (wsId: string) => ["workspaces", wsId, "members"] as const,
  invitations: (wsId: string) => ["workspaces", wsId, "invitations"] as const,
  shareLinks: (wsId: string) => ["workspaces", wsId, "share-links"] as const,
  myInvitations: () => ["invitations", "mine"] as const,
  agents: (wsId: string) => ["workspaces", wsId, "agents"] as const,
  agent: (wsId: string, agentId: string) =>
    ["workspaces", wsId, "agents", "detail", agentId] as const,
  squads: (wsId: string) => ["workspaces", wsId, "squads"] as const,
  // Full roster for one squad. Same key the squad detail page already
  // uses, so list-filter fetches and the Members tab share a cache.
  // Lives under the workspace key tree so a broad
  // `["workspaces", wsId, "squads"]` invalidation covers it.
  squadMembers: (wsId: string, squadId: string) =>
    ["workspaces", wsId, "squads", squadId, "members"] as const,
  // Per-squad member status. Lives under the workspace key tree so
  // workspace switches naturally drop the cache, and so a broad
  // `["workspaces", wsId, "squads"]` invalidation covers it.
  squadMemberStatus: (wsId: string, squadId: string) =>
    ["workspaces", wsId, "squads", squadId, "members-status"] as const,
  skills: (wsId: string) => ["workspaces", wsId, "skills"] as const,
  assigneeFrequency: (wsId: string) => ["workspaces", wsId, "assignee-frequency"] as const,
  mcpServers: (wsId: string) => ["workspaces", wsId, "mcp-servers"] as const,
  routingHealth: (wsId: string) => ["workspaces", wsId, "routing-health"] as const,
  modules: (wsId: string) => ["workspaces", wsId, "modules"] as const,
};

export function workspaceListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.list(),
    queryFn: () => api.listWorkspaces(),
  });
}

/** Resolves the workspace whose slug matches, from the cached workspace list. */
export function workspaceBySlugOptions(slug: string) {
  return queryOptions({
    ...workspaceListOptions(),
    select: (list: Workspace[]) => list.find((w) => w.slug === slug) ?? null,
  });
}

/**
 * Whether routing is actually working for this workspace right now.
 *
 * The routing design keeps failures off tickets, so the settings section is
 * the only place a person can learn that the routing model is rejected or
 * cooling down (DENE-633). The read is cheap — the server reports breaker
 * state and never dials the model — but it is still refetched on an interval
 * rather than continuously, because a cooldown ends on a clock and the chip
 * should stop lying without the person reloading.
 */
export function moduleVisibilityOptions(wsId: string) {
  return queryOptions<ModuleVisibility[]>({
    queryKey: workspaceKeys.modules(wsId),
    queryFn: () => api.listModuleVisibility(),
    enabled: !!wsId,
  });
}

export function routingHealthOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.routingHealth(wsId),
    queryFn: () => api.getRoutingHealth(wsId),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

export function memberListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.members(wsId),
    queryFn: () => api.listMembers(wsId),
  });
}

export function agentListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agents(wsId),
    queryFn: () =>
      api.listAgents({ workspace_id: wsId, include_archived: true }),
    // Projected unstable can age offline without an event; polling online also
    // covers a missed demotion. Offline recovery is event/reconnect-driven.
    refetchInterval: (query) =>
      query.state.data?.some(
        (agent) =>
          !agent.archived_at &&
          (agent.runtime_availability === "online" ||
            agent.runtime_availability === "unstable"),
      )
        ? 30_000
        : false,
  });
}

export function agentDetailOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: workspaceKeys.agent(wsId, agentId),
    queryFn: () => api.getAgent(agentId),
    enabled: !!wsId && !!agentId,
    retry: false,
  });
}

/**
 * Makes an authoritative agent response immediately visible to both agent
 * detail and list consumers. A cold list stays cold so one agent is never
 * mistaken for the complete workspace list.
 */
export function cacheAgentResponse(
  queryClient: QueryClient,
  wsId: string,
  agent: Agent,
  options: { insertIntoList?: boolean } = {},
) {
  queryClient.setQueryData(workspaceKeys.agent(wsId, agent.id), agent);
  queryClient.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (current) => {
    if (!current) return current;
    const existingIndex = current.findIndex((item) => item.id === agent.id);
    if (existingIndex === -1) {
      return options.insertIntoList === false ? current : [...current, agent];
    }
    return current.map((item, index) =>
      index === existingIndex ? agent : item,
    );
  });
}

export function squadListOptions(wsId: string) {
  return queryOptions<Squad[]>({
    queryKey: workspaceKeys.squads(wsId),
    queryFn: () => api.listSquads(),
    enabled: !!wsId,
  });
}

export function squadMembersOptions(wsId: string, squadId: string) {
  return queryOptions({
    queryKey: workspaceKeys.squadMembers(wsId, squadId),
    queryFn: () => api.listSquadMembers(squadId),
    enabled: !!wsId && !!squadId,
  });
}

// Per-squad members status snapshot. The freshness signal is the WS task /
// agent / runtime invalidation wired in use-realtime-sync (which broadly
// invalidates `["workspaces", wsId, "squads"]`); the staleTime is a
// tab-focus safety net.
export function squadMemberStatusOptions(wsId: string, squadId: string) {
  return queryOptions({
    queryKey: workspaceKeys.squadMemberStatus(wsId, squadId),
    queryFn: () => api.getSquadMemberStatus(squadId),
    enabled: !!wsId && !!squadId,
    staleTime: 30 * 1000,
    refetchOnWindowFocus: true,
  });
}

export function skillListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.skills(wsId),
    queryFn: () => api.listSkills(),
  });
}

export function skillDetailOptions(wsId: string, skillId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.skills(wsId), skillId] as const,
    queryFn: () => api.getSkill(skillId),
    enabled: !!skillId,
  });
}

/**
 * Builds a `Map<skillId, Agent[]>` from the cached agent list. The server
 * already returns each agent with its full skill list inline, so no extra
 * request is needed — "which agents use skill X" is pure client-side fold.
 *
 * Exposed as a plain helper rather than a `queryOptions` with `select` so
 * the Map's identity is stable across unrelated agent-cache rerenders —
 * callers wrap this in `useMemo(..., [agents])` and only re-fold when the
 * agent array identity actually changes. Previously this was `{ select }`,
 * which returned a new Map every subscription tick and triggered cascading
 * re-renders on every `agent:updated` WS event.
 */
export function selectSkillAssignments(
  agents: Agent[] | undefined,
): Map<string, Agent[]> {
  const map = new Map<string, Agent[]>();
  if (!agents) return map;
  for (const a of agents) {
    if (a.archived_at) continue;
    for (const s of a.skills ?? []) {
      const existing = map.get(s.id);
      if (existing) existing.push(a);
      else map.set(s.id, [a]);
    }
  }
  return map;
}

export function invitationListOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.invitations(wsId),
    queryFn: () => api.listWorkspaceInvitations(wsId),
  });
}

export function shareLinkListOptions(wsId: string, enabled = true) {
  return queryOptions({
    queryKey: workspaceKeys.shareLinks(wsId),
    queryFn: () => api.listShareLinks(wsId),
    enabled: enabled && !!wsId,
  });
}

export function myInvitationListOptions() {
  return queryOptions({
    queryKey: workspaceKeys.myInvitations(),
    queryFn: () => api.listMyInvitations(),
  });
}

export function assigneeFrequencyOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.assigneeFrequency(wsId),
    queryFn: () => api.getAssigneeFrequency(),
  });
}

/**
 * The workspace's MCP server library. Fetched for plain members too — the
 * payload is names and transports only, and an agent owner needs to see what
 * is available to assign.
 */
export function workspaceMcpServersOptions(wsId: string) {
  return queryOptions({
    queryKey: workspaceKeys.mcpServers(wsId),
    queryFn: () => api.listWorkspaceMcpServers(wsId),
    enabled: wsId !== "",
  });
}

/** The workspace MCP servers assigned to one agent, with their toggles. */
export function agentMcpServersOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agents", agentId, "mcp-servers"] as const,
    queryFn: () => api.listAgentMcpServers(agentId),
    enabled: agentId !== "",
  });
}
