"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

const EMPTY_IDS: string[] = [];

/**
 * Legacy local project-bar pins kept by older builds.
 *
 * The current project bar reads the server-backed sidebar pin list. This
 * store remains only long enough to migrate pins written by older builds;
 * it is keyed by user id so another signed-in person on the same device is
 * never migrated from this user's legacy data.
 */
interface ChatProjectBarState {
  byUser: Record<string, string[]>;
  /** Remove legacy local pins after they have been copied to the server. */
  remove: (userId: string, projectIds: readonly string[]) => void;
}

export const useChatProjectBarStore = create<ChatProjectBarState>()(
  persist(
    (set) => ({
      byUser: {},
      remove: (userId, projectIds) =>
        set((state) => {
          const current = state.byUser[userId];
          if (!current || projectIds.length === 0) return state;
          const remove = new Set(projectIds);
          const next = current.filter((id) => !remove.has(id));
          if (next.length === current.length) return state;
          if (next.length === 0) {
            const { [userId]: _, ...rest } = state.byUser;
            return { byUser: rest };
          }
          return { byUser: { ...state.byUser, [userId]: next } };
        }),
    }),
    {
      name: "multica_chat_project_bar",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ byUser: state.byUser }),
    },
  ),
);

export function selectPinnedProjectIds(userId: string | null) {
  return (state: ChatProjectBarState): string[] =>
    userId ? (state.byUser[userId] ?? EMPTY_IDS) : EMPTY_IDS;
}
