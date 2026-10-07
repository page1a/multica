/**
 * The domain scene an agent picker sorts by (DENE-1477): the issue's own
 * domain, else its project's domains, else generic. Rule and sort live in
 * `@multica/core/agents/domain-fit`, shared with web and the server's case
 * table; this hook only reads the issue / project from mobile's caches.
 *
 * Pass `issueId` for an existing issue (its project is read off it), or
 * `projectId` for a project or a new-issue draft. Neither → generic, which
 * puts base roles first (chat has no project on mobile).
 */
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { domainScene } from "@multica/core/agents/domain-fit";
import { issueDetailOptions } from "@/data/queries/issues";
import { projectDetailOptions } from "@/data/queries/projects";
import { useWorkspaceStore } from "@/data/workspace-store";

export function useDomainScene({
  issueId,
  projectId,
}: {
  issueId?: string | null;
  projectId?: string | null;
}): string[] {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const { data: issue } = useQuery({
    ...issueDetailOptions(wsId, issueId ?? ""),
    enabled: !!wsId && !!issueId,
  });
  const resolvedProjectId = projectId ?? issue?.project_id ?? "";
  const { data: project } = useQuery({
    ...projectDetailOptions(wsId, resolvedProjectId),
    enabled: !!wsId && !!resolvedProjectId && !issue?.domain_id,
  });
  const issueDomainId = issue?.domain_id;
  const projectDomainIds = project?.domain_ids;
  return useMemo(
    () => domainScene({ issueDomainId, projectDomainIds }),
    [issueDomainId, projectDomainIds],
  );
}
