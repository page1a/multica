/**
 * @vitest-environment jsdom
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { WSClient } from "../api/ws-client";
import { workspaceKeys } from "../workspace/queries";
import { projectKeys } from "../projects/queries";
import { projectMemberKeys } from "../projects/member-queries";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../search-index/instance", () => ({
  forgetLocalSearchIndex: vi.fn(async () => undefined),
}));

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: vi.fn(() => "ws-1"),
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));

vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

function createMockWs(): WSClient {
  return {
    on: vi.fn(() => () => {}),
    onAny: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
  } as unknown as WSClient;
}

function createStores(): RealtimeSyncStores {
  return {
    authStore: Object.assign(() => ({}), {
      getState: () => ({ user: { id: "u1" } }),
      subscribe: () => () => {},
      setState: () => {},
      destroy: () => {},
    }),
  } as unknown as RealtimeSyncStores;
}

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const rosterKey = workspaceKeys.members("ws-1");
const projectMembersKey = projectMemberKeys.list("ws-1", "p-1");
const projectListKey = projectKeys.list("ws-1");

// The roster carries each member's projects, and project member rows carry
// the member's workspace role. Both are derived from the other side's events,
// and the query cache never goes stale on its own (staleTime: Infinity).
describe("useRealtimeSync — membership projections (DENE-1706)", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  async function emit(type: string) {
    vi.useFakeTimers();
    const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
    for (const key of [rosterKey, projectMembersKey, projectListKey]) {
      qc.setQueryData(key, []);
    }
    const ws = createMockWs();
    const hook = renderHook(() => useRealtimeSync(ws, createStores()), {
      wrapper: createWrapper(qc),
    });
    const handler = vi.mocked(ws.onAny).mock.calls[0]![0];
    handler({ type, payload: { project_id: "p-1" } } as never);
    await vi.advanceTimersByTimeAsync(1000);
    const invalidated = (key: readonly unknown[]) => qc.getQueryState(key)?.isInvalidated;
    const result = {
      roster: invalidated(rosterKey),
      projectMembers: invalidated(projectMembersKey),
      projectList: invalidated(projectListKey),
    };
    hook.unmount();
    qc.clear();
    return result;
  }

  it.each(["project:updated", "project:deleted"])(
    "refreshes the roster on %s",
    async (type) => {
      expect((await emit(type)).roster).toBe(true);
    },
  );

  it.each(["member:updated", "member:removed", "member:added"])(
    "refreshes project member lists on %s without touching the project list",
    async (type) => {
      const result = await emit(type);
      expect(result.projectMembers).toBe(true);
      expect(result.roster).toBe(true);
      expect(result.projectList).toBe(false);
    },
  );
});
