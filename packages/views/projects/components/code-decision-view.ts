import type { CodeDecision } from "@multica/core/types";

/**
 * What the source list is allowed to say about a server Decision.
 *
 * Every string here is a field the server already computed. Nothing in this
 * function looks at the resource list, so it cannot pick a different directory
 * than the one the next task will open.
 */
export type CodeDecisionView =
  | { kind: "place"; name: string }
  | { kind: "worktree"; root: string }
  | { kind: "remote"; url: string }
  | { kind: "scratch" }
  | { kind: "unresolvable"; code: string; reason?: string }
  | { kind: "unknown"; kindName: string };

export function isCodeDecision(value: unknown): value is CodeDecision {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    typeof (value as { kind?: unknown }).kind === "string"
  );
}

function serverLabel(decision: CodeDecision): string {
  const name = decision.display_name?.trim();
  if (name) return name;
  const path = decision.path?.trim();
  if (path) return path;
  return decision.kind;
}

export function viewOfCodeDecision(decision: CodeDecision): CodeDecisionView {
  switch (decision.kind) {
    case "local_in_place":
    case "local_shared":
      return { kind: "place", name: serverLabel(decision) };
    case "local_worktree": {
      const root =
        decision.worktree_root?.trim() || decision.path?.trim() || serverLabel(decision);
      return { kind: "worktree", root };
    }
    case "remote_cache":
      return { kind: "remote", url: decision.url?.trim() || decision.kind };
    case "shared_scratch":
      return { kind: "scratch" };
    case "unresolvable":
      return {
        kind: "unresolvable",
        code: decision.code?.trim() || "unresolvable",
        reason: decision.reason?.trim() || undefined,
      };
    default:
      return { kind: "unknown", kindName: decision.kind };
  }
}

/** A folder with no Git at all. An empty repository, or one this machine
 *  could not measure, is not this — the first already has a `.git`, the
 *  second must not be told it doesn't. */
export function isPlainFolder(
  isGitRepo: boolean | undefined,
  gitRoot?: string,
): boolean {
  return isGitRepo === false && !gitRoot;
}
