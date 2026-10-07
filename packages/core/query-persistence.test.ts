import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryObserver } from "@tanstack/react-query";
import type { StorageAdapter } from "./types/storage";
import {
  clearPersistedQueryCache,
  createPersistedQueryCache,
  QUERY_CACHE_MAX_CHARS,
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

  it("skips data JSON cannot round-trip, so a restored Map never comes back as {}", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["issues", "ws", "children-by-parents", ["p1"]], new Map([["p1", [{ id: "c1" }]]]));
    writer.setQueryData(["inbox", "ws", "ids"], new Set(["a"]));
    writer.setQueryData(["issues", "ws", "nested"], { at: new Date(0) });
    writer.setQueryData(["projects", "ws"], [{ id: "p1", meta: { tags: ["x"], none: null } }]);
    await vi.advanceTimersByTimeAsync(60);
    stop();

    const envelope = JSON.parse(storage.data[`${queryCacheStoragePrefix()}user-a`]!);
    expect(envelope.state.queries.map((q: { queryKey: unknown[] }) => q.queryKey)).toEqual([["projects", "ws"]]);
    vi.useRealTimers();
  });

  it("drops a snapshot written by an older schema", () => {
    const storage = memoryStorage();
    const key = `${queryCacheStoragePrefix()}user-a`;
    storage.setItem(key, JSON.stringify({
      schemaVersion: QUERY_CACHE_SCHEMA_VERSION - 1,
      userId: "user-a",
      state: {
        mutations: [],
        queries: [{
          queryKey: ["issues", "ws", "children-by-parents", ["p1"]],
          queryHash: JSON.stringify(["issues", "ws", "children-by-parents", ["p1"]]),
          state: { data: {}, status: "success", dataUpdatedAt: 1 },
        }],
      },
    }));
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["issues", "ws", "children-by-parents", ["p1"]])).toBeUndefined();
    expect(storage.data[key]).toBeUndefined();
  });

  it("keeps the most recently updated queries within the size budget", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    const big = "x".repeat(Math.floor(QUERY_CACHE_MAX_CHARS / 2));
    writer.setQueryData(["projects", "old"], big, { updatedAt: 1 });
    writer.setQueryData(["projects", "mid"], big, { updatedAt: 2 });
    writer.setQueryData(["projects", "new"], big, { updatedAt: 3 });
    await vi.advanceTimersByTimeAsync(60);
    stop();

    const raw = storage.data[`${queryCacheStoragePrefix()}user-a`]!;
    expect(raw.length).toBeLessThanOrEqual(QUERY_CACHE_MAX_CHARS);
    const reader = new QueryClient();
    createPersistedQueryCache(reader, storage, "user-a");
    expect(reader.getQueryData(["projects", "new"])).toBe(big);
    expect(reader.getQueryData(["projects", "old"])).toBeUndefined();
    vi.useRealTimers();
  });

  it("frees the old snapshot when the write fails", async () => {
    vi.useFakeTimers();
    const storage = memoryStorage();
    const key = `${queryCacheStoragePrefix()}user-a`;
    storage.data[key] = "stale";
    storage.setItem = () => { throw new Error("QuotaExceededError"); };
    const writer = new QueryClient();
    const stop = createPersistedQueryCache(writer, storage, "user-a");
    writer.setQueryData(["projects", "ws"], [{ id: "p1" }]);
    await vi.advanceTimersByTimeAsync(60);
    stop();
    expect(storage.data[key]).toBeUndefined();
    vi.useRealTimers();
  });
});
