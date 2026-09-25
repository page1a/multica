import { ApiError, errorCode } from "../api/client";
import type {
  IssueDraftPayload,
  IssueDraftProjectChoice,
  IssueDraftProjectProposal,
} from "../types";

/**
 * How a new project's directory is shared, before anyone overrides it.
 *
 * A directory this flow creates and `git init`s is one repository, so each
 * task gets its own worktree. A container of repositories — or a folder that
 * was deliberately left without git — is shared: tasks run in the folder
 * itself, side by side. `in_place` is never inferred; someone has to pick it.
 */
export function inferNewProjectExecutionMode(input: {
  gitInit: boolean;
  container: boolean;
}): "worktree" | "shared" {
  if (input.container || !input.gitInit) return "shared";
  return "worktree";
}

/** A directory name taken from a project title: one path segment, nothing else. */
export function sanitizeProjectDirectoryName(name: string): string {
  const cleaned = name
    .trim()
    .replace(/[\\/:\0]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (cleaned === "." || cleaned === "..") return "";
  return cleaned.slice(0, 80);
}

const POSITIONING_LIMIT = 400;

function positioningFrom(description: string): string {
  const trimmed = description.trim();
  if (trimmed.length <= POSITIONING_LIMIT) return trimmed;
  return trimmed.slice(0, POSITIONING_LIMIT).trimEnd();
}

export interface AlignmentProjectPlan {
  kind: "existing" | "create" | "none";
  projectId?: string | null;
  name?: string;
  icon?: string | null;
  description?: string | null;
  /** The carrier suggested this and the person has not replaced it. */
  suggested?: boolean;
}

/**
 * What the confirm panel should show for the project.
 *
 * The carrier may name an existing project or propose a new one. A proposal
 * to create is kept only for a group — a parent plus at least one sub-issue.
 * A name that matches nothing, on a group, becomes a create proposal and is
 * on by default. A single issue never starts a project from a proposal; the
 * person can still make one from the picker. An explicit choice wins over
 * all of that, including "do not attach".
 */
export function resolveAlignmentProjectPlan(input: {
  draft: IssueDraftPayload;
  projects: readonly { id: string; title: string }[];
}): AlignmentProjectPlan {
  const draft = input.draft;
  const isGroup = (draft.children ?? []).length > 0;
  const choice = draft.project_choice ?? null;

  if (choice?.kind === "none") return { kind: "none" };
  if (choice?.kind === "existing" && choice.project_id) {
    return { kind: "existing", projectId: choice.project_id };
  }
  if (choice?.kind === "create" && (choice.name ?? "").trim().length > 0) {
    return {
      kind: "create",
      name: choice.name?.trim(),
      icon: choice.icon ?? null,
      description: choice.description ?? "",
    };
  }
  if (draft.project_id) {
    return { kind: "existing", projectId: draft.project_id };
  }

  const proposal = draft.project_proposal ?? null;
  if (!proposal || proposal.name.trim().length === 0) return { kind: "none" };

  if (proposal.action === "existing") {
    const match = input.projects.find((project) => project.title === proposal.name);
    if (match) {
      return { kind: "existing", projectId: match.id, suggested: true };
    }
    if (!isGroup) return { kind: "none" };
    return createFrom(proposal, draft, true);
  }

  if (proposal.action === "create" && isGroup) {
    return createFrom(proposal, draft, true);
  }
  return { kind: "none" };
}

function createFrom(
  proposal: IssueDraftProjectProposal,
  draft: IssueDraftPayload,
  suggested: boolean,
): AlignmentProjectPlan {
  const described = (proposal.description ?? "").trim();
  return {
    kind: "create",
    name: proposal.name.trim(),
    icon: proposal.icon ?? null,
    description: described.length > 0 ? described : positioningFrom(draft.description),
    suggested,
  };
}

/** The choice to store when the person edits the create card. */
export function createChoice(input: {
  name: string;
  icon?: string | null;
  description?: string | null;
}): IssueDraftProjectChoice {
  return {
    kind: "create",
    name: input.name,
    icon: input.icon ?? null,
    description: input.description ?? "",
  };
}

/**
 * Whether a failed confirm should delete the directory it just created.
 *
 * A 4xx, and a create the server explicitly rolled back, left no project, so
 * the directory would be an orphan. A failure after the project committed
 * (`committed`) must keep the directory: deleting it would leave a project
 * with nowhere to work. A network error is the same — the commit may have
 * landed and the response been lost.
 */
export function provisionedDirectoryShouldBeRemoved(err: unknown): boolean {
  const code = errorCode(err);
  if (code === "committed") return false;
  if (code === "rolled_back") return true;
  return err instanceof ApiError && err.status >= 400 && err.status < 500;
}
