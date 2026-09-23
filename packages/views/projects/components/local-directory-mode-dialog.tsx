"use client";

import { useEffect, useState } from "react";
import { Folders, GitBranch, Pencil, TriangleAlert } from "lucide-react";
import type { LocalDirectoryExecutionMode } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n/use-t";
import type { WorktreeUnavailableReason } from "./local-directory-mode";
import { PlainFolderGitOffer } from "./plain-folder-git-offer";
import { worktreeRootProblem } from "./worktree-root";

export type { WorktreeUnavailableReason } from "./local-directory-mode";

interface LocalDirectoryModeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Absolute path being configured, shown so the user knows what they picked. */
  path: string;
  /** Mode to preselect — the current mode when editing, in_place when adding. */
  value: LocalDirectoryExecutionMode;
  /** Set when worktree cannot be chosen; the option renders disabled with a reason. */
  unavailableReason?: WorktreeUnavailableReason;
  /** Set when shared cannot be chosen (server would silently drop the field
   *  AND this client cannot record a local daemon override). */
  sharedUnavailable?: boolean;
  /** Set when shared is selectable but will be honoured locally, not stored
   *  as `execution_mode=shared` on the connected server. */
  sharedUsesLocalOverride?: boolean;
  /**
   * Where parallel mode would put this folder's working copies — the
   * repository's sibling, on the user's own disk.
   *
   * Shown while the option is still a choice. Parallel mode's cost is a full
   * working copy (plus its dependencies) per task, charged to the user's own
   * drive; naming the directory is what turns that from a surprise into a
   * decision. Absent on web and on older desktop builds, which cannot read
   * the filesystem — the copy still lands there, we just cannot say so.
   */
  worktreeRootPreview?: string;
  /** The repository root, when the machine could read it. Used only to warn
   *  that a typed landing folder sits inside the repository. */
  gitRoot?: string;
  /** Other directories already bound on this machine. A landing folder that
   *  overlaps one of them is refused before save. */
  boundIdentities?: string[];
  /** Called when the user edits where parallel-mode copies should land. When
   *  absent the location is shown but not editable — a surface that cannot
   *  check a path should not invite one to be typed. */
  onWorktreeRootChange?: (next: string) => void;
  /** Server-side rejection to show inline (e.g. a 422 that only the API can detect). */
  errorMessage?: string;
  saving?: boolean;
  /** Confirm label differs between adding a resource and editing one. */
  confirmLabel: string;
  onConfirm: (mode: LocalDirectoryExecutionMode) => void;
  /** Set for a folder the machine measured as having no Git. The offer can
   *  be skipped: confirming still adds the folder. */
  plainFolder?: boolean;
  onInitGit?: () => void;
  initGitPending?: boolean;
  initGitError?: string;
}

/**
 * Mode picker for a local_directory resource.
 *
 * Deliberately does NOT surface the raw `in_place` / `worktree` / `shared`
 * identifiers as the primary label. The choice a user is actually making is
 * about how they get their results back — edits appearing in their working
 * copy versus a branch they review versus concurrent work they isolate
 * themselves — so the options lead with that, and the identifier is only a
 * secondary hint for anyone matching this against the CLI or the docs.
 */
