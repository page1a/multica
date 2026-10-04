"use client";

import { useState } from "react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects";
import {
  useAcceptWorkspaceLink,
  useCreateWorkspaceLink,
  useRevokeWorkspaceLink,
  useUpdateWorkspaceLinkProjects,
  workspaceLinkAuditOptions,
  workspaceLinksOptions,
} from "@multica/core/workspace-links";
import type { WorkspaceLink, WorkspaceLinkAuditEntry } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { SettingsSection, SettingsTab } from "./settings-layout";
import { WorkspaceAvatar } from "../../workspace/workspace-avatar";
import { useT, useTimeAgo } from "../../i18n";

const EMPTY_LINKS: WorkspaceLink[] = [];

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

/**
 * Settings → Linked workspaces (DENE-1225). The server's decision table
 * answers what this caller may do (`can`); this page only disables what it
 * would refuse and says why.
 */
export function WorkspaceLinksTab() {
  const { t } = useT("workspace");
  const wsId = useWorkspaceId();
  const { data, isLoading } = useQuery(workspaceLinksOptions(wsId));
  const can = data?.can;
  const links = data?.links ?? EMPTY_LINKS;
  const outgoing = links.filter((link) => link.side === "source");
  const incoming = links.filter((link) => link.side === "viewer");
  const [revoking, setRevoking] = useState<WorkspaceLink | null>(null);
  const revoke = useRevokeWorkspaceLink(wsId);

  return (
    <SettingsTab title={t(($) => $.links.tab.title)} description={t(($) => $.links.tab.description)}>
      <SettingsSection title={t(($) => $.links.tab.outgoing_title)} description={t(($) => $.links.tab.outgoing_description)}>
        <CreateLinkForm wsId={wsId} enabled={!!can?.create} loaded={!!can} />
        {!isLoading && outgoing.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.outgoing_empty)}</p>
        ) : null}
        <div className="space-y-3">
          {outgoing.map((link) => (
            <OutgoingLinkRow key={link.id} wsId={wsId} link={link} canChange={!!can?.create} onRevoke={() => setRevoking(link)} />
          ))}
        </div>
      </SettingsSection>

      <SettingsSection title={t(($) => $.links.tab.incoming_title)} description={t(($) => $.links.tab.incoming_description)}>
        {!isLoading && incoming.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.incoming_empty)}</p>
        ) : null}
        <div className="space-y-3">
          {incoming.map((link) => (
            <IncomingLinkRow key={link.id} wsId={wsId} link={link} canAccept={!!can?.accept} onRevoke={() => setRevoking(link)} />
          ))}
        </div>
        {can && !can.accept ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.accept_reason)}</p>
        ) : null}
      </SettingsSection>

      {can?.audit ? <AuditSection wsId={wsId} /> : null}

      <AlertDialog open={revoking !== null} onOpenChange={(open) => !open && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t(($) => $.links.tab.revoke_title)}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.links.tab.revoke_description, {
                name: revoking ? (revoking.side === "source" ? revoking.target.name : revoking.source.name) : "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={revoke.isPending}>{t(($) => $.links.tab.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              disabled={revoke.isPending}
              onClick={() =>
                revoking &&
                revoke.mutate(revoking.id, {
                  onSuccess: () => setRevoking(null),
                  onError: (error) => toast.error(errorMessage(error, t(($) => $.links.tab.failed))),
                })
              }
            >
              {t(($) => $.links.tab.revoke)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}

function ProjectPicker({
  wsId,
  selected,
  onChange,
  disabled,
}: {
  wsId: string;
  selected: string[];
  onChange: (ids: string[]) => void;
  disabled: boolean;
}) {
  const { t } = useT("workspace");
  const { data: projects = [] } = useQuery(projectListOptions(wsId));
  const shareable = projects.filter((project) => (project.visibility ?? "private") !== "private");
  if (shareable.length === 0) {
    return <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.no_shareable_projects)}</p>;
  }
  return (
    <fieldset className="flex flex-wrap gap-2" disabled={disabled}>
      <legend className="sr-only">{t(($) => $.links.tab.projects_label)}</legend>
      {shareable.map((project) => {
        const checked = selected.includes(project.id);
        return (
          <label
            key={project.id}
            className="flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-body has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60"
          >
            <input
              type="checkbox"
              checked={checked}
              onChange={() => onChange(checked ? selected.filter((id) => id !== project.id) : [...selected, project.id])}
            />
            {project.icon ? <span aria-hidden>{project.icon}</span> : null}
            {project.title}
          </label>
        );
      })}
    </fieldset>
  );
}

