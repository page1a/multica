import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const repoLinkKeys = {
  all: (wsId: string) => ["repo-links", wsId] as const,
  catalog: (wsId: string) => [...repoLinkKeys.all(wsId), "catalog"] as const,
};

export const repoLinksOptions = (wsId: string) =>
  queryOptions({
    queryKey: repoLinkKeys.catalog(wsId),
    queryFn: () => api.listRepoLinks(wsId),
    enabled: !!wsId,
  });
