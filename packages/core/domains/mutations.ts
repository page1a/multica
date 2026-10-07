import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workspaceKeys } from "../workspace/queries";
import { projectKeys } from "../projects/queries";
import { domainKeys } from "./queries";

// Domain writes await the server: a rename also renames the specialisations
// still named base role + old domain, and a delete is refused while anything
// uses the domain, so neither result is predictable enough to patch ahead.

export function useCreateDomain(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.createDomain(name),
    onSettled: () => qc.invalidateQueries({ queryKey: domainKeys.all(wsId) }),
  });
}

export function useRenameDomain(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => api.renameDomain(id, name),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: domainKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    },
  });
}

export function useDeleteDomain(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteDomain(id),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: domainKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: projectKeys.all(wsId) });
    },
  });
}
