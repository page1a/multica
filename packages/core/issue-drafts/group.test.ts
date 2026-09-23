// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { IssueDraftChild, IssueDraftPayload } from "../types";
import {
  ISSUE_DRAFT_COORDINATOR_STATUS,
  issueDraftChildStatus,
  issueDraftCreatedGroup,
  issueDraftLandingIssueId,
  issueDraftNodeRunsOnCreate,
  issueDraftParentIssueId,
  maxIssueDraftChildStage,
  mintIssueDraftChildKeys,
  normalizeIssueDraftChildren,
  normalizeIssueDraftPayloadGroup,
  planIssueDraftGroup,
  planIssueDraftGroupProgress,
  sameIssueDraftChildren,
} from "./group";

/**
 * The group rules the preview and the confirm must agree on.
 *
 * They are pure and live here rather than in the panel because both halves of
 * the promise are decided by them: what the panel SHOWS ("3 issues, 1 starts
 * now") and what the server is asked to CREATE are derived from the same
 * normalization, and a divergence between the two is a confirm that does
 * something other than what the user just approved.
 */

function child(overrides: Partial<IssueDraftChild> = {}): IssueDraftChild {
  return {
    key: "c1",
    title: "A sub-issue",
    description: "",
    status: "",
    priority: "",
    assignee_type: null,
    assignee_id: null,
    stage: null,
    assignee_hint: null,
    ...overrides,
  };
}

function payload(overrides: Partial<IssueDraftPayload> = {}): IssueDraftPayload {
  return {
    title: "Parent",
    description: "",
    status: "",
    priority: "",
    ...overrides,
  };
}

describe("issueDraftChildStatus", () => {
  it("starts the first stage and parks the rest", () => {
    expect(issueDraftChildStatus(1)).toBe("todo");
    expect(issueDraftChildStatus(2)).toBe("backlog");
    expect(issueDraftChildStatus(9)).toBe("backlog");
  });

  it("treats an unstaged sub-issue as the implicit single stage", () => {
    // A group nobody staged is not a group that waits: it is one stage, and
    // everything in it is meant to run.
    expect(issueDraftChildStatus(null)).toBe("todo");
    expect(issueDraftChildStatus(undefined)).toBe("todo");
  });
});

describe("normalizeIssueDraftChildStages", () => {
  it("leaves an unstaged group unstaged", () => {
    const children = [child({ key: "a" }), child({ key: "b" })];
    expect(
      normalizeIssueDraftChildren(children).map((c) => c.stage),
    ).toEqual([null, null]);
  });

  it("fills stage 1 for the members a staged group left unstaged", () => {
    // An unstaged sibling in a staged group falls out of the stage barrier
    // entirely — it neither holds a stage back nor is woken by one — so it
    // would be dispatched by nobody. Stage 1 is the only safe answer.
    const children = [
      child({ key: "a", stage: 2 }),
      child({ key: "b", stage: null }),
    ];
    expect(normalizeIssueDraftChildren(children).map((c) => c.stage)).toEqual([
      2, 1,
    ]);
  });

  it("renumbers by order of appearance, closing gaps", () => {
    const children = [
      child({ key: "a", stage: 3 }),
      child({ key: "b", stage: 1 }),
      child({ key: "c", stage: 3 }),
    ];
    expect(normalizeIssueDraftChildren(children).map((c) => c.stage)).toEqual([
      2, 1, 2,
    ]);
  });

  it("does not promote a group whose only stage is a later one", () => {
    // Stage 2 alone means "this waits". Squashing it to stage 1 while closing
    // gaps would silently turn a parked sub-issue into one that runs the moment
    // it is created — the opposite of what the carrier said.
    const children = [child({ key: "a", stage: 2 }), child({ key: "b", stage: 3 })];
    const normalized = normalizeIssueDraftChildren(children);
    expect(normalized.map((c) => c.stage)).toEqual([2, 3]);
    expect(normalized.map((c) => c.status)).toEqual(["backlog", "backlog"]);
  });

  it("treats a stage outside the server's range as no stage at all", () => {
    // The server refuses stage > 20 outright, so a payload carrying one must be
    // repaired here rather than 400 at confirm time. With nothing else staged,
    // the group is simply unstaged.
    const children = [child({ key: "a", stage: 21 }), child({ key: "b", stage: 0 })];
    const normalized = normalizeIssueDraftChildren(children);
    expect(normalized.map((c) => c.stage)).toEqual([null, null]);
    expect(normalized.map((c) => c.status)).toEqual(["todo", "todo"]);
  });

  it("fills an unusable stage into a staged group like any missing one", () => {
    // The documented rule for a sub-issue with no usable stage in a staged
    // group is stage 1: an unprestaged sibling would fall out of the barrier
    // and be dispatched by nobody.
    const children = [child({ key: "a", stage: 21 }), child({ key: "b", stage: 3 })];
    const normalized = normalizeIssueDraftChildren(children);
    expect(normalized.map((c) => c.stage)).toEqual([1, 2]);
    expect(normalized.map((c) => c.status)).toEqual(["todo", "backlog"]);
  });

  it("keeps a high but contiguous range where it is", () => {
    const children = [18, 19, 20].map((stage, index) =>
      child({ key: `c${index + 1}`, stage }),
    );
    expect(normalizeIssueDraftChildren(children).map((c) => c.stage)).toEqual([
      18, 19, 20,
    ]);
  });
});