export function LocalDirectoryModeDialog({
  open,
  onOpenChange,
  path,
  value,
  unavailableReason,
  sharedUnavailable,
  sharedUsesLocalOverride,
  worktreeRootPreview,
  gitRoot,
  boundIdentities,
  onWorktreeRootChange,
  errorMessage,
  saving = false,
  confirmLabel,
  onConfirm,
  plainFolder = false,
  onInitGit,
  initGitPending = false,
  initGitError,
}: LocalDirectoryModeDialogProps) {
  const { t } = useT("projects");
  const [selected, setSelected] = useState<LocalDirectoryExecutionMode>(value);

  // Re-sync when the dialog is reopened for a different resource, otherwise the
  // previous row's mode would be preselected for this one.
  useEffect(() => {
    if (open) setSelected(value);
  }, [open, value]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t(($) => $.resources.mode_dialog_title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.resources.mode_dialog_description)}
          </DialogDescription>
        </DialogHeader>

        <div className="rounded-md bg-muted px-2.5 py-1.5 font-mono text-micro text-muted-foreground break-all">
          {path}
        </div>

        {plainFolder && onInitGit && (
          <PlainFolderGitOffer
            onInit={onInitGit}
            pending={initGitPending}
            error={initGitError}
          />
        )}

        <LocalDirectoryModeOptions
          value={selected}
          onChange={setSelected}
          unavailableReason={unavailableReason}
          sharedUnavailable={sharedUnavailable}
          sharedUsesLocalOverride={sharedUsesLocalOverride}
          worktreeRootPreview={worktreeRootPreview}
          gitRoot={gitRoot}
          boundIdentities={boundIdentities}
          onWorktreeRootChange={onWorktreeRootChange}
        />

        {errorMessage && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
            <TriangleAlert className="size-3.5 mt-0.5 shrink-0" />
            <span>{errorMessage}</span>
          </div>
        )}

        <DialogFooter>
          <Button
            variant="ghost"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t(($) => $.resources.mode_cancel)}
          </Button>
          <Button onClick={() => onConfirm(selected)} disabled={saving}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

interface LocalDirectoryModeOptionsProps {
  value: LocalDirectoryExecutionMode;
  onChange: (mode: LocalDirectoryExecutionMode) => void;
  unavailableReason?: WorktreeUnavailableReason;
  sharedUnavailable?: boolean;
  sharedUsesLocalOverride?: boolean;
  worktreeRootPreview?: string;
  gitRoot?: string;
  boundIdentities?: string[];
  onWorktreeRootChange?: (next: string) => void;
}

/**
 * The three-option choice itself, without any surrounding chrome.
 *
 * Shared so the dialog (editing an existing resource) and the compact picker in
 * the create-project modal offer literally the same options, copy and blocked
 * states — the decision is identical, only the container differs.
 */
