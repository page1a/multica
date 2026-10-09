import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const projectKeys = {
  all: (wsId: string) => ["projects", wsId] as const,
  list: (wsId: string) => [...projectKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "detail", id] as const,
  memory: (wsId: string, id: string) =>
    [...projectKeys.all(wsId), "memory", id] as const,
  memoryMonitor: (wsId: string, id: string) =>
    [...projectKeys.memory(wsId, id), "monitor"] as const,
  memoryLocations: (wsId: string) =>
    [...projectKeys.all(wsId), "memory-locations"] as const,
  reports: (wsId: string) => [...projectKeys.all(wsId), "report"] as const,
  report: (wsId: string, id: string) =>
    [...projectKeys.reports(wsId), id] as const,
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

/** Writes, unsettled closes, idle rounds and chat receipts over the default window. */
export function projectMemoryMonitorOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.memoryMonitor(wsId, id),
    queryFn: () => api.getProjectMemoryMonitor(id),
  });
}

export function projectMemoryLocationsOptions(wsId: string) {
  return queryOptions({
    queryKey: projectKeys.memoryLocations(wsId),
    queryFn: () => api.listProjectMemoryLocations(),
    staleTime: Infinity,
  });
}

/** "听汇报" preview: what moved in the project since the person last heard it. */
export function projectReportOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: projectKeys.report(wsId, id),
    queryFn: () => api.getProjectReport(id),
  });
}