describe("mintIssueDraftChildKeys", () => {
  it("keeps the keys that are already usable", () => {
    const children = [child({ key: "c1" }), child({ key: "design" })];
    expect(mintIssueDraftChildKeys(children).map((c) => c.key)).toEqual([
      "c1",
      "design",
    ]);
  });

  it("mints a key for a sub-issue that arrived without one", () => {
    const children = [child({ key: "" }), child({ key: "  " })];
    expect(mintIssueDraftChildKeys(children).map((c) => c.key)).toEqual([
      "c1",
      "c2",
    ]);
  });

  it("repairs a duplicate without stealing the first one's identity", () => {
    // Two nodes with one key derive one origin_id: the second insert collides
    // inside the create transaction, which is a 500 rather than a sentence the
    // user can act on.
    const children = [child({ key: "c1" }), child({ key: "c1" })];
    expect(mintIssueDraftChildKeys(children).map((c) => c.key)).toEqual([
      "c1",
      "c2",
    ]);
  });

  it("replaces a key the server would refuse as too long", () => {
    const children = [child({ key: "k".repeat(65) })];
    expect(mintIssueDraftChildKeys(children)[0]?.key).toBe("c1");
  });

  it("trims a key so the stored key is the one identity was derived from", () => {
    const children = [child({ key: " c1 " })];
    expect(mintIssueDraftChildKeys(children)[0]?.key).toBe("c1");
  });
});

describe("normalizeIssueDraftChildren", () => {
  it("writes the status each stage implies", () => {
    const children = [
      child({ key: "a", stage: 1 }),
      child({ key: "b", stage: 2 }),
    ];
    expect(normalizeIssueDraftChildren(children).map((c) => c.status)).toEqual([
      "todo",
      "backlog",
    ]);
  });

  it("overrides a stored status that contradicts the stage", () => {
    // Stage is the only thing the user edits; the status is the derived half.
    // Keeping a stale one would dispatch by the wrong rule.
    const children = [child({ key: "a", stage: 3, status: "todo" })];
    expect(normalizeIssueDraftChildren(children)[0]?.status).toBe("backlog");
  });

  it("defaults a missing priority", () => {
    expect(
      normalizeIssueDraftChildren([child({ priority: "  " })])[0]?.priority,
    ).toBe("none");
  });
});

describe("normalizeIssueDraftPayloadGroup", () => {
  it("returns the same payload when there is nothing to normalize", () => {
    const draft = payload({
      status: ISSUE_DRAFT_COORDINATOR_STATUS,
      children: [child({ key: "a", stage: 1, status: "todo", priority: "none" })],
    });
    expect(normalizeIssueDraftPayloadGroup(draft)).toBe(draft);
  });

  it("leaves a payload with no children alone", () => {
    const draft = payload();
    expect(normalizeIssueDraftPayloadGroup(draft)).toBe(draft);
    // A single issue keeps whatever status its author chose, including the
    // parking lot: there is no group for it to coordinate.
    const parked = payload({ status: "backlog" });
    expect(normalizeIssueDraftPayloadGroup(parked)).toBe(parked);
  });

  it("normalizes the group when it needs it", () => {
    const draft = payload({ children: [child({ key: "", stage: 2 })] });
    const normalized = normalizeIssueDraftPayloadGroup(draft);
    expect(normalized.children?.[0]?.key).toBe("c1");
    expect(normalized.children?.[0]?.status).toBe("backlog");
  });

  it("writes the coordinator status onto a root that has sub-issues", () => {
    // The stored draft is what the confirm reads back (it sends a revision, not
    // a payload), so a root the preview calls a coordinator has to BE one in
    // the payload too. Backlog is the interesting input: it is what the root
    // used to be parked in, and it is the status the stage barrier refuses to
    // wake.
    for (const status of ["", "todo", "backlog"]) {
      const draft = payload({
        status,
        children: [child({ key: "a", status: "todo", priority: "none" })],
      });
      const normalized = normalizeIssueDraftPayloadGroup(draft);
      expect(normalized.status).toBe(ISSUE_DRAFT_COORDINATOR_STATUS);
      // The children are untouched when they were already normalized, so the
      // root's status change alone must not rebuild them.
      expect(normalized.children).toBe(draft.children);
    }
  });
});

