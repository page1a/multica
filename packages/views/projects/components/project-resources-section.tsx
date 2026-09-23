"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  ArrowDown,
  ArrowUp,
  ChevronRight,
  FolderGit,
  FolderOpen,
  Folders,
  GitBranch,
  Pencil,
  Plus,
  Search,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import {
  projectResourcesOptions,
  useCreateProjectResource,
  useDeleteProjectResource,
  useUpdateProjectResource,
} from "@multica/core/projects";
import { projectCodeDecisionOptions } from "@multica/core/projects/code-decision";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCurrentWorkspace } from "@multica/core/paths";
import type {
  GithubRepoResourceRef,
  LocalDirectoryExecutionMode,
  LocalDirectoryResourceRef,
  ProjectResource,
} from "@multica/core/types";
import { useConfigStore } from "@multica/core/config";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import {
  initLocalGit,
  isDesktopShell,
  pickDirectory,
  useLocalDaemonStatus,
  useLocalDirectorySharedOverrides,
  validateLocalDirectory,
  validateWritablePath,
  type ValidateLocalDirectoryResult,
} from "../../platform";
// The source rule is pure and imported from its own module rather than the
// projects query barrel: it must give the same answer in a test that mocks the
// queries as it does in production, and a mocked barrel would strip it.
import { findDuplicateSources } from "@multica/core/projects/source-rule";
import { DuplicateSourceBanner } from "./duplicate-source-banner";
import { CodeDecisionBanner } from "./code-decision-banner";
import { isCodeDecision, isPlainFolder } from "./code-decision-view";
import { LocalDirectoryModeDialog } from "./local-directory-mode-dialog";
import { localDirectoryLabel } from "./local-directory-label";
import {
  canMoveLocalDirectory,
  moveLocalDirectory,
  type MoveDirection,
} from "./local-directory-order";
import { worktreeRootForSave, worktreeRootProblem } from "./worktree-root";
import {
  apiExecutionMode,
  displayedExecutionMode,
  executionModeOf,
  isSharedModeRejectedByServer,
  needsLocalSharedOverride,
  sharedModeUnavailable,
  worktreeUnavailableReason,
} from "./local-directory-mode";
import { useT } from "../../i18n";
import { githubShortLabel } from "../../common/github-url";

// Project Resources sidebar section.
//
// Type-dispatched at the row + add-flow level. Add a new resource_type by:
//   (1) extending the server validator
//   (2) extending ProjectResourceType in @multica/core/types
//   (3) adding a render case in ResourceRow and an add-control here
function isGithubRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: GithubRepoResourceRef;
} {
  return r.resource_type === "github_repo";
}

function isLocalDirectoryRef(r: ProjectResource): r is ProjectResource & {
  resource_ref: LocalDirectoryResourceRef;
} {
  return r.resource_type === "local_directory";
}

/** Pending mode edit — either for a directory being added, or an existing row. */
type ModeDialogState = {
  path: string;
  daemonId: string | null;
  mode: LocalDirectoryExecutionMode;
  /** undefined = unknown (older desktop build); treated as "cannot verify". */
  isGitRepo: boolean | undefined;
  /** Set for an edit; absent when adding a new resource. */
  resource?: ProjectResource & { resource_ref: LocalDirectoryResourceRef };
  /** Only used when adding. */
  label?: string;
  /**
   * Identity this machine measured at pick time (DENE-617). Only the machine
   * holding the directory can produce these, so they are carried from the
   * pick to the save rather than re-derived: the server has a string, not a
   * filesystem.
   */
  realPath?: string;
  repoKey?: string;
  /**
   * Where parallel mode would put the working copies. Shown in the dialog
   * BEFORE the user commits to that mode — its cost is a copy per task on
   * their own disk, and a cost you only discover afterwards is not a choice.
   */
  defaultWorktreeRoot?: string;
  /** The repository root, when the machine could read it — only used to warn
   *  that a typed landing folder sits inside the repository. */
  gitRoot?: string;
  /** What the user has typed for the landing folder, once they have edited
   *  it. Undefined means "still the default", which is stored as absent. */
  worktreeRoot?: string;
};