export function LocalDirectoryModeOptions({
  value,
  onChange,
  unavailableReason,
  sharedUnavailable = false,
  sharedUsesLocalOverride = false,
  worktreeRootPreview,
  gitRoot,
  boundIdentities,
  onWorktreeRootChange,
}: LocalDirectoryModeOptionsProps) {
  const { t } = useT("projects");
  const worktreeDisabled = unavailableReason !== undefined;
  const rootProblem = worktreeRootProblem(
    worktreeRootPreview ?? "",
    gitRoot,
    boundIdentities,
  );

  return (
    <div className="flex flex-col gap-2">
      <ModeOption
        icon={<Pencil className="size-4" />}
        title={t(($) => $.resources.mode_in_place_title)}
        description={t(($) => $.resources.mode_in_place_description)}
        identifier="in_place"
        selected={value === "in_place"}
        onSelect={() => onChange("in_place")}
      />
      <ModeOption
        icon={<GitBranch className="size-4" />}
        title={t(($) => $.resources.mode_worktree_title)}
        description={t(($) => $.resources.mode_worktree_description)}
        identifier="worktree"
        selected={value === "worktree"}
        disabled={worktreeDisabled}
        disabledReason={
          unavailableReason === "not_git"
            ? t(($) => $.resources.mode_worktree_needs_git)
            : unavailableReason === "server_outdated"
              ? t(($) => $.resources.mode_worktree_needs_server_upgrade)
              : undefined
        }
        note={
          // Two keys, not one with an empty interpolation: a sentence that
          // promises to name the directory and then names nothing is worse
          // than one that says where copies go without the exact path.
          worktreeDisabled
            ? undefined
            : worktreeRootPreview
              ? t(($) => $.resources.mode_worktree_cost, { path: worktreeRootPreview })
              : t(($) => $.resources.mode_worktree_cost_unknown_path)
        }
        onSelect={() => onChange("worktree")}
      />
      {/* The landing folder, editable, and only while parallel is the choice
          in front of the user — it is the one mode it applies to, and showing
          a path field next to two modes that ignore it invites the wrong
          edit. Shown as read-only text where the platform cannot check it
          (web has no filesystem), because a field that silently accepts a
          path nothing validated is worse than one that does not exist. */}
      {value === "worktree" && !worktreeDisabled && worktreeRootPreview && (
        <div className="ml-7 flex flex-col gap-1">
          <label className="text-micro text-muted-foreground" htmlFor="worktree-root">
            {t(($) => $.resources.mode_worktree_root_label)}
          </label>
          {onWorktreeRootChange ? (
            <Input
              id="worktree-root"
              className="font-mono text-micro"
              value={worktreeRootPreview}
              aria-invalid={rootProblem !== undefined}
              onChange={(e) => onWorktreeRootChange(e.target.value)}
            />
          ) : (
            <div className="rounded-md bg-muted px-2.5 py-1.5 font-mono text-micro text-muted-foreground break-all">
              {worktreeRootPreview}
            </div>
          )}
          {rootProblem && (
            <p className="text-micro text-destructive">
              {rootProblem === "not_absolute"
                ? t(($) => $.resources.mode_worktree_root_not_absolute)
                : rootProblem === "conflicts_with_binding"
                  ? t(($) => $.resources.mode_worktree_root_conflicts)
                  : t(($) => $.resources.mode_worktree_root_inside_repo)}
            </p>
          )}
        </div>
      )}
      <ModeOption
        icon={<Folders className="size-4" />}
        title={t(($) => $.resources.mode_shared_title)}
        description={t(($) => $.resources.mode_shared_description)}
        identifier="shared"
        selected={value === "shared"}
        disabled={sharedUnavailable}
        disabledReason={
          sharedUnavailable
            ? t(($) => $.resources.mode_shared_needs_server_upgrade)
            : undefined
        }
        note={
          !sharedUnavailable && sharedUsesLocalOverride
            ? t(($) => $.resources.mode_shared_uses_local_override)
            : undefined
        }
        onSelect={() => onChange("shared")}
      />
    </div>
  );
}

interface ModeOptionProps {
  icon: React.ReactNode;
  title: string;
  description: string;
  identifier: string;
  selected: boolean;
  disabled?: boolean;
  disabledReason?: string;
  note?: string;
  onSelect: () => void;
}

function ModeOption({
  icon,
  title,
  description,
  identifier,
  selected,
  disabled = false,
  disabledReason,
  note,
  onSelect,
}: ModeOptionProps) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      // Selection is carried by border + ring rather than a background tint so
      // it stays legible while hovered — hover only moves the background.
      className={`flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors ${
        selected
          ? "border-primary ring-1 ring-primary"
          : "border-border hover:bg-muted/50"
      } ${disabled ? "cursor-not-allowed opacity-60" : ""}`}
    >
      <span
        className={`mt-0.5 shrink-0 ${
          selected ? "text-primary" : "text-muted-foreground"
        }`}
      >
        {icon}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="text-body font-medium">{title}</span>
          <span className="font-mono text-micro text-muted-foreground">
            {identifier}
          </span>
        </span>
        <span className="mt-0.5 block text-caption text-muted-foreground">
          {description}
        </span>
        {disabled && disabledReason && (
          <span className="mt-1.5 flex items-start gap-1.5 text-caption text-warning">
            <TriangleAlert className="size-3 mt-0.5 shrink-0" />
            <span>{disabledReason}</span>
          </span>
        )}
        {!disabled && note && (
          <span className="mt-1.5 block text-caption text-muted-foreground">
            {note}
          </span>
        )}
      </span>
    </button>
  );
}
