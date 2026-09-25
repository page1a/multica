"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Globe2, Loader2, LockKeyhole, Users } from "lucide-react";
import {
  projectMembersOptions,
  useAddProjectMember,
  useRemoveProjectMember,
} from "@multica/core/projects";
import {
  resourceSharesOptions,
  useAddResourceShare,
  useProjectVisibilityPreview,
  useRemoveResourceShare,
  useSetIssueVisibility,
  useSetProjectVisibility,
  useSetRepoVisibility,
  type ShareableResource,
  type VisibilityScope,
} from "@multica/core/visibility";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import type { MemberWithUser } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { ActorAvatar } from "./actor-avatar";
import { matchesPinyin } from "../editor/extensions/pinyin-match";
import { useT } from "../i18n";

export type ShareScopeTarget =
  | {
      kind: "issue";
      resourceId: string;
      currentScope?: VisibilityScope;
      audienceSize?: number;
      projectId?: string | null;
      resourceLabel?: string;
    }
  | {
      kind: "repo";
      resourceId: string;
      currentScope?: VisibilityScope;
      audienceSize?: number;
      projectId?: string | null;
      resourceLabel?: string;
    }
  | {
      kind: "project";
      resourceId: string;
      currentScope?: VisibilityScope;
      audienceSize?: number;
      resourceLabel?: string;
    };

export interface ShareScopeDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  target: ShareScopeTarget;
  /** Scope pre-selected on open; defaults to the saved scope. */
  initialScope?: VisibilityScope;
  onSaved?: (result: { visibility: VisibilityScope; audience_size?: number }) => void;
}

const SCOPE_META = {
  private: { icon: LockKeyhole, labelKey: "private_title", descriptionKey: "private_description" },
  project: { icon: Users, labelKey: "project_title", descriptionKey: "project_description" },
  workspace: { icon: Globe2, labelKey: "workspace_title", descriptionKey: "workspace_description" },
} as const;

const SEARCH_THRESHOLD = 6;

/**
 * Shared resource sharing dialog for issue, repository and project surfaces.
 *
 * The "project" scope is presented as "specific people": the owner picks who
 * can view right here. For a project the picked people are its member list;
 * for an issue or repository they are the resource's own direct shares.
 */