describe("issueDraftNodeRunsOnCreate", () => {
  it("runs a todo node with an agent assignee", () => {
    expect(
      issueDraftNodeRunsOnCreate({
        status: "todo",
        assignee_type: "agent",
        assignee_id: "a1",
      }),
    ).toBe(true);
  });

  it("runs a todo node with a squad assignee — the server wakes its leader", () => {
    expect(
      issueDraftNodeRunsOnCreate({
        status: "todo",
        assignee_type: "squad",
        assignee_id: "s1",
      }),
    ).toBe(true);
  });

  it("treats an empty status the way the server does", () => {
    expect(
      issueDraftNodeRunsOnCreate({
        status: "",
        assignee_type: "agent",
        assignee_id: "a1",
      }),
    ).toBe(true);
  });

  it("does not run a backlog node", () => {
    expect(
      issueDraftNodeRunsOnCreate({
        status: "backlog",
        assignee_type: "agent",
        assignee_id: "a1",
      }),
    ).toBe(false);
  });

  it("does not run an unassigned node — the parent's normal shape", () => {
    expect(issueDraftNodeRunsOnCreate({ status: "todo" })).toBe(false);
  });

  it("does not call a member assignment a running agent", () => {
    // Assigning a person enqueues nothing; saying "runs now" would tell the
    // user a human had been paged.
    expect(
      issueDraftNodeRunsOnCreate({
        status: "todo",
        assignee_type: "member",
        assignee_id: "u1",
      }),
    ).toBe(false);
  });
});

