import { QueryClient } from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";
import { createPersistedQueryCache } from "./query-persistence";

export function createQueryClient(options?: { storage?: StorageAdapter; userId?: string }): QueryClient {
  const client = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: Infinity,
        gcTime: 10 * 60 * 1000, // 10 minutes
        refetchOnWindowFocus: false,
        refetchOnReconnect: true,
        retry: 1,
      },
      mutations: {
        retry: false,
      },
    },
  });
  if (options?.storage && options.userId) createPersistedQueryCache(client, options.storage, options.userId);
  return client;
}
