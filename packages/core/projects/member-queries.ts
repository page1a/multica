import { queryOptions, useMutation, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";
import { workspaceKeys } from "../workspace/queries";
import type { ProjectMember } from "../types";

export const projectMemberKeys = {
  list: (wsId: string, projectId: string) =>
    [...projectKeys.detail(wsId, projectId), "members"] as const,
};

// Project member rows carry the member's workspace role, which also decides
// their order, so a role change or removal on the roster makes every cached
// project member list stale.
export function invalidateProjectMemberLists(qc: QueryClient, wsId: string) {
  return qc.invalidateQueries({
    queryKey: projectKeys.all(wsId),
    predicate: (q) => q.queryKey.at(-1) === "members",
  });
}

export function projectMembersOptions(wsId: string, projectId: string) {
  return queryOptions({
    queryKey: projectMemberKeys.list(wsId, projectId),
    queryFn: () => api.listProjectMembers(projectId),
  });
}

export function useAddProjectMember(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (memberId: string) =>
      api.addProjectMember(projectId, { member_id: memberId }),
    onSuccess: (created) => {
      qc.setQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
        (old) => {
          if (!old) return old;
          if (old.some((m) => m.member_id === created.member_id)) return old;
          return [...old, created];
        },
      );
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
      // The roster lists each member's projects (DENE-1706).
      qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
    },
  });
}

export function useRemoveProjectMember(wsId: string, projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (memberId: string) => api.removeProjectMember(projectId, memberId),
    onMutate: async (memberId) => {
      await qc.cancelQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
      const prev = qc.getQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
      );
      qc.setQueryData<ProjectMember[]>(
        projectMemberKeys.list(wsId, projectId),
        (old) => old?.filter((m) => m.member_id !== memberId),
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev) {
        qc.setQueryData(projectMemberKeys.list(wsId, projectId), ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: projectMemberKeys.list(wsId, projectId),
      });
      // The roster lists each member's projects (DENE-1706).
      qc.invalidateQueries({ queryKey: workspaceKeys.members(wsId) });
    },
  });
}
