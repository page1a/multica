import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type {
  Agent,
  BulkUpdateAgentRoutingRequest,
  BulkUpdateAgentRoutingResponse,
} from "../types";
import { workspaceKeys } from "../workspace/queries";

/**
 * The routing seats table's write (DENE-922): tier and/or usage on one or
 * more seats, all or nothing on the server. Not optimistic — a batch that the
 * server refuses as a whole must not flash a half-applied table — so the
 * cache is patched from the returned rows and then reconverged.
 */
export function useBulkUpdateAgentRouting(wsId: string) {
  const qc = useQueryClient();

  return useMutation<
    BulkUpdateAgentRoutingResponse,
    Error,
    BulkUpdateAgentRoutingRequest
  >({
    mutationFn: (data) => api.bulkUpdateAgentRouting(data),
    onSuccess: ({ agents }) => {
      const byId = new Map(agents.map((a) => [a.id, a]));
      qc.setQueryData<Agent[]>(workspaceKeys.agents(wsId), (old) =>
        old?.map((a) => {
          const next = byId.get(a.id);
          return next
            ? { ...a, routing_tier: next.routing_tier, routing_usage: next.routing_usage }
            : a;
        }),
      );
    },
    onSettled: (_data, _error, variables) => {
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
      for (const id of variables.agent_ids) {
        qc.invalidateQueries({ queryKey: workspaceKeys.agent(wsId, id) });
      }
    },
  });
}
