import { queryOptions } from "@tanstack/react-query";
import { agentListRefetchInterval } from "@multica/core/agents/list-freshness";
import { api } from "@/data/api";

export const agentListOptions = (wsId: string | null) =>
  queryOptions({
    queryKey: ["agents", wsId] as const,
    queryFn: ({ signal }) => api.listAgents({ signal }),
    enabled: !!wsId,
    // Same cadence as Web/Desktop: unstable ages offline without an event.
    refetchInterval: (query) => agentListRefetchInterval(query.state.data),
  });
