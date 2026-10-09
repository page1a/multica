// @vitest-environment jsdom
/**
 * The work-thread strip on issue and chat headers.
 *
 * It used to show "Thread · completed" on every finished issue next to a
 * "Queue" button that enqueued a run for the assignee with no instruction at
 * all — the real way to queue input is the comment / chat composer, which
 * carries the text. The strip now only appears when there is something to do
 * here: interrupt a running turn, resume a stopped one, or reorder the queue.
 */
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { WorkThreadSnapshot } from "@multica/core/types/work_thread";
import { renderWithI18n } from "../test/i18n";
import { WorkThreadPanel } from "./work-thread-panel";

const snap = vi.hoisted(() => ({ value: null as WorkThreadSnapshot | null }));

vi.mock("@multica/core/api", () => ({
  api: {
    getIssueWorkThread: () => Promise.resolve(snap.value),
    getChatWorkThread: () => Promise.resolve(snap.value),
  },
}));

function snapshot(patch: Partial<WorkThreadSnapshot>): WorkThreadSnapshot {
  return {
    thread_id: "t1",
    agent_id: "a1",
    continuous: false,
    state: "completed",
    can_resume: false,
    queued_inputs: [],
    queue_truncated: false,
    context: { generation: 0, message_limit: 0, token_budget: 0, summary_available: false },
    updated_at: "2026-10-08T00:00:00Z",
    ...patch,
  };
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <WorkThreadPanel kind="issue" id="i1" />
    </QueryClientProvider>,
    { locale: "zh-Hans" },
  );
}

describe("WorkThreadPanel", () => {
  it("stays hidden on a finished thread with nothing to act on", async () => {
    snap.value = snapshot({ state: "completed" });
    renderPanel();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("work-thread-issue")).toBeNull();
  });

  it("offers interrupt while a turn runs, and never a blank queue button", async () => {
    snap.value = snapshot({ state: "active", current_turn: { id: "r1", status: "running" } });
    renderPanel();
    expect(await screen.findByRole("button", { name: "打断工作线程" })).toBeTruthy();
    expect(screen.queryByText("排队")).toBeNull();
    expect(screen.queryByText(/completed|active/)).toBeNull();
  });

  it("says in words that the thread stopped midway and can continue", async () => {
    snap.value = snapshot({ state: "resumable", can_resume: true });
    renderPanel();
    expect(await screen.findByText("上次停在半路")).toBeTruthy();
    expect(screen.getByRole("button", { name: "从智能体停下的地方接着做" })).toBeTruthy();
  });
});
