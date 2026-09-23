import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";
import { projectKeys } from "../projects/queries";
import { workspaceKeys } from "../workspace/queries";
import type { Issue, ListIssuesCache, Project, Workspace } from "../types";

export type VisibilityScope = "private" | "project" | "workspace";

export interface VisibilityResult {
  id?: string;
  project_id?: string;
  url?: string;
  visibility: VisibilityScope;
  audience_size?: number;
  affected_count?: number;
  previously_private_count?: number;
}

export interface ProjectVisibilityPreview {
  project_id: string;
  visibility: VisibilityScope;
  affected_count: number;
  previously_private_count: number;
}

export const visibilityKeys = {
  projectPreview: (projectId: string) => ["visibility", "projects", projectId, "preview"] as const,
};

export function projectVisibilityPreviewOptions(projectId: string, enabled = true) {
  return {
    queryKey: visibilityKeys.projectPreview(projectId),
    queryFn: () => api.previewProjectVisibility(projectId),
    enabled: enabled && !!projectId,
    staleTime: 15_000,
  };
}

export function useSetIssueVisibility(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ issueId, visibility }: { issueId: string; visibility: VisibilityScope }) =>
      api.setIssueVisibility(issueId, visibility),
    onSuccess: (result, vars) => {
      qc.setQueryData<Issue>(issueKeys.detail(wsId, vars.issueId), (old) =>
        old ? { ...old, visibility: result.visibility } : old,
      );
      qc.setQueriesData<ListIssuesCache>({ queryKey: issueKeys.list(wsId) }, (old) => {
        if (!old) return old;
        const byStatus = { ...old.byStatus };
        for (const [status, bucket] of Object.entries(byStatus)) {
          if (!bucket) continue;
          byStatus[status as keyof typeof byStatus] = {
            ...bucket,
            issues: bucket.issues.map((issue) =>
              issue.id === vars.issueId ? { ...issue, visibility: result.visibility } : issue,
            ),
          };
        }
        return { ...old, byStatus };
      });
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
    },
  });
}

export function useSetRepoVisibility(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ url, visibility }: { url: string; visibility: VisibilityScope }) =>
      api.setRepoVisibility(url, visibility),
    onSuccess: (result) => {
      qc.setQueryData<Workspace[]>(workspaceKeys.list(), (old) =>
        old?.map((workspace) => workspace.id !== wsId ? workspace : {
          ...workspace,
          repos: workspace.repos.map((repo) =>
            repo.url === result.url ? { ...repo, visibility: result.visibility } : repo,
          ),
        }),
      );
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

export function useSetProjectVisibility(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ projectId, visibility }: { projectId: string; visibility: VisibilityScope }) =>
      api.setProjectVisibility(projectId, visibility),
    onSuccess: (result, vars) => {
      qc.setQueryData<Project>(projectKeys.detail(wsId, vars.projectId), (old) =>
        old ? { ...old, visibility: result.visibility } : old,
      );
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: issueKeys.all(wsId) });
      qc.invalidateQueries({ queryKey: workspaceKeys.list() });
    },
  });
}

export function useProjectVisibilityPreview(projectId: string, enabled: boolean) {
  return useQuery(projectVisibilityPreviewOptions(projectId, enabled));
}
