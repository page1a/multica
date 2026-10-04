import { describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";
import {
  clearPersistedQueryCache,
  createPersistedQueryCache,
  QUERY_CACHE_SCHEMA_VERSION,
  queryCacheStoragePrefix,
} from "./query-persistence";

function memoryStorage(): StorageAdapter & { data: Record<string, string> } {
  const data: Record<string, string> = {};
  return {
    data,
    getItem: (key) => data[key] ?? null,
    setItem: (key, value) => { data[key] = value; },
    removeItem: (key) => { delete data[key]; },
    keys: () => Object.keys(data),
  };
}

describe("persisted query cache", () => {
  it("hydrates only the account namespace and writes a schema version", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const first = new QueryClient();
    const stop = createPersistedQueryCache(first, storage, "user-a");
    first.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    first.setQueryData(["messages", "workspace-a"], [{ id: "secret" }]);
    vi.advanceTimersByTime(60);

    const key = `${queryCacheStoragePrefix()}user-a`;
    const envelope = JSON.parse(storage.data[key]!);
    expect(envelope.schemaVersion).toBe(QUERY_CACHE_SCHEMA_VERSION);
    expect(envelope.userId).toBe("user-a");
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([
      ["projects", "workspace-a"],
    ]);

    const second = new QueryClient();
    createPersistedQueryCache(second, storage, "user-a");
    expect(second.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
    expect(second.getQueryData(["messages", "workspace-a"])).toBeUndefined();
    stop();
    vi.useRealTimers();
  });

  it("removes one account or all persisted accounts", () => {
    const storage = memoryStorage();
    storage.setItem(`${queryCacheStoragePrefix()}a`, "a");
    storage.setItem(`${queryCacheStoragePrefix()}b`, "b");
    clearPersistedQueryCache(storage, "a");
    expect(storage.keys?.()).toEqual([`${queryCacheStoragePrefix()}b`]);
    clearPersistedQueryCache(storage);
    expect(storage.keys?.()).toEqual([]);
  });
});
