"use client";

import { useRef, useState } from "react";
import { ArrowLeftRight, FolderKanban, Image as ImageIcon, Plus, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { toggleProjectId } from "@multica/core/chat/project-context";
import type {
  ChatLinkedProject,
  ChatLinkedProjectRef,
  LinkedProjectOption,
  Project,
} from "@multica/core/types";
import { ProjectIcon } from "../../projects/components/project-icon";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface ChatAddMenuProps {
  /** Called with each selected file — the caller routes it through the
   *  editor's upload extension, same path as paste / drag-drop. */
  onSelectFile?: (file: File) => void;
  projects?: Project[];
  /** The attached projects, in selection order. */
  projectIds?: string[];
  /** The project the main UI is currently on. It heads the submenu's first
   *  screen, which is otherwise limited to what is already attached — the
   *  full workspace list is one click away (DENE-603 §3). */
  currentProjectId?: string | null;
  /** Called with the COMPLETE next set — this menu toggles one entry at a
   *  time, but the set is what the session stores, so the caller never has to
   *  reconstruct it from an add/remove event. */
  onProjectsChange?: (projectIds: string[]) => void;
  /** Soft warning: the active agent's daemon is too old to receive the
   *  project description. Selection stays enabled; the submenu only appends
   *  an explanatory hint so the user knows before choosing. */
  projectContextUnsupported?: boolean;
  /** Read-only projects attached from linked workspaces (DENE-1643), stale
   *  ones included so they can be unticked. */
  linkedProjects?: ChatLinkedProject[];
  linkedProjectOptions?: LinkedProjectOption[];
  /** Called with the COMPLETE next linked set. */
  onLinkedProjectsChange?: (refs: ChatLinkedProjectRef[]) => void;
  disabled?: boolean;
}

interface LinkedRow {
  key: string;
  ref: ChatLinkedProjectRef;
  title: string;
  icon: string | null;
  sourceName: string;
  available: boolean;
}

function linkedKey(ref: ChatLinkedProjectRef): string {
  return `${ref.link_id}/${ref.project_id}`;
}

/** Every shared project on offer, then attached ones the link no longer
 *  offers — those stay listed (ticked, marked stale) until removed. */
function linkedRows(options: LinkedProjectOption[], attached: ChatLinkedProject[]): LinkedRow[] {
  const rows: LinkedRow[] = options.map((option) => ({
    key: linkedKey({ link_id: option.link_id, project_id: option.id }),
    ref: { link_id: option.link_id, project_id: option.id },
    title: option.title,
    icon: option.icon,
    sourceName: option.source.name,
    available: true,
  }));
  const offered = new Set(rows.map((row) => row.key));
  for (const item of attached) {
    const key = linkedKey(item);
    if (offered.has(key)) continue;
    rows.push({
      key,
      ref: { link_id: item.link_id, project_id: item.project_id },
      title: item.title,
      icon: item.icon,
      sourceName: item.source_name,
      available: false,
    });
  }
  return rows;
}

/**
 * The "+" affordance at the bottom-left of the chat composer. Replaces the
 * standalone paperclip button: file upload now lives here as a submenu entry,
 * leaving room for future add-actions (agents, skills, tools) under one entry
 * point without crowding the input bar.
 */
export function ChatAddMenu({
  onSelectFile,
  projects = [],
  projectIds = [],
  currentProjectId,
  onProjectsChange,
  projectContextUnsupported,
  linkedProjects = [],
  linkedProjectOptions = [],
  onLinkedProjectsChange,
  disabled,
}: ChatAddMenuProps) {
  const { t } = useT("chat");
  const inputRef = useRef<HTMLInputElement>(null);
  // Cross-project picking is the rare case: the submenu opens focused on the
  // project at hand and only expands to the whole workspace on request. Reset
  // on close so the next open starts focused again.
  const [showAllProjects, setShowAllProjects] = useState(false);

  // The focused screen. Falls back to the full list when it would otherwise be
  // empty — an empty first screen teaches the user nothing and costs a click.
  const focusedProjects = projects.filter(
    (project) => project.id === currentProjectId || projectIds.includes(project.id),
  );
  const visibleProjects =
    showAllProjects || focusedProjects.length === 0 ? projects : focusedProjects;
  const canExpand = !showAllProjects && visibleProjects.length < projects.length;

  const linked = onLinkedProjectsChange ? linkedRows(linkedProjectOptions, linkedProjects) : [];
  const attachedLinked = new Set(linkedProjects.map(linkedKey));
  const linkedRefs = linkedProjects.map((item) => ({ link_id: item.link_id, project_id: item.project_id }));
  const toggleLinked = (row: LinkedRow) =>
    onLinkedProjectsChange?.(
      attachedLinked.has(row.key)
        ? linkedRefs.filter((ref) => linkedKey(ref) !== row.key)
        : [...linkedRefs, row.ref],
    );
  const attachedCount = projectIds.length + linkedProjects.length;

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    e.target.value = "";
    for (const file of files) onSelectFile?.(file);
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              disabled={disabled}
              aria-label={t(($) => $.input.add_tooltip)}
              title={t(($) => $.input.add_tooltip)}
              className="rounded-full text-muted-foreground"
            >
              <Plus />
            </Button>
          }
        />
        <DropdownMenuContent align="start" side="top" sideOffset={6}>
          {onSelectFile && (
            <DropdownMenuItem onClick={() => inputRef.current?.click()}>
              <ImageIcon />
              {t(($) => $.input.upload_file)}
            </DropdownMenuItem>
          )}
          {onProjectsChange && (
            <DropdownMenuSub onOpenChange={(open) => !open && setShowAllProjects(false)}>
              <DropdownMenuSubTrigger>
                <FolderKanban />
                <span className="flex-1">{t(($) => $.input.project_context)}</span>
                {attachedCount > 0 && (
                  <span className="text-caption text-muted-foreground tabular-nums">
                    {attachedCount}
                  </span>
                )}
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="max-h-72 min-w-52 max-w-[min(24rem,calc(100vw-1rem))] overflow-y-auto">
                {visibleProjects.map((project) => (
                  // Checkbox items keep the menu open on click (Base UI), which
                  // is the point: attaching two or three projects is one trip.
                  <DropdownMenuCheckboxItem
                    key={project.id}
                    checked={projectIds.includes(project.id)}
                    onCheckedChange={() =>
                      onProjectsChange(toggleProjectId(projectIds, project.id))
                    }
                  >
                    <ProjectIcon project={project} size="md" />
                    <span className="min-w-0 flex-1 truncate">{project.title}</span>
                  </DropdownMenuCheckboxItem>
                ))}
                {canExpand && (
                  // Not a checkbox item: this opens the rest of the workspace
                  // rather than attaching anything, so it must not close the
                  // menu or look like a selection.
                  <DropdownMenuItem
                    closeOnClick={false}
                    onClick={() => setShowAllProjects(true)}
                  >
                    <ArrowLeftRight />
                    {t(($) => $.input.switch_project)}
                  </DropdownMenuItem>
                )}
                {projects.length === 0 && linked.length === 0 && (
                  <div className="px-2 py-1.5 text-caption text-muted-foreground">
                    {t(($) => $.input.no_projects)}
                  </div>
                )}
                {linked.length > 0 && (
                  <DropdownMenuGroup>
                    {projects.length > 0 && <DropdownMenuSeparator />}
                    <DropdownMenuLabel>{t(($) => $.input.linked_projects)}</DropdownMenuLabel>
                    {linked.map((row) => (
                      <DropdownMenuCheckboxItem
                        key={row.key}
                        checked={attachedLinked.has(row.key)}
                        onCheckedChange={() => toggleLinked(row)}
                        title={row.available ? undefined : t(($) => $.input.linked_project_stale_hint)}
                      >
                        <ProjectIcon project={row} size="md" />
                        <span className={cn("min-w-0 flex-1 truncate", !row.available && "text-muted-foreground line-through")}>
                          {row.title}
                        </span>
                        <span className="max-w-24 shrink-0 truncate text-caption text-muted-foreground">
                          {row.available ? row.sourceName : t(($) => $.input.linked_project_stale)}
                        </span>
                      </DropdownMenuCheckboxItem>
                    ))}
                  </DropdownMenuGroup>
                )}
                {attachedCount > 0 && <DropdownMenuSeparator />}
                {attachedCount > 0 && (
                  <DropdownMenuItem
                    onClick={() => {
                      if (projectIds.length > 0) onProjectsChange([]);
                      if (linkedProjects.length > 0) onLinkedProjectsChange?.([]);
                    }}
                  >
                    <X />
                    {t(($) => $.input.remove_all_project_context)}
                  </DropdownMenuItem>
                )}
                {projectContextUnsupported && (
                  <>
                    <DropdownMenuSeparator />
                    <div className="max-w-56 px-2 py-1.5 text-caption text-muted-foreground">
                      {t(($) => $.input.project_context_unsupported)}
                    </div>
                  </>
                )}
              </DropdownMenuSubContent>
            </DropdownMenuSub>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {onSelectFile && (
        <input
          ref={inputRef}
          type="file"
          multiple
          className="hidden"
          onChange={handleChange}
        />
      )}
    </>
  );
}
