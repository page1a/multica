/**
 * When the person last opened a chat's progress sheet (DENE-1667), per chat:
 * moves after it are marked 「刚变」. In-memory only, like
 * `last-viewed-store.ts` (no AsyncStorage on mobile); after a cold start a
 * move within the last day counts as fresh.
 */
import { create } from "zustand";

export const FIRST_LOOK_WINDOW_MS = 24 * 60 * 60 * 1000;

interface ChatProgressSeenState {
  seen: Record<string, number>;
  markSeen: (sessionId: string) => void;
}

export const useChatProgressSeenStore = create<ChatProgressSeenState>((set, get) => ({
  seen: {},
  markSeen: (sessionId) => set({ seen: { ...get().seen, [sessionId]: Date.now() } }),
}));

export function useChatProgressSeenAt(sessionId: string | null): number {
  const at = useChatProgressSeenStore((s) => (sessionId ? s.seen[sessionId] : undefined));
  // Stable for the render: the fallback only moves when the store does.
  return at ?? startOfFirstLook;
}

const startOfFirstLook = Date.now() - FIRST_LOOK_WINDOW_MS;