export function ProjectResourcesSection({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const workspace = useCurrentWorkspace();
  const daemonStatus = useLocalDaemonStatus();
  const [open, setOpen] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const [repoSearch, setRepoSearch] = useState("");
  const [picking, setPicking] = useState(false);
  const [modeDialog, setModeDialog] = useState<ModeDialogState | null>(null);
  const [modeSaving, setModeSaving] = useState(false);
  const [modeError, setModeError] = useState<string | null>(null);
  // A reorder is several position writes in a row. Until they land and the
  // list refetches, every arrow on screen would compute its patch from the old
  // order, so one is in flight for the whole list, not for one row.
  const [reordering, setReordering] = useState(false);
  const [gitInitPending, setGitInitPending] = useState(false);
  const [gitInitError, setGitInitError] = useState<string | null>(null);

  const { data: resources = [] } = useQuery(
    projectResourcesOptions(wsId, projectId),
  );
  const createResource = useCreateProjectResource(wsId, projectId);
  const updateResource = useUpdateProjectResource(wsId, projectId);
  const deleteResource = useDeleteProjectResource(wsId, projectId);

  // Desktop-only entry points. We hide (not just disable) on web so users
  // there don't see an action they can never complete — the spec calls for
  // read-only on web because the daemon-id check can't be performed in the
  // browser.
  const desktopMode = isDesktopShell();
  const localDaemonId = daemonStatus.daemonId;
  // Cache identity only. The decision itself comes back from the server;
  // this string just says "the list the answer was about has changed".
  const decisionInputKey = resources
    .map(
      (resource) =>
        `${resource.position}:${resource.id}:${resource.resource_type}:${JSON.stringify(resource.resource_ref)}`,
    )
    .join("|");
  const decisionQuery = useQuery({
    ...projectCodeDecisionOptions(
      wsId,
      projectId,
      localDaemonId ?? "",
      decisionInputKey,
    ),
    enabled: desktopMode && !!wsId && !!localDaemonId,
  });
  const codeDecision = isCodeDecision(decisionQuery.data)
    ? decisionQuery.data
    : undefined;

  // The one thing the client must still check up front: whether this server
  // performs that gate at all. One declared boolean, no inference — servers
  // that predate it drop execution_mode and answer 201.
  const serverValidatesWorktree = useConfigStore((state) => state.localWorktreeSupported);
  const serverAcceptsShared = useConfigStore((state) => state.localSharedSupported);
  const { canPersist, hasOverride, setOverride } = useLocalDirectorySharedOverrides();
  const sharedUnavailable = sharedModeUnavailable({
    serverAcceptsShared,
    canSetLocalOverride: canPersist,
  });
  const sharedUsesLocalOverride = !serverAcceptsShared && canPersist;
  // The daemon's worktree capability is deliberately NOT read here any more.
  // It existed only to decide what to PRESELECT, and a new directory now
  // always starts on in-place (DENE-617): whether a machine COULD run
  // parallel mode is no longer a reason to start the user there. Whether it
  // MAY is still the server's call, gated on save and surfaced inline.
  const attachedUrls = new Set(
    resources.filter(isGithubRef).map((r) => r.resource_ref.url),
  );
  const attachedLocalPaths = new Set(
    resources
      .filter(isLocalDirectoryRef)
      .filter((r) => r.resource_ref.daemon_id === localDaemonId)
      .map((r) => r.resource_ref.local_path),
  );
  // A project may hold SEVERAL directories on one machine (DENE-617): four
  // unrelated plain folders, a repository plus its docs checkout. What it may
  // not hold is the same directory twice, or two checkouts of one repository.
  // Both are the server's rules (and Postgres indexes); the UI checks the
  // first one at pick time so the answer arrives while the folder is still on
  // screen, instead of as a 409 afterwards.
  //
  // Which of them a run writes is the server's Decision, read from this
  // order. The list shows that answer; it does not pick the directory.
  const boundIdentitiesForDialog = useMemo(() => {
    return resources
      .filter(isLocalDirectoryRef)
      .filter((r) => r.resource_ref.daemon_id === localDaemonId)
      .filter((r) => r.id !== modeDialog?.resource?.id)
      .map((r) => r.resource_ref.real_path || r.resource_ref.local_path);
  }, [resources, localDaemonId, modeDialog?.resource?.id]);

  const identityBackfilled = useRef(new Set<string>());
  useEffect(() => {
    if (!desktopMode || !localDaemonId) return;
    for (const r of resources) {
      if (!isLocalDirectoryRef(r)) continue;
      if (r.resource_ref.daemon_id !== localDaemonId) continue;
      if (r.resource_ref.real_path) continue;
      if (identityBackfilled.current.has(r.id)) continue;
      identityBackfilled.current.add(r.id);
      void (async () => {
        try {
          const measured = await validateLocalDirectory(r.resource_ref.local_path);
          if (!measured?.ok || !measured.real_path) return;
          await updateResource.mutateAsync({
            resourceId: r.id,
            data: {
              resource_ref: {
                ...r.resource_ref,
                real_path: measured.real_path,
                ...(measured.repo_key ? { repo_key: measured.repo_key } : {}),
                ...(measured.is_git_repo === undefined
                  ? {}
                  : { is_git_repo: measured.is_git_repo }),
              },
            },
          });
        } catch {
          // Unique conflict or a path we cannot see: leave the first row
          // and do not fail the page.
        }
      })();
    }
  }, [resources, desktopMode, localDaemonId, updateResource]);

  const attachedRealPaths = new Set(
    resources
      .filter(isLocalDirectoryRef)
      .filter((r) => r.resource_ref.daemon_id === localDaemonId)
      .map((r) => r.resource_ref.real_path || r.resource_ref.local_path),
  );

  // Duplicate detection runs on the saved list rather than only at save time:
  // this workspace already held five repositories configured both ways before
  // the check existed, and a save-time-only warning would never reach them.
  // findDuplicateSources already collects EVERY github_repo row naming the
  // repository, including two rows spelling one URL differently, so a merge
  // clears it completely. Nothing else may be folded into a group: it is
  // offered as "merge <name>", and a row for a different repository inside it
  // would be deleted by a button that never mentioned it.
  const mergeGroups = findDuplicateSources(resources);

  const handleMergeIntoLocal = async (remotes: ProjectResource[]) => {
    try {
      // Sequential, not Promise.all: a partial failure must leave a list the
      // user can read, and the next attempt re-derives what is still there.
      for (const remote of remotes) {
        await deleteResource.mutateAsync(remote.id);
      }
      toast.success(t(($) => $.resources.duplicate_merged));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t(($) => $.resources.toast_remove_failed);
      toast.error(msg);
    }
  };

  const repoQuery = repoSearch.trim().toLowerCase();
  const filteredRepos =
    workspace?.repos?.filter((repo) => repo.url.toLowerCase().includes(repoQuery)) ?? [];

  const handleAttach = async (url: string) => {
    try {
      await createResource.mutateAsync({
        resource_type: "github_repo",
        resource_ref: { url },
      });
      toast.success(t(($) => $.resources.toast_attached));
    } catch (err) {
      const msg = err instanceof Error ? err.message : t(($) => $.resources.toast_attach_failed);
      toast.error(msg);
    }
  };

  const handleAttachLocalDirectory = async () => {
    if (picking) return;
    setPicking(true);
    try {
      if (!localDaemonId || !daemonStatus.running) {
        toast.error(t(($) => $.resources.toast_local_daemon_not_running));
        return;
      }
      const picked = await pickDirectory();
      if (!picked.ok) {
        if (picked.reason && picked.reason !== "cancelled") {
          toast.error(
            picked.error ?? t(($) => $.resources.toast_local_pick_failed),
          );
        }
        return;
      }
      const path = picked.path ?? "";
      const fallbackLabel = picked.basename ?? path;
      if (attachedLocalPaths.has(path)) {
        toast.error(t(($) => $.resources.toast_local_already_attached));
        return;
      }
      const validation = await validateLocalDirectory(path);
      if (!validation.ok) {
        toast.error(
          localValidationMessage(validation, {
            not_absolute: t(($) => $.resources.local_validate_not_absolute),
            not_found: t(($) => $.resources.local_validate_not_found),
            not_a_directory: t(($) => $.resources.local_validate_not_a_directory),
            not_readable: t(($) => $.resources.local_validate_not_readable),
            not_writable: t(($) => $.resources.local_validate_not_writable),
            unsupported: t(($) => $.resources.local_validate_unsupported),
            fallback: t(($) => $.resources.toast_local_pick_failed),
          }),
        );
        return;
      }
      // Refuse the same directory twice while the folder is still on screen.
      // Identity is the resolved real path, so picking a symlink to a folder
      // already added is caught too — comparing the typed strings would not.
      const identity = validation.real_path || path;
      if (attachedRealPaths.has(identity)) {
        toast.error(t(($) => $.resources.toast_local_already_attached));
        return;
      }
      // Ask for the execution mode before creating. It is part of what the
      // user is choosing — whether tasks edit this folder or hand back a
      // branch — not a setting to discover afterwards.
      setModeError(null);
      setModeDialog({
        path,
        daemonId: localDaemonId,
        // Always in place (DENE-617 invariant 3). A new directory runs tasks
        // IN the folder the user just picked, which is what "I added my
        // project folder" plainly means and costs no disk. Parallel mode is
        // the one that copies the repository per task onto their own drive,
        // so it is an explicit choice, never a preselection — this used to
        // start on parallel for any git repository, and the copies it made
        // were the surprise this change removes.
        mode: "in_place",
        isGitRepo: validation.is_git_repo,
        label: fallbackLabel,
        realPath: validation.real_path,
        repoKey: validation.repo_key,
        defaultWorktreeRoot: validation.default_worktree_root,
        gitRoot: validation.git_root,
      });
      setAddOpen(false);
    } catch (err) {
      const msg =
        err instanceof Error
          ? err.message
          : t(($) => $.resources.toast_local_pick_failed);
      toast.error(msg);
    } finally {
      setPicking(false);
    }
  };

  // What the dialog currently shows as the landing folder: the user's edit
  // when they made one, otherwise the default this machine computed (adding)
  // or the location already stored (editing).
  const worktreeRootShown =
    modeDialog?.worktreeRoot ?? modeDialog?.defaultWorktreeRoot;
  const worktreeRootToStore = modeDialog
    ? worktreeRootForSave(
        worktreeRootShown ?? "",
        modeDialog.defaultWorktreeRoot ?? undefined,
      )
    : undefined;

  // Re-measures a directory that is already saved, and folds the result into
  // the open dialog. Late and best-effort by design: the dialog must be usable
  // the instant it opens, and a machine that cannot answer must not block it.
  const refreshMeasuredDirectory = async (path: string) => {
    try {
      const measured = await validateLocalDirectory(path);
      if (!measured.ok) return;
      setModeDialog((current) =>
        current && current.path === path
          ? {
              ...current,
              isGitRepo: measured.is_git_repo,
              gitRoot: measured.git_root,
              defaultWorktreeRoot: measured.default_worktree_root,
            }
          : current,
      );
    } catch {
      // Unmeasurable is the same as unmeasured: the stored value is shown and
      // the daemon still has the final say.
    }
  };

  const gitInitMessage = (reason?: string, error?: string) =>
    reason === "inside_repo"
      ? t(($) => $.resources.plain_folder_inside_repo)
      : error || t(($) => $.resources.plain_folder_init_failed);

  // Creates the repository, then re-measures. A success the measurement does
  // not agree with is a failure: the badge would otherwise disappear while
  // the folder is still not a repository.
  const initGitAt = async (
    path: string,
  ): Promise<
    | { ok: true; measured: ValidateLocalDirectoryResult }
    | { ok: false; message: string }
  > => {
    const result = await initLocalGit(path);
    if (!result.ok) {
      const message = gitInitMessage(result.reason, result.error);
      setGitInitError(message);
      return { ok: false, message };
    }
    const measured = await validateLocalDirectory(path);
    if (!measured.ok || measured.is_git_repo !== true) {
      const message = gitInitMessage(undefined, measured.ok ? undefined : measured.error);
      setGitInitError(message);
      return { ok: false, message };
    }
    setGitInitError(null);
    return { ok: true, measured };
  };

  const handleInitGitInDialog = async () => {
    if (!modeDialog || gitInitPending) return;
    setGitInitPending(true);
    setGitInitError(null);
    try {
      const outcome = await initGitAt(modeDialog.path);
      if (!outcome.ok) return;
      const measured = outcome.measured;
      setModeDialog((current) =>
        current && current.path === modeDialog.path
          ? {
              ...current,
              isGitRepo: true,
              gitRoot: measured.git_root,
              realPath: measured.real_path ?? current.realPath,
              repoKey: measured.repo_key ?? current.repoKey,
              defaultWorktreeRoot: measured.default_worktree_root,
            }
          : current,
      );
      if (modeDialog.resource) {
        const ref = modeDialog.resource.resource_ref;
        await updateResource.mutateAsync({
          resourceId: modeDialog.resource.id,
          data: {
            resource_ref: {
              ...ref,
              is_git_repo: true,
              ...(measured.real_path ? { real_path: measured.real_path } : {}),
              ...(measured.repo_key ? { repo_key: measured.repo_key } : {}),
            },
          },
        });
      }
    } catch (err) {
      setGitInitError(
        gitInitMessage(undefined, err instanceof Error ? err.message : undefined),
      );
    } finally {
      setGitInitPending(false);
    }
  };

  const handleInitGitOnRow = async (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => {
    if (gitInitPending) return;
    setGitInitPending(true);
    setGitInitError(null);
    try {
      const outcome = await initGitAt(resource.resource_ref.local_path);
      if (!outcome.ok) {
        toast.error(outcome.message);
        return;
      }
      const measured = outcome.measured;
      await updateResource.mutateAsync({
        resourceId: resource.id,
        data: {
          resource_ref: {
            ...resource.resource_ref,
            is_git_repo: true,
            ...(measured.real_path ? { real_path: measured.real_path } : {}),
            ...(measured.repo_key ? { repo_key: measured.repo_key } : {}),
          },
        },
      });
    } catch (err) {
      toast.error(
        err instanceof Error
          ? err.message
          : t(($) => $.resources.plain_folder_init_failed),
      );
    } finally {
      setGitInitPending(false);
    }
  };

  const handleConfirmMode = async (mode: LocalDirectoryExecutionMode) => {
    if (!modeDialog || modeSaving) return;
    // A landing folder the daemon would refuse must not be saved: the refusal
    // would arrive as a failed task, long after the dialog is gone.
    if (
      mode === "worktree" &&
      worktreeRootProblem(
        worktreeRootShown ?? "",
        modeDialog.gitRoot,
        boundIdentitiesForDialog,
      ) !== undefined
    ) {
      return;
    }
    if (mode === "worktree" && worktreeRootShown) {
      const writable = await validateWritablePath(worktreeRootShown);
      if (!writable) {
        setModeError(t(($) => $.resources.mode_worktree_root_not_writable));
        return;
      }
    }
    setModeSaving(true);
    setModeError(null);
    const apiMode = apiExecutionMode(mode, serverAcceptsShared);
    const needOverride = needsLocalSharedOverride(mode, serverAcceptsShared);
    const daemonId = modeDialog.daemonId ?? localDaemonId;
    try {
      if (modeDialog.resource) {
        const ref = modeDialog.resource.resource_ref;
        const stored = executionModeOf(ref);
        const currentlySharedLocally = hasOverride(ref.daemon_id, ref.local_path);
        const currentDisplay = displayedExecutionMode(ref, currentlySharedLocally);
        const apiChanged = stored !== apiMode;
        const overrideChanged = currentlySharedLocally !== needOverride;
        if (!apiChanged && !overrideChanged && currentDisplay === mode) {
          setModeDialog(null);
          return;
        }
        const rootChanged = (ref.worktree_root ?? "") !== (worktreeRootToStore ?? "");
        if (apiChanged || rootChanged) {
          await updateResource.mutateAsync({
            resourceId: modeDialog.resource.id,
            data: {
              // Spread first so every other ref field survives the edit — the
              // server replaces the whole ref, it does not deep-merge. The
              // landing folder is then set or REMOVED, so clearing it back to
              // the default is expressible.
              resource_ref: {
                ...ref,
                execution_mode: apiMode,
                ...(worktreeRootToStore
                  ? { worktree_root: worktreeRootToStore }
                  : { worktree_root: undefined }),
              },
            },
          });
        }
      } else {
        if (!localDaemonId) return;
        await createResource.mutateAsync({
          resource_type: "local_directory",
          resource_ref: {
            local_path: modeDialog.path,
            daemon_id: localDaemonId,
            label: modeDialog.label ?? modeDialog.path,
            execution_mode: apiMode,
            // Identity and repository, measured on this machine at pick time.
            // Omitted rather than sent empty when unknown: an empty repo_key
            // means "unidentifiable", and the server must be able to tell that
            // apart from a key it was never given.
            ...(modeDialog.realPath ? { real_path: modeDialog.realPath } : {}),
            ...(modeDialog.repoKey ? { repo_key: modeDialog.repoKey } : {}),
            ...(modeDialog.isGitRepo === undefined
              ? {}
              : { is_git_repo: modeDialog.isGitRepo }),
            // Only a location the user actually chose is stored. The default
            // is "beside the repository", which follows a repository they
            // later move; a stored literal would keep pointing at the old place.
            ...(worktreeRootToStore ? { worktree_root: worktreeRootToStore } : {}),
          },
        });
      }
      if (daemonId) {
        const result = await setOverride(daemonId, modeDialog.path, needOverride);
        if (needOverride && !result.ok) {
          setModeError(
            result.error ?? t(($) => $.resources.toast_local_mode_update_failed),
          );
          return;
        }
      }
      if (needOverride) {
        toast.success(t(($) => $.resources.toast_local_shared_via_daemon));
      } else if (modeDialog.resource) {
        toast.success(t(($) => $.resources.toast_local_mode_updated));
      } else {
        toast.success(t(($) => $.resources.toast_local_attached));
      }
      setModeDialog(null);
    } catch (err) {
      // Keep the dialog open and show the reason inline: the most likely
      // failure is the server's daemon-version gate, and closing the dialog
      // would leave the user with a toast and no way to act on it.
      const raw =
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_local_mode_update_failed);
      setModeError(
        isSharedModeRejectedByServer(raw)
          ? t(($) => $.resources.mode_shared_rejected_by_server)
          : raw,
      );
    } finally {
      setModeSaving(false);
    }
  };

  // Which local directory a run WRITES is the first one on this machine, so
  // reordering is the control over that — not a cosmetic list preference. The
  // rule lives in local-directory-order.ts; this only sends what it decided.
  const handleMoveLocalDirectory = async (
    resource: ProjectResource,
    direction: MoveDirection,
  ) => {
    const patches = moveLocalDirectory(
      resources,
      resource.id,
      localDaemonId,
      direction,
    );
    if (patches.length === 0) return;
    if (reordering) return;
    setReordering(true);
    try {
      // Sequential, not concurrent: the list is a handful of rows, and two
      // position writes racing on one project would leave an order neither
      // request asked for.
      for (const patch of patches) {
        // Position only — resending resource_ref on an unrelated edit is how
        // a rename silently dropped a directory's isolation (#7113).
        await updateResource.mutateAsync({
          resourceId: patch.resourceId,
          data: { position: patch.position },
        });
      }
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_local_mode_update_failed),
      );
    } finally {
      setReordering(false);
    }
  };

  const handleRemove = async (resource: ProjectResource) => {
    try {
      await deleteResource.mutateAsync(resource.id);
      toast.success(t(($) => $.resources.toast_removed));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.resources.toast_remove_failed),
      );
    }
  };

  const handleRenameLocalDirectory = async (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => {
    const trimmed = nextLabel.trim();
    if (trimmed === localDirectoryLabel(resource)) return;
    try {
      // Top-level label ONLY — renaming must not resend resource_ref.
      //
      // The server replaces the ref wholesale with whatever it can parse, so a
      // server that predates a ref field drops it and answers 200. On a backend
      // rolled back below v0.4.25 (documented as supported while the runtimes
      // stay current) that turned "rename this folder" into "silently forget
      // this folder was isolated", and the next task edited the working copy
      // (#7113). Omitting the ref keeps the stored one untouched on every
      // server version — the same reason it must not be resent for any other
      // unrelated edit either.
      await updateResource.mutateAsync({
        resourceId: resource.id,
        data: { label: trimmed },
      });
      toast.success(t(($) => $.resources.toast_local_renamed));
    } catch (err) {
      const msg =
        err instanceof Error
          ? err.message
          : t(($) => $.resources.toast_local_rename_failed);
      toast.error(msg);
    }
  };

  return (
    <div>
      <button
        type="button"
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.resources.section_header)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="pl-2 space-y-1.5">
          {codeDecision && <CodeDecisionBanner decision={codeDecision} />}
          {decisionQuery.isError && !codeDecision && (
            <p className="px-2 py-1 text-micro text-muted-foreground">
              {t(($) => $.resources.code_decision_unavailable)}
            </p>
          )}
          {resources.length === 0 && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.resources.empty)}
            </p>
          )}
          <DuplicateSourceBanner
            groups={mergeGroups}
            onMerge={handleMergeIntoLocal}
            disabled={deleteResource.isPending}
          />
          {resources.length > 0 && (
            <div className="max-h-64 space-y-1.5 overflow-y-auto pr-1">
              {resources.map((resource) => (
                <ResourceRow
                  key={resource.id}
                  resource={resource}
                  localDaemonId={localDaemonId}
                  localSharedOverride={
                    isLocalDirectoryRef(resource) &&
                    hasOverride(
                      resource.resource_ref.daemon_id,
                      resource.resource_ref.local_path,
                    )
                  }
                  canEdit={desktopMode}
                  canMoveUp={canMoveLocalDirectory(
                    resources,
                    resource.id,
                    localDaemonId,
                    "up",
                  )}
                  canMoveDown={canMoveLocalDirectory(
                    resources,
                    resource.id,
                    localDaemonId,
                    "down",
                  )}
                  onMove={(direction) =>
                    void handleMoveLocalDirectory(resource, direction)
                  }
                  movePending={reordering}
                  onRemove={() => handleRemove(resource)}
                  onRenameLocalDirectory={handleRenameLocalDirectory}
                  onEditLocalDirectoryMode={(target) => {
                    setModeError(null);
                    setModeDialog({
                      path: target.resource_ref.local_path,
                      daemonId: target.resource_ref.daemon_id,
                      mode: displayedExecutionMode(
                        target.resource_ref,
                        hasOverride(
                          target.resource_ref.daemon_id,
                          target.resource_ref.local_path,
                        ),
                      ),
                      // Opened with what the row already knows; the measured
                      // fields below arrive a moment later. Unknown means the
                      // option stays available and the daemon has the final
                      // say, so the dialog is useful before they land.
                      isGitRepo: undefined,
                      resource: target,
                      worktreeRoot: target.resource_ref.worktree_root,
                    });
                    // Upgrading an existing directory to parallel is the same
                    // decision as choosing it when adding one, so it gets the
                    // same information: where the copies would land, and
                    // whether a folder typed there sits inside the repository.
                    // Only this machine can measure that, and only for a
                    // directory it holds — a resource pinned elsewhere, or a
                    // web client, simply gets no measurement and falls back to
                    // showing the stored value.
                    void refreshMeasuredDirectory(target.resource_ref.local_path);
                  }}
                  onInitGit={(target) => void handleInitGitOnRow(target)}
                  gitInitPending={gitInitPending}
                />
              ))}
            </div>
          )}
          <Popover
            open={addOpen}
            onOpenChange={(v) => {
              setAddOpen(v);
              if (!v) setRepoSearch("");
            }}
          >
            <PopoverTrigger
              render={
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2 text-caption text-muted-foreground hover:text-foreground"
                >
                  <Plus className="size-3" />
                  {t(($) => $.resources.add_button)}
                </Button>
              }
            />
            <PopoverContent align="start" className="w-72 p-2 space-y-2">
              <div className="text-caption font-medium text-muted-foreground">
                {t(($) => $.resources.popover_title)}
              </div>
              {workspace?.repos && workspace.repos.length > 0 && (
                <>
                  <div className="relative">
                    <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                    <input
                      type="text"
                      value={repoSearch}
                      onChange={(e) => setRepoSearch(e.target.value)}
                      aria-label={t(($) => $.resources.repos_search_placeholder)}
                      placeholder={t(($) => $.resources.repos_search_placeholder)}
                      className="h-8 w-full rounded-md border bg-transparent pl-7 pr-2 text-caption outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
                    />
                  </div>
                  <div className="max-h-48 space-y-1 overflow-y-auto">
                    {filteredRepos.length === 0 && repoQuery && (
                      <p className="py-2 text-center text-caption text-muted-foreground">
                        {t(($) => $.resources.repos_search_empty)}
                      </p>
                    )}
                    {filteredRepos.map((repo) => {
                      const isAttached = attachedUrls.has(repo.url);
                      const isDisabled = isAttached || createResource.isPending;
                      return (
                        // Use aria-disabled instead of the native `disabled` attribute so
                        // hover events still reach the tooltip trigger on attached rows
                        // (browsers suppress pointer events on disabled form controls).
                        <button
                          key={repo.url}
                          type="button"
                          aria-disabled={isDisabled}
                          onClick={async () => {
                            if (isDisabled) return;
                            await handleAttach(repo.url);
                            setAddOpen(false);
                          }}
                          className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-caption text-left hover:bg-accent transition-colors aria-disabled:opacity-50 aria-disabled:cursor-not-allowed aria-disabled:hover:bg-transparent"
                        >
                          <FolderGit className="size-3.5" />
                          <Tooltip>
                            <TooltipTrigger
                              render={
                                <span className="truncate flex-1">{githubShortLabel(repo.url)}</span>
                              }
                            />
                            <TooltipContent side="top">{repo.url}</TooltipContent>
                          </Tooltip>
                          {isAttached && (
                            <span className="text-micro text-muted-foreground">
                              {t(($) => $.resources.attached_badge)}
                            </span>
                          )}
                        </button>
                      );
                    })}
                  </div>
                </>
              )}
              <CustomRepoForm
                onSubmit={async (url) => {
                  await handleAttach(url);
                  setAddOpen(false);
                }}
              />
            </PopoverContent>
          </Popover>
          {desktopMode && (
            <div className="flex flex-col">
              <Button
                variant="ghost"
                size="sm"
                className="h-7 justify-start px-2 text-caption text-muted-foreground hover:text-foreground"
                disabled={
                  picking || createResource.isPending || !daemonStatus.running
                }
                onClick={() => {
                  void handleAttachLocalDirectory();
                }}
              >
                <FolderOpen className="size-3" />
                {t(($) => $.resources.add_local_directory_button)}
              </Button>
              {!daemonStatus.running && (
                <p className="px-2 pt-0.5 text-micro text-muted-foreground">
                  {t(($) => $.resources.local_daemon_offline_hint)}
                </p>
              )}
            </div>
          )}
        </div>
      )}
      {modeDialog && (
        <LocalDirectoryModeDialog
          open
          onOpenChange={(next) => {
            if (!next) {
              setModeDialog(null);
              setModeError(null);
              setGitInitError(null);
            }
          }}
          path={modeDialog.path}
          value={modeDialog.mode}
          unavailableReason={worktreeUnavailableReason(
            modeDialog.isGitRepo,
            serverValidatesWorktree,
          )}
          sharedUnavailable={sharedUnavailable}
          sharedUsesLocalOverride={sharedUsesLocalOverride}
          worktreeRootPreview={worktreeRootShown}
          gitRoot={modeDialog.gitRoot}
          boundIdentities={boundIdentitiesForDialog}
          onWorktreeRootChange={(next) =>
            setModeDialog((current) =>
              current ? { ...current, worktreeRoot: next } : current,
            )
          }
          errorMessage={modeError ?? undefined}
          saving={modeSaving}
          confirmLabel={
            modeDialog.resource
              ? t(($) => $.resources.mode_save)
              : t(($) => $.resources.mode_add)
          }
          onConfirm={(mode) => void handleConfirmMode(mode)}
          plainFolder={isPlainFolder(modeDialog.isGitRepo, modeDialog.gitRoot)}
          onInitGit={() => void handleInitGitInDialog()}
          initGitPending={gitInitPending}
          initGitError={gitInitError ?? undefined}
        />
      )}
    </div>
  );
}

