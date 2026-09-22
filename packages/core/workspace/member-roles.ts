import type { MemberRole } from "../types";

/**
 * The four workspace tiers, strongest first. This order is the one the
 * members table and every role picker render in — keep the picker reading
 * from here rather than re-listing the tiers at each call site.
 */
export const MEMBER_ROLES = ["owner", "admin", "member", "guest"] as const;

export function isMemberRole(value: unknown): value is MemberRole {
  return (
    typeof value === "string" &&
    (MEMBER_ROLES as readonly string[]).includes(value)
  );
}

/**
 * Narrow a server-supplied role string, or `null` when the backend sent a
 * tier this build does not know. Callers render the raw value and refuse to
 * edit that row rather than guessing a tier for it — guessing either invents
 * privileges or mislabels someone.
 */
export function asMemberRole(value: unknown): MemberRole | null {
  return isMemberRole(value) ? value : null;
}

/**
 * What a tier can do, as the members table explains it to the person making
 * the change. These mirror the workspace-level rows of the permission matrix
 * in `docs/kun/permission-model.md`; the executable copy is
 * `server/internal/permission`.
 *
 * `see_workspace_scope` is the one entry that comes from the visibility
 * layer rather than the tier layer: `workspace`-scoped resources are visible
 * to every tier except guest, so demoting someone to guest hides things that
 * were never shared with them individually. That is the single most
 * surprising consequence of a tier change, so it is stated alongside.
 */
export type RoleCapability =
  | "view_shared"
  | "see_workspace_scope"
  | "create_resources"
  | "comment_and_edit"
  | "be_assigned"
  | "manage_members"
  | "workspace_settings"
  | "billing_and_transfer";

/** Render order for capability lists, least privileged first. */
export const ROLE_CAPABILITIES: readonly RoleCapability[] = [
  "view_shared",
  "see_workspace_scope",
  "create_resources",
  "comment_and_edit",
  "be_assigned",
  "manage_members",
  "workspace_settings",
  "billing_and_transfer",
];

const CAPABILITIES_BY_ROLE: Record<MemberRole, readonly RoleCapability[]> = {
  owner: ROLE_CAPABILITIES,
  admin: [
    "view_shared",
    "see_workspace_scope",
    "create_resources",
    "comment_and_edit",
    "be_assigned",
    "manage_members",
    "workspace_settings",
  ],
  member: [
    "view_shared",
    "see_workspace_scope",
    "create_resources",
    "comment_and_edit",
    "be_assigned",
  ],
  guest: ["view_shared"],
};

export function roleCapabilities(role: MemberRole): readonly RoleCapability[] {
  return CAPABILITIES_BY_ROLE[role];
}

export interface RoleChangeImpact {
  gained: RoleCapability[];
  lost: RoleCapability[];
}

/**
 * What changes for a person moved from one tier to another, so the table can
 * say which doors just opened or closed instead of only "saved".
 */
export function roleChangeImpact(
  from: MemberRole,
  to: MemberRole,
): RoleChangeImpact {
  const before = new Set(roleCapabilities(from));
  const after = new Set(roleCapabilities(to));
  return {
    gained: ROLE_CAPABILITIES.filter((c) => after.has(c) && !before.has(c)),
    lost: ROLE_CAPABILITIES.filter((c) => before.has(c) && !after.has(c)),
  };
}

/** Why a tier cannot be picked for this member right now. */
export type RoleOptionBlock =
  | "last_owner"
  | "owner_requires_owner";

export interface RoleOption {
  role: MemberRole;
  disabled: boolean;
  block?: RoleOptionBlock;
}

export interface RoleOptionsInput {
  /** The tier the member holds today. */
  current: MemberRole;
  /** Whether the person making the change is an owner. */
  actorIsOwner: boolean;
  /** Whether this member is the workspace's only owner. */
  isLastOwner: boolean;
}

/**
 * The selectable tiers for one row, with the reason each blocked one is
 * blocked. The gates match what the server enforces in
 * `workspace.go` UpdateMember: only owners touch the owner tier, and the last
 * owner cannot be demoted.
 */
export function roleOptions({
  current,
  actorIsOwner,
  isLastOwner,
}: RoleOptionsInput): RoleOption[] {
  return MEMBER_ROLES.map((role) => {
    if (role === current) return { role, disabled: false };
    if (role === "owner" && !actorIsOwner) {
      return { role, disabled: true, block: "owner_requires_owner" as const };
    }
    if (isLastOwner) {
      return { role, disabled: true, block: "last_owner" as const };
    }
    return { role, disabled: false };
  });
}
