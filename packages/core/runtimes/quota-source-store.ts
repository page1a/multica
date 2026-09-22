import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import {
  createWorkspaceAwareStorage,
  registerForWorkspaceRehydration,
} from "../platform/workspace-storage";
import { defaultStorage } from "../platform/storage";

const DEFAULTS = {
  selectedByProvider: {} as Record<string, string>,
};

interface ProviderQuotaSourceState {
  /** Provider slug → runtime id last chosen in the status bar. */
  selectedByProvider: Record<string, string>;
  setSelectedRuntime: (provider: string, runtimeId: string) => void;
}

export const useProviderQuotaSourceStore = create<ProviderQuotaSourceState>()(
  persist(
    (set) => ({
      selectedByProvider: DEFAULTS.selectedByProvider,
      setSelectedRuntime: (provider, runtimeId) => {
        const key = provider.trim().toLowerCase();
        if (!key || !runtimeId) return;
        set((state) => {
          if (state.selectedByProvider[key] === runtimeId) return state;
          return {
            selectedByProvider: {
              ...state.selectedByProvider,
              [key]: runtimeId,
            },
          };
        });
      },
    }),
    {
      name: "multica_provider_quota_source",
      storage: createJSONStorage(() =>
        createWorkspaceAwareStorage(defaultStorage),
      ),
      partialize: (state) => ({
        selectedByProvider: state.selectedByProvider,
      }),
      merge: (persisted, current) => {
        if (!persisted) return { ...current, ...DEFAULTS };
        const p = persisted as Partial<typeof DEFAULTS>;
        return {
          ...current,
          selectedByProvider: p.selectedByProvider ?? DEFAULTS.selectedByProvider,
        };
      },
    },
  ),
);

registerForWorkspaceRehydration(() =>
  useProviderQuotaSourceStore.persist.rehydrate(),
);