interface ResourceRowProps {
  resource: ProjectResource;
  localDaemonId: string | null;
  localSharedOverride: boolean;
  canEdit: boolean;
  /** False at the ends of this machine's group, and on every other row type. */
  canMoveUp: boolean;
  canMoveDown: boolean;
  /** True while a reorder's position writes are still in flight. */
  movePending: boolean;
  onMove: (direction: MoveDirection) => void;
  onRemove: () => void;
  onRenameLocalDirectory: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
  onEditLocalDirectoryMode: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
  onInitGit: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
  gitInitPending: boolean;
}

function ResourceRow({
  resource,
  localDaemonId,
  localSharedOverride,
  canEdit,
  canMoveUp,
  canMoveDown,
  movePending,
  onMove,
  onRemove,
  onRenameLocalDirectory,
  onEditLocalDirectoryMode,
  onInitGit,
  gitInitPending,
}: ResourceRowProps) {
  const { t } = useT("projects");
  if (isGithubRef(resource)) {
    const ref = resource.resource_ref;
    const display = resource.label || (ref.ref ? `${githubShortLabel(ref.url)} @ ${ref.ref}` : githubShortLabel(ref.url));
    const tooltip = ref.ref ? `${ref.url}\nref: ${ref.ref}` : ref.url;
    return (
      <div className="flex items-center gap-2 text-caption group">
        <FolderGit className="size-3.5 text-muted-foreground shrink-0" />
        <Tooltip>
          <TooltipTrigger
            render={
              <a
                href={ref.url}
                target="_blank"
                rel="noopener noreferrer"
                className="truncate flex-1 hover:underline"
              >
                {display}
              </a>
            }
          />
          <TooltipContent side="top" className="whitespace-pre-line">{tooltip}</TooltipContent>
        </Tooltip>
        <button
          type="button"
          onClick={onRemove}
          className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
          title={t(($) => $.resources.remove_tooltip)}
        >
          <Trash2 className="size-3 text-muted-foreground" />
        </button>
      </div>
    );
  }

  if (isLocalDirectoryRef(resource)) {
    return (
      <LocalDirectoryRow
        resource={resource}
        localDaemonId={localDaemonId}
        localSharedOverride={localSharedOverride}
        canEdit={canEdit}
        canMoveUp={canMoveUp}
        canMoveDown={canMoveDown}
        movePending={movePending}
        onMove={onMove}
        onRemove={onRemove}
        onRename={onRenameLocalDirectory}
        onEditMode={onEditLocalDirectoryMode}
        onInitGit={onInitGit}
        gitInitPending={gitInitPending}
      />
    );
  }

  return (
    <div className="flex items-center gap-2 text-caption text-muted-foreground">
      <span className="truncate flex-1">
        {resource.label || resource.resource_type}
      </span>
      <button
        type="button"
        onClick={onRemove}
        className="rounded-sm p-0.5 hover:bg-accent"
        title={t(($) => $.resources.remove_tooltip)}
      >
        <Trash2 className="size-3" />
      </button>
    </div>
  );
}

