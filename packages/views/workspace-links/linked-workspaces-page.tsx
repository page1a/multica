"use client";

import { useMemo, useState } from "react";
import { Bot, Eye, Link2 } from "lucide-react";
import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { linkedViewOptions, workspaceLinksOptions } from "@multica/core/workspace-links";
import type { LinkedViewIssue, LinkedViewProject, WorkspaceLink } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { WorkspaceAvatar } from "../workspace/workspace-avatar";
import { useT, useTimeAgo } from "../i18n";

/**
 * Read-only panel over the workspaces linked to this one (DENE-1225). It
 * renders exactly the server's whitelisted view: no edit controls, and
 * nothing links into the source workspace. A managed link only changes the
 * access line: its tasks are changed by agents, not from this page.
 */
export function LinkedWorkspacesPage() {
  const { t } = useT("workspace");
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(workspaceLinksOptions(wsId));
  const links = useMemo(
    () => (data?.links ?? []).filter((link) => link.side === "viewer" && link.status === "active"),
    [data],
  );
  const [picked, setPicked] = useState<string | null>(null);
  const current = links.find((link) => link.id === picked) ?? links[0];

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Link2}
        title={t(($) => $.links.panel.title)}
        description={t(($) => $.links.panel.description)}
      />
      <div className="flex-1 overflow-y-auto">
        <div className="mx-auto max-w-6xl space-y-5 p-6">
          {isLoading ? (
            <Skeleton className="h-40 rounded-lg" />
          ) : links.length === 0 ? (
            <PanelNotice title={t(($) => $.links.panel.empty_title)} body={t(($) => $.links.panel.empty_body)} />
          ) : (
            <>
              {links.length > 1 ? (
                <div className="flex flex-wrap gap-2" role="tablist">
                  {links.map((link) => (
                    <SourceChip key={link.id} link={link} active={link.id === current?.id} onPick={() => setPicked(link.id)} />
                  ))}
                </div>
              ) : null}
              {current ? <LinkedView key={current.id} wsId={wsId} link={current} /> : null}
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function SourceChip({ link, active, onPick }: { link: WorkspaceLink; active: boolean; onPick: () => void }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onPick}
      className={cn(
        "flex items-center gap-2 rounded-md border px-3 py-1.5 text-body",
        active ? "border-primary bg-accent" : "text-muted-foreground hover:bg-accent/60",
      )}
    >
      <WorkspaceAvatar name={link.source.name} avatarUrl={link.source.avatar_url} size="sm" className="size-4 rounded-xs" />
      {link.source.name}
    </button>
  );
}