describe("planIssueDraftGroup", () => {
  it("is empty for a draft that does not exist", () => {
    expect(planIssueDraftGroup(null)).toEqual({
      rows: [],
      total: 0,
      creating: 0,
      starting: 0,
      parked: 0,
      built: 0,
    });
  });

  it("counts an adopted node as neither created nor started", () => {
    // A continuation round: the node already owns an issue, so the confirm
    // skips it and never rewrites its fields. Counting it would promise work
    // the confirm does not do — and "runs immediately" for an issue that is not
    // being created is the same lie one size smaller (DENE-415).
    const plan = planIssueDraftGroup(
      payload({
        children: [
          child({ key: "a", stage: 1, assignee_type: "agent", assignee_id: "ag1" }),
          child({ key: "b", stage: 1, assignee_type: "agent", assignee_id: "ag2" }),
        ],
      }),
      new Set(["a"]),
    );
    expect(plan.total).toBe(3);
    expect(plan.creating).toBe(2);
    expect(plan.starting).toBe(1);
    expect(plan.built).toBe(1);
    expect(plan.rows.map((row) => row.alreadyBuilt)).toEqual([false, true, false]);
  });

  it("counts every node of a first round as being created", () => {
    const plan = planIssueDraftGroup(
      payload({ children: [child({ key: "a", stage: 1 })] }),
      new Set<string>(),
    );
    expect(plan.creating).toBe(plan.total);
    expect(plan.built).toBe(0);
  });

  it("reads a draft with no children as exactly one issue", () => {
    // The old shape is not a legacy branch: it is a group with only its root.
    const plan = planIssueDraftGroup(payload({ title: "One" }));
    expect(plan.total).toBe(1);
    expect(plan.rows[0]?.isRoot).toBe(true);
    expect(plan.parked).toBe(0);
  });

  it("counts the whole group and says which rows start", () => {
    const plan = planIssueDraftGroup(
      payload({
        children: [
          child({ key: "a", stage: 1, assignee_type: "agent", assignee_id: "ag1" }),
          child({ key: "b", stage: 2, assignee_type: "agent", assignee_id: "ag2" }),
        ],
      }),
    );
    expect(plan.total).toBe(3);
    expect(plan.starting).toBe(1);
    expect(plan.parked).toBe(1);
    expect(plan.rows.map((row) => row.startsOnCreate)).toEqual([
      false,
      true,
      false,
    ]);
  });

  it("derives a row's status from its stage, not from the stored value", () => {
    // Mid-edit the panel has moved the stage but nothing has been saved yet;
    // the numbers under the confirm button have to follow the screen.
    const plan = planIssueDraftGroup(
      payload({ children: [child({ key: "a", stage: 2, status: "todo" })] }),
    );
    expect(plan.rows[1]?.status).toBe("backlog");
  });

  it("runs every member of an unstaged group that has an agent", () => {
    const plan = planIssueDraftGroup(
      payload({
        children: [
          child({ key: "a", assignee_type: "agent", assignee_id: "ag1" }),
          child({ key: "b", assignee_type: "agent", assignee_id: "ag2" }),
        ],
      }),
    );
    expect(plan.starting).toBe(2);
    expect(plan.parked).toBe(0);
  });

  it("never counts a coordinator root as starting, even with an agent on it", () => {
    // The root's assignee is the seat the stage barrier wakes, not an
    // executor. Counting it would promise a run that the confirm suppresses.
    const plan = planIssueDraftGroup(
      payload({
        status: "todo",
        assignee_type: "agent",
        assignee_id: "coord",
        children: [
          child({ key: "a", stage: 1, assignee_type: "agent", assignee_id: "ag1" }),
          child({ key: "b", stage: 2, assignee_type: "agent", assignee_id: "ag2" }),
        ],
      }),
    );
    expect(plan.rows[0]?.startsOnCreate).toBe(false);
    expect(plan.rows[0]?.outcome).toBe("coordinates");
    expect(plan.rows[0]?.status).toBe(ISSUE_DRAFT_COORDINATOR_STATUS);
    expect(plan.starting).toBe(1);
    // Backlog is what "waiting for a stage" means, and the coordinator is not
    // waiting for one: it is the node that promotes it.
    expect(plan.parked).toBe(1);
  });

  it("still runs a single-issue root that has an agent", () => {
    const plan = planIssueDraftGroup(
      payload({ status: "todo", assignee_type: "agent", assignee_id: "ag1" }),
    );
    expect(plan.rows[0]?.outcome).toBe("starts");
    expect(plan.starting).toBe(1);
  });

  it("names one outcome per row", () => {
    const plan = planIssueDraftGroup(
      payload({
        children: [
          child({ key: "starts", stage: 1, assignee_type: "agent", assignee_id: "ag1" }),
          child({ key: "squad", stage: 1, assignee_type: "squad", assignee_id: "sq1" }),
          child({ key: "parked", stage: 2, assignee_type: "agent", assignee_id: "ag2" }),
          child({ key: "unassigned", stage: 1 }),
          child({ key: "member", stage: 1, assignee_type: "member", assignee_id: "u1" }),
        ],
      }),
    );
    expect(plan.rows.map((row) => row.outcome)).toEqual([
      "coordinates",
      "starts",
      "starts",
      "parked",
      "unassigned",
      "member",
    ]);
    // An unassigned stage-1 sub-issue is created todo and starts nothing: it is
    // not parked, it is a gap somebody has to fill.
    expect(plan.rows[4]?.status).toBe("todo");
    expect(plan.rows[4]?.startsOnCreate).toBe(false);
    expect(plan.starting).toBe(2);
    expect(plan.parked).toBe(1);
  });
});

describe("sameIssueDraftChildren", () => {
  it("compares the set and every field that decides what is created", () => {
    expect(sameIssueDraftChildren([child()], [child()])).toBe(true);
    expect(sameIssueDraftChildren([child()], [child({ title: "Other" })])).toBe(
      false,
    );
    expect(sameIssueDraftChildren([child()], [child({ stage: 2 })])).toBe(false);
    expect(
      sameIssueDraftChildren(
        [child()],
        [child({ assignee_type: "agent", assignee_id: "ag1" })],
      ),
    ).toBe(false);
    expect(sameIssueDraftChildren([child()], [])).toBe(false);
  });
});

describe("maxIssueDraftChildStage", () => {
  it("is 0 when nothing is staged", () => {
    expect(maxIssueDraftChildStage([child(), child()])).toBe(0);
    expect(maxIssueDraftChildStage([child({ stage: 3 }), child({ stage: 2 })])).toBe(3);
  });
});

