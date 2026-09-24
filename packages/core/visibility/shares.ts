import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { ResourceShare } from "../types";

/**
 * Direct "specific people" shares for a single issue or repository. Projects
 * use their member list instead (see `projectMembersOptions`).
 */
export type ShareableResource =
  | { kind: "issue"; id: string }
  | { kind: "repo"; url: string };

function resourceKey(resource: ShareableResource) {
  return resource.kind === "issue" ? resource.id : resource.url;
}

export const resourceShareKeys = {
  all: (wsId: string) => ["resource-shares", wsId] as const,
  list: (wsId: string, resource: ShareableResource) =>
    [...resourceShareKeys.all(wsId), resource.kind, resourceKey(resource)] as const,
};

export function resourceSharesOptions(wsId: string, resource: ShareableResource) {
  return queryOptions({
    queryKey: resourceShareKeys.list(wsId, resource),
    queryFn: () =>
      resource.kind === "issue"
        ? api.listIssueShares(resource.id)
        : api.listRepoShares(resource.url),
    enabled: !!resourceKey(resource),
  });
}

export function useAddResourceShare(wsId: string, resource: ShareableResource) {
  const qc = useQueryClient();
  const key = resourceShareKeys.list(wsId, resource);
  return useMutation({
    mutationFn: (memberId: string) =>
      resource.kind === "issue"
        ? api.addIssueShare(resource.id, memberId)
        : api.addRepoShare(resource.url, memberId),
    onSuccess: (created) => {
      qc.setQueryData<ResourceShare[]>(key, (old) => {
        if (!old) return old;
        if (old.some((share) => share.member_id === created.member_id)) return old;
        return [...old, created];
      });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: key });
    },
  });
}

export function useRemoveResourceShare(wsId: string, resource: ShareableResource) {
  const qc = useQueryClient();
  const key = resourceShareKeys.list(wsId, resource);
  return useMutation({
    mutationFn: (memberId: string) =>
      resource.kind === "issue"
        ? api.removeIssueShare(resource.id, memberId)
        : api.removeRepoShare(resource.url, memberId),
    onSuccess: (_result, memberId) => {
      qc.setQueryData<ResourceShare[]>(key, (old) =>
        old?.filter((share) => share.member_id !== memberId),
      );
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: key });
    },
  });
}
