import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { vcsKeys } from "../vcs/queries";
import { workspaceKeys } from "../workspace/queries";
import { projectResourceKeys } from "../projects/resource-queries";
import type { RepoConnectionCard } from "../types";

// Both live under the "vcs" prefix so a `vcs_connection` realtime event
// refreshes them together with the connection list.
export const repoReachKeys = {
  projectRepos: (wsId: string, projectId: string) =>
    [...vcsKeys.all(wsId), "project-repos", projectId] as const,
  connections: (wsId: string) => [...vcsKeys.all(wsId), "repo-connections"] as const,
};

export const projectReposOptions = (wsId: string, projectId: string) =>
  queryOptions({
    queryKey: repoReachKeys.projectRepos(wsId, projectId),
    queryFn: () => api.listProjectRepos(projectId),
    enabled: !!wsId && !!projectId,
    select: (data) => data.repos,
  });

export const repoConnectionsOptions = (wsId: string) =>
  queryOptions({
    queryKey: repoReachKeys.connections(wsId),
    queryFn: () => api.listRepoConnections(wsId),
    enabled: !!wsId,
    select: (data) => data.repos,
  });

/** Index by the server's normalized key, so a page never re-parses URLs to find a repository. */
export function connectionsByKey(
  cards: readonly RepoConnectionCard[],
): Map<string, RepoConnectionCard> {
  return new Map(cards.map((card) => [card.reach.key, card]));
}

function useRefreshRepoReach(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: repoReachKeys.projectRepos(wsId, projectId) }),
      qc.invalidateQueries({ queryKey: repoReachKeys.connections(wsId) }),
      qc.invalidateQueries({ queryKey: projectResourceKeys.list(wsId, projectId) }),
    ]);
}

export function useAttachProjectRepo(wsId: string, projectId: string) {
  const refresh = useRefreshRepoReach(wsId, projectId);
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (repoUrl: string) => api.attachProjectRepo(projectId, { repo_url: repoUrl }),
    onSettled: async (result) => {
      // A repository new to the workspace also changes the workspace list.
      if (result?.registered) {
        await qc.invalidateQueries({ queryKey: workspaceKeys.list() });
      }
      await refresh();
    },
  });
}

export function useRemoveProjectRepo(wsId: string, projectId: string) {
  const refresh = useRefreshRepoReach(wsId, projectId);
  return useMutation({
    mutationFn: (resourceId: string) => api.removeProjectRepo(projectId, resourceId),
    onSettled: refresh,
  });
}
