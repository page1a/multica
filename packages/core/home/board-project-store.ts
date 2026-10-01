"use client";

import { useQuery } from "@tanstack/react-query";
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { projectListOptions } from "../projects/queries";
import { defaultStorage } from "../platform/storage";
import type { Project } from "../types";

// The project the inbox board is narrowed to, per workspace (DENE-1019). Empty
// or missing means every project. Client-owned filter state: the board itself
// stays server data in the query cache, keyed by this id.
//
// Keyed by wsId inside one storage entry, like the done-seen mark, so it
// hydrates synchronously before the board's first read.

interface BoardProjectState {
  projectByWs: Record<string, string>;
  setProject: (wsId: string, projectId: string | null) => void;
}

export const useBoardProjectStore = create<BoardProjectState>()(
  persist(
    (set) => ({
      projectByWs: {},
      setProject: (wsId, projectId) =>
        set((s) => {
          const next = { ...s.projectByWs };
          if (projectId) next[wsId] = projectId;
          else delete next[wsId];
          return { projectByWs: next };
        }),
    }),
    {
      name: "multica_inbox_board_project",
      version: 1,
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ projectByWs: state.projectByWs }),
    },
  ),
);

const EMPTY_PROJECTS: Project[] = [];

export interface BoardProject {
  /** The project the board is narrowed to; null shows every project. */
  projectId: string | null;
  /** That project, once the project list has loaded. */
  project: Project | null;
  /** Every project the dropdown offers; empty until the list loads. */
  projects: Project[];
  setProjectId: (projectId: string | null) => void;
}

/**
 * The board's project filter for this workspace. A remembered project that no
 * longer exists (deleted, or no longer visible) counts as "all projects" once
 * the list has loaded, so a stale choice cannot leave the board empty.
 */
export function useBoardProject(wsId: string): BoardProject {
  const stored = useBoardProjectStore((s) => s.projectByWs[wsId] ?? null);
  const setProject = useBoardProjectStore((s) => s.setProject);
  const { data } = useQuery(projectListOptions(wsId));
  const projects = data ?? EMPTY_PROJECTS;
  const match = stored ? data?.find((p) => p.id === stored) : undefined;
  const gone = !!stored && !!data && !match;
  return {
    projectId: gone ? null : stored,
    project: match ?? null,
    projects,
    setProjectId: (projectId) => setProject(wsId, projectId),
  };
}
