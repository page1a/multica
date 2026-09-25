import type { Agent, AgentSkillSummary } from "@multica/core/types";
import { ApiError, errorCode } from "@multica/core/api";

/**
 * Two-level specialisation (DENE-301), as the client sees it.
 *
 * A base role has no `parent_agent_id`; a specialisation points at exactly one
 * base role and can never be a parent itself (the server enforces the depth).
 * Every helper here reads the SAME fields the server serves — no client-side
 * inference of the relationship beyond "non-empty id means attached", which is
 * also how the backend models it (`parent_agent_id IS NULL` = base role).
 *
 * Older backends omit all of these fields. `undefined` must therefore behave
 * exactly like the empty string / empty list: the flat list the product had
 * before this feature, never a broken tree.
 */

/** The separator the backend joins a parent's prompt with — keep in sync with
 *  `agentInstructionSeparator` in `server/internal/handler/agent.go`. */
export const SPECIALIZATION_INSTRUCTION_SEPARATOR = "\n\n";

export function isSpecialization(
  agent: Pick<Agent, "parent_agent_id">,
): boolean {
  return (agent.parent_agent_id ?? "").length > 0;
}

export function isBaseRole(agent: Pick<Agent, "parent_agent_id">): boolean {
  return !isSpecialization(agent);
}

/**
 * Active specialisations of one base role, from an already-loaded list.
 *
 * The list carries `parent_agent_id` per row, so the nested view and the
 * archive guard never need a per-agent request. Archived children are included
 * — the caller decides; the archive guard's server-side counterpart counts
 * only active ones (`child_count`).
 */
export function childrenOf(
  agents: readonly Agent[],
  parentId: string,
): Agent[] {
  if (!parentId) return [];
  return agents.filter((agent) => agent.parent_agent_id === parentId);
}

/** Active children of one base role, in list order. */
export function activeChildrenOf(
  agents: readonly Agent[],
  parentId: string,
): Agent[] {
  return childrenOf(agents, parentId).filter((agent) => !agent.archived_at);
}

/**
 * How many specialisations a base-role row reports.
 *
 * `child_count` is the server's workspace-wide count of ACTIVE children, so it
 * stays right even when filters hide some of them; it is preferred over the
 * visible children the caller happens to hold. A backend that does not send it
 * still gets an honest number from the rows on screen.
 */
export function specializationCount(
  agent: Pick<Agent, "parent_agent_id" | "child_count">,
  visibleChildren: number,
): number {
  if (isSpecialization(agent)) return 0;
  const count = agent.child_count;
  if (typeof count === "number" && Number.isFinite(count) && count >= 0) {
    return count;
  }
  return visibleChildren;
}

/**
 * Agents a new specialisation may be attached to: base roles only (the server
 * refuses a parent that is itself a specialisation), never archived ones (that
 * refusal is a 400), sorted the way every other agent picker sorts.
 */
export function baseRoleOptions(agents: readonly Agent[]): Agent[] {
  return agents
    .filter((agent) => isBaseRole(agent) && !agent.archived_at)
    .toSorted((a, b) => a.name.localeCompare(b.name));
}

/**
 * Base roles an EXISTING agent may be attached to (DENE-300 follow-up).
 *
 * Same rule as the create picker, minus the agent itself: the server refuses a
 * self-reference with the same 400 it refuses a specialisation parent with, so
 * offering it would only produce an error the user cannot act on.
 */
export function baseRoleOptionsFor(
  agents: readonly Agent[],
  agentId: string,
): Agent[] {
  return baseRoleOptions(agents).filter((agent) => agent.id !== agentId);
}

/**
 * Whether an existing agent's base role can still be changed.
 *
 * Only depth blocks it: an agent that already HAS specialisations cannot be
 * given a parent of its own, because that would be three levels and the server
 * rejects it. A specialisation may always be re-pointed or detached, and a
 * childless base role may always be attached. Permission is a separate gate
 * the caller applies (`canEdit`).
 */
export function canChangeBaseRole(
  agent: Pick<Agent, "parent_agent_id" | "child_count">,
  visibleChildren: number,
): boolean {
  if (isSpecialization(agent)) return true;
  return specializationCount(agent, visibleChildren) === 0;
}

