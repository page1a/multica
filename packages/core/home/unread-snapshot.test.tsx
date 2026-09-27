/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode, type ReactNode } from "react";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { UnreadInboxIssue } from "../types/home";
import { useBoardUnreadSnapshot } from "./queries";

const ROWS: UnreadInboxIssue[] = [
  {
    issue_id: "i-1",
    identifier: "DENE-1",
    title: "t",
    status: "in_progress",
    parent_issue_id: null,
    unread_count: 2,
    held_count: 1,
    latest_at: "2026-09-26T08:00:00Z",
  },
];

describe("useBoardUnreadSnapshot (DENE-901)", () => {
  it("snapshots, then reads all once, and keeps the snapshot under StrictMode", async () => {
    const listUnreadInboxIssues = vi.fn().mockResolvedValue(ROWS);
    const markAllInboxRead = vi.fn().mockResolvedValue({ count: 1 });
    setApiInstance({
      listUnreadInboxIssues,
      markAllInboxRead,
      listInbox: vi.fn().mockResolvedValue([]),
      getInboxUnreadSummary: vi.fn().mockResolvedValue([]),
    } as unknown as ApiClient);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <StrictMode>
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </StrictMode>
    );

    const { result } = renderHook(() => useBoardUnreadSnapshot("ws-1"), { wrapper });

    await waitFor(() => expect(result.current).toEqual(ROWS));
    expect(listUnreadInboxIssues).toHaveBeenCalledTimes(1);
    expect(markAllInboxRead).toHaveBeenCalledTimes(1);
    expect(listUnreadInboxIssues.mock.invocationCallOrder[0]).toBeLessThan(
      markAllInboxRead.mock.invocationCallOrder[0]!,
    );
  });

  it("skips the bulk read when only open calls are unread", async () => {
    const markAllInboxRead = vi.fn();
    setApiInstance({
      listUnreadInboxIssues: vi.fn().mockResolvedValue([{ ...ROWS[0]!, unread_count: 1, held_count: 1 }]),
      markAllInboxRead,
      listInbox: vi.fn().mockResolvedValue([]),
      getInboxUnreadSummary: vi.fn().mockResolvedValue([]),
    } as unknown as ApiClient);
    const qc = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    );
    const { result } = renderHook(() => useBoardUnreadSnapshot("ws-1"), { wrapper });
    await waitFor(() => expect(result.current).toHaveLength(1));
    expect(markAllInboxRead).not.toHaveBeenCalled();
  });
});
