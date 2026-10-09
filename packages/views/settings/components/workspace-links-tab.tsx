"use client";

import { useRef, useState } from "react";
import { toast } from "sonner";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import {
  useAcceptWorkspaceLink,
  useCreateWorkspaceLink,
  useRevokeWorkspaceLink,
  useUpdateWorkspaceLinkProjects,
  workspaceLinkAuditOptions,
  workspaceLinkKeys,
  workspaceLinkLookupOptions,
  workspaceLinksOptions,
} from "@multica/core/workspace-links";
import type {
  WorkspaceLink,
  WorkspaceLinkAuditEntry,
  WorkspaceLinkDirection,
  WorkspaceLinkLookup,
  WorkspaceLinkProject,
  WorkspaceLinkWorkspace,
} from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent } from "@multica/ui/components/ui/popover";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { SettingsSection, SettingsTab } from "./settings-layout";
import { WorkspaceAvatar } from "../../workspace/workspace-avatar";
import { useT, useTimeAgo } from "../../i18n";
import { useDebouncedValue } from "../../common/use-debounced-value";

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
      <SettingsSection anchor="outgoing" title={t(($) => $.links.tab.outgoing_title)} description={t(($) => $.links.tab.outgoing_description)}>
        <CreateLinkForm
          wsId={wsId}
          direction="offer"
          enabled={!!can?.create}
          loaded={!!can}
          linkedSlugs={outgoing.map((link) => link.target.slug)}
        />
        {!isLoading && outgoing.length === 0 ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.outgoing_empty)}</p>
        ) : null}
        <div className="space-y-3">
          {outgoing.map((link) => (
            <OutgoingLinkRow key={link.id} wsId={wsId} link={link} canChange={!!can?.create} onRevoke={() => setRevoking(link)} />
          ))}
        </div>
      </SettingsSection>

      <SettingsSection anchor="incoming" title={t(($) => $.links.tab.incoming_title)} description={t(($) => $.links.tab.incoming_description)}>
        <CreateLinkForm
          wsId={wsId}
          direction="pull"
          enabled={!!can?.pull}
          loaded={!!can}
          linkedSlugs={incoming.map((link) => link.source.slug)}
        />
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
  return (
    <ProjectChecklist
      projects={shareable}
      selected={selected}
      onChange={onChange}
      disabled={disabled}
      legend={t(($) => $.links.tab.projects_label)}
      empty={t(($) => $.links.tab.no_shareable_projects)}
    />
  );
}

