import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { onInboxInvalidate, onInboxSummaryInvalidate } from "../inbox/ws-updaters";
import type {
  AgentAccessPass,
  AgentAccessRequest,
  ApproveAgentAccessRequestBody,
  ApproveAgentAccessRequestResponse,
  CreateAgentAccessPassRequest,
} from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * Agent borrowing (kun fork, DENE-808): doorbell requests and timed passes.
 *
 * Both are server data — a pass expires on the server's clock and a request
 * can be resolved by another owner session — so they live in TanStack Query
 * under workspace-scoped keys, never in a Zustand store.
 */
export const agentAccessKeys = {
  all: (wsId: string) => ["workspaces", wsId, "agent-access"] as const,
  requests: (wsId: string) => [...agentAccessKeys.all(wsId), "requests"] as const,
  passes: (wsId: string, agentId: string) =>
    [...agentAccessKeys.all(wsId), "passes", agentId] as const,
};

export function agentAccessRequestsOptions(wsId: string) {
  return queryOptions({
    queryKey: agentAccessKeys.requests(wsId),
    queryFn: () => api.listAgentAccessRequests(),
  });
}

export function agentAccessPassesOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: agentAccessKeys.passes(wsId, agentId),
    queryFn: () => api.listAgentAccessPasses(agentId),
  });
}

export function useAgentAccessRequests(enabled = true) {
  const wsId = useWorkspaceId();
  return useQuery({ ...agentAccessRequestsOptions(wsId), enabled });
}

export function useAgentAccessPasses(agentId: string, enabled = true) {
  const wsId = useWorkspaceId();
  return useQuery({ ...agentAccessPassesOptions(wsId, agentId), enabled: enabled && !!agentId });
}

/**
 * Resolving a request changes three projections at once: the request list,
 * the owner's inbox row (its `status` detail flips), and — when a pass was
 * issued — the agent's pass list plus which agents the requester can see.
 * Refresh them all after the server confirms rather than patching by hand.
 */
function refreshAfterAccessDecision(
  qc: ReturnType<typeof useQueryClient>,
  wsId: string,
  agentId: string | null,
) {
  void qc.invalidateQueries({ queryKey: agentAccessKeys.requests(wsId) });
  if (agentId) {
    void qc.invalidateQueries({ queryKey: agentAccessKeys.passes(wsId, agentId) });
  }
  void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
  void onInboxInvalidate(qc, wsId);
  void onInboxSummaryInvalidate(qc);
}

export function useApproveAgentAccessRequest() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<
    ApproveAgentAccessRequestResponse,
    Error,
    { id: string; body?: ApproveAgentAccessRequestBody }
  >({
    mutationFn: ({ id, body }) => api.approveAgentAccessRequest(id, body ?? {}),
    onSuccess: (res) => refreshAfterAccessDecision(qc, wsId, res.request.agent_id),
  });
}

export function useDeclineAgentAccessRequest() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<AgentAccessRequest, Error, string>({
    mutationFn: (id) => api.declineAgentAccessRequest(id),
    onSuccess: (req) => refreshAfterAccessDecision(qc, wsId, req.agent_id),
  });
}

export function useCreateAgentAccessPass(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<AgentAccessPass, Error, CreateAgentAccessPassRequest>({
    mutationFn: (body) => api.createAgentAccessPass(agentId, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentAccessKeys.passes(wsId, agentId) });
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

export function useRevokeAgentAccessPass(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<AgentAccessPass, Error, string>({
    mutationFn: (passId) => api.revokeAgentAccessPass(agentId, passId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: agentAccessKeys.passes(wsId, agentId) });
      void qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

/** Preset pass lengths offered at approval time and in the settings page. */
export type AgentAccessPassPreset = "2h" | "today" | "custom";

/**
 * Turn a preset into the request body the server accepts. `today` means the
 * end of the caller's local day; `custom` needs an explicit expiry.
 */
export function agentAccessPassExpiry(
  preset: AgentAccessPassPreset,
  customExpiresAt?: Date,
  now: Date = new Date(),
): { expires_at?: string; duration_minutes?: number } | null {
  switch (preset) {
    case "2h":
      return { duration_minutes: 120 };
    case "today": {
      const end = new Date(now);
      end.setHours(23, 59, 59, 999);
      if (end.getTime() - now.getTime() < 60_000) {
        // Less than a minute of "today" left — roll to end of tomorrow so the
        // grant is never dead on arrival.
        end.setDate(end.getDate() + 1);
      }
      return { expires_at: end.toISOString() };
    }
    case "custom":
      if (!customExpiresAt || Number.isNaN(customExpiresAt.getTime())) return null;
      if (customExpiresAt.getTime() <= now.getTime()) return null;
      return { expires_at: customExpiresAt.toISOString() };
  }
}
