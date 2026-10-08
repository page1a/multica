import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { WorkspaceLinkDirection } from "../types/workspace-link";
import { workspaceLinkKeys } from "./queries";

// Every link mutation awaits the server, then re-reads the workspace's links,
// audit and views: what the viewer may see changes on the next read.
function useLinkMutation<V, R>(wsId: string, fn: (vars: V) => Promise<R>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => qc.invalidateQueries({ queryKey: workspaceLinkKeys.all(wsId) }),
  });
}

export function useCreateWorkspaceLink(wsId: string) {
  return useLinkMutation(wsId, (body: { target_slug: string; project_ids: string[]; direction?: WorkspaceLinkDirection }) =>
    api.createWorkspaceLink(body),
  );
}

export function useUpdateWorkspaceLinkProjects(wsId: string) {
  return useLinkMutation(wsId, ({ linkId, projectIds }: { linkId: string; projectIds: string[] }) =>
    api.updateWorkspaceLink(linkId, { project_ids: projectIds }),
  );
}

export function useAcceptWorkspaceLink(wsId: string) {
  return useLinkMutation(wsId, (linkId: string) => api.updateWorkspaceLink(linkId, { accept: true }));
}

export function useRevokeWorkspaceLink(wsId: string) {
  return useLinkMutation(wsId, (linkId: string) => api.revokeWorkspaceLink(linkId));
}
