"use client";

import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LockKeyhole } from "lucide-react";
import { api } from "@multica/core/api";
import { chatKeys } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { projectListOptions } from "@multica/core/projects";
import { memberListOptions } from "@multica/core/workspace/queries";
import type { ChatSession } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

type Mode = "project" | "extra" | "private";

interface DraftShare {
  user_id: string;
  access: "view" | "speak";
}

/**
 * The creator's three-way access editor: follow the project, follow it and
 * name extra people, or keep the chat private.
 */
export function ChatAccessDialog({
  session,
  open,
  onOpenChange,
}: {
  session: ChatSession | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data: settings } = useQuery({
    queryKey: ["chat", wsId, "access", session?.id],
    queryFn: () => api.getChatAccess(session!.id),
    enabled: open && !!session,
  });
  const { data: projects = [] } = useQuery({
    ...projectListOptions(wsId),
    enabled: open,
  });
  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: open,
  });

  const [mode, setMode] = useState<Mode>("private");
  const [shares, setShares] = useState<DraftShare[]>([]);
  const [adding, setAdding] = useState("");

  useEffect(() => {
    if (!settings) return;
    setMode(settings.has_project ? settings.mode : "private");
    setShares(settings.shares.map((share) => ({ user_id: share.user_id, access: share.access })));
  }, [settings]);

  const bound = settings?.has_project ?? (session?.project_ids?.length ?? 0) > 0;
  const projectNames = useMemo(() => {
    const ids = new Set(session?.project_ids ?? (session?.project_id ? [session.project_id] : []));
    const names = projects.filter((project) => ids.has(project.id)).map((project) => project.title);
    return names.join("、");
  }, [projects, session]);

  const save = useMutation({
    mutationFn: () =>
      api.putChatAccess(session!.id, {
        mode: bound ? mode : "private",
        shares: mode === "extra" ? shares : [],
      }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      if (session) {
        await qc.invalidateQueries({ queryKey: chatKeys.session(wsId, session.id) });
      }
      onOpenChange(false);
    },
  });

  const candidates = members.filter(
    (member) =>
      member.user_id !== session?.creator_id &&
      !shares.some((share) => share.user_id === member.user_id),
  );
  const filtered = candidates.filter((member) => {
    const q = adding.trim().toLowerCase();
    if (!q) return true;
    return member.name.toLowerCase().includes(q) || member.email.toLowerCase().includes(q);
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {t(($) => $.sharing.title)}
            {session?.title ? ` · ${session.title}` : ""}
          </DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <AccessOption
            selected={mode === "project"}
            disabled={!bound}
            title={t(($) => $.sharing.follow)}
            hint={
              bound
                ? t(($) => $.sharing.follow_hint_bound, { projects: projectNames || "—" })
                : t(($) => $.sharing.follow_hint_unbound)
            }
            onSelect={() => setMode("project")}
          />
          <AccessOption
            selected={mode === "extra"}
            disabled={!bound}
            title={t(($) => $.sharing.extra)}
            hint={t(($) => $.sharing.extra_hint)}
            onSelect={() => setMode("extra")}
          />
          {mode === "extra" && bound && (
            <div className="flex flex-col gap-1 border-t border-border px-1 py-2">
              {shares.map((share) => {
                const member = members.find((item) => item.user_id === share.user_id);
                return (
                  <div key={share.user_id} className="flex items-center gap-2">
                    <span className="min-w-0 flex-1 truncate text-body">
                      {member?.name || share.user_id}
                    </span>
                    <select
                      className="rounded-md border border-border bg-background px-2 py-1 text-caption"
                      value={share.access}
                      onChange={(event) =>
                        setShares((current) =>
                          current.map((item) =>
                            item.user_id === share.user_id
                              ? { ...item, access: event.target.value as DraftShare["access"] }
                              : item,
                          ),
                        )
                      }
                    >
                      <option value="view">{t(($) => $.sharing.view)}</option>
                      <option value="speak">{t(($) => $.sharing.speak)}</option>
                    </select>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        setShares((current) => current.filter((item) => item.user_id !== share.user_id))
                      }
                    >
                      {t(($) => $.sharing.remove)}
                    </Button>
                  </div>
                );
              })}
              <label className="flex flex-col gap-1 text-caption text-muted-foreground">
                {t(($) => $.sharing.add)}
                <input
                  value={adding}
                  onChange={(event) => setAdding(event.target.value)}
                  placeholder={t(($) => $.sharing.search)}
                  className="rounded-md border border-border bg-background px-2 py-1 text-body text-foreground"
                />
              </label>
              {adding.trim() && (
                <div className="max-h-32 overflow-auto">
                  {filtered.slice(0, 8).map((member) => (
                    <button
                      key={member.user_id}
                      type="button"
                      className="block w-full truncate rounded-md px-2 py-1 text-left text-body hover:bg-accent"
                      onClick={() => {
                        setShares((current) => [
                          ...current,
                          { user_id: member.user_id, access: "view" },
                        ]);
                        setAdding("");
                      }}
                    >
                      {member.name}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
          <AccessOption
            selected={mode === "private"}
            title={t(($) => $.sharing.private)}
            hint={t(($) => $.sharing.private_hint)}
            onSelect={() => setMode("private")}
            icon
          />
          <p className="rounded-md bg-amber-50 px-2.5 py-2 text-caption text-amber-800">
            {bound ? t(($) => $.sharing.note_bound) : t(($) => $.sharing.note_unbound)}
          </p>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.sharing.cancel)}
          </Button>
          <Button type="button" disabled={!session || save.isPending} onClick={() => save.mutate()}>
            {t(($) => $.sharing.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AccessOption({
  selected,
  disabled,
  title,
  hint,
  onSelect,
  icon,
}: {
  selected: boolean;
  disabled?: boolean;
  title: string;
  hint: string;
  onSelect: () => void;
  icon?: boolean;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "flex items-start gap-2 rounded-lg border px-3 py-2 text-left",
        selected ? "border-brand bg-brand/5" : "border-border",
        disabled && "cursor-not-allowed opacity-45",
      )}
    >
      <span
        className={cn(
          "mt-1 size-3 shrink-0 rounded-full border",
          selected ? "border-brand bg-brand" : "border-muted-foreground",
        )}
      />
      <span>
        <span className="flex items-center gap-1 text-body">
          {icon && <LockKeyhole className="size-3.5" />}
          {title}
        </span>
        <span className="block text-caption text-muted-foreground">{hint}</span>
      </span>
    </button>
  );
}