// The row's hover-reveal rule has to survive `disabled`. Written as four
// explicit states because `disabled:opacity-30` alone is MORE specific than
// `group-hover:opacity-100`: a disabled arrow stayed visible without hovering
// the row, while the one the user could actually click was the hidden one.
const MOVE_ARROW_CLASS =
  "opacity-0 disabled:opacity-0 group-hover:opacity-100 group-hover:disabled:opacity-30 transition-opacity rounded-sm p-0.5 hover:bg-accent disabled:hover:bg-transparent";

interface LocalDirectoryRowProps {
  resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef };
  localDaemonId: string | null;
  localSharedOverride: boolean;
  canEdit: boolean;
  canMoveUp: boolean;
  canMoveDown: boolean;
  movePending: boolean;
  onMove: (direction: MoveDirection) => void;
  onRemove: () => void;
  onRename: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
    nextLabel: string,
  ) => Promise<void>;
  onEditMode: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
  onInitGit: (
    resource: ProjectResource & { resource_ref: LocalDirectoryResourceRef },
  ) => void;
  gitInitPending: boolean;
}

function LocalDirectoryRow({
  resource,
  localDaemonId,
  localSharedOverride,
  canEdit,
  canMoveUp,
  canMoveDown,
  movePending,
  onMove,
  onRemove,
  onRename,
  onEditMode,
  onInitGit,
  gitInitPending,
}: LocalDirectoryRowProps) {
  const { t } = useT("projects");
  const ref = resource.resource_ref;
  const mode = displayedExecutionMode(ref, localSharedOverride);
  const display = localDirectoryLabel(resource);
  const isForeignDaemon =
    localDaemonId !== null && ref.daemon_id !== localDaemonId;
  const isLocalUnknown = localDaemonId === null;
  // "disabled" in the spec sense — visual de-emphasis + no chat hint, and
  // rename is hidden on foreign / unknown-daemon rows because the label
  // belongs to the owning device. Delete stays available so the user can
  // drop a stale registration from any device.
  const mismatch = isForeignDaemon || isLocalUnknown;

  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(display);

  const startEdit = () => {
    setDraft(display);
    setEditing(true);
  };
  const commit = async () => {
    setEditing(false);
    await onRename(resource, draft);
  };
  const cancel = () => {
    setEditing(false);
    setDraft(display);
  };

  return (
    <div
      className={`flex items-center gap-2 text-caption group ${
        mismatch ? "opacity-60" : ""
      }`}
    >
      <FolderOpen className="size-3.5 text-muted-foreground shrink-0" />
      {editing ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => void commit()}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void commit();
            } else if (e.key === "Escape") {
              e.preventDefault();
              cancel();
            }
          }}
          className="flex-1 min-w-0 rounded-sm border bg-transparent px-1 py-0.5 text-caption outline-none focus-visible:ring-1 focus-visible:ring-ring"
          aria-label={t(($) => $.resources.local_rename_label)}
        />
      ) : (
        <Tooltip>
          <TooltipTrigger
            render={
              <span className="min-w-0 flex-1">
                <span className="block truncate">{display}</span>
                {ref.is_git_repo === false && (
                  <span className="block truncate text-micro text-amber-700 dark:text-amber-400">
                    {t(($) => $.resources.plain_folder_badge)}
                  </span>
                )}
              </span>
            }
          />
          <TooltipContent side="top">
            <div className="space-y-0.5 text-micro">
              <div className="font-mono">{ref.local_path}</div>
              {mismatch && (
                <div className="text-muted-foreground">
                  {isLocalUnknown
                    ? t(($) => $.resources.local_no_daemon_tooltip)
                    : t(($) => $.resources.local_other_machine_tooltip)}
                </div>
              )}
            </div>
          </TooltipContent>
        </Tooltip>
      )}
      {/* Always visible, unlike the hover-only actions: without it there is no
          way to tell whether tasks on this folder edit it directly or hand back
          a branch, which is the first thing someone asks when a task queues (or
          does not). */}
      {mode === "worktree" && !editing && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Badge variant="secondary" className="shrink-0 gap-1 font-normal">
                <GitBranch className="size-3" />
                {t(($) => $.resources.mode_badge_worktree)}
              </Badge>
            }
          />
          <TooltipContent side="top">
            {t(($) => $.resources.mode_badge_worktree_tooltip)}
          </TooltipContent>
        </Tooltip>
      )}
      {mode === "shared" && !editing && (
        <Tooltip>
          <TooltipTrigger
            render={
              <Badge variant="secondary" className="shrink-0 gap-1 font-normal">
                <Folders className="size-3" />
                {t(($) => $.resources.mode_badge_shared)}
              </Badge>
            }
          />
          <TooltipContent side="top">
            {t(($) => $.resources.mode_badge_shared_tooltip)}
          </TooltipContent>
        </Tooltip>
      )}
      {ref.is_git_repo === false && !mismatch && canEdit && !editing && (
        <button
          type="button"
          disabled={gitInitPending}
          onClick={() => onInitGit(resource)}
          className="shrink-0 text-micro text-muted-foreground underline-offset-2 hover:underline disabled:opacity-50"
        >
          {t(($) => $.resources.plain_folder_init)}
        </button>
      )}
      {/* Reordering, not decoration: the first directory on this machine is the
          one a run writes, so these are how the user picks it. Rendered only
          when there IS a sibling to move past, because a permanently disabled
          arrow on a single-directory project is a control that never means
          anything. */}
      {!editing && (canMoveUp || canMoveDown) && (
        <>
          <button
            type="button"
            disabled={!canMoveUp || movePending}
            onClick={() => onMove("up")}
            className={MOVE_ARROW_CLASS}
            title={t(($) => $.resources.local_directory_move_up_tooltip)}
            aria-label={t(($) => $.resources.local_directory_move_up_tooltip)}
          >
            <ArrowUp className="size-3 text-muted-foreground" />
          </button>
          <button
            type="button"
            disabled={!canMoveDown || movePending}
            onClick={() => onMove("down")}
            className={MOVE_ARROW_CLASS}
            title={t(($) => $.resources.local_directory_move_down_tooltip)}
            aria-label={t(($) => $.resources.local_directory_move_down_tooltip)}
          >
            <ArrowDown className="size-3 text-muted-foreground" />
          </button>
        </>
      )}
      {/* Not gated on `mismatch`: switching the mode only rewrites a field, so
          it works from the web app or another device, unlike rename (whose
          label belongs to the owning machine) or the folder picker. */}
      {!editing && (
        <button
          type="button"
          onClick={() => onEditMode(resource)}
          className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
          title={t(($) => $.resources.mode_edit_tooltip)}
        >
          <GitBranch className="size-3 text-muted-foreground" />
        </button>
      )}
      {canEdit && !mismatch && !editing && (
        <button
          type="button"
          onClick={startEdit}
          className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
          title={t(($) => $.resources.local_rename_tooltip)}
        >
          <Pencil className="size-3 text-muted-foreground" />
        </button>
      )}
      <button
        type="button"
        onClick={onRemove}
        className="opacity-0 group-hover:opacity-100 transition-opacity rounded-sm p-0.5 hover:bg-accent"
        title={t(($) => $.resources.remove_tooltip)}
      >
        <Trash2 className="size-3 text-muted-foreground" />
      </button>
    </div>
  );
}

