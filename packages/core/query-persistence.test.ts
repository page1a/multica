import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
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
    const invalidate = vi.spyOn(second, "invalidateQueries");
    createPersistedQueryCache(second, storage, "user-a");
    expect(second.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
    expect(second.getQueryData(["messages", "workspace-a"])).toBeUndefined();
    expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({
      refetchType: "active",
      predicate: expect.any(Function),
    }));
    stop();
    vi.useRealTimers();
  });

  it("never persists attachment bytes and drops them from an older snapshot", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["attachment-inline-blob", "att-1"], new Blob(["x"]));
    writer.setQueryData(["inline-bytes", "att-2"], new Blob(["y"]));
    writer.setQueryData(["projects", "workspace-a"], [{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(60);
    stop();

    const key = `${queryCacheStoragePrefix()}user-a`;
    const envelope = JSON.parse(storage.data[key]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([
      ["projects", "workspace-a"],
    ]);

    // A snapshot written before the fix holds the Blob as `{}`.
    envelope.state.queries.push({
      ...envelope.state.queries[0],
      queryKey: ["attachment-inline-blob", "att-1"],
      queryHash: JSON.stringify(["attachment-inline-blob", "att-1"]),
      state: { ...envelope.state.queries[0].state, data: {} },
    });
    storage.setItem(key, JSON.stringify(envelope));
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["attachment-inline-blob", "att-1"])).toBeUndefined();
    expect(reader.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "p1" }]);
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

  it("refetches a restored query when its page mounts", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stopWriter = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "workspace-a"], [{ id: "old" }]);
    await vi.advanceTimersByTimeAsync(60);
    stopWriter();

    const reader = new QueryClient();
    const stopReader = createPersistedQueryCache(reader, storage, "user-a");
    const queryFn = vi.fn().mockResolvedValue([{ id: "new" }]);
    const observer = new QueryObserver(reader, {
      queryKey: ["projects", "workspace-a"],
      queryFn,
    });
    const unsubscribe = observer.subscribe(() => undefined);
    await vi.waitFor(() => expect(queryFn).toHaveBeenCalledTimes(1));
    await vi.waitFor(() =>
      expect(reader.getQueryData(["projects", "workspace-a"])).toEqual([{ id: "new" }]),
    );
    unsubscribe();
    stopReader();
    vi.useRealTimers();
  });
});
