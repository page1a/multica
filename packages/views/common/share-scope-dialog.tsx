"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, ExternalLink, Globe2, Loader2, LockKeyhole, Users } from "lucide-react";
import {
  projectMembersOptions,
} from "@multica/core/projects";
import {
  useProjectVisibilityPreview,
  useSetIssueVisibility,
  useSetProjectVisibility,
  useSetRepoVisibility,
  type VisibilityScope,
} from "@multica/core/visibility";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useAuthStore } from "@multica/core/auth";
import { Button } from "@multica/ui/components/ui/button";
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
  onSaved?: (result: { visibility: VisibilityScope; audience_size?: number }) => void;
  onManageMembers?: () => void;
}

const SCOPE_META = {
  private: { icon: LockKeyhole, labelKey: "private_title", descriptionKey: "private_description" },
  project: { icon: Users, labelKey: "project_title", descriptionKey: "project_description" },
  workspace: { icon: Globe2, labelKey: "workspace_title", descriptionKey: "workspace_description" },
} as const;

/** Shared resource sharing dialog for issue, repository and project surfaces. */
export function ShareScopeDialog({
  open,
  onOpenChange,
  target,
  onSaved,
  onManageMembers,
}: ShareScopeDialogProps) {
  const { t } = useT("common");
  const wsId = useWorkspaceId();
  const currentUserId = useAuthStore((state) => state.user?.id ?? null);
  const [scope, setScope] = useState<VisibilityScope>(target.currentScope ?? "private");
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);

  const projectId = target.kind === "project" ? target.resourceId : target.projectId ?? null;
  const isProjectTarget = target.kind === "project";
  const projectScopeDisabled = !isProjectTarget && !projectId;
  const { data: projectMembers = [], isLoading: projectMembersLoading } = useQuery({
    ...projectMembersOptions(wsId, projectId ?? ""),
    enabled: open && !!projectId,
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
  const saving = issueMutation.isPending || repoMutation.isPending || projectMutation.isPending;

  useEffect(() => {
    if (open) {
      setScope(target.currentScope ?? "private");
      setError(null);
      setConfirming(false);
    }
  }, [open, target.currentScope, target.resourceId]);

  const projectMemberRows = useMemo(() => {
    const workspaceRoleById = new Map(workspaceMembers.map((member) => [member.user_id, member.role]));
    return projectMembers.filter((member) => member.member_id !== currentUserId).map((member) => ({
      ...member,
      role: workspaceRoleById.get(member.member_id),
    }));
  }, [currentUserId, projectMembers, workspaceMembers]);

  const save = async () => {
    if (saving || projectScopeDisabled) return;
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
  const audienceSizeByScope: Record<VisibilityScope, number | undefined> = {
    private: 1,
    project: projectId ? projectMembers.length : undefined,
    workspace: workspaceMembers.filter((member) => member.role !== "guest").length,
  };
  if (target.audienceSize !== undefined) {
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
                const disabled = value === "project" && projectScopeDisabled;
                return (
                  <label
                    key={value}
                    className={cn(
                      "flex min-h-[76px] items-start gap-3 px-5 py-4 transition-colors",
                      disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer hover:bg-accent/40",
                      scope === value && !disabled && "bg-accent/20",
                    )}
                  >
                    <input
                      type="radio"
                      name="share-scope"
                      value={value}
                      checked={scope === value}
                      disabled={disabled}
                      onChange={() => { setScope(value); setConfirming(false); setError(null); }}
                      className="mt-1 size-4 shrink-0 accent-foreground"
                    />
                    <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                      <Icon className="size-4" aria-hidden="true" />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-body font-medium">{t(($) => $.share_scope[meta.labelKey])}</span>
                      <span className="mt-0.5 block text-caption leading-5 text-muted-foreground">
                        {disabled
                          ? t(($) => $.share_scope.project_requires_project)
                          : t(($) => $.share_scope[meta.descriptionKey])}
                      </span>
                      {!disabled && audienceSizeByScope[value] !== undefined && (
                        <span className="mt-1 block text-caption font-medium text-foreground/70">
                          {t(($) => $.share_scope.audience_count, { count: audienceSizeByScope[value] ?? 0 })}
                        </span>
                      )}
                    </span>
                  </label>
                );
              })}
            </fieldset>

            {scope === "project" && (
              <div className="border-t bg-muted/15 px-5 py-4">
                {projectMembersLoading ? (
                  <div className="space-y-2" aria-label={t(($) => $.loading)}>
                    <div className="h-3 w-28 animate-pulse rounded bg-muted" />
                    <div className="h-9 w-full animate-pulse rounded bg-muted" />
                    <div className="h-9 w-4/5 animate-pulse rounded bg-muted" />
                  </div>
                ) : projectMemberRows.length === 0 ? (
                  <div className="space-y-2">
                    <p className="text-caption leading-5 text-muted-foreground">
                      {t(($) => $.share_scope.project_only_you)}
                    </p>
                    <Button variant="outline" size="sm" onClick={onManageMembers}>
                      <ExternalLink className="size-3.5" aria-hidden="true" />
                      {t(($) => $.share_scope.manage_members)}
                    </Button>
                  </div>
                ) : (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between">
                      <p className="text-caption font-medium text-muted-foreground">
                        {t(($) => $.share_scope.member_count, { count: projectMemberRows.length })}
                      </p>
                      <Button variant="ghost" size="sm" className="h-7 px-2 text-caption" onClick={onManageMembers}>
                        {t(($) => $.share_scope.manage_members)}
                      </Button>
                    </div>
                    <div className="max-h-40 divide-y overflow-y-auto rounded-md border bg-background">
                      {projectMemberRows.map((member) => (
                        <div key={member.member_id} className="flex items-center gap-2.5 px-3 py-2">
                          <ActorAvatar actorType="member" actorId={member.member_id} size="sm" />
                          <span className="min-w-0 flex-1 truncate text-caption">{member.name || member.email}</span>
                          {member.role === "guest" && (
                            <span className="shrink-0 text-[11px] text-muted-foreground">{t(($) => $.share_scope.read_only)}</span>
                          )}
                        </div>
                      ))}
                    </div>
                  </div>
                )}
              </div>
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
                disabled={saving || projectScopeDisabled || (target.kind === "project" && preview.isLoading)}
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
