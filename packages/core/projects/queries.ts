import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
  memory: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "memory", id] as const,
  memoryLocations: (wsId: string) =>
    [...projectKeys.all(wsId), "memory-locations"] as const,
};

export function projectListOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.list(wsId),
    queryFn: () => api.listProjects(),
    select: (data) => data.projects,
  });
}

export function projectDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.detail(wsId, id),
    queryFn: () => api.getProject(id),
  });
}

export function projectMemoryOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.memory(wsId, id),
    queryFn: () => api.getProjectMemory(id),
  });
}

export function projectMemoryLocationsOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.memoryLocations(wsId),
    queryFn: () => api.listProjectMemoryLocations(),
    staleTime: Infinity,
  });
}
