import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "./queries";

/** One row of the agent permissions table. 0 means unlimited. */
export interface AgentSpawnCell {
  enabled: boolean;
  per_chat?: number;
  per_run: number;
}

/** What an agent run may create, keyed by where it runs → what it creates. */
export interface AgentSpawnPolicy {
  chat_issue: AgentSpawnCell;
  issue_issue: AgentSpawnCell;
  chat_chat: AgentSpawnCell;
}

export type AgentSpawnPolicyPatch = {
  [K in keyof AgentSpawnPolicy]?: Partial<AgentSpawnCell>;
};

export function agentSpawnOptions(wsId: string) {
  return queryOptions({
    queryKey: [...workspaceKeys.all(wsId), "agent-spawn"] as const,
    queryFn: () => api.getAgentSpawn(wsId),
    enabled: !!wsId,
  });
}

export function useUpdateAgentSpawn(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (patch: AgentSpawnPolicyPatch) => api.updateAgentSpawn(wsId, patch),
    onSuccess: (policy) => client.setQueryData(agentSpawnOptions(wsId).queryKey, policy),
    onSettled: () => client.invalidateQueries({ queryKey: workspaceKeys.list() }),
  });
}
