import type { Agent, AgentStatus } from "../types/agent";

const AGENT_STATUSES: ReadonlySet<string> = new Set<AgentStatus>([
  "idle",
  "working",
  "blocked",
  "error",
  "offline",
]);

export interface AgentStatusChange {
  agentId: string;
  status: AgentStatus;
}

/**
 * Reads an `agent:status` payload that announces ONLY a status flip.
 *
 * The server marks such events with top-level `agent_id` + `status` (task
 * start/finish and the runtime sweeper). Every other `agent:status` — a config
 * edit, a skill change, a runtime unbind — omits that pair and returns null, so
 * the caller still refetches. Patching those would miss per-viewer fields the
 * broadcast does not carry. (DENE-1504)
 */
export function readAgentStatusChange(payload: unknown): AgentStatusChange | null {
  if (!payload || typeof payload !== "object") return null;
  const { agent_id: agentId, status } = payload as Record<string, unknown>;
  if (typeof agentId !== "string" || !agentId) return null;
  if (typeof status !== "string" || !AGENT_STATUSES.has(status)) return null;
  return { agentId, status: status as AgentStatus };
}

/**
 * Applies a status flip to one row of a cached agent list. Returns null when
 * the list is cold or the row is missing — the caller must refetch, since the
 * event alone cannot build a full row.
 */
export function patchAgentListStatus(
  list: Agent[] | undefined,
  change: AgentStatusChange,
): Agent[] | null {
  if (!list) return null;
  const index = list.findIndex((agent) => agent.id === change.agentId);
  if (index === -1) return null;
  if (list[index]!.status === change.status) return list;
  return list.map((agent, i) => (i === index ? { ...agent, status: change.status } : agent));
}

const UNSTABLE_POLL_MS = 30_000;
const ONLINE_POLL_MS = 5 * 60_000;

/**
 * Poll interval for the workspace agent list. Only "unstable" ages into
 * "offline" by the clock alone, so it keeps the 30s poll; "online" demotions
 * arrive as daemon events (and reconnect refetches), so its poll is just a
 * slow safety net. Shared by Web/Desktop and mobile. (DENE-1504)
 */
export function agentListRefetchInterval(agents: Agent[] | undefined): number | false {
  let online = false;
  for (const agent of agents ?? []) {
    if (agent.archived_at) continue;
    if (agent.runtime_availability === "unstable") return UNSTABLE_POLL_MS;
    if (agent.runtime_availability === "online") online = true;
  }
  return online ? ONLINE_POLL_MS : false;
}
