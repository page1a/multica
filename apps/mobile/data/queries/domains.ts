/**
 * Workspace domain list (DENE-1451). Projects carry several domains, an issue
 * picks one of its project's; both pickers read this list.
 */
import { queryOptions } from "@tanstack/react-query";
import { api } from "@/data/api";

export const domainKeys = {
  all: (wsId: string | null) => ["domains", wsId] as const,
};

export const domainListOptions = (wsId: string | null) =>
  queryOptions({
    queryKey: domainKeys.all(wsId),
    queryFn: async ({ signal }) => {
      const res = await api.listDomains({ signal });
      return res.domains;
    },
    enabled: !!wsId,
  });