/**
 * Runtime inheritance (DENE-505), as both front-end surfaces see it.
 *
 * `inherited` — this specialisation follows its base role's runtime profile:
 * `runtime_id`, `runtime_mode`, `runtime_config`, `model`, `thinking_level` and
 * `service_tier` are the base role's, and the server re-copies them whenever the
 * base role's change.
 * `independent` — it owns its runtime profile, the pre-feature behaviour.
 * `unknown` — not a specialisation, or a backend that predates the field.
 *
 * `unknown` must render no inheritance affordance at all rather than assuming a
 * state: a base role cannot inherit (`runtime_inherited: true` is a 400 for one)
 * and an older backend cannot honour a toggle it never modelled. Read the flag
 * with `=== true` / `=== false`, never truthiness, for exactly that reason.
 */
export type RuntimeInheritanceState = "inherited" | "independent" | "unknown";

export function runtimeInheritanceState(
  agent: Pick<Agent, "parent_agent_id" | "runtime_inherited">,
): RuntimeInheritanceState {
  if (!isSpecialization(agent)) return "unknown";
  if (agent.runtime_inherited === true) return "inherited";
  if (agent.runtime_inherited === false) return "independent";
  return "unknown";
}

/**
 * Whether the runtime-profile controls (runtime / model / thinking / speed) may
 * be edited for this agent.
 *
 * While following the base role they are a copy of another agent's settings: the
 * values are worth reading, so they stay on screen, but the server refuses any
 * of them in the same request as `runtime_inherited: true` (and refuses them
 * alone with "pass runtime_inherited=false first"), so offering an editable
 * control could only produce a 400. Switching the toggle is the one way back to
 * editing. Permission is the separate `canEdit` gate.
 */
export function canEditRuntimeProfile(
  agent: Pick<Agent, "parent_agent_id" | "runtime_inherited">,
  canEdit: boolean,
): boolean {
  return canEdit && runtimeInheritanceState(agent) !== "inherited";
}

/**
 * Whether this specialisation also takes its base role's execution config —
 * env, custom args, MCP config (DENE-854). The server copies those only while
 * the agent follows its base role AND both rows share an owner (they carry the
 * base role's credentials), and refuses an edit to them in exactly that case.
 * A base role this client cannot see answers `false`: the server still has the
 * final word, but a guess of "locked" would hide controls that may work.
 */
export function followsBaseRoleExecutionConfig(
  agent: Pick<Agent, "parent_agent_id" | "runtime_inherited" | "owner_id">,
  baseRole: Pick<Agent, "owner_id"> | null | undefined,
): boolean {
  return (
    runtimeInheritanceState(agent) === "inherited" &&
    baseRole != null &&
    baseRole.owner_id != null &&
    baseRole.owner_id === agent.owner_id
  );
}

/**
 * The prompt a specialisation actually runs with: the base role's prompt, a
 * blank line, then its own. Mirrors `composeAgentInstructions` on the server,
 * including the "no stray blank lines when one side is empty" rule — the
 * preview is only useful if it shows exactly what the daemon is handed.
 */
export function composeEffectiveInstructions(
  inherited: string | undefined,
  own: string | undefined,
): string {
  const parent = inherited ?? "";
  const child = own ?? "";
  if (parent === "") return child;
  if (child === "") return parent;
  return `${parent}${SPECIALIZATION_INSTRUCTION_SEPARATOR}${child}`;
}

/**
 * Inherited skills that the child does not also hold itself. The server serves
 * the parent's list verbatim, so the overlap is expected when both attached
 * the same skill; the chip row must not show it twice (v1 cannot remove an
 * inherited binding, so a duplicate would read as an unremovable twin).
 */
export function inheritedSkillChips(
  agent: Pick<Agent, "inherited_skills" | "skills">,
): AgentSkillSummary[] {
  const inherited = agent.inherited_skills ?? [];
  if (inherited.length === 0) return [];
  const own = new Set((agent.skills ?? []).map((skill) => skill.id));
  const seen = new Set<string>();
  const chips: AgentSkillSummary[] = [];
  for (const skill of inherited) {
    if (own.has(skill.id) || seen.has(skill.id)) continue;
    seen.add(skill.id);
    chips.push(skill);
  }
  return chips;
}

/**
 * True when there is an inherited prompt to show the viewer.
 *
 * An empty `inherited_instructions` on a specialisation has two causes — the
 * base role genuinely has no prompt, or the base role is private to someone
 * else and the server serves the relationship without the text. Neither is
 * renderable, and the copy for "nothing inherited (or not visible here)"
 * covers both honestly; what must NOT happen is inventing a parent half for
 * the effective-prompt preview.
 */
