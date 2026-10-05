import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { IssueStateCard } from "@multica/core/types";

const state = vi.hoisted(() => ({
  card: vi.fn(),
  add: vi.fn(),
  edit: vi.fn(),
  remove: vi.fn(),
}));

vi.mock("@multica/core/issues/queries", () => ({
  issueContextOptions: (_ws: string, id: string) => ({ queryKey: ["context", id], queryFn: state.card }),
}));
vi.mock("@multica/core/issues/mutations", () => ({
  useIssueDecisionMutations: () => ({
    add: { mutateAsync: state.add, isPending: false },
    edit: { mutateAsync: state.edit, isPending: false },
    remove: { mutateAsync: state.remove, isPending: false },
  }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: (type: string, id: string) => `${type}:${id}` }),
}));
vi.mock("../../i18n", () => ({
  useT: () => ({
    t: (pick: (s: unknown) => unknown, opts?: Record<string, unknown>) => {
      const key = pick(new Proxy({}, { get: (_t, ns) => new Proxy({}, { get: (_u, k) => `${String(ns)}.${String(k)}` }) }));
      return opts ? `${String(key)} ${JSON.stringify(opts)}` : key;
    },
  }),
  useTimeAgo: () => () => "1m ago",
}));

import { IssueStateCardSection } from "./issue-state-card";

const base: IssueStateCard = {
  issue_id: "i-1",
  identifier: "DENE-1",
  goal: { title: "做状态卡" },
  decisions: [
    { id: "d-1", text: "拍板单独建表", source: "close", author_type: "agent", author_id: "a-1", created_at: "", updated_at: "" },
  ],
  now: { status: "in_progress", closed: false },
  baton: { kind: "handoff", summary: "服务端已合，剩前端", by_type: "agent", by_id: "a-1", to: "孙悟天", at: "2026-10-04T11:00:00Z" },
  changes: {
    anchor: "last_comment",
    threads: [{ thread_id: "t-1", title: "能不能先做手机", author_type: "member", new_count: 2, last_at: "" }],
    more: 3,
  },
  text: "",
};

function renderCard(onJump = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrap = ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  render(<IssueStateCardSection wsId="ws-1" issueId="i-1" onJumpToThread={onJump} />, { wrapper: wrap });
  return onJump;
}

beforeEach(() => {
  state.card.mockReset();
  state.add.mockReset();
  state.edit.mockReset();
  state.remove.mockReset();
});
afterEach(cleanup);

describe("IssueStateCardSection", () => {
  it("shows decisions, the baton and the viewer's new threads", async () => {
    state.card.mockResolvedValue(base);
    const onJump = renderCard();
    await waitFor(() => expect(screen.getByText("拍板单独建表")).toBeTruthy());
    expect(screen.getByText("服务端已合，剩前端")).toBeTruthy();
    expect(screen.getByText(/state_card.baton_handoff .*孙悟天.* · agent:a-1 · 1m ago/)).toBeTruthy();
    fireEvent.click(screen.getByText("能不能先做手机"));
    expect(onJump).toHaveBeenCalledWith("t-1");
    expect(screen.getByText(/state_card.changes_more .*3/)).toBeTruthy();
  });

  it("adds, edits and deletes a decision through the server", async () => {
    state.card.mockResolvedValue(base);
    state.add.mockResolvedValue({});
    state.edit.mockResolvedValue({});
    state.remove.mockResolvedValue(undefined);
    renderCard();
    await waitFor(() => expect(screen.getByText("拍板单独建表")).toBeTruthy());

    fireEvent.click(screen.getByText("state_card.decision_add"));
    fireEvent.change(screen.getByLabelText("state_card.decision_placeholder"), { target: { value: " 手机端先做网页 " } });
    fireEvent.click(screen.getByText("state_card.decision_save"));
    await waitFor(() => expect(state.add).toHaveBeenCalledWith("手机端先做网页"));

    await waitFor(() => expect(screen.getByLabelText("state_card.decision_edit")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("state_card.decision_edit"));
    fireEvent.change(screen.getByLabelText("state_card.decision_placeholder"), { target: { value: "拍板改用新表" } });
    fireEvent.click(screen.getByText("state_card.decision_save"));
    await waitFor(() => expect(state.edit).toHaveBeenCalledWith({ id: "d-1", text: "拍板改用新表" }));

    await waitFor(() => expect(screen.getByLabelText("state_card.decision_delete")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("state_card.decision_delete"));
    await waitFor(() => expect(state.remove).toHaveBeenCalledWith("d-1"));
  });

  it("shows the server's refusal instead of dropping it", async () => {
    state.card.mockResolvedValue(base);
    state.edit.mockRejectedValue(new Error("智能体只能改自己写的拍板"));
    renderCard();
    await waitFor(() => expect(screen.getByText("拍板单独建表")).toBeTruthy());
    fireEvent.click(screen.getByLabelText("state_card.decision_edit"));
    fireEvent.change(screen.getByLabelText("state_card.decision_placeholder"), { target: { value: "改" } });
    fireEvent.click(screen.getByText("state_card.decision_save"));
    await waitFor(() => expect(screen.getByText(/智能体只能改自己写的拍板/)).toBeTruthy());
  });

  it("says when nothing is settled and nothing is new", async () => {
    state.card.mockResolvedValue({ ...base, decisions: [], baton: null, changes: { anchor: "none", threads: [] } });
    renderCard();
    await waitFor(() => expect(screen.getByText("state_card.decisions_empty")).toBeTruthy());
    expect(screen.getByText("state_card.baton_none")).toBeTruthy();
    expect(screen.getByText("state_card.changes_first")).toBeTruthy();
    expect(screen.getByText("state_card.changes_none")).toBeTruthy();
  });
});
