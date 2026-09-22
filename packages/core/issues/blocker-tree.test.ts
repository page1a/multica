// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Issue } from "../types";
import { deriveBlockerTree, orderBlockerRootCauses } from "./blocker-tree";

function issue(id: string, overrides: Partial<Issue> = {}): Issue {
  return {
    id, workspace_id: "w", number: 1, identifier: id, title: id,
    description: null, status: "todo", priority: "none", assignee_type: null,
    assignee_id: null, reviewer_type: null, reviewer_id: null, creator_type: "member", creator_id: "u", parent_issue_id: null,
    project_id: null, position: 0, stage: null, start_date: null, due_date: null,
    metadata: {}, properties: {}, created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z", ...overrides,
  };
}

describe("deriveBlockerTree", () => {
  it("marks an attributed blocked child ROOT and its parent PROPAGATED", () => {
    const child = issue("DENE-2", { parent_issue_id: "DENE-1", stage: 1, metadata: {
      "close.conclusion": "blocked", "close.block_kind": "decision", "close.block_action": "decide",
    }});
    const root = issue("DENE-1");
    const result = deriveBlockerTree(root, { childrenByParent: new Map([[root.id, [child]]]) });
    expect(result.state).toBe("PROPAGATED");
    expect(result.rootCauses.map((x) => x.id)).toEqual([child.id]);
    expect(result.nodes.get(child.id)?.state).toBe("ROOT");
  });

  it("does not call a normal in_review hand-off a blocker", () => {
    const root = issue("DENE-1", { status: "in_review", metadata: {
      "close.conclusion": "awaiting_review", "close.status": "in_review",
      "close.at": "2026-09-16T00:00:00Z", "close.next_owner_type": "member",
    }});
    expect(deriveBlockerTree(root, { now: "2026-09-16T12:00:00Z" }).state).toBe("CLEAR");
  });

  it("treats a dependency whose target is done as a derived ROOT", () => {
    const target = issue("DENE-9", { status: "done" });
    const waiting = issue("DENE-2", { metadata: { "close.waiting_on": target.identifier } });
    const result = deriveBlockerTree(waiting, { issueByIdentifier: { [target.identifier]: target } });
    expect(result.state).toBe("ROOT");
    expect(result.derived).toBe(true);
    expect(result.rootCauses.map((x) => x.id)).toEqual([waiting.id]);
  });

  it("does not count capacity blockers as requiring a human action", () => {
    const root = issue("DENE-1", { metadata: {
      "close.conclusion": "blocked", "close.block_kind": "capacity", "close.block_action": "wait",
    }});
    expect(deriveBlockerTree(root).userActionCount).toBe(0);
  });

  it("keeps a non-terminal waiting dependency visible even when its target is clear", () => {
    const target = issue("DENE-2");
    const waiting = issue("DENE-1", { metadata: {
      "close.conclusion": "blocked", "close.block_kind": "dependency", "close.waiting_on": target.identifier,
    }});
    const result = deriveBlockerTree(waiting, { issueByIdentifier: { [target.identifier]: target } });
    expect(result.state).toBe("PROPAGATED");
    expect(result.rootCauses.map((x) => x.identifier)).toContain(target.identifier);
  });

  it("retains a dependency reference when the target snapshot is missing", () => {
    const waiting = issue("DENE-1", { metadata: { "close.waiting_on": "DENE-2" } });
    const result = deriveBlockerTree(waiting);
    expect(result.state).toBe("PROPAGATED");
    expect(result.rootCauses).toEqual([{ id: "DENE-2", identifier: "DENE-2" }]);
  });

  it("attributes a terminal dependency as a wake_missed root", () => {
    const target = issue("DENE-2", { status: "cancelled" });
    const waiting = issue("DENE-1", { metadata: { "close.waiting_on": target.identifier } });
    const result = deriveBlockerTree(waiting, { issueByIdentifier: { [target.identifier]: target } });
    expect(result.state).toBe("ROOT");
    expect(result.rootCauses.map((x) => x.id)).toEqual([waiting.id]);
  });

  it("preserves cycle attribution in the root result and node memo", () => {
    const a = issue("DENE-1", { metadata: { "close.waiting_on": "DENE-2" } });
    const b = issue("DENE-2", { metadata: { "close.waiting_on": "DENE-1" } });
    const result = deriveBlockerTree(a, { issueByIdentifier: { [a.identifier]: a, [b.identifier]: b } });
    expect(result.cycle).toBe(true);
    expect(result.state).toBe("ROOT");
    expect(result.nodes.get(a.id)?.cycle).toBe(true);
    expect(result.attribution?.kind).toBe("cycle");
  });

  it("resolves the recorded block attribution onto the node, once", () => {
    const child = issue("DENE-2", { parent_issue_id: "DENE-1", stage: 2, metadata: {
      "close.conclusion": "blocked", "close.block_kind": "decision",
      "close.block_action": "pick a table", "close.next_owner_type": "member",
      "close.next_owner_id": "member-1", "close.at": "2026-09-15T00:00:00Z",
    }});
    const root = issue("DENE-1");
    const result = deriveBlockerTree(root, { childrenByParent: new Map([[root.id, [child]]]) });
    expect(result.nodes.get(child.id)?.attribution).toEqual({
      kind: "decision", action: "pick a table", needsUserAction: true,
      nextOwnerType: "member", nextOwnerId: "member-1", waitingOn: null, at: "2026-09-15T00:00:00Z",
    });
    expect(result.nodes.get(child.id)?.stage).toBe(2);
    // A propagated parent owns no blocker, so it carries no action to show.
    expect(result.nodes.get(root.id)?.attribution).toBeNull();
  });

  it("derives review_overdue past the threshold core owns", () => {
    const child = issue("DENE-2", { parent_issue_id: "DENE-1", status: "in_review", metadata: {
      "close.conclusion": "awaiting_review", "close.status": "in_review",
      "close.next_owner_type": "member", "close.next_owner_id": "member-1",
      "close.at": "2026-09-15T00:00:00Z",
    }});
    const root = issue("DENE-1");
    // 36h later — past the 24h human review threshold, and a member owns it.
    const result = deriveBlockerTree(root, {
      childrenByParent: new Map([[root.id, [child]]]),
      now: "2026-09-16T12:00:00Z",
    });
    expect(result.nodes.get(child.id)?.attribution?.kind).toBe("review_overdue");
    expect(result.nodes.get(child.id)?.attribution?.needsUserAction).toBe(true);
    expect(result.userActionCount).toBe(1);
  });

  it("keeps the waited-on ticket on a wake_missed attribution", () => {
    const target = issue("DENE-2", { status: "done" });
    const waiting = issue("DENE-1", { metadata: { "close.waiting_on": target.identifier } });
    const result = deriveBlockerTree(waiting, { issueByIdentifier: { [target.identifier]: target } });
    expect(result.attribution?.kind).toBe("wake_missed");
    expect(result.attribution?.waitingOn).toBe(target.identifier);
  });
});

