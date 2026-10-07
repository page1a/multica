import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { projectListOptions } from "../projects/queries";
import { agentSceneOf, type AgentScene } from "./domain-fit";

/**
 * The scene a pick is made in (DENE-1477): issue detail passes its project and
 * own domain, a new issue its chosen project, a chat its projects. Unknown or
 * empty project ids mean no scene (null) — pickers then keep their own order.
 */
export function useAgentScene(
  wsId: string,
  projectIds: readonly (string | null | undefined)[],
  issueDomainId?: string | null,
): AgentScene | null {
  const { data: projects } = useQuery(projectListOptions(wsId));
  const key = projectIds.filter(Boolean).join(",");
  return useMemo(() => {
    if (!projects || !key) return null;
    const ids = key.split(",");
    return agentSceneOf(
      projects.filter((p) => ids.includes(p.id)),
      issueDomainId,
    );
  }, [projects, key, issueDomainId]);
}
