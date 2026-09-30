import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("../../editor", () => ({
  ReadonlyContent: ({ content }: { content: string }) => <div>{content}</div>,
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (type: string, id: string) => {
      if (type === "agent" && id === "agent-1") return "Executor";
      if (type === "member" && id === "user-1") return "Test User";
      return `${type}:${id}`;
    },
  }),
}));

import { IssueCloseRecordSection } from "./issue-close-record";

afterEach(cleanup);

const NOW = "2026-09-15T14:00:00.000Z";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(NOW));
});

afterEach(() => {
  vi.useRealTimers();
});

function issue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: "issue-1",
    workspace_id: "ws-1",
    number: 230,
    identifier: "DENE-230",
    title: "Close record",
    description: null,
    status: "in_progress",
    priority: "high",
    assignee_type: "agent",
    assignee_id: "agent-1",
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "agent",
    creator_id: "agent-1",
    parent_issue_id: null,
    project_id: null,
    position: 0,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    created_at: "2026-09-15T10:47:57Z",
    updated_at: "2026-09-15T11:15:22Z",
    last_activity_at: "2026-09-15T11:15:22Z",
    ...overrides,
  };
}

const continuingMeta = {
  "close.at": "2026-09-15T11:52:15Z",
  "close.conclusion": "continuing",
  "close.evidence_comment_id": "comment-9",
  "close.next_owner_id": "agent-1",
  "close.next_owner_type": "agent",
  "close.status": "in_progress",
  "close.waiting_on": "",
  "close.wake_action": "clock",
};

describe("IssueCloseRecordSection (DENE-1002)", () => {
  it("shows the conclusion, the status it wrote, who continues and the reason", () => {
    renderWithI18n(
      <IssueCloseRecordSection
        issue={issue({ status: "in_progress", metadata: continuingMeta })}
        evidenceBody="网关改造做到一半，先停；等扩容窗口到点继续。"
      />,
    );

    const section = screen.getByTestId("issue-close-record");
    expect(section).toHaveTextContent("Close record");
    expect(section).toHaveTextContent("Paused, continues next round");
    expect(section).toHaveTextContent("in_progress");
    expect(section).toHaveTextContent("Next: Executor");
    expect(section).toHaveTextContent("网关改造做到一半，先停；等扩容窗口到点继续。");
    expect(section).toHaveTextContent("2h ago");
  });

  it("names a deferred close and says nothing is next", () => {
    renderWithI18n(
      <IssueCloseRecordSection
        issue={issue({
          status: "todo",
          metadata: {
            ...continuingMeta,
            "close.conclusion": "deferred",
            "close.status": "todo",
            "close.next_owner_type": "none",
            "close.next_owner_id": "",
            "close.wake_action": "none",
          },
        })}
        evidenceBody="需求还没定，放回待办。"
      />,
    );

    const section = screen.getByTestId("issue-close-record");
    expect(section).toHaveTextContent("Returned to planning / to-do");
    expect(section).toHaveTextContent("Next: none");
  });

  it("says so when the reason comment is not loaded yet", () => {
    renderWithI18n(
      <IssueCloseRecordSection
        issue={issue({ metadata: continuingMeta })}
        evidenceBody={null}
      />,
    );
    expect(screen.getByTestId("issue-close-record")).toHaveTextContent(
      "The reason comment was not loaded.",
    );
  });

  it("stays hidden while the close record is incomplete", () => {
    renderWithI18n(
      <IssueCloseRecordSection
        issue={issue({ metadata: { "close.conclusion": "delivered" } })}
        evidenceBody="half a record"
      />,
    );
    expect(screen.queryByTestId("issue-close-record")).not.toBeInTheDocument();
  });
});
