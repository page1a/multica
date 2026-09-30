"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Plus, X } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  projectReposOptions,
  useAttachProjectRepo,
  useRemoveProjectRepo,
} from "@multica/core/repo-reach";
import type { ProjectRepoItem } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { githubShortLabel } from "../../common/github-url";
import { useT } from "../../i18n";
import {
  RepoReachAction,
  RepoReachStatus,
} from "../../settings/components/repo-reach-view";
import { AddRepoDialog } from "./add-repo-dialog";

/**
 * The project's repositories, one per row. Each row shows the server's
 * `mode` / `next_action` for that repository and offers its next step in place.
 */
export function ProjectCodeSection({ projectId }: { projectId: string }) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const repos = useQuery(projectReposOptions(wsId, projectId));
  const attach = useAttachProjectRepo(wsId, projectId);
  const remove = useRemoveProjectRepo(wsId, projectId);

  const items: ProjectRepoItem[] = repos.data ?? [];

  const handleAttach = async (url: string) => {
    try {
      await attach.mutateAsync(url);
      toast.success(t(($) => $.code.toast_added));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.code.toast_add_failed));
      throw err;
    }
  };

  const handleRemove = async (item: ProjectRepoItem) => {
    try {
      await remove.mutateAsync(item.resource.id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.code.toast_remove_failed));
    }
  };

  return (
    <div>
      <button
        type="button"
        className={`mb-2 flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.code.title)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="space-y-1 pl-2">
          {repos.isError ? (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.code.load_failed)}
            </p>
          ) : null}
          {!repos.isError && !repos.isPending && items.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.code.empty)}</p>
          ) : null}
          {items.map((item) => (
            <div
              key={item.resource.id}
              className="group flex items-center justify-between gap-2 py-1.5"
            >
              <div className="min-w-0">
                <a
                  href={item.repo.repo_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  title={item.repo.repo_url}
                  className="block truncate text-caption font-medium hover:underline"
                >
                  {item.resource.label || githubShortLabel(item.repo.repo_url)}
                </a>
                <RepoReachStatus reach={item.repo} />
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <RepoReachAction reach={item.repo} />
                <button
                  type="button"
                  aria-label={t(($) => $.code.remove)}
                  title={t(($) => $.code.remove)}
                  disabled={remove.isPending}
                  onClick={() => void handleRemove(item)}
                  className="rounded-sm p-0.5 text-muted-foreground transition-opacity hover:bg-accent focus-visible:opacity-100 group-hover:opacity-100 [@media(hover:hover)]:opacity-0 [@media(pointer:coarse)]:p-2"
                >
                  <X className="size-3" />
                </button>
              </div>
            </div>
          ))}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-caption text-muted-foreground hover:text-foreground"
            onClick={() => setAddOpen(true)}
          >
            <Plus className="size-3" />
            {t(($) => $.code.add)}
          </Button>
        </div>
      )}
      <AddRepoDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        attachedUrls={items.map((item) => item.repo.repo_url)}
        onSelect={handleAttach}
      />
    </div>
  );
}
