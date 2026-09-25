"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { chatKeys } from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";

// One open notice per page load, even if both the page and the window mount it.
let noticeClaimed = false;

/**
 * Once per person: "you have N chats that project members can now see",
 * with a way to make the checked ones private.
 */
export function ChatVisibilityNotice() {
  const { t } = useT("chat");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ["chat", wsId, "visibility-notice"],
    queryFn: () => api.getChatVisibilityNotice(),
  });
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);

  useEffect(() => {
    if (!data?.pending || noticeClaimed) return;
    noticeClaimed = true;
    setSelected(data.sessions.map((session) => session.id));
    setOpen(true);
  }, [data]);

  const dismiss = useMutation({
    mutationFn: () => api.dismissChatVisibilityNotice(),
    onSuccess: () => {
      setOpen(false);
      void qc.invalidateQueries({ queryKey: ["chat", wsId, "visibility-notice"] });
    },
  });

  const makePrivate = useMutation({
    mutationFn: async () => {
      if (selected.length > 0) {
        await api.makeChatSessionsPrivate(selected);
      }
      await api.dismissChatVisibilityNotice();
    },
    onSuccess: async () => {
      setOpen(false);
      await qc.invalidateQueries({ queryKey: chatKeys.sessions(wsId) });
      await qc.invalidateQueries({ queryKey: ["chat", wsId, "visibility-notice"] });
    },
  });

  if (!data?.pending) return null;

  return (
    <Dialog open={open} onOpenChange={(next) => !next && dismiss.mutate()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.notice.title)}</DialogTitle>
        </DialogHeader>
        <p className="text-body text-muted-foreground">
          {t(($) => $.notice.body, { count: data.count })}
        </p>
        <div className="max-h-64 overflow-auto">
          {data.sessions.map((session) => (
            <label key={session.id} className="flex items-center gap-2 py-1 text-body">
              <input
                type="checkbox"
                checked={selected.includes(session.id)}
                onChange={(event) =>
                  setSelected((current) =>
                    event.target.checked
                      ? [...current, session.id]
                      : current.filter((id) => id !== session.id),
                  )
                }
              />
              <span className="min-w-0 flex-1 truncate">
                {session.title || t(($) => $.window.untitled)}
              </span>
            </label>
          ))}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => dismiss.mutate()} disabled={dismiss.isPending}>
            {t(($) => $.notice.dismiss)}
          </Button>
          <Button
            type="button"
            onClick={() => makePrivate.mutate()}
            disabled={makePrivate.isPending || selected.length === 0}
          >
            {t(($) => $.notice.make_private)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