export function hasInheritedPrompt(
  agent: Pick<Agent, "parent_agent_id" | "inherited_instructions">,
): boolean {
  return (
    isSpecialization(agent) &&
    (agent.inherited_instructions ?? "").length > 0
  );
}

/**
 * How the read that carries `inherited_instructions` — the agent DETAIL
 * endpoint, never the list — went.
 *
 * An absent `inherited_instructions` has three causes and the UI must not
 * merge them: the read is still in flight, it failed, or it succeeded and the
 * base role genuinely has no prompt (or is private to someone else). Only the
 * last one is a fact worth stating; the first two are "not known yet"
 * (DENE-384).
 */
export type InheritedPromptState = "ready" | "loading" | "failed";

/**
 * The state to render for the inheritance block, from the detail query's own
 * flags.
 *
 * A 403 is NOT a failure: "you may not read the base role" is a real answer
 * about the relationship, and it is the one thing the "no prompt, or you don't
 * have access to it" copy is for. Everything else the read can do wrong — a
 * 404, a 5xx, a dropped connection — is a failure and must be said as one.
 */
export function inheritedPromptReadState(
  isSpecializationAgent: boolean,
  detail: { succeeded: boolean; failed: boolean; accessDenied: boolean },
): InheritedPromptState {
  if (!isSpecializationAgent || detail.succeeded) return "ready";
  if (detail.failed) return detail.accessDenied ? "ready" : "failed";
  return "loading";
}

/**
 * The stable code the archive handler attaches when a base role still has
 * specialisations (`server/internal/handler/agent.go`). The refusal carries
 * the child names as well, but the client already holds the children — it
 * needs their IDs to offer "solidify & unbind", which the body does not carry.
 */
export const AGENT_HAS_CHILDREN_CODE = "agent_has_children";

export function isAgentHasChildrenError(err: unknown): boolean {
  return errorCode(err) === AGENT_HAS_CHILDREN_CODE;
}

/**
 * The child names the archive refusal reported, or an empty list.
 *
 * The refusal lists every active child of the base role, INCLUDING ones the
 * caller cannot see — the server counts them whether or not they are in the
 * caller's agent list. That list is therefore the honest denominator for "how
 * many specialisations still block this archive", while the local rows are the
 * subset the viewer can act on (DENE-384).
 *
 * Older backends send no `children` field; an empty list means "the server
 * said nothing", never "the server said none" — callers fall back to the rows
 * they hold.
 */
export function agentHasChildrenNames(err: unknown): string[] {
  if (!isAgentHasChildrenError(err)) return [];
  const body = err instanceof ApiError ? err.body : null;
  if (!body || typeof body !== "object") return [];
  const names = (body as { children?: unknown }).children;
  if (!Array.isArray(names)) return [];
  return names.filter(
    (name): name is string => typeof name === "string" && name.length > 0,
  );
}

/**
 * One row of the "will be solidified and unbound" list.
 *
 * `agent` is null for a specialisation the server named but the viewer cannot
 * read: there is no id to solidify, so it is listed as a fact about the
 * blockage rather than offered as an action.
 */
export interface SolidifyTarget {
  agent: Agent | null;
  name: string;
}

/**
 * The rows the solidify dialog must show.
 *
 * The refusal's names are the authoritative count. Visible children supply the
 * avatars and the ids solidify needs; any name the viewer cannot see is kept
 * as a name-only row instead of being silently dropped — the failure mode
 * DENE-384 fixes, where the dialog listed fewer specialisations than the
 * archive guard, so the user believed the work was done and the next archive
 * was refused again. Names are the only join key the refusal carries, so a
 * visible child is consumed by the first matching name.
 */
export function solidifyTargets(
  visible: readonly Agent[],
  serverNames: readonly string[],
): SolidifyTarget[] {
  if (serverNames.length === 0) {
    return visible.map((agent) => ({ agent, name: agent.name }));
  }
  const remaining = [...visible];
  const targets: SolidifyTarget[] = serverNames.map((name) => {
    const index = remaining.findIndex((agent) => agent.name === name);
    if (index === -1) return { agent: null, name };
    const [agent] = remaining.splice(index, 1);
    return { agent: agent ?? null, name };
  });
  // A visible child the refusal did not name (a stale list, or one that
  // changed between the two reads) is still actionable, so it is never
  // dropped — the dialog must not hide something the user can solidify.
  for (const agent of remaining) targets.push({ agent, name: agent.name });
  return targets;
}