export function ShareScopeDialog({
  open,
  onOpenChange,
  target,
  initialScope,
  onSaved,
}: ShareScopeDialogProps) {
  const { t } = useT("common");
  const wsId = useWorkspaceId();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);
  const [scope, setScope] = useState<VisibilityScope>(initialScope ?? target.currentScope ?? "private");
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [pickerError, setPickerError] = useState<string | null>(null);
  const [pendingIds, setPendingIds] = useState<ReadonlySet<string>>(() => new Set());

  const isProjectTarget = target.kind === "project";
  const projectId = target.kind === "project" ? target.resourceId : target.projectId ?? null;
  const shareResource: ShareableResource =
    target.kind === "repo"
      ? { kind: "repo", url: target.resourceId }
      : { kind: "issue", id: target.kind === "issue" ? target.resourceId : "" };

  const { data: projectMembers = [], isLoading: projectMembersLoading } = useQuery({
    ...projectMembersOptions(wsId, projectId ?? ""),
    enabled: open && !!projectId,
  });
  const { data: resourceShares = [], isLoading: resourceSharesLoading } = useQuery({
    ...resourceSharesOptions(wsId, shareResource),
    enabled: open && !isProjectTarget,
  });
  const { data: workspaceMembers = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: open,
  });
  const preview = useProjectVisibilityPreview(
    target.kind === "project" ? target.resourceId : "",
    open && target.kind === "project",
  );
  const issueMutation = useSetIssueVisibility(wsId);
  const repoMutation = useSetRepoVisibility(wsId);
  const projectMutation = useSetProjectVisibility(wsId);
  const addProjectMember = useAddProjectMember(wsId, projectId ?? "");
  const removeProjectMember = useRemoveProjectMember(wsId, projectId ?? "");
  const addShare = useAddResourceShare(wsId, shareResource);
  const removeShare = useRemoveResourceShare(wsId, shareResource);
  const saving = issueMutation.isPending || repoMutation.isPending || projectMutation.isPending;

  useEffect(() => {
    if (open) {
      setScope(initialScope ?? target.currentScope ?? "private");
      setError(null);
      setConfirming(false);
      setPickerError(null);
    }
  }, [open, initialScope, target.currentScope, target.resourceId]);

  // People the owner picked: project members for a project, direct shares otherwise.
  const pickedIds = useMemo(() => {
    const source = isProjectTarget ? projectMembers : resourceShares;
    return new Set(source.map((entry) => entry.member_id).filter((id) => id !== currentUserId));
  }, [currentUserId, isProjectTarget, projectMembers, resourceShares]);
  const pickedLoading = isProjectTarget ? projectMembersLoading : resourceSharesLoading;

  // An issue/repo inside a project is also visible to that project's members.
  const inheritedProjectIds = useMemo(() => {
    if (isProjectTarget || !projectId) return new Set<string>();
    return new Set(
      projectMembers.map((member) => member.member_id).filter((id) => id !== currentUserId),
    );
  }, [currentUserId, isProjectTarget, projectId, projectMembers]);

  const candidates = useMemo(
    () => workspaceMembers.filter((member) => member.user_id !== currentUserId),
    [currentUserId, workspaceMembers],
  );

  const togglePerson = async (memberId: string, next: boolean) => {
    if (pendingIds.has(memberId)) return;
    setPickerError(null);
    setPendingIds((current) => new Set(current).add(memberId));
    try {
      if (isProjectTarget) {
        if (next) await addProjectMember.mutateAsync(memberId);
        else await removeProjectMember.mutateAsync(memberId);
      } else if (next) {
        await addShare.mutateAsync(memberId);
      } else {
        await removeShare.mutateAsync(memberId);
      }
    } catch (cause) {
      setPickerError(
        cause instanceof Error && cause.message ? cause.message : t(($) => $.share_scope.toggle_failed),
      );
    } finally {
      setPendingIds((current) => {
        const rest = new Set(current);
        rest.delete(memberId);
        return rest;
      });
    }
  };

  const save = async () => {
    if (saving) return;
    setError(null);
    try {
      let result: { visibility: VisibilityScope; audience_size?: number };
      if (target.kind === "issue") {
        result = await issueMutation.mutateAsync({ issueId: target.resourceId, visibility: scope });
      } else if (target.kind === "repo") {
        result = await repoMutation.mutateAsync({ url: target.resourceId, visibility: scope });
      } else {
        result = await projectMutation.mutateAsync({ projectId: target.resourceId, visibility: scope });
      }
      onSaved?.(result);
      onOpenChange(false);
    } catch (cause) {
      setError(cause instanceof Error && cause.message ? cause.message : t(($) => $.share_scope.save_failed));
      setConfirming(false);
    }
  };

  const scopeLabel = (value: VisibilityScope) => t(($) => $.share_scope[SCOPE_META[value].labelKey]);
  const currentLabel = scopeLabel(scope);
  const previewReady = target.kind !== "project" || !!preview.data;
  const specificAudience = new Set([...pickedIds, ...inheritedProjectIds]).size + 1;
  const audienceSizeByScope: Record<VisibilityScope, number | undefined> = {
    private: 1,
    project: pickedLoading ? undefined : specificAudience,
    workspace: workspaceMembers.filter((member) => member.role !== "guest").length,
  };
  // The saved audience from the server is authoritative for fixed scopes; the
  // specific-people count follows live picks, so it is always computed here.
  if (target.audienceSize !== undefined && scope !== "project") {
    audienceSizeByScope[scope] = target.audienceSize;
  }
  const showConfirm = confirming && target.kind === "project";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg gap-0 overflow-hidden p-0">
        <DialogHeader className="border-b px-5 py-4 text-left">
          <DialogTitle>{t(($) => $.share_scope.title)}</DialogTitle>
          <DialogDescription>
            {target.resourceLabel
              ? t(($) => $.share_scope.description_named, { name: target.resourceLabel })
              : t(($) => $.share_scope.description)}
          </DialogDescription>
        </DialogHeader>

        {error && (
          <div role="alert" className="mx-5 mt-4 flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
            <AlertCircle className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            <span className="min-w-0">{error}</span>
          </div>
        )}

        {showConfirm ? (
          <div className="space-y-4 px-5 py-5">
            <div className="rounded-lg border border-warning/40 bg-warning/5 px-4 py-3">
              <p className="text-body font-medium">{t(($) => $.share_scope.confirm_title)}</p>
              <p className="mt-1 text-caption leading-5 text-muted-foreground">
                {preview.isLoading
                  ? t(($) => $.share_scope.loading_preview)
                  : t(($) => $.share_scope.confirm_description, {
                      affected: preview.data?.affected_count ?? 0,
                      private: preview.data?.previously_private_count ?? 0,
                    })}
              </p>
            </div>
            <div className="flex items-center justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setConfirming(false)} disabled={saving}>
                {t(($) => $.cancel)}
              </Button>
              <Button size="sm" onClick={() => void save()} disabled={saving || !previewReady}>
                {saving && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
                {t(($) => $.share_scope.confirm_action)}
              </Button>
            </div>
          </div>
        ) : (
          <>
            <fieldset className="divide-y divide-border">
              <legend className="sr-only">{t(($) => $.share_scope.title)}</legend>
              {(["private", "project", "workspace"] as VisibilityScope[]).map((value) => {
                const meta = SCOPE_META[value];
                const Icon = meta.icon;
                return (
                  <label
                    key={value}
                    className={cn(
                      "flex min-h-[76px] cursor-pointer items-start gap-3 px-5 py-4 transition-colors hover:bg-accent/40",
                      scope === value && "bg-accent/20",
                    )}
                  >
                    <input
                      type="radio"
                      name="share-scope"
                      value={value}
                      checked={scope === value}
                      onChange={() => { setScope(value); setConfirming(false); setError(null); }}
                      className="mt-1 size-4 shrink-0 accent-foreground"
                    />
                    <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                      <Icon className="size-4" aria-hidden="true" />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-body font-medium">{t(($) => $.share_scope[meta.labelKey])}</span>
                      <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
                        {t(($) => $.share_scope[meta.descriptionKey])}
                      </span>
                      {audienceSizeByScope[value] !== undefined && (
                        <span className="mt-1 block text-caption font-medium text-muted-foreground">
                          {t(($) => $.share_scope.audience_count, { count: audienceSizeByScope[value] ?? 0 })}
                        </span>
                      )}
                    </span>
                  </label>
                );
              })}
            </fieldset>

            {scope === "project" && (
              <SpecificPeoplePicker
                candidates={candidates}
                pickedIds={pickedIds}
                loading={pickedLoading}
                pendingIds={pendingIds}
                error={pickerError}
                inheritedCount={inheritedProjectIds.size}
                onToggle={(memberId, next) => void togglePerson(memberId, next)}
              />
            )}

            <DialogFooter className="border-t px-5 py-3">
              <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)} disabled={saving}>
                {t(($) => $.cancel)}
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  if (target.kind === "project") setConfirming(true);
                  else void save();
                }}
                disabled={saving || (target.kind === "project" && preview.isLoading)}
              >
                {saving && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
                {t(($) => $.share_scope.save_as, { scope: currentLabel })}
              </Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

function SpecificPeoplePicker({
  candidates,
  pickedIds,
  loading,
  pendingIds,
  error,
  inheritedCount,
  onToggle,
}: {
  candidates: MemberWithUser[];
  pickedIds: ReadonlySet<string>;
  loading: boolean;
  pendingIds: ReadonlySet<string>;
  error: string | null;
  inheritedCount: number;
  onToggle: (memberId: string, next: boolean) => void;
}) {
  const { t } = useT("common");
  const [filter, setFilter] = useState("");
  const query = filter.trim().toLowerCase();
  const visible = query
    ? candidates.filter((member) =>
        member.name.toLowerCase().includes(query) ||
        member.email.toLowerCase().includes(query) ||
        matchesPinyin(member.name, query),
      )
    : candidates;

  return (
    <div className="space-y-2 border-t bg-muted/15 px-5 py-4">
      <div className="flex items-center justify-between gap-2">
        <p className="text-caption font-medium text-muted-foreground">{t(($) => $.share_scope.picker_title)}</p>
        {!loading && (
          <span className="text-caption text-muted-foreground">
            {t(($) => $.share_scope.picked_count, { count: pickedIds.size })}
          </span>
        )}
      </div>

      {inheritedCount > 0 && (
        <p className="text-caption leading-5 text-muted-foreground">
          {t(($) => $.share_scope.project_members_also, { count: inheritedCount })}
        </p>
      )}

      {error && (
        <p role="alert" className="flex items-start gap-1.5 text-caption text-destructive">
          <AlertCircle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span className="min-w-0">{error}</span>
        </p>
      )}

      {loading ? (
        <div className="space-y-2" aria-label={t(($) => $.loading)}>
          <div className="h-9 w-full animate-pulse rounded bg-muted" />
          <div className="h-9 w-4/5 animate-pulse rounded bg-muted" />
        </div>
      ) : candidates.length === 0 ? (
        <p className="text-caption leading-5 text-muted-foreground">{t(($) => $.share_scope.picker_no_members)}</p>
      ) : (
        <>
          {candidates.length > SEARCH_THRESHOLD && (
            <input
              type="text"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={t(($) => $.share_scope.picker_search)}
              aria-label={t(($) => $.share_scope.picker_search)}
              className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-caption outline-none placeholder:text-muted-foreground focus-visible:border-ring"
            />
          )}
          <div className="max-h-48 divide-y overflow-y-auto rounded-md border bg-background">
            {visible.map((member) => {
              const checked = pickedIds.has(member.user_id);
              const pending = pendingIds.has(member.user_id);
              return (
                <label
                  key={member.user_id}
                  className={cn(
                    "flex items-center gap-2.5 px-3 py-2",
                    pending ? "cursor-wait" : "cursor-pointer hover:bg-accent/40",
                  )}
                >
                  <Checkbox
                    checked={checked}
                    disabled={pending}
                    aria-label={member.name || member.email}
                    onCheckedChange={(next) => onToggle(member.user_id, next === true)}
                  />
                  <ActorAvatar actorType="member" actorId={member.user_id} size="sm" />
                  <span className="min-w-0 flex-1 truncate text-caption">
                    {member.name || member.email}
                    {member.name && member.email && (
                      <span className="ml-1.5 text-muted-foreground">{member.email}</span>
                    )}
                  </span>
                  {pending && <Loader2 className="size-3.5 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />}
                  {member.role === "guest" && (
                    <span className="shrink-0 text-[11px] text-muted-foreground">{t(($) => $.share_scope.read_only)}</span>
                  )}
                </label>
              );
            })}
            {visible.length === 0 && (
              <p className="px-3 py-3 text-center text-caption text-muted-foreground">{t(($) => $.share_scope.picker_no_results)}</p>
            )}
          </div>
          {pickedIds.size === 0 && inheritedCount === 0 && (
            <p className="text-caption leading-5 text-muted-foreground">{t(($) => $.share_scope.picker_empty_selection)}</p>
          )}
        </>
      )}
    </div>
  );
}

export function ShareScopeTrigger({
  scope,
  audienceSize,
  onClick,
}: {
  scope?: VisibilityScope;
  audienceSize?: number;
  onClick: () => void;
}) {
  const { t } = useT("common");
  const value = scope ?? "private";
  return (
    <Button variant="outline" size="sm" onClick={onClick} className="gap-1.5">
      {value === "private" ? <LockKeyhole className="size-3.5" /> : value === "project" ? <Users className="size-3.5" /> : <Globe2 className="size-3.5" />}
      {t(($) => $.share_scope[SCOPE_META[value].labelKey])}
      {audienceSize !== undefined && (
        <span className="text-caption text-muted-foreground">
          · {t(($) => $.share_scope.audience_count, { count: audienceSize })}
        </span>
      )}
    </Button>
  );
}