function CreateLinkForm({ wsId, enabled, loaded }: { wsId: string; enabled: boolean; loaded: boolean }) {
  const { t } = useT("workspace");
  const [slug, setSlug] = useState("");
  const [projectIds, setProjectIds] = useState<string[]>([]);
  const create = useCreateWorkspaceLink(wsId);
  const ready = enabled && slug.trim() !== "" && projectIds.length > 0 && !create.isPending;
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <label className="block space-y-1.5">
          <span className="text-label">{t(($) => $.links.tab.target_label)}</span>
          <Input
            value={slug}
            disabled={!enabled}
            placeholder={t(($) => $.links.tab.target_placeholder)}
            onChange={(event) => setSlug(event.target.value)}
          />
        </label>
        <div className="space-y-1.5">
          <span className="text-label">{t(($) => $.links.tab.projects_label)}</span>
          <ProjectPicker wsId={wsId} selected={projectIds} onChange={setProjectIds} disabled={!enabled} />
        </div>
        <div className="flex items-center justify-between gap-3">
          <p className="text-caption text-muted-foreground">
            {/* Hold the refusal until the server has answered, so an owner
                never sees it flash while the list loads. */}
            {enabled ? t(($) => $.links.tab.create_hint) : loaded ? t(($) => $.links.tab.create_reason) : null}
          </p>
          <Button
            size="sm"
            disabled={!ready}
            onClick={() =>
              create.mutate(
                { target_slug: slug.trim(), project_ids: projectIds },
                {
                  onSuccess: () => {
                    setSlug("");
                    setProjectIds([]);
                    toast.success(t(($) => $.links.tab.created));
                  },
                  onError: (error) => toast.error(errorMessage(error, t(($) => $.links.tab.failed))),
                },
              )
            }
          >
            {t(($) => $.links.tab.create)}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function StatusBadge({ link }: { link: WorkspaceLink }) {
  const { t } = useT("workspace");
  return (
    <span
      className={
        link.status === "active"
          ? "rounded-sm bg-emerald-500/10 px-1.5 py-0.5 text-caption text-emerald-700 dark:text-emerald-400"
          : "rounded-sm bg-amber-500/10 px-1.5 py-0.5 text-caption text-amber-700 dark:text-amber-400"
      }
    >
      {link.status === "active" ? t(($) => $.links.tab.status_active) : t(($) => $.links.tab.status_pending)}
    </span>
  );
}

function LinkHeading({ name, avatarUrl, slug, link }: { name: string; avatarUrl: string | null; slug: string; link: WorkspaceLink }) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      <WorkspaceAvatar name={name} avatarUrl={avatarUrl} size="sm" className="size-5 rounded-xs" />
      <span className="truncate font-medium">{name}</span>
      <span className="truncate text-caption text-muted-foreground">{slug}</span>
      <StatusBadge link={link} />
    </div>
  );
}

