import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getActorName: (type: string, id: string) => {
      if (type === "agent" && id === "reviewer-1") return "Code Reviewer";
      if (type === "member" && id === "user-1") return "Test User";
      return `${type}:${id}`;
    },
  }),
}));

import { SubIssueCloseStrip } from "./sub-issue-close-strip";

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
    id: "child-1",
    workspace_id: "ws-1",
    number: 230,
    identifier: "DENE-230",
    title: "Stage child",
    description: null,
    status: "done",
    priority: "high",
    assignee_type: "agent",
    assignee_id: "agent-1",
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "agent",
    creator_id: "agent-1",
    parent_issue_id: "parent-1",
    project_id: null,
    position: 0,
    stage: 1,
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

const deliveredMeta = {
  "close.at": "2026-09-15T11:52:15Z",
  "close.conclusion": "delivered",
  "close.evidence_comment_id": "01a0a4e9-1c4e-75a6-8895-79f1210f494e",
  "close.next_owner_id": "",
  "close.next_owner_type": "none",
  "close.status": "done",
  "close.waiting_on": "",
  "close.wake_action": "stage_done",
};

// The chips carry a technical `data-chip` identifier rather than a `title`
// tooltip: an untranslated key like "close.waiting_on" has no business being
// read out to a screen-reader user.
function chip(kind: string): HTMLElement {
  const el = document.querySelector(`[data-chip="${kind}"]`);
  if (!el) throw new Error(`no chip with data-chip="${kind}"`);
  return el as HTMLElement;
}

function queryChip(kind: string): HTMLElement | null {
  return document.querySelector(`[data-chip="${kind}"]`);
}

describe("SubIssueCloseStrip", () => {
  it("shows the five Stage 5 fields for a delivered close (DENE-231 shape)", () => {
    renderWithI18n(
      <SubIssueCloseStrip
        issue={issue({
          identifier: "DENE-231",
          stage: 2,
          status: "done",
          metadata: deliveredMeta,
          last_activity_at: "2026-09-15T12:16:13Z",
        })}
      />,
    );

    const strip = screen.getByTestId("sub-issue-close-strip");
    expect(strip).toHaveAttribute("data-close-state", "ok");
    expect(chip("stage")).toHaveTextContent("Stage 2");
    expect(chip("close.conclusion")).toHaveTextContent("delivered");
    expect(chip("close.next_owner")).toHaveTextContent("Next: none");
    expect(queryChip("close.waiting_on")).not.toBeInTheDocument();
    expect(chip("last_activity_at")).toHaveTextContent("1h ago");
    expect(queryChip("close.missing")).not.toBeInTheDocument();
  });

  it("marks a bag with no close.* keys as not closed under protocol (DENE-230)", () => {
    renderWithI18n(<SubIssueCloseStrip issue={issue({ metadata: {} })} />);

    const strip = screen.getByTestId("sub-issue-close-strip");
    expect(strip).toHaveAttribute("data-close-state", "missing");
    expect(chip("close.missing")).toHaveTextContent(
      "Not closed under protocol",
    );
    expect(chip("stage")).toHaveTextContent("Stage 1");
    expect(queryChip("close.conclusion")).not.toBeInTheDocument();
  });

  it("marks close.status drift instead of rendering as a normal close (DENE-233 history)", () => {
    renderWithI18n(
      <SubIssueCloseStrip
        issue={issue({
          status: "done",
          metadata: {
            ...deliveredMeta,
            "close.status": "in_review",
            "close.conclusion": "awaiting_review",
          },
        })}
      />,
    );

    const strip = screen.getByTestId("sub-issue-close-strip");
    expect(strip).toHaveAttribute("data-close-state", "drift");
    expect(chip("close.status")).toHaveTextContent(
      "close.status ≠ done",
    );
    expect(chip("close.conclusion")).toHaveTextContent(
      "awaiting_review",
    );
  });

  it("shows the in_review hand-off owner and wait source without marking it stuck", () => {
    renderWithI18n(
      <SubIssueCloseStrip
        issue={issue({
          status: "in_review",
          stage: 3,
          last_activity_at: "2026-09-15T10:00:00Z",
          metadata: {
            "close.at": "2026-09-15T10:00:00Z",
            "close.conclusion": "awaiting_review",
            "close.evidence_comment_id": "comment-1",
            "close.next_owner_type": "agent",
            "close.next_owner_id": "reviewer-1",
            "close.status": "in_review",
            "close.waiting_on": "DENE-196",
            "close.wake_action": "mention",
          },
        })}
      />,
    );

    const strip = screen.getByTestId("sub-issue-close-strip");
    expect(strip).toHaveAttribute("data-stuck", "false");
    expect(chip("close.next_owner")).toHaveTextContent(
      "Next: Code Reviewer",
    );
    expect(chip("close.waiting_on")).toHaveTextContent(
      "Waiting on DENE-196",
    );
    expect(chip("last_activity_at")).toHaveTextContent("4h ago");
  });
});
