import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

const toggleMutate = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/issues/mutations", () => ({
  useToggleIssueReaction: () => ({ mutate: toggleMutate }),
}));
vi.mock("@multica/core/issues/queries", () => ({
  issueReactionsOptions: (id: string) => ({ queryKey: ["issues", "reactions", id] }),
  issueKeys: { reactions: (id: string) => ["issues", "reactions", id] },
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: [], isLoading: false }),
  useQueryClient: () => ({ invalidateQueries: vi.fn(), setQueryData: vi.fn() }),
  useMutationState: () => [],
}));
vi.mock("@multica/core/realtime", () => ({ useWSEvent: vi.fn(), useWSReconnect: vi.fn() }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

import { useIssueReactions } from "./use-issue-reactions";

describe("useIssueReactions", () => {
  it("says so when a reaction is refused", async () => {
    toggleMutate.mockImplementation((_vars: unknown, opts?: { onError?: () => void }) => opts?.onError?.());
    const { result } = renderHook(() => useIssueReactions("issue-1", "user-1"));

    await act(() => result.current.toggleReaction("👍"));

    expect(toggleMutate).toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledTimes(1);
  });
});
