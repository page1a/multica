import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Progress } from "@multica/core/types";

const state = vi.hoisted(() => ({
  snapshot: [] as unknown[],
  history: vi.fn(),
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/agents", () => ({
  agentTaskSnapshotOptions: () => ({ queryKey: ["snapshot"], queryFn: () => state.snapshot }),
}));
vi.mock("@multica/core/issues/queries", () => ({
  issueProgressHistoryOptions: (_ws: string, id: string) => ({ queryKey: ["progress", id], queryFn: state.history }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (type: string, id: string) => `${type}:${id}` }),
}));
vi.mock("../../i18n", () => ({
  useT: () => ({ t: (pick: (s: unknown) => unknown) => pick(new Proxy({}, { get: (_t, ns) => new Proxy({}, { get: (_u, key) => `${String(ns)}.${String(key)}` }) })) }),
  useTimeAgo: () => () => "1m ago",
}));

import { IssueProgressBar } from "./issue-progress-line";

const current: Progress = {
  text: "Detail bar wired",
  source: "agent",
  tone: "working",
  author_type: "agent",
  author_id: "a-1",
  updated_at: "2026-01-02T00:00:00Z",
};

function renderBar(progress: Progress | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return render(<IssueProgressBar issue={{ id: "i-1", progress }} />, { wrapper: wrap });
}

beforeEach(() => {
  state.snapshot = [];
  state.history.mockReset();
});
afterEach(cleanup);

describe("IssueProgressBar", () => {
  it("shows who reported the line and loads history only when expanded", async () => {
    state.history.mockResolvedValue([current, { ...current, text: "Schema landed", source: "close", author_type: "member", author_id: "u-1" }]);
    renderBar(current);
    expect(screen.getByText("Detail bar wired")).toBeTruthy();
    expect(screen.getByText("agent:a-1 · progress_line.source_agent · 1m ago")).toBeTruthy();
    expect(state.history).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "progress_line.history_show" }));
    await waitFor(() => expect(screen.getByText("Schema landed")).toBeTruthy());
    expect(screen.getByText("member:u-1 · progress_line.source_close · 1m ago")).toBeTruthy();
    // The current line is history's first row; it is not repeated.
    expect(screen.getAllByText("Detail bar wired")).toHaveLength(1);
  });

  it("leads with a failed run newer than the line, in red", async () => {
    state.snapshot = [{ id: "t", issue_id: "i-1", status: "failed", completed_at: "2026-01-03T00:00:00Z" }];
    const { container } = renderBar(current);
    await waitFor(() => expect(screen.getByText("progress_line.live_failed")).toBeTruthy());
    expect(container.querySelector(".bg-destructive")).not.toBeNull();
  });

  it("renders nothing without a line or live state", () => {
    const { container } = renderBar(null);
    expect(container.textContent).toBe("");
  });
});
