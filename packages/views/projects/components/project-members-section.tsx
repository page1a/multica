"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  projectMembersOptions,
  useAddProjectMember,
  useRemoveProjectMember,
} from "@multica/core/projects";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { ActorAvatar } from "../../common/actor-avatar";
import { matchesPinyin } from "../../editor/extensions/pinyin-match";
import { useT } from "../../i18n";
import type { ProjectMember } from "@multica/core/types";

export function ProjectMembersSection({
  projectId,
  canManage,
}: {
  projectId: string;
  canManage: boolean;
}) {
  const { t } = useT("projects");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(true);
  const [addOpen, setAddOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const { data: projectMembers = [] } = useQuery(
    projectMembersOptions(wsId, projectId),
  );
  const { data: workspaceMembers = [] } = useQuery(memberListOptions(wsId));
  const addMember = useAddProjectMember(wsId, projectId);
  const removeMember = useRemoveProjectMember(wsId, projectId);

  // The server orders the column (owner, lead, admins, rest) and blanks roles
  // the viewer may not see, so this only labels what came back.
  const roleLabels: Record<string, string> = {
    owner: t(($) => $.members.roles.owner),
    admin: t(($) => $.members.roles.admin),
    member: t(($) => $.members.roles.member),
    guest: t(($) => $.members.roles.guest),
  };
  const roleText = (member: ProjectMember) => {
    const parts: string[] = [];
    if (member.is_lead) parts.push(t(($) => $.members.lead));
    if (member.role) parts.push(roleLabels[member.role] ?? member.role);
    return parts.join(" · ");
  };

  const addedIds = new Set(projectMembers.map((m) => m.member_id));
  const query = filter.toLowerCase();
  const candidates = workspaceMembers.filter((m) => {
    if (addedIds.has(m.user_id)) return false;
    if (!query) return true;
    return (
      m.name.toLowerCase().includes(query) ||
      m.email.toLowerCase().includes(query) ||
      matchesPinyin(m.name, query)
    );
  });

  const handleAdd = async (userId: string) => {
    try {
      await addMember.mutateAsync(userId);
      toast.success(t(($) => $.members.toast_added));
      setAddOpen(false);
      setFilter("");
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.members.toast_add_failed),
      );
    }
  };

  const handleRemove = async (memberId: string) => {
    try {
      await removeMember.mutateAsync(memberId);
      toast.success(t(($) => $.members.toast_removed));
    } catch (err) {
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.members.toast_remove_failed),
      );
    }
  };

  return (
    <div>
      <button
        type="button"
        className={`flex w-full items-center gap-1 rounded-md px-2 py-1 text-caption font-medium transition-colors mb-2 hover:bg-accent/70 ${open ? "" : "text-muted-foreground hover:text-foreground"}`}
        onClick={() => setOpen(!open)}
      >
        {t(($) => $.members.section_header)}
        <ChevronRight
          className={`!size-3 shrink-0 stroke-[2.5] text-muted-foreground transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="pl-2 space-y-1.5">
          <p className="text-caption text-muted-foreground">
            {t(($) => $.members.hint)}
          </p>
          {projectMembers.length === 0 && (
            <p className="text-caption text-muted-foreground">
              {t(($) => $.members.empty)}
            </p>
          )}
          {projectMembers.length > 0 && (
            <div className="max-h-64 space-y-1 overflow-y-auto pr-1">
              {projectMembers.map((member) => (
                <div
                  key={member.id}
                  className="flex items-center gap-2 text-caption group"
                >
                  <ActorAvatar actorType="member" actorId={member.member_id} size="sm" />
                  <span className="truncate flex-1">{member.name || member.email}</span>
                  {roleText(member) && (
                    <span className="shrink-0 text-muted-foreground">
                      {roleText(member)}
                    </span>
                  )}
                  {canManage && (
                    <button
                      type="button"
                      onClick={() => handleRemove(member.member_id)}
                      className="[@media(hover:hover)]:opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity rounded-sm p-0.5 [@media(pointer:coarse)]:p-2 hover:bg-accent"
                      title={t(($) => $.members.remove_tooltip)}
                    >
                      <Trash2 className="size-3 text-muted-foreground" />
                    </button>
                  )}
                </div>
              ))}
            </div>
          )}
          {canManage && (
            <Popover
              open={addOpen}
              onOpenChange={(v) => {
                setAddOpen(v);
                if (!v) setFilter("");
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
                    {t(($) => $.members.add_button)}
                  </Button>
                }
              />
              <PopoverContent align="start" className="w-52 p-0">
                <div className="px-2 py-1.5 border-b">
                  <input
                    type="text"
                    value={filter}
                    onChange={(e) => setFilter(e.target.value)}
                    placeholder={t(($) => $.members.add_placeholder)}
                    className="w-full bg-transparent text-body placeholder:text-muted-foreground outline-none"
                  />
                </div>
                <div className="p-1 max-h-48 overflow-y-auto">
                  {candidates.map((m) => (
                    <button
                      type="button"
                      key={m.user_id}
                      onClick={() => handleAdd(m.user_id)}
                      disabled={addMember.isPending}
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-body hover:bg-accent transition-colors"
                    >
                      <ActorAvatar actorType="member" actorId={m.user_id} size="sm" />
                      <span className="truncate">{m.name}</span>
                    </button>
                  ))}
                  {candidates.length === 0 && (
                    <div className="px-2 py-3 text-center text-body text-muted-foreground">
                      {t(($) => $.members.no_results)}
                    </div>
                  )}
                </div>
              </PopoverContent>
            </Popover>
          )}
        </div>
      )}
    </div>
  );
}
