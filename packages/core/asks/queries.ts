import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const askKeys = {
  all: (wsId: string) => ["asks", wsId] as const,
  list: (wsId: string, status = "open", issueId?: string) => ["asks", wsId, status, issueId ?? null] as const,
  detail: (wsId: string, id: string) => ["asks", wsId, "detail", id] as const,
};

export function asksOptions(wsId: string, status = "open", issueId?: string) {
  return queryOptions({
    queryKey: askKeys.list(wsId, status, issueId),
    queryFn: () => api.listAsks(status, issueId).then((response) => response.asks),
    enabled: !!wsId,
    staleTime: 5_000,
  });
}

export function askOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: askKeys.detail(wsId, id),
    queryFn: () => api.getAsk(id),
    enabled: !!wsId && !!id,
  });
}
