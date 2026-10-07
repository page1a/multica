import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const domainKeys = {
  all: (wsId: string) => ["domains", wsId] as const,
  list: (wsId: string) => [...domainKeys.all(wsId), "list"] as const,
};

/** The workspace domain list (DENE-1451), in workspace order. */
export function domainListOptions(wsId: string) {
  return queryOptions({
    queryKey: domainKeys.list(wsId),
    queryFn: () => api.listDomains(),
    select: (data) => data.domains,
    enabled: Boolean(wsId),
  });
}
