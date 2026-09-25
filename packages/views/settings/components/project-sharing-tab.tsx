"use client";

import { useMemo, useState } from "react";
import { FolderKanban, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { projectKeys, projectListOptions } from "@multica/core/projects";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useCurrentMember } from "@multica/core/permissions";
import { useCurrentWorkspace } from "@multica/core/paths";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { SettingsSection, SettingsTab } from "./settings-layout";
import { ShareScopeDialog } from "../../common/share-scope-dialog";
import { useT } from "../../i18n";

type Visibility = "private" | "project" | "workspace";

export function ProjectSharingTab() {
  const { t } = useT("settings");
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { role, userId, isLoading: memberLoading } = useCurrentMember(wsId);
  const { data: projects = [], isLoading } = useQuery({ ...projectListOptions(wsId), enabled: !!wsId });
  const { data: members = [] } = useQuery({ ...memberListOptions(wsId), enabled: !!wsId });
  const memberQueries = useQueries({
    queries: projects.map((project) => ({
      queryKey: [...projectKeys.detail(wsId, project.id), "members"],
      queryFn: () => api.listProjectMembers(project.id),
      enabled: !!wsId,
    })),
  });
  const previews = useQueries({
    queries: projects.map((project) => ({
      queryKey: [...projectKeys.detail(wsId, project.id), "visibility-preview"],
      queryFn: () => api.previewProjectVisibility(project.id),
      enabled: !!wsId,
    })),
  });
  const qc = useQueryClient();
  const [pending, setPending] = useState<{ id: string; title: string; visibility: Visibility } | null>(null);
  const [pickingFor, setPickingFor] = useState<{ id: string; title: string; visibility: Visibility; initialScope?: Visibility } | null>(null);
  const mutation = useMutation({
    mutationFn: ({ id, visibility }: { id: string; visibility: Visibility }) => api.setProjectVisibility(id, visibility),
    onSuccess: (_result, vars) => {
      qc.invalidateQueries({ queryKey: projectKeys.list(wsId) });
      qc.invalidateQueries({ queryKey: [...projectKeys.detail(wsId, vars.id), "visibility-preview"] });
      setPending(null);
    },
    onError: (error) => {
      toast.error(error instanceof Error && error.message ? error.message : t(($) => $.project_sharing.save_failed));
    },
  });
  const canManageProject = (project: (typeof projects)[number]) =>
    role === "owner" || role === "admin" ||
    (role === "member" && project.created_by === userId);

  const labels = useMemo(() => ({
    private: t(($) => $.project_sharing.visibility_private),
    project: t(($) => $.project_sharing.visibility_project),
    workspace: t(($) => $.project_sharing.visibility_workspace),
  }), [t]);
  const pendingPreview = pending
    ? previews[projects.findIndex((project) => project.id === pending.id)]
    : undefined;

  return (
    <SettingsTab title={t(($) => $.project_sharing.title)} description={t(($) => $.project_sharing.description)}>
      <SettingsSection title={t(($) => $.project_sharing.section_title)} description={t(($) => $.project_sharing.section_description)}>
        {isLoading || memberLoading ? <div className="flex items-center gap-2 text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t(($) => $.project_sharing.loading)}</div> : null}
        {!isLoading && projects.length === 0 ? <Card><CardContent className="py-8 text-center text-muted-foreground">{t(($) => $.project_sharing.empty)}</CardContent></Card> : null}
        <div className="space-y-3">
          {projects.map((project, index) => {
            const previewQuery = previews[index];
            const preview = previewQuery?.data;
            const projectMembers = memberQueries[index]?.data ?? [];
            const projectGuestCount = projectMembers.filter((projectMember) =>
              members.some((member) => member.user_id === projectMember.member_id && member.role === "guest"),
            ).length;
            const visibility = (project.visibility ?? "private") as Visibility;
            const canManage = canManageProject(project);
            return (
              <Card key={project.id} data-testid="project-sharing-row">
                <CardContent className="flex flex-col gap-4 p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2 font-medium"><FolderKanban className="size-4 text-muted-foreground" /><span className="truncate">{project.title}</span></div>
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-caption text-muted-foreground">
                      <span>{labels[visibility]}</span><span>{t(($) => $.project_sharing.members, { count: projectMembers.length })}</span><span>{t(($) => $.project_sharing.guests, { count: projectGuestCount })}</span><span>{t(($) => $.project_sharing.resources, { count: preview?.affected_count ?? project.resource_count + project.issue_count })}</span>
                    </div>
                    {visibility === "project" ? (
                      <p className={projectMembers.length === 0 ? "mt-2 text-caption text-amber-600" : "mt-2 text-caption text-muted-foreground"}>
                        {projectMembers.length === 0 ? <>{t(($) => $.project_sharing.no_members)} </> : null}
                        {canManage ? <button type="button" className="underline" onClick={() => setPickingFor({ id: project.id, title: project.title, visibility })}>{t(($) => $.project_sharing.manage_members)}</button> : null}
                      </p>
                    ) : null}
                  </div>
                  <select aria-label={t(($) => $.project_sharing.change_for, { name: project.title })} className="h-9 rounded-md border border-input bg-background px-3 text-body" value={visibility} disabled={!canManage || mutation.isPending} onChange={(event) => {
                    const next = event.target.value as Visibility;
                    if (next === visibility) return;
                    // Specific people needs a pick list, so go straight to the picker.
                    if (next === "project") setPickingFor({ id: project.id, title: project.title, visibility, initialScope: next });
                    else setPending({ id: project.id, title: project.title, visibility: next });
                  }}>
                    <option value="private">{labels.private}</option><option value="project">{labels.project}</option><option value="workspace">{labels.workspace}</option>
                  </select>
                </CardContent>
              </Card>
            );
          })}
        </div>
        {!memberLoading && role === "guest" ? <p className="text-caption text-muted-foreground">{t(($) => $.project_sharing.read_only)}</p> : null}
      </SettingsSection>
      {pickingFor ? (
        <ShareScopeDialog
          open
          onOpenChange={(open) => { if (!open) setPickingFor(null); }}
          target={{ kind: "project", resourceId: pickingFor.id, currentScope: pickingFor.visibility, resourceLabel: pickingFor.title }}
          initialScope={pickingFor.initialScope}
        />
      ) : null}
      <AlertDialog open={pending !== null} onOpenChange={(open) => !open && setPending(null)}>
        <AlertDialogContent>
          <AlertDialogHeader><AlertDialogTitle>{t(($) => $.project_sharing.confirm_title, { name: pending?.title ?? "" })}</AlertDialogTitle><AlertDialogDescription>
            {pendingPreview?.isError ? t(($) => $.project_sharing.preview_failed) : !pendingPreview?.data ? t(($) => $.project_sharing.preview_loading) : t(($) => $.project_sharing.confirm_description, { count: pendingPreview.data.affected_count, privateCount: pendingPreview.data.previously_private_count, visibility: pending ? labels[pending.visibility] : "" })}
          </AlertDialogDescription></AlertDialogHeader>
          <AlertDialogFooter><AlertDialogCancel disabled={mutation.isPending}>{t(($) => $.project_sharing.cancel)}</AlertDialogCancel><AlertDialogAction disabled={mutation.isPending || !pendingPreview?.data || pendingPreview.isError} onClick={() => pending && mutation.mutate({ id: pending.id, visibility: pending.visibility })}>{mutation.isPending ? t(($) => $.project_sharing.saving) : t(($) => $.project_sharing.confirm)}</AlertDialogAction></AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </SettingsTab>
  );
}
