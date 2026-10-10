"use client";

import { useState } from "react";
import { toast } from "sonner";
import { useSetChatTicket } from "@multica/core/chat/mutations";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../../i18n";

/**
 * Pins an existing issue to the chat by its number (DENE-1719): it joins the
 * chat's ticket cards and progress bar as 「手动挂上」. The issue itself is
 * untouched; the server checks who may change this chat.
 */
export function ChatTicketPinDialog({
  sessionId,
  open,
  onOpenChange,
}: {
  sessionId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("chat");
  const [value, setValue] = useState("");
  const pin = useSetChatTicket();
  const issue = value.trim();

  const submit = () => {
    if (!issue || pin.isPending) return;
    pin.mutate(
      { sessionId, issue },
      {
        onSuccess: () => {
          toast.success(t(($) => $.tickets.pinned, { id: issue }));
          setValue("");
          onOpenChange(false);
        },
        onError: (err) => {
          toast.error(err instanceof Error && err.message ? err.message : t(($) => $.tickets.pin_failed, { id: issue }));
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.tickets.pin_title)}</DialogTitle>
          <DialogDescription>{t(($) => $.tickets.pin_hint)}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
        >
          <Input
            autoFocus
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={t(($) => $.tickets.pin_placeholder)}
            aria-label={t(($) => $.tickets.pin_placeholder)}
            className="max-sm:h-11"
          />
          <DialogFooter className="mt-4">
            <Button type="submit" disabled={!issue || pin.isPending} className="max-sm:h-11">
              {t(($) => $.tickets.pin_submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
