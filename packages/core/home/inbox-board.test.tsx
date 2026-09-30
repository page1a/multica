/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode, type ReactNode } from "react";

import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { InboxBoardResponse } from "../types/home";
import { useInboxBoard } from "./queries";

function response(over: Partial<InboxBoardResponse> = {}): InboxBoardResponse {
  return {
    waiting: [],
    stalled: [],
    running: [],
    todo: [],
    fresh: [
      {
        issue_id: "i-1",
        identifier: "DENE-1",
        title: "t",
        parent_issue_id: null,
        lane: "fresh",
        kind: "fresh",
        stuck_kind: "",
        reason: "",
        before: "",
        from: null,
        from_name: "",
        next: null,
        next_name: "",
        at: "2026-09-26T08:00:00Z",
        timeline: [],
        unread: 2,
        children: [],
      },
    ],
    done: [],
    viewer_id: "me",
    as_of: "2026-09-26T08:30:00.123456Z",
    unread_since: null,
    tz: "UTC",
    day_start: "2026-09-26T00:00:00Z",
    unread_markable: 1,
    ...over,
  };
}

function mount(api: Record<string, unknown>) {
  setApiInstance({
    listInbox: vi.fn().mockResolvedValue([]),
    getInboxUnreadSummary: vi.fn().mockResolvedValue([]),
    ...api,
  } as unknown as ApiClient);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <StrictMode>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </StrictMode>
  );
  return renderHook(() => useInboxBoard("ws-1"), { wrapper });
}

describe("useInboxBoard (DENE-975)", () => {
  it("reads live, marks all read once, then replays from the server's mark", async () => {
    const getInboxBoard = vi.fn().mockResolvedValue(response());
    const markAllInboxRead = vi.fn().mockResolvedValue({ count: 1 });
    const { result } = mount({ getInboxBoard, markAllInboxRead });

    await waitFor(() =>
      expect(getInboxBoard).toHaveBeenCalledWith(
        expect.objectContaining({ unread_since: "2026-09-26T08:30:00.123456Z" }),
      ),
    );
    expect(getInboxBoard.mock.calls[0]![0]).toMatchObject({ unread_since: undefined });
    expect(markAllInboxRead).toHaveBeenCalledTimes(1);
    expect(getInboxBoard.mock.invocationCallOrder[0]).toBeLessThan(
      markAllInboxRead.mock.invocationCallOrder[0]!,
    );
    expect(result.current.board.fresh[0]).toMatchObject({ issueId: "i-1", unread: 2 });
  });

  it("skips the bulk read when only open calls are unread", async () => {
    const getInboxBoard = vi.fn().mockResolvedValue(response({ unread_markable: 0 }));
    const markAllInboxRead = vi.fn();
    mount({ getInboxBoard, markAllInboxRead });

    await waitFor(() =>
      expect(getInboxBoard).toHaveBeenCalledWith(expect.objectContaining({ unread_since: expect.any(String) })),
    );
    expect(markAllInboxRead).not.toHaveBeenCalled();
  });
});
