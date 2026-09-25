// @vitest-environment node
import { describe, expect, it } from "vitest";
import { ApiError } from "@multica/core/api";
import type { Agent, AgentSkillSummary } from "@multica/core/types";
import {
  activeChildrenOf,
  agentHasChildrenNames,
  baseRoleOptions,
  baseRoleOptionsFor,
  canChangeBaseRole,
  canEditRuntimeProfile,
  childrenOf,
  composeEffectiveInstructions,
  followsBaseRoleExecutionConfig,
  hasInheritedPrompt,
  inheritedPromptReadState,
  inheritedSkillChips,
  isAgentHasChildrenError,
  isBaseRole,
  isSpecialization,
  runtimeInheritanceState,
  solidifyTargets,
  specializationCount,
} from "./specialization";

function agent(overrides: Partial<Agent>): Agent {
  return {
    id: "agent",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: null,
    skills: [],
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function skill(id: string, name = id): AgentSkillSummary {
  return { id, name, description: "" };
}

describe("specialization relationship", () => {
  it("treats an absent parent field exactly like an empty one", () => {
    // Older backends omit the field entirely; that must read as a base role
    // rather than "unknown", which is what the flat list assumes.
    expect(isSpecialization(agent({}))).toBe(false);
    expect(isSpecialization(agent({ parent_agent_id: "" }))).toBe(false);
    expect(isSpecialization(agent({ parent_agent_id: "base-1" }))).toBe(true);
    expect(isBaseRole(agent({}))).toBe(true);
    expect(isBaseRole(agent({ parent_agent_id: "base-1" }))).toBe(false);
  });

  it("selects children by parent id, keeping archived ones distinguishable", () => {
    const agents = [
      agent({ id: "base-1", name: "Base" }),
      agent({ id: "child-1", parent_agent_id: "base-1" }),
      agent({
        id: "child-2",
        parent_agent_id: "base-1",
        archived_at: "2026-02-01T00:00:00Z",
      }),
      agent({ id: "child-3", parent_agent_id: "base-2" }),
    ];
    expect(childrenOf(agents, "base-1").map((a) => a.id)).toEqual([
      "child-1",
      "child-2",
    ]);
    expect(activeChildrenOf(agents, "base-1").map((a) => a.id)).toEqual([
      "child-1",
    ]);
    expect(childrenOf(agents, "")).toEqual([]);
  });

  it("counts a base role from child_count, falling back to visible children", () => {
    // child_count is workspace-wide (and active-only), so it stays right when
    // filters hide some children.
    expect(specializationCount({ parent_agent_id: "", child_count: 3 }, 1)).toBe(
      3,
    );
    expect(specializationCount({ parent_agent_id: "", child_count: 0 }, 2)).toBe(
      0,
    );
    // Absent (older backend, or a payload that failed that one field).
    expect(specializationCount({ parent_agent_id: "" }, 2)).toBe(2);
    // Never counts a specialisation's own (impossible) children.
    expect(
      specializationCount({ parent_agent_id: "base-1", child_count: 4 }, 0),
    ).toBe(0);
  });

  it("offers live base roles only, sorted by name", () => {
    const agents = [
      agent({ id: "b-z", name: "Zeta" }),
      agent({ id: "b-a", name: "Alpha" }),
      agent({ id: "child", name: "Child", parent_agent_id: "b-a" }),
      agent({
        id: "b-archived",
        name: "Archived",
        archived_at: "2026-02-01T00:00:00Z",
      }),
    ];
    expect(baseRoleOptions(agents).map((a) => a.id)).toEqual(["b-a", "b-z"]);
  });
});

describe("changing an existing agent's base role", () => {
  const base = agent({ id: "base-1", name: "Base" });
  const other = agent({ id: "base-2", name: "Other base" });
  const child = agent({ id: "child-1", parent_agent_id: "base-1" });

  it("omits the agent itself from its own options", () => {
    expect(
      baseRoleOptionsFor([base, other, child], "base-1").map((a) => a.id),
    ).toEqual(["base-2"]);
  });

  it("keeps the create picker's rules — no specialisations, no archived", () => {
    const archived = agent({ id: "base-3", name: "Archived", archived_at: "x" });
    expect(
      baseRoleOptionsFor([base, child, archived], "child-1").map((a) => a.id),
    ).toEqual(["base-1"]);
  });

  it("lets a specialisation be re-pointed or detached", () => {
    expect(canChangeBaseRole(child, 0)).toBe(true);
  });

  it("lets a childless base role be attached", () => {
    expect(canChangeBaseRole(base, 0)).toBe(true);
  });

  it("refuses an agent that already has specialisations — that would be three levels", () => {
    expect(canChangeBaseRole({ child_count: 1 }, 0)).toBe(false);
    // No child_count from an older backend: the visible children still count.
    expect(canChangeBaseRole({}, 2)).toBe(false);
  });
});

describe("composeEffectiveInstructions", () => {
  it("mirrors the server's parent + blank line + child rule", () => {
    expect(composeEffectiveInstructions("parent", "child")).toBe(
      "parent\n\nchild",
    );
  });

  it("never leaves a stray separator when one side is empty", () => {
    // The claim path hands this exact string to the daemon; an extra blank
    // line here is a prompt the agent actually receives.
    expect(composeEffectiveInstructions("", "child")).toBe("child");
    expect(composeEffectiveInstructions("parent", "")).toBe("parent");
    expect(composeEffectiveInstructions(undefined, "child")).toBe("child");
    expect(composeEffectiveInstructions("parent", undefined)).toBe("parent");
    expect(composeEffectiveInstructions("", "")).toBe("");
  });

  it("does not trim the halves — they are shown verbatim", () => {
    // The preview must show the same bytes the server composes; trimming here
    // would make it disagree with what the daemon receives.
    expect(composeEffectiveInstructions("parent\n", "\nchild")).toBe(
      "parent\n\n\n\nchild",
    );
  });
});

describe("inherited skills", () => {
  it("drops skills the child also holds, and duplicates", () => {
    const chips = inheritedSkillChips({
      inherited_skills: [skill("a"), skill("b"), skill("a")],
      skills: [skill("a", "own copy")],
    });
    expect(chips.map((s) => s.id)).toEqual(["b"]);
  });

  it("is empty when the backend does not serve the inherited list", () => {
    expect(inheritedSkillChips({ skills: [skill("a")] })).toEqual([]);
  });
});

describe("hasInheritedPrompt", () => {
  it("needs both a parent and readable text", () => {
    expect(
      hasInheritedPrompt({
        parent_agent_id: "base-1",
        inherited_instructions: "parent prompt",
      }),
    ).toBe(true);
    // Parent set but text empty: either the base role has no prompt or this
    // viewer may not read it. Neither may be rendered as a parent half.
    expect(
      hasInheritedPrompt({ parent_agent_id: "base-1", inherited_instructions: "" }),
    ).toBe(false);
    expect(hasInheritedPrompt({ parent_agent_id: "" })).toBe(false);
  });
});

// DENE-384: the archive refusal names every active child, including ones this
// viewer cannot see, so its names — not the visible rows — are what the
// solidify dialog counts. The dialog test pins the rendering; this is the
// matrix behind it.
describe("agentHasChildrenNames", () => {
  const refusal = (body: unknown) =>
    new ApiError("refused", 409, "Conflict", body);

  it("reads the names off an agent_has_children refusal", () => {
    expect(
      agentHasChildrenNames(
        refusal({ code: "agent_has_children", children: ["One", "Two"] }),
      ),
    ).toEqual(["One", "Two"]);
  });

  it("returns nothing for a different refusal or a malformed body", () => {
    // An empty list means "the server said nothing", never "the server said
    // none", so every one of these falls back to the visible rows.
    expect(agentHasChildrenNames(refusal({ code: "agent_has_children" }))).toEqual(
      [],
    );
    expect(
      agentHasChildrenNames(refusal({ code: "agent_has_children", children: [] })),
    ).toEqual([]);
    expect(
      agentHasChildrenNames(
        refusal({ code: "agent_has_children", children: [1, "Two", ""] }),
      ),
    ).toEqual(["Two"]);
    expect(agentHasChildrenNames(refusal({ code: "something_else" }))).toEqual([]);
    expect(agentHasChildrenNames(new Error("network"))).toEqual([]);
  });

  it("keeps the error-code check in step with the guard that opens the dialog", () => {
    expect(isAgentHasChildrenError(refusal({ code: "agent_has_children" }))).toBe(
      true,
    );
  });
});

// DENE-384: the detail read that carries the base role's prompt can be in
// flight, failed, or genuinely empty — and the payload for all three is the
// same absent field. Which of the three the UI states is a boundary matrix,
// so it lives here.
describe("inheritedPromptReadState", () => {
  const flags = (over: Partial<{
    succeeded: boolean;
    failed: boolean;
    accessDenied: boolean;
  }>) => ({ succeeded: false, failed: false, accessDenied: false, ...over });

  it("is ready as soon as the read succeeded", () => {
    expect(inheritedPromptReadState(true, flags({ succeeded: true }))).toBe(
      "ready",
    );
  });

  it("is loading while the read is still in flight", () => {
    expect(inheritedPromptReadState(true, flags({}))).toBe("loading");
  });

  it("reports a failed read as a failure", () => {
    expect(inheritedPromptReadState(true, flags({ failed: true }))).toBe(
      "failed",
    );
  });

  it("keeps a 403 an answer rather than a failure", () => {
    // "You may not read the base role" is exactly what the tab's "no prompt,
    // or you don't have access to it" copy covers.
    expect(
      inheritedPromptReadState(
        true,
        flags({ failed: true, accessDenied: true }),
      ),
    ).toBe("ready");
  });

  it("asks nothing of the read for a base role", () => {
    expect(inheritedPromptReadState(false, flags({}))).toBe("ready");
    expect(inheritedPromptReadState(false, flags({ failed: true }))).toBe(
      "ready",
    );
  });
});

describe("solidifyTargets", () => {
  const visible = agent({ id: "child-1", name: "Nightly Variant" });

  it("lists exactly the rows it was handed when the server named none", () => {
    expect(solidifyTargets([visible], [])).toEqual([
      { agent: visible, name: "Nightly Variant" },
    ]);
  });

  it("keeps a name the viewer cannot see, so the count matches the guard", () => {
    const targets = solidifyTargets(
      [visible],
      ["Nightly Variant", "Somebody Else's Variant"],
    );
    expect(targets.map((target) => target.name)).toEqual([
      "Nightly Variant",
      "Somebody Else's Variant",
    ]);
    // Only the visible one can actually be solidified — the other has no id.
    expect(targets.map((target) => target.agent?.id ?? null)).toEqual([
      "child-1",
      null,
    ]);
  });

  it("matches names one-for-one when two children share one", () => {
    const twin = agent({ id: "child-2", name: "Nightly Variant" });
    const targets = solidifyTargets(
      [visible, twin],
      ["Nightly Variant", "Nightly Variant", "Hidden Variant"],
    );
    expect(targets.map((target) => target.agent?.id ?? null)).toEqual([
      "child-1",
      "child-2",
      null,
    ]);
  });

  it("never drops a visible child the refusal did not name", () => {
    // A list that changed between the archive attempt and the dialog must not
    // hide a row the user can still act on.
    const targets = solidifyTargets([visible], ["Only Named Variant"]);
    expect(targets.map((target) => target.name)).toEqual([
      "Only Named Variant",
      "Nightly Variant",
    ]);
  });
});

// DENE-506. The runtime-profile controls and the list's runtime tag both read
// this one decision, so its boundary cases live here rather than in either
// component's DOM test.
describe("runtime inheritance state", () => {
  it("reads the flag strictly, never by truthiness", () => {
    expect(
      runtimeInheritanceState(
        agent({ parent_agent_id: "base-1", runtime_inherited: true }),
      ),
    ).toBe("inherited");
    expect(
      runtimeInheritanceState(
        agent({ parent_agent_id: "base-1", runtime_inherited: false }),
      ),
    ).toBe("independent");
  });

  it("reports a base role as unknown even when the flag is present", () => {
    // The server always sends false for a base role, and `true` is a 400 for
    // one: either way there is no inheritance affordance to offer.
    expect(runtimeInheritanceState(agent({ runtime_inherited: false }))).toBe(
      "unknown",
    );
    expect(runtimeInheritanceState(agent({ runtime_inherited: true }))).toBe(
      "unknown",
    );
    expect(runtimeInheritanceState(agent({}))).toBe("unknown");
  });

  it("reports a specialisation on a pre-feature backend as unknown", () => {
    // An agent list read from a backend that never heard of the flag must not
    // be offered a switch it cannot honour.
    expect(runtimeInheritanceState(agent({ parent_agent_id: "base-1" }))).toBe(
      "unknown",
    );
  });

  it("locks the runtime profile only while following", () => {
    const following = agent({
      parent_agent_id: "base-1",
      runtime_inherited: true,
    });
    const independent = agent({
      parent_agent_id: "base-1",
      runtime_inherited: false,
    });
    expect(canEditRuntimeProfile(following, true)).toBe(false);
    expect(canEditRuntimeProfile(independent, true)).toBe(true);
    // A base role and an older backend keep the pre-feature behaviour: the
    // controls follow the permission gate alone.
    expect(canEditRuntimeProfile(agent({}), true)).toBe(true);
    expect(canEditRuntimeProfile(agent({ parent_agent_id: "base-1" }), true)).toBe(
      true,
    );
  });

  it("never grants edit rights the viewer does not have", () => {
    expect(
      canEditRuntimeProfile(
        agent({ parent_agent_id: "base-1", runtime_inherited: false }),
        false,
      ),
    ).toBe(false);
  });
});

describe("execution config inheritance", () => {
  const base = agent({ id: "base-1", owner_id: "user-1" });
  const following = agent({
    parent_agent_id: "base-1",
    runtime_inherited: true,
    owner_id: "user-1",
  });

  it("follows only while following a base role with the same owner", () => {
    expect(followsBaseRoleExecutionConfig(following, base)).toBe(true);
    expect(
      followsBaseRoleExecutionConfig(
        { ...following, runtime_inherited: false },
        base,
      ),
    ).toBe(false);
    // Another member's base role keeps its credentials to itself.
    expect(
      followsBaseRoleExecutionConfig({ ...following, owner_id: "user-2" }, base),
    ).toBe(false);
  });

  it("does not lock against a base role it cannot see or an ownerless row", () => {
    expect(followsBaseRoleExecutionConfig(following, undefined)).toBe(false);
    expect(
      followsBaseRoleExecutionConfig(
        { ...following, owner_id: null },
        { owner_id: null },
      ),
    ).toBe(false);
  });
});