function CustomRepoForm({
  onSubmit,
}: {
  onSubmit: (url: string) => Promise<void> | void;
}) {
  const { t } = useT("projects");
  const [url, setUrl] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const handle = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = url.trim();
    if (!trimmed) return;
    setSubmitting(true);
    try {
      await onSubmit(trimmed);
      setUrl("");
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <form onSubmit={handle} className="flex items-center gap-1.5 pt-1 border-t">
      <input
        type="text"
        value={url}
        onChange={(e) => setUrl(e.target.value)}
        placeholder={t(($) => $.resources.url_placeholder)}
        className="flex-1 bg-transparent text-caption px-2 py-1 outline-none placeholder:text-muted-foreground"
      />
      <Button
        type="submit"
        size="sm"
        variant="ghost"
        className="h-6 px-2 text-caption"
        disabled={!url.trim() || submitting}
      >
        {t(($) => $.resources.url_submit)}
      </Button>
    </form>
  );
}

function localValidationMessage(
  result: ValidateLocalDirectoryResult,
  strings: {
    not_absolute: string;
    not_found: string;
    not_a_directory: string;
    not_readable: string;
    not_writable: string;
    unsupported: string;
    fallback: string;
  },
): string {
  switch (result.reason) {
    case "not_absolute":
      return strings.not_absolute;
    case "not_found":
      return strings.not_found;
    case "not_a_directory":
      return strings.not_a_directory;
    case "not_readable":
      return strings.not_readable;
    case "not_writable":
      return strings.not_writable;
    case "unsupported":
      return strings.unsupported;
    case "error":
    default:
      return result.error ?? strings.fallback;
  }
}