function LinkedView({ wsId, link }: { wsId: string; link: WorkspaceLink }) {
  const { t } = useT("workspace");
  const timeAgo = useTimeAgo();
  const [projectId, setProjectId] = useState<string | null>(null);
  const query = useInfiniteQuery(linkedViewOptions(wsId, link.id, projectId));
  const first = query.data?.pages[0];
  const issues = useMemo(() => query.data?.pages.flatMap((page) => page.issues) ?? [], [query.data]);
  const statusName = useMemo(() => {
    const names = new Map((first?.statuses ?? []).map((status) => [status.key, status.name]));
    return (key: string) => names.get(key) ?? key;
  }, [first]);

  if (query.isLoading) return <Skeleton className="h-64 rounded-lg" />;
  if (query.isError || !first) {
    // Every refusal is the same "link not found": revoked, unticked or no
    // longer allowed. Say so plainly instead of offering a retry.
    return <PanelNotice title={t(($) => $.links.panel.gone_title)} body={t(($) => $.links.panel.gone_body)} />;
  }

  return (
    <div className="space-y-5">
      {/* On a managed link the page itself stays read-only; agents do the
          work through `--linked` (DENE-1663), so say that they can. */}
      <div className="flex items-center gap-2 text-caption text-muted-foreground" data-testid="linked-view-access">
        {link.managed ? <Bot className="size-3.5 shrink-0" /> : <Eye className="size-3.5 shrink-0" />}
        {link.managed
          ? t(($) => $.links.panel.managed_from, { name: first.source.name })
          : t(($) => $.links.panel.read_only_from, { name: first.source.name })}
      </div>

      <section className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3" aria-label={t(($) => $.links.panel.projects)}>
        {first.projects.map((project) => (
          <ProjectCard
            key={project.id}
            project={project}
            statusLabel={t(($) => $.links.panel.project_progress, { done: project.done, total: project.total })}
            selected={projectId === project.id}
            onSelect={() => setProjectId(projectId === project.id ? null : project.id)}
          />
        ))}
      </section>

      <section className="rounded-lg border bg-card">
        <header className="flex items-center justify-between border-b px-4 py-2.5 text-label">
          <span>
            {projectId
              ? first.projects.find((p) => p.id === projectId)?.title
              : t(($) => $.links.panel.all_tasks)}
          </span>
          {projectId ? (
            <Button variant="ghost" size="sm" onClick={() => setProjectId(null)}>
              {t(($) => $.links.panel.show_all)}
            </Button>
          ) : null}
        </header>
        {issues.length === 0 ? (
          <p className="px-4 py-8 text-center text-caption text-muted-foreground">{t(($) => $.links.panel.no_tasks)}</p>
        ) : (
          <ul className="divide-y">
            {issues.map((issue) => (
              <IssueRow key={issue.identifier} issue={issue} status={statusName(issue.status)} updated={timeAgo(issue.updated_at)} />
            ))}
          </ul>
        )}
        {query.hasNextPage ? (
          <div className="border-t p-2 text-center">
            <Button variant="ghost" size="sm" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
              {t(($) => $.links.panel.load_more)}
            </Button>
          </div>
        ) : null}
      </section>
    </div>
  );
}

function ProjectCard({
  project,
  statusLabel,
  selected,
  onSelect,
}: {
  project: LinkedViewProject;
  statusLabel: string;
  selected: boolean;
  onSelect: () => void;
}) {
  const percent = project.total > 0 ? Math.round((project.done / project.total) * 100) : 0;
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn(
        "rounded-lg border bg-card p-4 text-left transition-colors",
        selected ? "border-primary" : "hover:bg-accent/40",
      )}
    >
      <div className="flex items-center gap-2 font-medium">
        {project.icon ? <span aria-hidden>{project.icon}</span> : null}
        <span className="truncate">{project.title}</span>
      </div>
      <div className="mt-3 h-1.5 overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-primary" style={{ width: `${percent}%` }} />
      </div>
      <p className="mt-2 text-caption text-muted-foreground">{statusLabel}</p>
    </button>
  );
}

function IssueRow({ issue, status, updated }: { issue: LinkedViewIssue; status: string; updated: string }) {
  return (
    <li className="flex items-center gap-3 px-4 py-2.5 text-body">
      <span className="w-20 shrink-0 font-mono text-caption text-muted-foreground">{issue.identifier}</span>
      <span className="min-w-0 flex-1 truncate">{issue.title}</span>
      <span className="hidden shrink-0 text-caption text-muted-foreground sm:inline">{status}</span>
      <span className="hidden w-28 shrink-0 truncate text-caption text-muted-foreground md:inline">
        {issue.assignee_name ?? "—"}
      </span>
      <span className="w-20 shrink-0 text-right text-caption text-muted-foreground">{updated}</span>
    </li>
  );
}

function PanelNotice({ title, body }: { title: string; body: string }) {
  return (
    <div className="flex flex-col items-center rounded-lg border border-dashed py-12 text-center">
      <Link2 className="h-6 w-6 text-faint-foreground" />
      <p className="mt-3 text-body font-medium">{title}</p>
      <p className="mt-1 max-w-md text-caption text-muted-foreground">{body}</p>
    </div>
  );
}
