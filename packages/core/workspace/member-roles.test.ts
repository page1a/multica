// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  MEMBER_ROLES,
  asMemberRole,
  isMemberRole,
  roleCapabilities,
  roleChangeImpact,
  roleOptions,
} from "./member-roles";
import type { MemberRole } from "../types";

describe("member role vocabulary", () => {
  it("ladders the four tiers strongest first", () => {
    expect(MEMBER_ROLES).toEqual(["owner", "admin", "member", "guest"]);
  });

  it("accepts only the four tiers", () => {
    for (const role of MEMBER_ROLES) expect(isMemberRole(role)).toBe(true);
    for (const junk of ["", "OWNER", "superadmin", null, undefined, 3, {}]) {
      expect(isMemberRole(junk)).toBe(false);
    }
  });

  it("returns null for a tier this build does not know, never a guess", () => {
    expect(asMemberRole("admin")).toBe("admin");
    // A backend that grows a fifth tier must not be silently rendered as one
    // of ours — the row shows the raw value and refuses to edit.
    expect(asMemberRole("superadmin")).toBeNull();
    expect(asMemberRole(undefined)).toBeNull();
  });
});

describe("roleCapabilities", () => {
  // Canonical matrix: docs/kun/permission-model.md, second table.
  it.each([
    ["owner", 8],
    ["admin", 7],
    ["member", 5],
    ["guest", 1],
  ] as const)("%s holds %i capabilities", (role, count) => {
    expect(roleCapabilities(role)).toHaveLength(count);
  });

  it("gives guest nothing but viewing what was shared with them", () => {
    expect(roleCapabilities("guest")).toEqual(["view_shared"]);
  });

  it("keeps billing and transfer on owner alone", () => {
    expect(roleCapabilities("admin")).not.toContain("billing_and_transfer");
    expect(roleCapabilities("owner")).toContain("billing_and_transfer");
  });

  it("is monotonic down the ladder", () => {
    const tiers: MemberRole[] = ["owner", "admin", "member", "guest"];
    for (let i = 0; i < tiers.length - 1; i++) {
      const stronger = new Set(roleCapabilities(tiers[i]!));
      for (const cap of roleCapabilities(tiers[i + 1]!)) {
        expect(stronger).toContain(cap);
      }
    }
  });
});

describe("roleChangeImpact", () => {
  it("reports nothing for a no-op change", () => {
    expect(roleChangeImpact("member", "member")).toEqual({ gained: [], lost: [] });
  });

  it("names what a promotion opens", () => {
    expect(roleChangeImpact("member", "admin")).toEqual({
      gained: ["manage_members", "workspace_settings"],
      lost: [],
    });
  });

  it("names what a demotion closes", () => {
    expect(roleChangeImpact("admin", "member")).toEqual({
      gained: [],
      lost: ["manage_members", "workspace_settings"],
    });
  });

  it("surfaces the workspace-scope blind spot when demoting to guest", () => {
    const impact = roleChangeImpact("member", "guest");
    expect(impact.gained).toEqual([]);
    // The one consequence nobody predicts: workspace-scoped resources stop
    // being visible even though nothing about them changed.
    expect(impact.lost).toContain("see_workspace_scope");
    expect(impact.lost).toContain("comment_and_edit");
    expect(impact.lost).toContain("be_assigned");
  });

  it("lists capabilities in ladder order, not diff order", () => {
    const { lost } = roleChangeImpact("owner", "guest");
    expect(lost).toEqual([
      "see_workspace_scope",
      "create_resources",
      "comment_and_edit",
      "be_assigned",
      "manage_members",
      "workspace_settings",
      "billing_and_transfer",
    ]);
  });
});

describe("roleOptions", () => {
  const blocks = (opts: ReturnType<typeof roleOptions>) =>
    Object.fromEntries(opts.map((o) => [o.role, o.block ?? null]));

  it("lets an owner move a member anywhere released", () => {
    expect(
      blocks(roleOptions({ current: "member", actorIsOwner: true, isLastOwner: false })),
    ).toEqual({
      owner: null,
      admin: null,
      member: null,
      guest: null,
    });
  });

  it("keeps the owner tier out of an admin's reach", () => {
    const opts = roleOptions({ current: "member", actorIsOwner: false, isLastOwner: false });
    expect(opts.find((o) => o.role === "owner")).toEqual({
      role: "owner",
      disabled: true,
      block: "owner_requires_owner",
    });
  });

  it("blocks every demotion of the last owner but leaves owner selectable", () => {
    const opts = roleOptions({ current: "owner", actorIsOwner: true, isLastOwner: true });
    expect(blocks(opts)).toEqual({
      owner: null,
      admin: "last_owner",
      member: "last_owner",
      guest: "last_owner",
    });
  });

  it("never disables the tier the member already holds", () => {
    for (const current of MEMBER_ROLES) {
      for (const actorIsOwner of [true, false]) {
        const opts = roleOptions({ current, actorIsOwner, isLastOwner: current === "owner" });
        expect(opts.find((o) => o.role === current)?.disabled).toBe(false);
      }
    }
  });

  it("offers all four tiers regardless of gating", () => {
    const opts = roleOptions({ current: "guest", actorIsOwner: false, isLastOwner: false });
    expect(opts.map((o) => o.role)).toEqual([...MEMBER_ROLES]);
  });

  it("lets an admin hand out guest", () => {
    // The server enforces guest read-only on every write (DENE-697), so the
    // tier is released. See docs/kun/permission-model.md.
    const guest = roleOptions({ current: "member", actorIsOwner: false, isLastOwner: false }).find(
      (o) => o.role === "guest",
    );
    expect(guest).toEqual({ role: "guest", disabled: false });
  });
});
