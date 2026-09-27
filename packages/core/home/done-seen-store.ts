"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

// When the viewer last looked at the inbox's "done today" lane, per workspace.
// Finished issues show once; after a visit they fold away (DENE-882).
//
// Keyed by wsId inside one storage entry rather than through the
// workspace-aware storage: that storage rehydrates after the page has
// mounted, so the page would read an empty mark and then overwrite the real
// one. One entry hydrates synchronously on load, before anything renders.

interface DoneSeenState {
  seenAt: Record<string, string>;
  markSeen: (wsId: string, at: string) => void;
}

export const useDoneSeenStore = create<DoneSeenState>()(
  persist(
    (set) => ({
      seenAt: {},
      markSeen: (wsId, at) => set((s) => ({ seenAt: { ...s.seenAt, [wsId]: at } })),
    }),
    {
      name: "multica_inbox_done_seen",
      version: 2,
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ seenAt: state.seenAt }),
      // v1 kept one bare timestamp per workspace-scoped key; start over.
      migrate: () => ({ seenAt: {} }),
    },
  ),
);
