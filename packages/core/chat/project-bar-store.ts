"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

const EMPTY_IDS: string[] = [];

/**
 * Which projects a person keeps at the front of the chat-list project bar,
 * and in what order.
 *
 * This belongs to the person, not the workspace: two people in the same
 * workspace pin independently, and the same person's order is one list
 * (project ids are unique, so each workspace just renders the ids it owns).
 * It lives on `defaultStorage` keyed by user id — never in a workspace draft.
 * The casual-chat dismissal is a different fact and is stored on the chat
 * itself, on the server.
 */
interface ChatProjectBarState {
  byUser: Record<string, string[]>;
  pin: (userId: string, projectId: string) => void;
  unpin: (userId: string, projectId: string) => void;
  /** Move `fromId` to the slot currently occupied by `toId`. Both must be pinned. */
  move: (userId: string, fromId: string, toId: string) => void;
}

export const useChatProjectBarStore = create<ChatProjectBarState>()(
  persist(
    (set) => ({
      byUser: {},
      pin: (userId, projectId) =>
        set((state) => {
          if (!userId || !projectId) return state;
          const current = state.byUser[userId] ?? EMPTY_IDS;
          if (current.includes(projectId)) return state;
          return { byUser: { ...state.byUser, [userId]: [...current, projectId] } };
        }),
      unpin: (userId, projectId) =>
        set((state) => {
          const current = state.byUser[userId];
          if (!current?.includes(projectId)) return state;
          const next = current.filter((id) => id !== projectId);
          if (next.length === 0) {
            const { [userId]: _, ...rest } = state.byUser;
            return { byUser: rest };
          }
          return { byUser: { ...state.byUser, [userId]: next } };
        }),
      move: (userId, fromId, toId) =>
        set((state) => {
          const current = state.byUser[userId];
          if (!current || fromId === toId) return state;
          const from = current.indexOf(fromId);
          const to = current.indexOf(toId);
          if (from < 0 || to < 0) return state;
          const next = current.slice();
          next.splice(from, 1);
          next.splice(to, 0, fromId);
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
