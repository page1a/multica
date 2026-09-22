import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import {
  AppLink,
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";
import { IssueContextMenuProvider } from "../actions";
import { ParentIssueLookupProvider } from "../surface/parent-issue-context";
import { BoardCardContent } from "./board-card";
import { ListRow } from "./list-row";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: [] }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/properties", () => ({
  propertyListOptions: () => ({ queryKey: ["properties"] }),
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

const viewState = vi.hoisted(() => ({
  viewMode: "board",
  grouping: "status",
  swimlaneGrouping: "assignee",
  cardProperties: {
    priority: false,
    description: false,
    assignee: false,
    startDate: false,
    dueDate: false,
    project: false,
    childProgress: false,
    labels: false,
  },
  cardPropertyIds: [],
}));

vi.mock("@multica/core/issues/stores/view-store-context", () => ({
  useViewStore: (selector: (state: typeof viewState) => unknown) =>
    selector(viewState),
}));

vi.mock("@multica/core/issues/stores/selection-store", () => ({
  useIssueSelectionStore: (
    selector: (state: Record<string, unknown>) => unknown,
  ) =>
    selector({
      selectedIds: new Set<string>(),
      toggle: vi.fn(),
      select: vi.fn(),
      deselect: vi.fn(),
      clear: vi.fn(),
    }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: () => "Someone",
    getActorInitials: () => "SO",
    getActorAvatarUrl: () => null,
  }),
}));

vi.mock("../../i18n", () => ({
  useLocale: () => "en",
  useT: () => ({ t: () => "Open parent issue" }),
  useTimeAgo: () => () => "now",
}));

vi.mock("./issue-agent-activity-indicator", () => ({
  IssueAgentActivityIndicator: () => null,
}));

function makeIssue(overrides: Partial<Issue> & Pick<Issue, "id">): Issue {
  return {
    workspace_id: "ws-1",
    number: 1,
    identifier: "DENE-1",
    title: "An issue",
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "member",
    creator_id: "member-1",
    parent_issue_id: null,
    project_id: null,
    position: 1,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    labels: [],
    created_at: "2026-09-17T00:00:00Z",
    updated_at: "2026-09-17T00:00:00Z",
    ...overrides,
  };
}

const parent = makeIssue({
  id: "parent-1",
  number: 480,
  identifier: "DENE-480",
  title: "Board and list: sub-issue cards need a parent badge",
});

const child = makeIssue({
  id: "child-1",
  number: 481,
  identifier: "DENE-481",
  title: "Render the badge",
  parent_issue_id: parent.id,
});

function navigationAdapter(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => `https://app.example${path}`,
  };
}

function renderBoardCard(
  issue: Issue,
  lookup: Issue[],
  navigation = navigationAdapter(),
) {
  const result = render(
    <NavigationProvider value={navigation}>
      <ParentIssueLookupProvider issues={lookup}>
        <AppLink href={`/acme/issues/${issue.id}`}>
          <BoardCardContent issue={issue} />
        </AppLink>
      </ParentIssueLookupProvider>
    </NavigationProvider>,
  );
  return { ...result, navigation };
}

function renderListRow(
  issue: Issue,
  lookup: Issue[],
  navigation = navigationAdapter(),
) {
  const result = render(
    <NavigationProvider value={navigation}>
      <IssueContextMenuProvider>
        <ParentIssueLookupProvider issues={lookup}>
          <ListRow issue={issue} />
        </ParentIssueLookupProvider>
      </IssueContextMenuProvider>
    </NavigationProvider>,
  );
  return { ...result, navigation };
}

describe("parent ownership badge", () => {
  it("names the parent on a sub-issue board card", () => {
    renderBoardCard(child, [parent, child]);

    const badge = screen.getByTestId("parent-issue-badge");
    expect(badge).toHaveTextContent("DENE-480");
    expect(badge).toHaveTextContent(
      "Board and list: sub-issue cards need a parent badge",
    );
  });

  it("names the parent on a sub-issue list row", () => {
    renderListRow(child, [parent, child]);

    expect(screen.getByTestId("parent-issue-badge")).toHaveTextContent(
      "DENE-480",
    );
  });

  it("stays silent on a top-level issue", () => {
    renderBoardCard(parent, [parent, child]);

    expect(screen.queryByTestId("parent-issue-badge")).not.toBeInTheDocument();
  });

  it("stays silent when the parent is not in the surface's loaded set", () => {
    // A child whose parent sits outside the loaded window: a chip that could
    // only show a raw uuid costs the card a row and tells the reader nothing.
    renderBoardCard(child, [child]);

    expect(screen.queryByTestId("parent-issue-badge")).not.toBeInTheDocument();
  });

  it("stays silent outside a lookup provider", () => {
    render(
      <NavigationProvider value={navigationAdapter()}>
        <BoardCardContent issue={child} />
      </NavigationProvider>,
    );

    expect(screen.queryByTestId("parent-issue-badge")).not.toBeInTheDocument();
  });

  it("opens the parent instead of the card it sits on", () => {
    const { navigation } = renderBoardCard(child, [parent, child]);

    // The card is wrapped in the issue link; the badge must swallow the click
    // the way the inline pickers beside it do.
    expect(fireEvent.click(screen.getByTestId("parent-issue-badge"))).toBe(
      false,
    );
    expect(navigation.push).toHaveBeenCalledTimes(1);
    expect(navigation.push).toHaveBeenCalledWith("/acme/issues/parent-1");
  });
});