function ProjectChecklist({
  projects,
  selected,
  onChange,
  disabled,
  legend,
  empty,
}: {
  projects: Pick<WorkspaceLinkProject, "id" | "title" | "icon">[];
  selected: string[];
  onChange: (ids: string[]) => void;
  disabled: boolean;
  legend: string;
  empty: string;
}) {
  if (projects.length === 0) {
    return <p className="text-caption text-muted-foreground">{empty}</p>;
  }
  return (
    <fieldset className="flex flex-wrap gap-2" disabled={disabled}>
      <legend className="sr-only">{legend}</legend>
      {projects.map((project) => {
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

/**
 * One form, two directions. "offer" shares this workspace's projects and
 * waits for the other side to accept. "pull" reads the other workspace's
 * projects: the server lists them, and links them in at once, only for
 * someone who owns that workspace too (workspacelink.Pull).
 */
function CreateLinkForm({
  wsId,
  direction,
  enabled,
  loaded,
  linkedSlugs,
}: {
  wsId: string;
  direction: WorkspaceLinkDirection;
  enabled: boolean;
  loaded: boolean;
  linkedSlugs: string[];
}) {
  const { t } = useT("workspace");
  const pull = direction === "pull";
  const [address, setAddress] = useState("");
  const [projectIds, setProjectIds] = useState<string[]>([]);
  const create = useCreateWorkspaceLink(wsId);
  const target = useLinkTarget(wsId, address, enabled, pull);
  const theirs = pull ? (target.pull?.allowed ? target.pull.projects : null) : null;
  // A pick made for one workspace never rides along to another.
  const picked = pull ? projectIds.filter((id) => theirs?.some((p) => p.id === id)) : projectIds;
  const reason = !target.workspace
    ? pull
      ? t(($) => $.links.tab.pull_need_target)
      : t(($) => $.links.tab.need_target)
    : pull && !theirs
      ? t(($) => $.links.tab.pull_need_owner)
      : picked.length === 0
        ? t(($) => $.links.tab.need_projects)
        : null;
  const ready = enabled && reason === null && !create.isPending;
  const hint = pull ? t(($) => $.links.tab.pull_hint) : t(($) => $.links.tab.create_hint);
  const refusal = pull ? t(($) => $.links.tab.pull_reason) : t(($) => $.links.tab.create_reason);
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <TargetField
          wsId={wsId}
          direction={direction}
          value={address}
          onChange={setAddress}
          disabled={!enabled}
          excludeSlugs={linkedSlugs}
          target={target}
        />
        {pull ? (
          theirs ? (
            <div className="space-y-1.5">
              <span className="text-label">{t(($) => $.links.tab.pull_projects_label)}</span>
              <ProjectChecklist
                projects={theirs}
                selected={picked}
                onChange={setProjectIds}
                disabled={!enabled}
                legend={t(($) => $.links.tab.pull_projects_label)}
                empty={t(($) => $.links.tab.pull_no_projects)}
              />
            </div>
          ) : null
        ) : (
          <div className="space-y-1.5">
            <span className="text-label">{t(($) => $.links.tab.projects_label)}</span>
            <ProjectPicker wsId={wsId} selected={projectIds} onChange={setProjectIds} disabled={!enabled} />
          </div>
        )}
        <div className="flex items-center justify-between gap-3">
          <p className="text-caption text-muted-foreground">
            {/* Hold the refusal until the server has answered, so an owner
                never sees it flash while the list loads. */}
            {enabled ? (reason ?? hint) : loaded ? refusal : null}
          </p>
          <Button
            size="sm"
            className="shrink-0"
            disabled={!ready}
            onClick={() =>
              target.workspace &&
              create.mutate(
                pull
                  ? { target_slug: target.workspace.slug, project_ids: picked, direction: "pull" }
                  : { target_slug: target.workspace.slug, project_ids: picked },
                {
                  onSuccess: () => {
                    setAddress("");
                    setProjectIds([]);
                    toast.success(pull ? t(($) => $.links.tab.pull_created) : t(($) => $.links.tab.created));
                  },
                  onError: (error) => toast.error(errorMessage(error, t(($) => $.links.tab.failed))),
                },
              )
            }
          >
            {pull ? t(($) => $.links.tab.pull_create) : t(($) => $.links.tab.create)}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

type LinkTarget = {
  /** The workspace the address names, once the server has confirmed it. */
  workspace: WorkspaceLinkWorkspace | null;
  /** The server's pull answer for that workspace (pull form only). */
  pull: WorkspaceLinkLookup["pull"] | null;
  checking: boolean;
  /** Why the address names nothing usable, in words. */
  problem: string | null;
};

/**
 * Asks the server which workspace the typed address names. The server reads
 * links and slugs (workspacelink.TargetSlug); this only debounces and words
 * the answer.
 */
function useLinkTarget(wsId: string, address: string, enabled: boolean, pull: boolean): LinkTarget {
  const { t } = useT("workspace");
  const typed = address.trim();
  const settled = useDebouncedValue(typed, 300);
  const lookup = useQuery(workspaceLinkLookupOptions(wsId, settled, enabled, pull));
  if (typed === "") return { workspace: null, pull: null, checking: false, problem: null };
  if (settled !== typed || lookup.isFetching) return { workspace: null, pull: null, checking: true, problem: null };
  if (lookup.error) {
    const status = lookup.error instanceof ApiError ? lookup.error.status : 0;
    return {
      workspace: null,
      pull: null,
      checking: false,
      problem:
        status === 404
          ? t(($) => $.links.tab.target_not_found)
          : status === 400
            ? t(($) => $.links.tab.target_self)
            : errorMessage(lookup.error, t(($) => $.links.tab.failed)),
    };
  }
  return { workspace: lookup.data?.workspace ?? null, pull: lookup.data?.pull ?? null, checking: false, problem: null };
}

function TargetField({
  wsId,
  direction,
  value,
  onChange,
  disabled,
  excludeSlugs,
  target,
}: {
  wsId: string;
  direction: WorkspaceLinkDirection;
  value: string;
  onChange: (value: string) => void;
  disabled: boolean;
  excludeSlugs: string[];
  target: LinkTarget;
}) {
  const { t } = useT("workspace");
  const pull = direction === "pull";
  const fieldId = pull ? "workspace-link-source" : "workspace-link-target";
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const { data: workspaces = [] } = useQuery(workspaceListOptions());
  const needle = value.trim().toLowerCase();
  // Only workspaces the caller already belongs to: there is no search over
  // the deployment, which would hand out other people's workspace names.
  const mine = workspaces.filter(
    (ws) =>
      ws.id !== wsId &&
      !excludeSlugs.includes(ws.slug) &&
      (needle === "" || ws.slug.includes(needle) || ws.name.toLowerCase().includes(needle) || needle.includes(`/${ws.slug}`)),
  );
  const pick = (ws: { name: string; slug: string; avatar_url: string | null }) => {
    // The server would answer the same for a slug of the caller's own
    // workspace; seed it so the confirmation shows without a round trip.
    // The pull form needs the server's project list, so it always asks.
    if (!pull) {
      qc.setQueryData(workspaceLinkKeys.lookup(wsId, ws.slug), {
        workspace: { name: ws.name, slug: ws.slug, avatar_url: ws.avatar_url },
      });
    }
    onChange(ws.slug);
    setOpen(false);
  };
  return (
    <div className="space-y-1.5">
      <label htmlFor={fieldId} className="text-label">
        {pull ? t(($) => $.links.tab.pull_target_label) : t(($) => $.links.tab.target_label)}
      </label>
      <Input
        ref={inputRef}
        id={fieldId}
        role="combobox"
        aria-expanded={open && mine.length > 0}
        aria-controls={`${fieldId}-options`}
        autoComplete="off"
        value={value}
        disabled={disabled}
        placeholder={t(($) => $.links.tab.target_placeholder)}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === "Escape") setOpen(false);
        }}
        onChange={(event) => {
          onChange(event.target.value);
          setOpen(true);
        }}
      />
      {/* The list floats above the page in a portal so the settings card
          cannot clip it; focus stays in the input the whole time. */}
      <Popover
        open={open && mine.length > 0}
        onOpenChange={(next, details) => {
          if (!next && details.event?.target !== inputRef.current) setOpen(false);
        }}
      >
        <PopoverContent
          anchor={inputRef}
          align="start"
          initialFocus={false}
          finalFocus={false}
          className="w-(--anchor-width) max-h-[min(15rem,var(--available-height))] gap-0 overflow-y-auto p-1"
        >
          <div id={`${fieldId}-options`} role="listbox" aria-label={t(($) => $.links.tab.target_mine)}>
            <p className="px-2 py-1 text-caption text-muted-foreground">{t(($) => $.links.tab.target_mine)}</p>
            {mine.map((ws) => (
              <button
                key={ws.id}
                type="button"
                role="option"
                aria-selected={value === ws.slug}
                className="flex min-h-11 w-full items-center gap-2 rounded-sm px-2 text-left text-body hover:bg-accent"
                // Keep focus in the input so blur does not close the list
                // before the pick lands.
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => pick(ws)}
              >
                <WorkspaceAvatar name={ws.name} avatarUrl={ws.avatar_url} size="sm" className="size-5 shrink-0 rounded-xs" />
                <span className="truncate">{ws.name}</span>
                <span className="ml-auto shrink-0 truncate text-caption text-muted-foreground">{ws.slug}</span>
              </button>
            ))}
          </div>
        </PopoverContent>
      </Popover>
      {target.workspace ? (
        <div className="flex min-w-0 items-center gap-2 text-body" data-testid={fieldId}>
          <WorkspaceAvatar name={target.workspace.name} avatarUrl={target.workspace.avatar_url} size="sm" className="size-5 shrink-0 rounded-xs" />
          <span className="truncate font-medium">{target.workspace.name}</span>
          <span className="truncate text-caption text-muted-foreground">{target.workspace.slug}</span>
        </div>
      ) : target.checking ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.links.tab.target_checking)}</p>
      ) : target.problem ? (
        <p className="text-caption text-destructive">{target.problem}</p>
      ) : null}
    </div>
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