describe("issueDraftCreatedGroup", () => {
  it("reports the whole group the confirm answered with", () => {
    const issues = [
      { id: "i1", identifier: "MUL-1", title: "Parent", status: "todo" },
      { id: "i2", identifier: "MUL-2", title: "Child", status: "backlog" },
    ];
    expect(issueDraftCreatedGroup({ issue_id: "i1", issues })).toBe(issues);
  });

  it("degrades to the root alone for a backend that predates groups", () => {
    // No `issues` at all means "the group is the one issue named by issue_id",
    // which is exactly what a group with no sub-issues is.
    expect(issueDraftCreatedGroup({ issue_id: "i1" })).toEqual([
      {
        id: "i1",
        identifier: "",
        title: "",
        status: "",
        stage: null,
        assignee_type: null,
        assignee_id: null,
        parent_issue_id: null,
      },
    ]);
  });

  it("degrades to the root alone when the reported group is empty", () => {
    expect(issueDraftCreatedGroup({ issue_id: "i1", issues: [] })).toHaveLength(1);
  });

  it("does not invent a parent on the row it fabricates", () => {
    // The group's real parent is the issue an alignment started mid-flight was
    // filed under, and it is read off the DRAFT (`issueDraftParentIssueId`) —
    // never off this row. The fallback exists to say "the server told us
    // nothing beyond this id", so a row that named a parent would be asserting
    // a relationship no response ever reported (DENE-452).
    expect(issueDraftCreatedGroup({ issue_id: "i1" })[0]!.parent_issue_id).toBeNull();
    expect(
      issueDraftCreatedGroup({
        issue_id: "i1",
        issues: [
          {
            id: "i1",
            identifier: "MUL-1",
            title: "Filed under MUL-9",
            status: "todo",
            parent_issue_id: "parent-1",
          },
        ],
      })[0]!.parent_issue_id,
    ).toBe("parent-1");
  });
});

describe("issueDraftParentIssueId", () => {
  it("reads the issue an alignment started mid-flight was filed under", () => {
    expect(issueDraftParentIssueId({ parent_issue_id: "parent-1" })).toBe("parent-1");
  });

  it("is null for a standalone alignment and for a backend that sends nothing", () => {
    // An alignment started from nowhere in particular founds its own top-level
    // issue; only an entry point that had an issue at hand writes the field.
    expect(issueDraftParentIssueId({})).toBeNull();
    expect(issueDraftParentIssueId({ parent_issue_id: null })).toBeNull();
    // Empty string is absent, the same way the server reads it: addressing an
    // issue by "" is not a state worth representing.
    expect(issueDraftParentIssueId({ parent_issue_id: "" })).toBeNull();
    expect(issueDraftParentIssueId(null)).toBeNull();
  });
});

describe("issueDraftLandingIssueId", () => {
  it("lands back on the issue the alignment was started from", () => {
    // A group started from an existing issue is that issue's children. The
    // person was working in the parent's context, so the parent is where the
    // confirm puts them back (DENE-452).
    expect(issueDraftLandingIssueId("parent-1", "root-1")).toBe("parent-1");
  });

  it("lands on the root it created when there was no parent", () => {
    // A standalone alignment has no context to return to, which is exactly what
    // this returned before the parent case existed.
    expect(issueDraftLandingIssueId(null, "root-1")).toBe("root-1");
  });

  it("has nowhere to land until a confirm has produced something", () => {
    expect(issueDraftLandingIssueId(null, null)).toBeNull();
  });
});

describe("planIssueDraftGroupProgress", () => {
  const issue = (stage: number | null, status: string) => ({ stage, status });
  const done = (i: { status: string }) => i.status === "done";

  it("counts the group and names the stage being worked on", () => {
    // Reading it off the board's own two signals — the stage an issue carries
    // and whether its status is done — is what keeps this line from disagreeing
    // with the group itself. (DENE-415)
    const progress = planIssueDraftGroupProgress(
      [
        issue(null, "done"), // the root: a parent has no stage
        issue(1, "done"),
        issue(1, "todo"),
        issue(2, "backlog"),
      ],
      done,
    );
    expect(progress.total).toBe(4);
    expect(progress.done).toBe(2);
    expect(progress.stages).toBe(2);
    expect(progress.activeStage).toBe(1);
  });

  it("falls back to the stage that is waiting when nothing is running", () => {
    const progress = planIssueDraftGroupProgress(
      [issue(1, "done"), issue(2, "backlog")],
      done,
    );
    expect(progress.activeStage).toBe(2);
  });

  it("reports no active stage once the group is finished", () => {
    const progress = planIssueDraftGroupProgress([issue(1, "done")], done);
    expect(progress.done).toBe(1);
    expect(progress.activeStage).toBeNull();
  });

  it("treats an unstaged group as one implicit stage", () => {
    const progress = planIssueDraftGroupProgress([issue(null, "todo")], done);
    expect(progress.stages).toBe(0);
    expect(progress.activeStage).toBe(1);
  });
});