function OutgoingLinkRow({
  wsId,
  link,
  canChange,
  onRevoke,
}: {
  wsId: string;
  link: WorkspaceLink;
  canChange: boolean;
  onRevoke: () => void;
}) {
  const { t } = useT("workspace");
  const [editing, setEditing] = useState<string[] | null>(null);
  const update = useUpdateWorkspaceLinkProjects(wsId);
  return (
    <Card data-testid="workspace-link-outgoing">
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <LinkHeading name={link.target.name} avatarUrl={link.target.avatar_url} slug={link.target.slug} link={link} />
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={!canChange}
              title={canChange ? undefined : t(($) => $.links.tab.create_reason)}
              onClick={() => setEditing(editing ? null : link.projects.map((p) => p.id))}
            >
              {t(($) => $.links.tab.change_projects)}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!canChange}
              title={canChange ? undefined : t(($) => $.links.tab.create_reason)}
              onClick={onRevoke}
            >
              {t(($) => $.links.tab.revoke)}
            </Button>
          </div>
        </div>
        {editing ? (
          <div className="space-y-2">
            <ProjectPicker wsId={wsId} selected={editing} onChange={setEditing} disabled={update.isPending} />
            <div className="flex justify-end gap-2">
              <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>
                {t(($) => $.links.tab.cancel)}
              </Button>
              <Button
                size="sm"
                disabled={editing.length === 0 || update.isPending}
                onClick={() =>
                  update.mutate(
                    { linkId: link.id, projectIds: editing },
                    {
                      onSuccess: () => setEditing(null),
                      onError: (error) => toast.error(errorMessage(error, t(($) => $.links.tab.failed))),
                    },
                  )
                }
              >
                {t(($) => $.links.tab.save)}
              </Button>
            </div>
          </div>
        ) : (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.links.tab.sharing, { projects: link.projects.map((p) => p.title).join("、") || "—" })}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function IncomingLinkRow({
  wsId,
  link,
  canAccept,
  onRevoke,
}: {
  wsId: string;
  link: WorkspaceLink;
  canAccept: boolean;
  onRevoke: () => void;
}) {
  const { t } = useT("workspace");
  const accept = useAcceptWorkspaceLink(wsId);
  const reason = canAccept ? undefined : t(($) => $.links.tab.accept_reason);
  return (
    <Card data-testid="workspace-link-incoming">
      <CardContent className="flex flex-wrap items-center justify-between gap-3 p-4">
        <div className="min-w-0 space-y-1">
          <LinkHeading name={link.source.name} avatarUrl={link.source.avatar_url} slug={link.source.slug} link={link} />
          <p className="text-caption text-muted-foreground">
            {link.status === "pending" ? t(($) => $.links.tab.pending_hint) : t(($) => $.links.tab.active_hint)}
          </p>
        </div>
        <div className="flex gap-2">
          {link.status === "pending" ? (
            <Button
              size="sm"
              disabled={!canAccept || accept.isPending}
              title={reason}
              onClick={() =>
                accept.mutate(link.id, {
                  onError: (error) => toast.error(errorMessage(error, t(($) => $.links.tab.failed))),
                })
              }
            >
              {t(($) => $.links.tab.accept)}
            </Button>
          ) : null}
          <Button size="sm" variant="outline" disabled={!canAccept} title={reason} onClick={onRevoke}>
            {link.status === "pending" ? t(($) => $.links.tab.decline) : t(($) => $.links.tab.revoke)}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function AuditSection({ wsId }: { wsId: string }) {
  const { t } = useT("workspace");
  const timeAgo = useTimeAgo();
  const { data: entries = [] } = useQuery(workspaceLinkAuditOptions(wsId, true));
  const action = (entry: WorkspaceLinkAuditEntry) => {
    switch (entry.action) {
      case "create":
        return t(($) => $.links.audit.create);
      case "update_projects":
        return t(($) => $.links.audit.update_projects);
      case "accept":
        return t(($) => $.links.audit.accept);
      default:
        return t(($) => $.links.audit.revoke);
    }
  };
  return (
    <SettingsSection title={t(($) => $.links.audit.title)} description={t(($) => $.links.audit.description)}>
      {entries.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.links.audit.empty)}</p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {entries.map((entry, index) => (
            <li key={`${entry.link_id}-${entry.created_at}-${index}`} className="flex items-center gap-3 px-4 py-2 text-body">
              <span className="min-w-0 flex-1 truncate">
                {entry.actor_name || t(($) => $.links.audit.someone)} · {action(entry)}
              </span>
              <span className="shrink-0 text-caption text-muted-foreground">
                {entry.by_side === "source" ? t(($) => $.links.audit.by_source) : t(($) => $.links.audit.by_viewer)}
              </span>
              <span className="w-20 shrink-0 text-right text-caption text-muted-foreground">{timeAgo(entry.created_at)}</span>
            </li>
          ))}
        </ul>
      )}
    </SettingsSection>
  );
}
