import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

// A linked view is never cached server-side or pushed; it is re-read on
// focus and on this interval so a revoke or an untick shows up promptly.
export const LINKED_VIEW_REFRESH_MS = 60_000;
const LINKED_VIEW_PAGE_SIZE = 50;

export const workspaceLinkKeys = {
  all: (wsId: string) => ["workspace-links", wsId] as const,
  list: (wsId: string) => [...workspaceLinkKeys.all(wsId), "list"] as const,
  audit: (wsId: string) => [...workspaceLinkKeys.all(wsId), "audit"] as const,
  lookup: (wsId: string, target: string) => [...workspaceLinkKeys.all(wsId), "lookup", target] as const,
  view: (wsId: string, linkId: string, projectId: string | null) =>
    [...workspaceLinkKeys.all(wsId), "view", linkId, projectId ?? "all"] as const,
};

export const workspaceLinksOptions = (wsId: string) =>
  queryOptions({
    queryKey: workspaceLinkKeys.list(wsId),
    queryFn: () => api.listWorkspaceLinks(),
    enabled: !!wsId,
  });

export const workspaceLinkAuditOptions = (wsId: string, enabled: boolean) =>
  queryOptions({
    queryKey: workspaceLinkKeys.audit(wsId),
    queryFn: () => api.listWorkspaceLinkAudit(),
    enabled: !!wsId && enabled,
  });

/** Which workspace an address names. A miss is an answer, not a retry. */
export const workspaceLinkLookupOptions = (wsId: string, target: string, enabled: boolean) =>
  queryOptions({
    queryKey: workspaceLinkKeys.lookup(wsId, target),
    queryFn: () => api.lookupWorkspaceLinkTarget(target),
    enabled: !!wsId && enabled && target !== "",
    retry: false,
    staleTime: 60_000,
  });

export const linkedViewOptions = (wsId: string, linkId: string, projectId: string | null) =>
  infiniteQueryOptions({
    queryKey: workspaceLinkKeys.view(wsId, linkId, projectId),
    queryFn: ({ pageParam }) =>
      api.getLinkedView(linkId, {
        projectId: projectId ?? undefined,
        cursor: pageParam || undefined,
        limit: LINKED_VIEW_PAGE_SIZE,
      }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: !!wsId && !!linkId,
    refetchOnWindowFocus: true,
    refetchInterval: LINKED_VIEW_REFRESH_MS,
    // A refused read is the same 404 every time; retrying cannot change it.
    retry: false,
  });
