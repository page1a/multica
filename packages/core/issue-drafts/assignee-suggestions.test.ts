// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueDraftPayload } from "../types";
import {
  ISSUE_DRAFT_ROOT_ROW,
  applyIssueDraftAssigneeSuggestions,
  issueDraftSuggestionPhase,
  issueDraftSuggestionRequest,
  type DraftAssigneeSuggestion,
} from "./assignee-suggestions";

function draft(patch: Partial<IssueDraftPayload> = {}): IssueDraftPayload {
  return {
    title: "Ship the thing",
    description: "parent body",
    status: "todo",
    priority: "none",
    assignee_type: null,
    assignee_id: null,
    project_id: "project-1",
    parent_issue_id: null,
    children: [
      { key: "c1", title: "backend", description: "api", status: "todo", priority: "none" },
      { key: "c2", title: "frontend", description: "page", status: "todo", priority: "none" },
    ],
    ...patch,
  };
}

const seat = (id: string): DraftAssigneeSuggestion => ({
  assignee_type: "agent",
  assignee_id: id,
  name: id,
  tier: "strong",
});

describe("issueDraftSuggestionRequest", () => {
  it("lists the root first, then children in order", () => {
    const request = issueDraftSuggestionRequest(draft());
    expect(request?.project_id).toBe("project-1");
    expect(request?.rows.map((row) => row.title)).toEqual(["Ship the thing", "backend", "frontend"]);
    expect(request?.rows.map((row) => row.has_children)).toEqual([true, false, false]);
  });

  it("asks nothing when there is no draft, no title, or nothing unassigned", () => {
    expect(issueDraftSuggestionRequest(null)).toBeNull();
    expect(issueDraftSuggestionRequest(draft({ title: "  " }))).toBeNull();
    const assigned = draft({ assignee_type: "agent", assignee_id: "a" });
    assigned.children = assigned.children?.map((child) => ({
      ...child,
      assignee_type: "member",
      assignee_id: "m",
    }));
    expect(issueDraftSuggestionRequest(assigned)).toBeNull();
  });
});

describe("applyIssueDraftAssigneeSuggestions", () => {
  it("fills unassigned rows and leaves a row routing had no answer for empty", () => {
    const next = applyIssueDraftAssigneeSuggestions(draft(), [seat("root-seat"), null, seat("fe-seat")], new Set());
    expect(next.assignee_id).toBe("root-seat");
    expect(next.assignee_type).toBe("agent");
    expect(next.children?.[0]?.assignee_id ?? null).toBeNull();
    expect(next.children?.[1]?.assignee_id).toBe("fe-seat");
  });

  it("never overwrites an assignee a person already picked", () => {
    const picked = draft({ assignee_type: "member", assignee_id: "kun" });
    const next = applyIssueDraftAssigneeSuggestions(picked, [seat("root-seat"), null, null], new Set());
    expect(next).toBe(picked);
  });

  it("offers each row once, so a cleared suggestion stays cleared", () => {
    const offered = new Set<string>();
    const first = applyIssueDraftAssigneeSuggestions(draft(), [seat("root-seat"), seat("be-seat"), null], offered);
    expect(offered.has(ISSUE_DRAFT_ROOT_ROW)).toBe(true);
    const cleared: IssueDraftPayload = { ...first, assignee_type: null, assignee_id: null };
    const again = applyIssueDraftAssigneeSuggestions(cleared, [seat("root-seat"), seat("be-seat"), null], offered);
    expect(again).toBe(cleared);
  });

  it("refuses an answer that does not line up with the rows", () => {
    // The rows are read by index, so a short or padded array would put a seat
    // on a row the answer never described. Refusing it whole is the only safe
    // reading — "apply what we can" would silently assign the wrong row.
    const single = draft({ children: [] });
    const tooLong = applyIssueDraftAssigneeSuggestions(
      single,
      [seat("root-seat"), seat("ghost")],
      new Set(),
    );
    expect(tooLong).toBe(single);
    expect(tooLong.assignee_id).toBeNull();

    const tooShort = applyIssueDraftAssigneeSuggestions(
      draft(),
      [seat("root-seat")],
      new Set(),
    );
    expect(tooShort.assignee_id).toBeNull();
    expect(tooShort.children?.[0]?.assignee_id ?? null).toBeNull();
  });

  it("does not remember rows it refused to offer", () => {
    // A refused answer is not an offer: the row must still be fillable when a
    // well-formed answer arrives next.
    const offered = new Set<string>();
    applyIssueDraftAssigneeSuggestions(draft(), [seat("root-seat")], offered);
    expect(offered.size).toBe(0);
    const next = applyIssueDraftAssigneeSuggestions(
      draft(),
      [seat("root-seat"), seat("be-seat"), null],
      offered,
    );
    expect(next.assignee_id).toBe("root-seat");
  });
});

describe("issueDraftSuggestionPhase", () => {
  const request = issueDraftSuggestionRequest(draft());

  it("is idle when there is nothing to ask", () => {
    expect(
      issueDraftSuggestionPhase({
        request: null,
        data: undefined,
        isError: false,
        isPending: false,
      }),
    ).toBe("idle");
  });

  it("is loading until an answer arrives", () => {
    expect(
      issueDraftSuggestionPhase({
        request,
        data: undefined,
        isError: false,
        isPending: true,
      }),
    ).toBe("loading");
  });

  it("is failed when the ask failed", () => {
    expect(
      issueDraftSuggestionPhase({
        request,
        data: undefined,
        isError: true,
        isPending: false,
      }),
    ).toBe("failed");
  });

  it("is ready only for one answer per row", () => {
    expect(
      issueDraftSuggestionPhase({
        request,
        data: [seat("a"), null, null],
        isError: false,
        isPending: false,
      }),
    ).toBe("ready");
    expect(
      issueDraftSuggestionPhase({
        request,
        data: [seat("a"), null],
        isError: false,
        isPending: false,
      }),
    ).toBe("failed");
    expect(
      issueDraftSuggestionPhase({
        request,
        data: [],
        isError: false,
        isPending: false,
      }),
    ).toBe("failed");
  });
});
