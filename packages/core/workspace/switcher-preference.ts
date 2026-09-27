"use client";

import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../platform/storage";

/**
 * One person's arrangement of the workspace switcher: which workspaces sit at
 * the top, and the order of everything. It belongs to the person, not to a
 * workspace, so it is keyed by user id and never by slug — opening the
 * switcher from any workspace shows the same order.
 *
 * It lives on this device only. Web and Desktop each keep their own copy.
 */
export interface WorkspaceSwitcherPreference {
  /** Every workspace id this person has arranged, in their order. */
  order: string[];
  /** Pinned workspace ids. Their relative order comes from `order`. */
  pinned: string[];
}

const EMPTY_PREFERENCE: WorkspaceSwitcherPreference = { order: [], pinned: [] };

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function sanitizePreference(value: unknown): WorkspaceSwitcherPreference | null {
  if (value === null || typeof value !== "object") return null;
  const { order, pinned } = value as Record<string, unknown>;
  if (!isStringArray(order) || !isStringArray(pinned)) return null;
  return { order, pinned };
}

function sanitizeByUser(value: unknown): Record<string, WorkspaceSwitcherPreference> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return {};
  const result: Record<string, WorkspaceSwitcherPreference> = {};
  for (const [userId, raw] of Object.entries(value)) {
    const pref = sanitizePreference(raw);
    if (pref) result[userId] = pref;
  }
  return result;
}

/**
 * Apply a preference to the server's workspace list. Pinned workspaces come
 * first, the rest follow; both keep the person's order. A workspace the
 * preference has never seen (newly joined) goes to the end in server order,
 * and ids for workspaces the person has left are ignored.
 */
export function arrangeWorkspaces<T extends { id: string }>(
  workspaces: readonly T[],
  preference: WorkspaceSwitcherPreference = EMPTY_PREFERENCE,
): { pinned: T[]; rest: T[] } {
  const byId = new Map(workspaces.map((ws) => [ws.id, ws]));
  const seen = new Set<string>();
  const ordered: T[] = [];
  for (const id of preference.order) {
    const ws = byId.get(id);
    if (ws && !seen.has(id)) {
      seen.add(id);
      ordered.push(ws);
    }
  }
  for (const ws of workspaces) {
    if (!seen.has(ws.id)) ordered.push(ws);
  }
  const pinnedIds = new Set(preference.pinned);
  return {
    pinned: ordered.filter((ws) => pinnedIds.has(ws.id)),
    rest: ordered.filter((ws) => !pinnedIds.has(ws.id)),
  };
}

interface WorkspaceSwitcherPreferenceState {
  byUser: Record<string, WorkspaceSwitcherPreference>;
  /**
   * Replace the whole arrangement. `pinned` and `rest` are the two groups as
   * the person now sees them; the stored order is pinned followed by rest.
   */
  setArrangement: (userId: string, pinned: readonly string[], rest: readonly string[]) => void;
  /** Pin or unpin one workspace without moving anything else. */
  togglePinned: (userId: string, workspaceIds: readonly string[], workspaceId: string) => void;
}

export const useWorkspaceSwitcherPreferenceStore = create<WorkspaceSwitcherPreferenceState>()(
  persist(
    (set) => ({
      byUser: {},
      setArrangement: (userId, pinned, rest) =>
        set((state) => ({
          byUser: {
            ...state.byUser,
            [userId]: { order: [...pinned, ...rest], pinned: [...pinned] },
          },
        })),
      togglePinned: (userId, workspaceIds, workspaceId) =>
        set((state) => {
          const current = state.byUser[userId] ?? EMPTY_PREFERENCE;
          // Record every current workspace in the order, pin state aside, so
          // a workspace the person never arranged keeps its place and an
          // unpinned one drops back where it was instead of to the end.
          const known = new Set(workspaceIds);
          const order = current.order.filter((id) => known.has(id));
          for (const id of workspaceIds) {
            if (!order.includes(id)) order.push(id);
          }
          const isPinned = current.pinned.includes(workspaceId);
          const nextPinned = isPinned
            ? current.pinned.filter((id) => id !== workspaceId)
            : [...current.pinned, workspaceId];
          return {
            byUser: { ...state.byUser, [userId]: { order, pinned: nextPinned } },
          };
        }),
    }),
    {
      name: "multica_workspace_switcher",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ byUser: state.byUser }),
      version: 1,
      merge: (persisted, current) => ({
        ...current,
        byUser: sanitizeByUser((persisted as { byUser?: unknown } | undefined)?.byUser),
      }),
    },
  ),
);

/** The signed-in person's preference, or the empty default. Stable reference. */
export function useWorkspaceSwitcherPreference(
  userId: string | null | undefined,
): WorkspaceSwitcherPreference {
  return useWorkspaceSwitcherPreferenceStore((state) =>
    userId ? state.byUser[userId] ?? EMPTY_PREFERENCE : EMPTY_PREFERENCE,
  );
}
