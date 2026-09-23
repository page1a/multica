import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { projectKeys } from "./queries";

/**
 * The standing answer for "where does a new task on this project run, on this
 * machine?". `inputKey` is only a cache identity — the resource list the
 * answer was computed from — so a reorder or a mode change asks again instead
 * of showing the previous answer.
 */
export function projectCodeDecisionOptions(
  wsId: string,
  projectId: string,
  daemonId: string,
  inputKey: string,
) {
  return queryOptions({
    queryKey: [
      ...projectKeys.detail(wsId, projectId),
      "code-decision",
      daemonId,
      inputKey,
    ] as const,
    queryFn: () => api.previewProjectCodeDecision(projectId, daemonId),
  });
}
