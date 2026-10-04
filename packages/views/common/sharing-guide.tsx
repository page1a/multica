"use client";

import type { ReactElement } from "react";
import { Info, LockKeyhole } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from "@multica/ui/components/ui/hover-card";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

/**
 * Sharing guidance shared by issue, project and chat pages (DENE-1214): the
 * hover card on every share button, and the prompt shown before a private
 * link is copied. Callers decide what to say; the server decides who may
 * change it.
 */

export type SharingGuideKind = "issue" | "project" | "chat";

/**
 * Hover card on a share button: who can see this now, and whether the viewer
 * may change it. `trigger` keeps its own click behaviour; the card only adds
 * an explanation, so a disabled trigger still says why it is disabled.
 */
export function SharingHoverCard({
  trigger,
  title,
  description,
  note,
  canChange,
  changeLine,
}: {
  trigger: ReactElement;
  title: string;
  description: string;
  /** Extra line, e.g. a project's scope also moves its issues. */
  note?: string;
  /** undefined while the server answer is loading. */
  canChange: boolean | undefined;
  changeLine: string | undefined;
}) {
  return (
    <HoverCard>
      <HoverCardTrigger delay={250} render={trigger} />
      <HoverCardContent align="end" className="w-72 space-y-2 p-3" data-testid="sharing-hover-card">
        <p className="text-body font-medium">{title}</p>
        <p className="text-caption leading-5 text-muted-foreground">{description}</p>
        {note && <p className="text-caption leading-5 text-muted-foreground">{note}</p>}
        {changeLine && (
          <p
            className={cn(
              "flex items-start gap-1.5 border-t pt-2 text-caption leading-5",
              canChange ? "text-muted-foreground" : "text-foreground",
            )}
          >
            {canChange ? (
              <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            ) : (
              <LockKeyhole className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
            )}
            <span className="min-w-0">{changeLine}</span>
          </p>
        )}
      </HoverCardContent>
    </HoverCard>
  );
}

/**
 * Asked before a private resource's link is copied: whoever receives it will
 * land on "can't open this". Offers to change the scope first when the viewer
 * may, and always lets them copy anyway. The caller closes it from either
 * callback, since "change first" swaps it for the share dialog.
 */
export function PrivateLinkPrompt({
  open,
  onOpenChange,
  kind,
  canChange,
  onCopyAnyway,
  onChangeScope,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kind: SharingGuideKind;
  canChange: boolean;
  onCopyAnyway: () => void;
  onChangeScope: () => void;
}) {
  const { t } = useT("common");
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="private-link-prompt">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <LockKeyhole className="size-4 text-muted-foreground" aria-hidden="true" />
            {t(($) => $.share_guide.private_link.title[kind])}
          </DialogTitle>
          <DialogDescription>
            {canChange
              ? t(($) => $.share_guide.private_link.description)
              : t(($) => $.share_guide.private_link.description_readonly)}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button
            variant={canChange ? "ghost" : "default"}
            size="sm"
            onClick={onCopyAnyway}
          >
            {t(($) => $.share_guide.private_link.copy_anyway)}
          </Button>
          {canChange && (
            <Button
              size="sm"
              onClick={onChangeScope}
            >
              {t(($) => $.share_guide.private_link.change_scope)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
