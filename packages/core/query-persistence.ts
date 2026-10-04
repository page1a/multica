import {
  dehydrate,
  hydrate,
  type DehydratedState,
  type Query,
  type QueryClient,
} from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";

export const QUERY_CACHE_SCHEMA_VERSION = 1;
const STORAGE_PREFIX = "multica_query_cache:v1:";

/** Query families that are either sensitive, highly volatile, or contain transient work. */
const EXCLUDED_SEGMENTS = new Set([
  "messages",
  "timeline",
  "attachments",
  "presence",
  "unread",
  "pending",
  "activeTasks",
  "tasks",
  "search",
  "routing-health",
  "billing",
]);

export interface PersistedQueryCacheEnvelope {
  schemaVersion: number;
  userId: string;
  state: DehydratedState;
}

function keyForUser(userId: string): string {
  return `${STORAGE_PREFIX}${encodeURIComponent(userId)}`;
}

function isPersistableQuery(query: Query): boolean {
  const segments = query.queryKey.flatMap((part) =>
    typeof part === "string" ? [part] : [],
  );
  return segments.length > 0 && !segments.some((segment) => EXCLUDED_SEGMENTS.has(segment));
}

export function isPersistedQueryKey(queryKey: readonly unknown[]): boolean {
  return isPersistableQuery({ queryKey } as Query);
}

export function clearPersistedQueryCache(
  storage: StorageAdapter,
  userId?: string | null,
): void {
  if (userId) {
    storage.removeItem(keyForUser(userId));
    return;
  }
  for (const key of storage.keys?.() ?? []) {
    if (key.startsWith(STORAGE_PREFIX)) storage.removeItem(key);
  }
}

export function createPersistedQueryCache(
  queryClient: QueryClient,
  storage: StorageAdapter,
  userId: string,
): () => void {
  const key = keyForUser(userId);
  try {
    const raw = storage.getItem(key);
    if (raw) {
      const envelope = JSON.parse(raw) as Partial<PersistedQueryCacheEnvelope>;
      if (envelope.schemaVersion === QUERY_CACHE_SCHEMA_VERSION && envelope.userId === userId && envelope.state) {
        hydrate(queryClient, envelope.state);
      } else {
        storage.removeItem(key);
      }
    }
  } catch {
    storage.removeItem(key);
  }

  let writeTimer: ReturnType<typeof setTimeout> | undefined;
  const unsubscribe = queryClient.getQueryCache().subscribe(() => {
    if (writeTimer) clearTimeout(writeTimer);
    writeTimer = setTimeout(() => {
      try {
        const state = dehydrate(queryClient, {
          shouldDehydrateQuery: (query) => query.state.status === "success" && isPersistableQuery(query),
        });
        const envelope: PersistedQueryCacheEnvelope = {
          schemaVersion: QUERY_CACHE_SCHEMA_VERSION,
          userId,
          state,
        };
        storage.setItem(key, JSON.stringify(envelope));
      } catch {
        // Storage is best effort (private mode/quota must never break the app).
      }
    }, 50);
  });

  return () => {
    unsubscribe();
    if (writeTimer) clearTimeout(writeTimer);
  };
}

export function queryCacheStoragePrefix(): string {
  return STORAGE_PREFIX;
}
