"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";
import { CHAT_LIST_DEFAULT_WIDTH, CHAT_LIST_MIN_WIDTH } from "./list-width";

/**
 * How wide the person left the chat list, in px.
 *
 * This is the width they asked for, not the width on screen: a narrow window
 * clamps what is painted without overwriting the preference, so widening the
 * window brings the list back. It is a layout preference for this device
 * (like the other panel layouts), so it is not namespaced per workspace.
 *
 * It replaces the `multica_chat_layout` percentage layout, which capped the
 * list at 480px. The new key is what makes the 300px default reach people
 * who already had a saved layout.
 */
interface ChatListWidthState {
  width: number;
  setWidth: (width: number) => void;
}

export const useChatListWidthStore = create<ChatListWidthState>()(
  persist(
    (set) => ({
      width: CHAT_LIST_DEFAULT_WIDTH,
      setWidth: (width) => set({ width }),
    }),
    {
      name: "multica_chat_list_width",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ width: state.width }),
      merge: (persisted, current) => {
        const width = (persisted as { width?: unknown } | null)?.width;
        return {
          ...current,
          width:
            typeof width === "number" && Number.isFinite(width) && width >= CHAT_LIST_MIN_WIDTH
              ? width
              : CHAT_LIST_DEFAULT_WIDTH,
        };
      },
    },
  ),
);
