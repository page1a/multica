import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import {
  BoardCardSubIssueToggle,
  BoardCardSubIssues,
} from "./board-card-sub-issues";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws-1", slug: "acme" }),
  useWorkspacePaths: () => ({
    issueDetail: (id: string) => `/acme/issues/${id}`,
    memberDetail: (id: string) => `/acme/members/${id}`,
    agentDetail: (id: string) => `/acme/agents/${id}`,
    squadDetail: (id: string) => `/acme/squads/${id}`,
  }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Assignee",
    getActorInitials: () => "AA",
    getActorAvatarUrl: () => null,
  }),
}));

const listChildIssues = vi.hoisted(() => vi.fn());
vi.mock("@multica/core/api", () => ({
  api: { listChildIssues },
}));

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path: string) => `https://app.example${path}`,
};

function child(id: string, over: Partial<Issue> = {}): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title: `Child ${id}`,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: "parent-1",
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    labels: [],
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-01T00:00:00Z",
    ...over,
  };
}

function renderList() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderWithI18n(
    <QueryClientProvider client={queryClient}>
      <NavigationProvider value={navigation}>
        <BoardCardSubIssues parentId="parent-1" />
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
});

// The board hides sub-issues from the top level on a project surface, so the
// accordion is the only way back to them without leaving the board. Canonical
// coverage of the roll-up→state rule lives in `../utils/parent-rollup.test.ts`;
// this suite owns the wiring. (DENE-444)
describe("BoardCardSubIssueToggle", () => {
  it("toggles without letting the click reach the card's link", async () => {
    const user = userEvent.setup();
    const onToggle = vi.fn();
    renderWithI18n(
      <BoardCardSubIssueToggle
        expanded={false}
        rollup={{ done: 1, total: 4, blocked: 0, active: 2 }}
        onToggle={onToggle}
      />,
    );

    const button = screen.getByRole("button", { name: "4 sub-issues" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    await user.click(button);

    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("bubbles a blocked child onto the collapsed parent", () => {
    renderWithI18n(
      <BoardCardSubIssueToggle
        expanded={false}
        rollup={{ done: 0, total: 3, blocked: 2, active: 1 }}
        onToggle={vi.fn()}
      />,
    );

    expect(screen.getByTestId("board-card-pipeline-blocked")).toHaveTextContent(
      "2 blocked",
    );
    expect(screen.queryByTestId("board-card-pipeline-active")).toBeNull();
  });

  it("says nothing extra once the whole pipeline has landed", () => {
    renderWithI18n(
      <BoardCardSubIssueToggle
        expanded
        rollup={{ done: 3, total: 3, blocked: 0, active: 0 }}
        onToggle={vi.fn()}
      />,
    );

    expect(screen.queryByTestId("board-card-pipeline-blocked")).toBeNull();
    expect(screen.queryByTestId("board-card-pipeline-active")).toBeNull();
    expect(screen.queryByTestId("board-card-pipeline-stalled")).toBeNull();
  });
});

describe("BoardCardSubIssues", () => {
  it("lists children in stage order, staged groups first", async () => {
    listChildIssues.mockResolvedValue({
      issues: [
        child("c", { title: "Unstaged last" }),
        child("b", { title: "Stage two", stage: 2 }),
        child("a", { title: "Stage one", stage: 1 }),
      ],
    });
    renderList();

    await waitFor(() =>
      expect(screen.getByText("Stage one")).toBeInTheDocument(),
    );
    const rows = screen.getAllByRole("button").map((node) => node.textContent);
    expect(rows).toEqual(["Stage one", "Stage two", "Unstaged last"]);
    expect(screen.getByText("Stage 1")).toBeInTheDocument();
    expect(screen.getByText("Stage 2")).toBeInTheDocument();
  });

  it("opens a child in place of navigating the parent card", async () => {
    const user = userEvent.setup();
    listChildIssues.mockResolvedValue({ issues: [child("c-1")] });
    renderList();

    await waitFor(() => expect(screen.getByText("Child c-1")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /Child c-1/ }));

    expect(navigation.push).toHaveBeenCalledWith("/acme/issues/c-1");
  });

  it("says so when the children request fails instead of rendering an empty list", async () => {
    listChildIssues.mockRejectedValue(new Error("boom"));
    renderList();

    await waitFor(() =>
      expect(screen.getByText("Could not load sub-issues")).toBeInTheDocument(),
    );
    expect(screen.queryByText("No sub-issues")).toBeNull();
  });
});
