/**
 * @vitest-environment jsdom
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Issue } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enIssues from "../../locales/en/issues.json";
import { IssueSurfacePinnedProvider } from "../surface/pinned-context";
import { ListRow } from "./list-row";

// The List row's pin marker. Its data source is the surface's pinned-ids
// projection (built from each served row's own `is_pinned`), and the row is the
// only place it becomes visible — so this suite owns "pinned rows are marked,
// plain rows are not, and the marker names itself". (DENE-500)

const TEST_RESOURCES = { en: { common: enCommon, issues: enIssues } };

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () =>
    new Proxy({}, { get: () => (id: string) => `/test/issues/${id}` }),
}));
vi.mock("../../navigation", () => ({
  AppLink: ({ children, href }: { children: React.ReactNode; href: string }) => (
    <a href={href}>{children}</a>
  ),
  useNavigation: () => ({ push: vi.fn(), pathname: "/issues" }),
}));
// The right-click menu needs the whole context-menu provider tree; the badge
// does not depend on any of it.
vi.mock("../actions", () => ({
  IssueActionsContextMenu: ({ children }: { children: React.ReactNode }) => children,
}));
vi.mock("@multica/core/properties", () => ({
  propertyListOptions: () => ({
    queryKey: ["properties"],
    queryFn: async () => [],
  }),
}));
vi.mock("@multica/core/issues/stores/view-store-context", () => {
  const state = { cardProperties: {}, cardPropertyIds: [] };
  const useViewStore = (selector: (s: typeof state) => unknown) => selector(state);
  useViewStore.getState = () => state;
  return { useViewStore };
});

function makeIssue(id: string): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title: `Issue ${id}`,
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
    labels: [],
    metadata: {},
    properties: {},
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

function renderRow(issue: Issue, pinnedIssueIds: ReadonlySet<string>) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <QueryClientProvider client={qc}>
        <IssueSurfacePinnedProvider pinnedIssueIds={pinnedIssueIds}>
          <ListRow issue={issue} />
        </IssueSurfacePinnedProvider>
      </QueryClientProvider>
    </I18nProvider>,
  );
}

describe("ListRow pinned indicator", () => {
  it("marks a row the surface reported as pinned", () => {
    const issue = makeIssue("pinned");
    renderRow(issue, new Set([issue.id]));
    expect(screen.getByTestId("pinned-row-badge")).toBeTruthy();
    expect(screen.getByText(issue.title)).toBeTruthy();
  });

  it("draws nothing on a row the surface did not report as pinned", () => {
    const issue = makeIssue("plain");
    renderRow(issue, new Set(["someone-else"]));
    expect(screen.queryByTestId("pinned-row-badge")).toBeNull();
    expect(screen.getByText(issue.title)).toBeTruthy();
  });

  it("draws nothing when no pins are in this window at all", () => {
    const issue = makeIssue("empty-window");
    renderRow(issue, new Set());
    expect(screen.queryByTestId("pinned-row-badge")).toBeNull();
  });

  it("names the marker for assistive tech instead of leaving a bare icon", () => {
    const issue = makeIssue("labelled");
    renderRow(issue, new Set([issue.id]));
    // A decorative-only icon would be invisible to a screen reader; the
    // indicator has to say what the ordering means.
    expect(screen.getByRole("img", { name: "Pinned" })).toBeTruthy();
  });
});
