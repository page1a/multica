import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";

import { evictIssueAfterVisibilityChange } from "./delete-cache";
import { issueKeys } from "./queries";
import { useRecentIssuesStore } from "./stores/recent-issues-store";
import type { Issue, ListIssuesCache } from "../types";

const WS_ID = "ws-a";
const ISSUE_ID = "issue-1";

function issue(): Issue {
  return {
    id: ISSUE_ID,
    workspace_id: WS_ID,
    identifier: "MUL-1",
    title: "scoped issue",
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    parent_issue_id: null,
    project_id: null,
    position: 0,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    revision: 1,
  } as unknown as Issue;
}

function seedCaches(qc: QueryClient) {
  qc.setQueryData<Issue>(issueKeys.detail(WS_ID, ISSUE_ID), issue());
  qc.setQueryData<ListIssuesCache>(issueKeys.list(WS_ID), {
    byStatus: { unstarted: { issues: [issue()], total: 1 } },
  });
  qc.setQueryData<ListIssuesCache>(issueKeys.myAll(WS_ID), {
    byStatus: { unstarted: { issues: [issue()], total: 1 } },
  });
}

function listIssueIds(qc: QueryClient, key: readonly unknown[]) {
  const data = qc.getQueryData<ListIssuesCache>(key);
  return Object.values(data?.byStatus ?? {}).flatMap((bucket) =>
    (bucket?.issues ?? []).map((i) => i.id),
  );
}

beforeEach(() => {
  useRecentIssuesStore.setState({ byWorkspace: {} });
});

describe("evictIssueAfterVisibilityChange", () => {
  it("drops the issue from every list cache and the detail query", () => {
    const qc = new QueryClient();
    seedCaches(qc);

    evictIssueAfterVisibilityChange(qc, WS_ID, ISSUE_ID);

    expect(qc.getQueryData(issueKeys.detail(WS_ID, ISSUE_ID))).toBeUndefined();
    expect(listIssueIds(qc, issueKeys.list(WS_ID))).toEqual([]);
    expect(listIssueIds(qc, issueKeys.myAll(WS_ID))).toEqual([]);
  });

  it("refetches the lists, because the row is still there for whoever kept access", () => {
    const qc = new QueryClient();
    seedCaches(qc);
    const spy = vi.spyOn(qc, "invalidateQueries");

    evictIssueAfterVisibilityChange(qc, WS_ID, ISSUE_ID);

    const invalidated = spy.mock.calls.map(([arg]) => JSON.stringify(arg?.queryKey));
    expect(invalidated).toContain(JSON.stringify(issueKeys.list(WS_ID)));
    expect(invalidated).toContain(JSON.stringify(issueKeys.tableAll(WS_ID)));
    expect(invalidated).toContain(JSON.stringify(issueKeys.projectGanttAll(WS_ID)));
  });

  // A scope change is not a deletion: the id stays valid, so forgetting it
  // would drop a still-open issue out of the Cmd+K history of every viewer who
  // kept access — including the person who narrowed the scope.
  it("leaves the recent issues store alone", () => {
    useRecentIssuesStore.getState().recordVisit(WS_ID, ISSUE_ID);
    const qc = new QueryClient();
    seedCaches(qc);

    evictIssueAfterVisibilityChange(qc, WS_ID, ISSUE_ID);

    expect(
      useRecentIssuesStore.getState().byWorkspace[WS_ID]?.map((e) => e.id),
    ).toEqual([ISSUE_ID]);
  });

  it("does not touch another workspace's caches", () => {
    const qc = new QueryClient();
    seedCaches(qc);
    qc.setQueryData<Issue>(issueKeys.detail("ws-b", ISSUE_ID), issue());

    evictIssueAfterVisibilityChange(qc, WS_ID, ISSUE_ID);

    expect(qc.getQueryData(issueKeys.detail("ws-b", ISSUE_ID))).toBeDefined();
  });
});
