"use client";

import { useEffect, useRef, useState } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createQueryClient } from "./query-client";
import { createPersistedQueryCache, clearPersistedQueryCache } from "./query-persistence";
import { useAuthStore } from "./auth";
import { defaultStorage } from "./platform/storage";
import type { StorageAdapter } from "./types/storage";
import type { ReactNode } from "react";

export function QueryProvider({
  children,
  storage = defaultStorage,
}: {
  children: ReactNode;
  storage?: StorageAdapter;
}) {
  const [queryClient] = useState(createQueryClient);
  const userId = useAuthStore((state) => state.user?.id ?? null);
  const activeUserRef = useRef<string | null>(null);
  const stopPersistenceRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    const previousUser = activeUserRef.current;
    if (previousUser === userId) return;

    stopPersistenceRef.current?.();
    stopPersistenceRef.current = null;
    if (previousUser && previousUser !== userId) {
      // Never allow a query from account A to remain visible while account B
      // is being restored. Workspace IDs are not an account boundary.
      queryClient.clear();
      if (!userId) clearPersistedQueryCache(storage, previousUser);
    }
    activeUserRef.current = userId;
    if (userId) {
      stopPersistenceRef.current = createPersistedQueryCache(queryClient, storage, userId);
    }

    return () => {
      stopPersistenceRef.current?.();
      stopPersistenceRef.current = null;
      // React StrictMode replays mount effects. Clear the marker so the
      // replay reinstalls the subscription instead of returning early.
      if (activeUserRef.current === userId) activeUserRef.current = null;
    };
  }, [queryClient, storage, userId]);

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
