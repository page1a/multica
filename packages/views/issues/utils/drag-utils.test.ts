import { describe, expect, it } from "vitest";
import type { Issue } from "@multica/core/types";
import type { BoardColumnGroup } from "../components/board-column";
import {
  buildColumns,
  computePosition,
  getIssueGroupId,
  getMoveAnchors,
  getMoveUpdates,
  insertIdByPosition,
  issueMatchesGroup,
  projectGroupId,
  propertyGroupId,
} from "./drag-utils";

function mk(id: string, position: number): Issue {
  return {
    id,
    workspace_id: "ws-1",
    number: 1,
    identifier: `MUL-${id}`,
    title: id,
    description: null,
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    reviewer_type: null,
    reviewer_id: null,
    creator_type: "member",
    creator_id: "user-1",
    parent_issue_id: null,
    project_id: null,
    position,
    stage: null,
    start_date: null,
    due_date: null,
    metadata: {},
    properties: {},
    labels: [],
    created_at: "2025-01-01T00:00:00Z",
    updated_at: "2025-01-01T00:00:00Z",
  };
}

function mapOf(...issues: Issue[]): Map<string, Issue> {
  return new Map(issues.map((i) => [i.id, i]));
}

describe("getMoveAnchors", () => {
  it("derives relative neighbors from the optimistic order", () => {
    expect(getMoveAnchors(["a", "moving", "b"], "moving")).toEqual({
      before_id: "a",
      after_id: "b",
    });
    expect(getMoveAnchors(["moving"], "moving")).toEqual({
      before_id: null,
      after_id: null,
    });
  });
});

/**
 * The pinned block is a barrier inside a single branch, so a drop has to anchor
 * against a neighbour in the SAME arm. Otherwise the pair of anchors spans the
 * block boundary and the server computes a shared mid-position, which would
 * both let a plain row be inserted among pinned rows and let a pinned row fall
 * below them — the per-user order leaking into the shared `position` field.
 * (DENE-500)
 */
describe("getMoveAnchors across the pinned barrier", () => {
  // Served order: pinned block first, then the plain rows.
  const pinned = new Set(["p1", "p2"]);

  it("skips the block for a plain row dropped at its top", () => {
    // "a" was dragged above the block; its real neighbours are the plain rows.
    expect(getMoveAnchors(["p1", "p2", "a", "b", "c"], "a", pinned)).toEqual({
      before_id: null,
      after_id: "b",
    });
  });

  it("keeps a plain row anchored inside the block's rows", () => {
    expect(getMoveAnchors(["p1", "a", "p2", "b", "c"], "a", pinned)).toEqual({
      before_id: null,
      after_id: "b",
    });
  });

  it("keeps a pinned row inside the block", () => {
    // "p2" was dragged below the block: it still anchors to the pinned row
    // above it, so it cannot escape.
    expect(getMoveAnchors(["p1", "a", "b", "p2", "c"], "p2", pinned)).toEqual({
      before_id: "p1",
      after_id: null,
    });
  });

  it("anchors normally when the whole list is one arm", () => {
    expect(getMoveAnchors(["p1", "p2"], "p1", pinned)).toEqual({
      before_id: null,
      after_id: "p2",
    });
    expect(getMoveAnchors(["a", "b", "c"], "b", pinned)).toEqual({
      before_id: "a",
      after_id: "c",
    });
  });

  it("is unchanged when there are no pins at all", () => {
    expect(getMoveAnchors(["a", "moving", "b"], "moving", new Set())).toEqual({
      before_id: "a",
      after_id: "b",
    });
  });
});

describe("computePosition across the pinned barrier", () => {
  it("never writes a position between a pinned and a plain neighbour", () => {
    // Arms: pinned [p1(1), p4(4)], plain [a(10), b(20)].
    const map = mapOf(mk("p1", 1), mk("p4", 4), mk("a", 10), mk("b", 20));
    const pinned = new Set(["p1", "p4"]);
    // "a" dropped between p1 and b must land relative to its plain neighbours,
    // i.e. before b — not in the 4..10 range the block occupies.
    const position = computePosition(["p1", "a", "p4", "b"], "a", map, pinned);
    // Anchored to the next PLAIN row, so the value lands in the plain arm's
    // range instead of the 4..10 gap the pinned block occupies.
    expect(position).toBe(19); // b(20) - 1
  });

  it("keeps the position (a no-op drop) when the row is alone in its arm", () => {
    // Clamped, "a" has no plain neighbour on either side, so there is no slot
    // to move to and its own position comes back — the caller's
    // `position === newPosition` check then skips the write entirely.
    const map = mapOf(mk("p1", 1), mk("a", 10));
    expect(computePosition(["p1", "a"], "a", map, new Set(["p1"]))).toBe(10);
    // The unclamped version would have rewritten the SHARED position to sit
    // just under the pinned row, which is exactly the leak being prevented.
    expect(computePosition(["p1", "a"], "a", map)).toBe(2);
  });
});

