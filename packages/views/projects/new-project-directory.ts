import type { LocalDirectoryExecutionMode } from "@multica/core/types";
import { defaultStorage } from "@multica/core/platform";
import {
  inferNewProjectExecutionMode,
  sanitizeProjectDirectoryName,
} from "@multica/core/issue-drafts";
import {
  provisionProjectDirectory,
  removeProvisionedDirectory,
  validateLocalDirectory,
} from "../platform/local-directory";

export { sanitizeProjectDirectoryName };

/** This machine's default place for a new project's folder. */
export const DEFAULT_BUSINESS_PROJECT_ROOT = "/Volumes/Storge/project-business";

function rootKey(userId: string, daemonId: string): string {
  return `multica.business-project-root.${userId}.${daemonId}`;
}

/** The business-project root remembered for this person on this machine. */
export function readBusinessProjectRoot(userId: string, daemonId: string): string {
  if (!userId || !daemonId) return DEFAULT_BUSINESS_PROJECT_ROOT;
  const stored = defaultStorage.getItem(rootKey(userId, daemonId))?.trim() ?? "";
  return stored.length > 0 ? stored : DEFAULT_BUSINESS_PROJECT_ROOT;
}

export function writeBusinessProjectRoot(
  userId: string,
  daemonId: string,
  root: string,
): void {
  if (!userId || !daemonId) return;
  defaultStorage.setItem(rootKey(userId, daemonId), root);
}

export interface NewProjectDirectoryDraft {
  enabled: boolean;
  root: string;
  dirName: string;
  gitInit: boolean;
  /** Bind the root itself, as a folder that holds several repositories. */
  container: boolean;
  mode: LocalDirectoryExecutionMode;
  /** The person picked a mode. Inference stops overriding it. */
  modeTouched: boolean;
}

export function initialDirectoryDraft(input: {
  root: string;
  dirName: string;
  /** Desktop with a running local daemon can create the folder. */
  available: boolean;
}): NewProjectDirectoryDraft {
  return {
    enabled: input.available,
    root: input.root,
    dirName: input.dirName,
    gitInit: true,
    container: false,
    mode: "worktree",
    modeTouched: false,
  };
}

export function patchDirectoryDraft(
  current: NewProjectDirectoryDraft,
  patch: Partial<NewProjectDirectoryDraft>,
): NewProjectDirectoryDraft {
  const next: NewProjectDirectoryDraft = { ...current, ...patch };
  if (next.container) next.gitInit = false;
  if (!next.modeTouched) {
    next.mode = inferNewProjectExecutionMode({
      gitInit: next.gitInit,
      container: next.container,
    });
  }
  return next;
}

export function localDirectoryResourceRef(input: {
  localPath: string;
  daemonId: string;
  label: string | null;
  mode: LocalDirectoryExecutionMode;
  realPath?: string;
  repoKey?: string;
  isGitRepo?: boolean;
}): Record<string, unknown> {
  return {
    local_path: input.localPath,
    daemon_id: input.daemonId,
    ...(input.label ? { label: input.label } : {}),
    execution_mode: input.mode,
    ...(input.realPath ? { real_path: input.realPath } : {}),
    ...(input.repoKey ? { repo_key: input.repoKey } : {}),
    ...(input.isGitRepo === undefined ? {} : { is_git_repo: input.isGitRepo }),
  };
}

export type PreparedProjectDirectory =
  | {
      ok: true;
      /** Set only when this call created a subdirectory. Failure cleanup deletes this path and nothing else. */
      createdPath: string | null;
      root: string;
      resource: Record<string, unknown> | null;
    }
  | {
      ok: false;
      reason: "unavailable" | "offline" | "bad_name" | "exists" | "failed";
      detail?: string;
    };

/**
 * Make the folder (unless the person turned that off, or asked to bind the
 * root itself) and return the resource ref the server stores.
 */
export async function prepareNewProjectDirectory(input: {
  draft: NewProjectDirectoryDraft;
  daemonId: string | null;
  title: string;
}): Promise<PreparedProjectDirectory> {
  const draft = input.draft;
  if (!draft.enabled) {
    return { ok: true, createdPath: null, root: draft.root, resource: null };
  }
  if (!input.daemonId) return { ok: false, reason: "offline" };

  if (draft.container) {
    const checked = await validateLocalDirectory(draft.root);
    if (!checked.ok) return { ok: false, reason: "failed", detail: checked.error ?? checked.reason };
    return {
      ok: true,
      createdPath: null,
      root: draft.root,
      resource: localDirectoryResourceRef({
        localPath: draft.root,
        daemonId: input.daemonId,
        label: input.title,
        mode: draft.mode,
        realPath: checked.real_path,
        repoKey: checked.repo_key,
        isGitRepo: checked.is_git_repo === true,
      }),
    };
  }

  const dirName = sanitizeProjectDirectoryName(draft.dirName);
  if (!dirName) return { ok: false, reason: "bad_name" };
  const created = await provisionProjectDirectory({
    root: draft.root,
    dirName,
    gitInit: draft.gitInit,
  });
  if (!created.ok || !created.path) {
    return {
      ok: false,
      reason: created.reason === "exists" ? "exists" : created.reason === "unsupported" ? "unavailable" : created.reason === "bad_name" ? "bad_name" : "failed",
      detail: created.error,
    };
  }
  const checked = await validateLocalDirectory(created.path);
  return {
    ok: true,
    createdPath: created.path,
    root: draft.root,
    resource: localDirectoryResourceRef({
      localPath: created.path,
      daemonId: input.daemonId,
      label: input.title,
      mode: draft.mode,
      realPath: checked.real_path ?? created.path,
      repoKey: checked.repo_key,
      isGitRepo: draft.gitInit ? true : checked.is_git_repo === true,
    }),
  };
}

export async function discardProvisionedDirectory(createdPath: string | null, root: string): Promise<void> {
  if (!createdPath) return;
  await removeProvisionedDirectory({ root, path: createdPath });
}
