import type { StorageAdapter } from "../types/storage";

/** SSR-safe localStorage. Works in both Next.js (SSR) and Electron (always client). */
export const defaultStorage: StorageAdapter = {
  getItem: (k) =>
    typeof window !== "undefined" ? localStorage.getItem(k) : null,
  setItem: (k, v) => {
    if (typeof window !== "undefined") localStorage.setItem(k, v);
  },
  removeItem: (k) => {
    if (typeof window !== "undefined") localStorage.removeItem(k);
  },
  keys: () => {
    if (typeof window === "undefined") return [];
    return Object.keys(localStorage);
  },
};

/**
 * SSR-safe sessionStorage: survives a reload of the same browser tab —
 * including a phone browser discarding a background tab and reloading it when
 * the user comes back — but not a new tab or a restarted browser. The home for
 * "where was I on this screen" state (scroll offsets, which list view was
 * showing) that must outlive the tab's reload yet is not a preference worth
 * remembering forever.
 *
 * Every call is guarded: Safari's private mode and a full quota throw on
 * write, and losing a restore point must never break the screen.
 */
export const sessionStorageAdapter: StorageAdapter = {
  getItem: (k) => {
    if (typeof window === "undefined") return null;
    try {
      return sessionStorage.getItem(k);
    } catch {
      return null;
    }
  },
  setItem: (k, v) => {
    if (typeof window === "undefined") return;
    try {
      sessionStorage.setItem(k, v);
    } catch {
      // Quota or private mode: the restore point is best-effort.
    }
  },
  removeItem: (k) => {
    if (typeof window === "undefined") return;
    try {
      sessionStorage.removeItem(k);
    } catch {
      // Same best-effort contract as setItem.
    }
  },
  keys: () => {
    if (typeof window === "undefined") return [];
    try {
      return Object.keys(sessionStorage);
    } catch {
      return [];
    }
  },
};
