/** @vitest-environment jsdom */
import { QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { WSClient } from "../api/ws-client";
import { createQueryClient } from "../query-client";
import type { Agent, WSMessage } from "../types";
import { agentListOptions, workspaceKeys } from "../workspace/queries";
import { useRealtimeSync, type RealtimeSyncStores } from "./use-realtime-sync";

vi.mock("../platform/workspace-storage", () => ({
  getCurrentWsId: () => "ws-1",
  getCurrentSlug: () => "test-ws",
  createWorkspaceAwareStorage: (adapter: unknown) => adapter,
  registerForWorkspaceRehydration: () => {},
}));
vi.mock("../paths", () => ({
  useHasOnboarded: () => true,
  resolvePostAuthDestination: () => "/",
}));

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

it("patches a pure status flip without refetching the agent list (DENE-1504)", async () => {
  const qc = createQueryClient();
  const row = { id: "a1", status: "idle", archived_at: null } as unknown as Agent;
  const listAgents = vi.fn(async () => [row]);
  setApiInstance({
    listAgents,
    listWorkingAgents: async () => [],
  } as unknown as ApiClient);

  let emit: (msg: WSMessage) => void = () => {};
  const ws = {
    on: () => () => {},
    onAny: (handler: (msg: WSMessage) => void) => {
      emit = handler;
      return () => {};
    },
    onReconnect: () => () => {},
  } as unknown as WSClient;
  const stores = {
    authStore: { getState: () => ({ user: { id: "u1" } }) },
  } as unknown as RealtimeSyncStores;
  function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }

  const view = renderHook(
    () => {
      useRealtimeSync(ws, stores);
      return useQuery(agentListOptions("ws-1"));
    },
    { wrapper: Wrapper },
  );
  await waitFor(() => expect(view.result.current.data).toEqual([row]));
  expect(listAgents).toHaveBeenCalledTimes(1);

  await act(async () => {
    emit({
      type: "agent:status",
      payload: { agent: row, agent_id: "a1", status: "working" },
    });
    await new Promise((resolve) => setTimeout(resolve, 150));
  });
  expect(view.result.current.data?.[0]?.status).toBe("working");
  expect(qc.getQueryState(workspaceKeys.agents("ws-1"))?.isInvalidated).toBe(false);
  expect(listAgents).toHaveBeenCalledTimes(1);

  // A config edit carries only the redacted row, so it still refetches.
  await act(async () => {
    emit({ type: "agent:status", payload: { agent: { ...row, name: "x" } } });
    await new Promise((resolve) => setTimeout(resolve, 150));
  });
  await waitFor(() => expect(listAgents).toHaveBeenCalledTimes(2));
});
