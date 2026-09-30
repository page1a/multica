"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import type { ChatProjectFilter } from "./project-bar";
import { openMemoryScope } from "./project-switch";

/**
 * The conversation a person last opened in each project (and in the
 * no-project view). Belongs to the person, not the workspace: one map on
 * `defaultStorage`, keyed by user id, same rule as the project-bar pins.
 */
interface ChatProjectOpenState {
  byUser: Record<string, Record<string, string>>;
  remember: (userId: string, projectIds: readonly string[], sessionId: string) => void;
  recall: (userId: string, filter: ChatProjectFilter) => string | null;
}

const NONE_SCOPE = "\0none";

export const useChatProjectOpenStore = create<ChatProjectOpenState>()(
  persist(
    (set, get) => ({
      byUser: {},
      remember: (userId, projectIds, sessionId) => {
        if (!userId || !sessionId) return;
        const scopes = projectIds.length > 0 ? projectIds : [NONE_SCOPE];
        set((state) => {
          const current = state.byUser[userId] ?? {};
          let changed = false;
          const next = { ...current };
          for (const scope of scopes) {
            if (next[scope] === sessionId) continue;
            next[scope] = sessionId;
            changed = true;
          }
          if (!changed) return state;
          return { byUser: { ...state.byUser, [userId]: next } };
        });
      },
      recall: (userId, filter) => {
        const scope = openMemoryScope(filter);
        if (!userId || !scope) return null;
        return get().byUser[userId]?.[scope] ?? null;
      },
    }),
    {
      name: "multica_chat_project_open",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ byUser: state.byUser }),
    },
  ),
);