describe("insertIdByPosition", () => {
  it("inserts the id at its position-sorted slot", () => {
    const map = mapOf(mk("a", 1), mk("c", 3), mk("b", 2));
    expect(insertIdByPosition(["a", "c"], "b", 2, map)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("appends when the position is the largest", () => {
    const map = mapOf(mk("a", 1), mk("z", 9));
    expect(insertIdByPosition(["a"], "z", 9, map)).toEqual(["a", "z"]);
  });

  it("prepends when the position is the smallest", () => {
    const map = mapOf(mk("b", 2), mk("a", 1));
    expect(insertIdByPosition(["b"], "a", 1, map)).toEqual(["a", "b"]);
  });

  it("appends into an empty target column", () => {
    const map = mapOf(mk("a", 5));
    expect(insertIdByPosition([], "a", 5, map)).toEqual(["a"]);
  });

  it("matches insertByPosition ordering so the settle rebuild is a no-op", () => {
    // Same scenario the board's optimistic drop and the cache patch both apply:
    // landing a card between two neighbours must produce the same order in the
    // id list (board) and the issue list (cache).
    const map = mapOf(mk("x", 1), mk("y", 3), mk("moved", 2));
    expect(insertIdByPosition(["x", "y"], "moved", 2, map)).toEqual([
      "x",
      "moved",
      "y",
    ]);
  });
});

/** Same-category statuses still have independent column identities. */
describe("status grouping with custom statuses", () => {
  const custom = {
    ...mk("custom", 1),
    status: "awaiting_response",
    status_category: "started",
  } as Issue;
  const builtIn = { ...mk("built-in", 2), status: "in_review" } as Issue;
  const inReviewColumn: BoardColumnGroup = {
    id: "status:in_review",
    title: "In Review",
    status: "in_review",
  };
  const customColumn: BoardColumnGroup = { id: "status:awaiting_response", title: "Awaiting Response", status: "awaiting_response" };
  const todoColumn: BoardColumnGroup = {
    id: "status:todo",
    title: "Todo",
    status: "todo",
  };
  const doneColumn: BoardColumnGroup = {
    id: "status:done",
    title: "Completed",
    status: "done",
  };

  it("buckets custom and built-in statuses independently", () => {
    expect(getIssueGroupId(custom, "status")).toBe("status:awaiting_response");
    expect(getIssueGroupId(builtIn, "status")).toBe("status:in_review");
  });

  it("renders the card in that column instead of dropping it", () => {
    const columns = buildColumns([custom, builtIn], [inReviewColumn, customColumn], "status");
    expect(columns["status:in_review"]).toEqual(["built-in"]);
    expect(columns["status:awaiting_response"]).toEqual(["custom"]);
  });

  it("treats the card as already in the column it is drawn in", () => {
    expect(issueMatchesGroup(custom, customColumn)).toBe(true);
    expect(issueMatchesGroup(custom, inReviewColumn)).toBe(false);
    expect(issueMatchesGroup(custom, todoColumn)).toBe(false);
  });

  // A status change starts an agent run, so a same-column reorder that rewrote
  // `awaiting_response` to `in_review` would be a silent, side-effecting edit.
  it("reorders within the column without rewriting the status", () => {
    expect(getMoveUpdates(customColumn, 5, custom)).toEqual({ position: 5 });
  });

  it("still sets the status when the card moves to another column", () => {
    expect(getMoveUpdates(doneColumn, 5, custom)).toEqual({ status: "done", position: 5 });
  });

  it("sets the status when the caller has no issue to compare", () => {
    expect(getMoveUpdates(inReviewColumn, 5)).toEqual({ status: "in_review", position: 5 });
  });

  // A built-in card in its own column keeps carrying the (unchanged) key, so a
  // workspace without custom statuses sends exactly the payload it always did.
  it("keeps the status in the payload for a built-in card", () => {
    expect(getMoveUpdates(inReviewColumn, 5, builtIn)).toEqual({ position: 5 });
  });
});

describe("property grouping", () => {
  const propertyId = "prop-env";
  const withValue = { id: "A", properties: { [propertyId]: "opt-staging" } } as unknown as Issue;
  const withoutValue = { id: "B", properties: {} } as unknown as Issue;

  it("getIssueGroupId buckets by option id, no-value issues into the none column", () => {
    expect(getIssueGroupId(withValue, `property:${propertyId}`)).toBe(
      propertyGroupId(propertyId, "opt-staging"),
    );
    expect(getIssueGroupId(withoutValue, `property:${propertyId}`)).toBe(
      propertyGroupId(propertyId, null),
    );
  });

  it("issueMatchesGroup distinguishes option and no-value columns", () => {
    const optionColumn = { id: "c1", title: "Staging", propertyId, propertyOptionId: "opt-staging" };
    const noneColumn = { id: "c2", title: "No value", propertyId, propertyOptionId: null };
    expect(issueMatchesGroup(withValue, optionColumn)).toBe(true);
    expect(issueMatchesGroup(withValue, noneColumn)).toBe(false);
    expect(issueMatchesGroup(withoutValue, noneColumn)).toBe(true);
  });

  it("unknown option values bucket into the none column when the catalog is known", () => {
    const stale = { id: "C", properties: { [propertyId]: "opt-deleted" } } as unknown as Issue;
    const known = new Set(["opt-staging"]);
    expect(getIssueGroupId(stale, `property:${propertyId}`, known)).toBe(
      propertyGroupId(propertyId, null),
    );
    // Without the catalog, the raw bucket is preserved (caller may still map it).
    expect(getIssueGroupId(stale, `property:${propertyId}`)).toBe(
      propertyGroupId(propertyId, "opt-deleted"),
    );
  });

  it("getMoveUpdates for property columns only carries position", () => {
    expect(getMoveUpdates({ id: "c1", title: "Staging", propertyId, propertyOptionId: "opt-staging" }, 5)).toEqual({ position: 5 });
  });
});

describe("project grouping", () => {
  const inProject = { id: "A", project_id: "proj-1" } as unknown as Issue;
  const noProject = { id: "B", project_id: null } as unknown as Issue;
  const projectColumn: BoardColumnGroup = {
    id: projectGroupId("proj-1"),
    title: "Acme",
    projectId: "proj-1",
  };
  const noProjectColumn: BoardColumnGroup = {
    id: projectGroupId(null),
    title: "No project",
    projectId: null,
  };

  it("projectGroupId mirrors the server group keys", () => {
    // The board builds column ids from cards while the server builds them from
    // descriptors; a mismatch silently drops every card off the board.
    expect(projectGroupId("proj-1")).toBe("project:proj-1");
    expect(projectGroupId(null)).toBe("project:none");
  });

  it("getIssueGroupId buckets unassigned-project cards into the none column", () => {
    expect(getIssueGroupId(inProject, "project")).toBe(projectGroupId("proj-1"));
    expect(getIssueGroupId(noProject, "project")).toBe(projectGroupId(null));
  });

  it("issueMatchesGroup distinguishes project and no-project columns", () => {
    expect(issueMatchesGroup(inProject, projectColumn)).toBe(true);
    expect(issueMatchesGroup(inProject, noProjectColumn)).toBe(false);
    expect(issueMatchesGroup(noProject, noProjectColumn)).toBe(true);
    expect(issueMatchesGroup(noProject, projectColumn)).toBe(false);
  });

  it("getMoveUpdates writes the column's project, clearing it on the none column", () => {
    expect(getMoveUpdates(projectColumn, 5)).toEqual({
      project_id: "proj-1",
      position: 5,
    });
    expect(getMoveUpdates(noProjectColumn, 5)).toEqual({
      project_id: null,
      position: 5,
    });
  });

  it("a project column never falls through to the unassigned-assignee update", () => {
    // projectId/assigneeId are both optional on BoardColumnGroup, so an
    // unguarded project column would read as "no assignee" and unassign the
    // card on every drop.
    expect(getMoveUpdates(projectColumn, 5)).not.toHaveProperty("assignee_type");
  });

  it("buildColumns places cards into their project column", () => {
    expect(
      buildColumns([inProject, noProject], [projectColumn, noProjectColumn], "project"),
    ).toEqual({
      [projectGroupId("proj-1")]: ["A"],
      [projectGroupId(null)]: ["B"],
    });
  });
});