describe("orderBlockerRootCauses", () => {
  const blocked = (kind: string, at?: string) => ({
    "close.conclusion": "blocked", "close.block_kind": kind, "close.block_action": `${kind} now`,
    ...(at ? { "close.at": at } : {}),
  });

  it("pulls a needs-you root cause above an older one that needs nobody", () => {
    const parent = issue("DENE-1");
    const waiting = issue("DENE-2", { parent_issue_id: parent.id, stage: 1, metadata: blocked("capacity", "2026-09-01T00:00:00Z") });
    const decide = issue("DENE-3", { parent_issue_id: parent.id, stage: 1, metadata: blocked("decision", "2026-09-15T00:00:00Z") });
    const tree = deriveBlockerTree(parent, { childrenByParent: new Map([[parent.id, [waiting, decide]]]) });
    // Frontier order is the children order; §3.3 reorders the summary rows.
    expect(tree.rootCauses.map((x) => x.identifier)).toEqual(["DENE-2", "DENE-3"]);
    expect(orderBlockerRootCauses(tree).map((x) => x.identifier)).toEqual(["DENE-3", "DENE-2"]);
  });

  it("orders the rest by how long they have been blocked", () => {
    const parent = issue("DENE-1");
    const newer = issue("DENE-2", { parent_issue_id: parent.id, stage: 1, metadata: blocked("capacity", "2026-09-10T00:00:00Z") });
    const older = issue("DENE-3", { parent_issue_id: parent.id, stage: 1, metadata: blocked("capacity", "2026-09-01T00:00:00Z") });
    const untimed = issue("DENE-4", { parent_issue_id: parent.id, stage: 1, metadata: blocked("capacity") });
    const tree = deriveBlockerTree(parent, { childrenByParent: new Map([[parent.id, [newer, older, untimed]]]) });
    expect(orderBlockerRootCauses(tree).map((x) => x.identifier)).toEqual(["DENE-3", "DENE-2", "DENE-4"]);
  });

  it("breaks an equal close.at tie by stage", () => {
    const at = "2026-09-15T00:00:00Z";
    const parent = issue("DENE-1");
    // Two sibling carriers, so both deep root causes sit in the frontier and
    // their own stages differ from each other.
    const first = issue("DENE-2", { parent_issue_id: parent.id, stage: 1 });
    const second = issue("DENE-3", { parent_issue_id: parent.id, stage: 1 });
    const lateStage = issue("DENE-4", { parent_issue_id: first.id, stage: 9, metadata: blocked("capacity", at) });
    const earlyStage = issue("DENE-5", { parent_issue_id: second.id, stage: 2, metadata: blocked("capacity", at) });
    const tree = deriveBlockerTree(parent, { childrenByParent: new Map([[parent.id, [first, second]], [first.id, [lateStage]], [second.id, [earlyStage]]]) });
    expect(tree.rootCauses.map((x) => x.identifier)).toEqual(["DENE-4", "DENE-5"]);
    expect(orderBlockerRootCauses(tree).map((x) => x.identifier)).toEqual(["DENE-5", "DENE-4"]);
  });
});
