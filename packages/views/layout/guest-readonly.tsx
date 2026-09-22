"use client";

import {
  createContext,
  useCallback,
  useContext,
  useState,
  type ReactNode,
} from "react";
import { Eye, Inbox, Search } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { useCanWrite } from "@multica/core/permissions";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
  EmptyContent,
} from "@multica/ui/components/ui/empty";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { PAGE_GUTTER } from "./page-header";

export interface GuestReadOnlyValue {
  /** True only after membership has resolved and the viewer is a guest. */
  isGuest: boolean;
  /** True while the member list that names our role is still in flight. */
  isRoleLoading: boolean;
}

const GuestReadOnlyContext = createContext<GuestReadOnlyValue>({
  isGuest: false,
  isRoleLoading: false,
});

/**
 * Resolves the current viewer's guest flag from membership. Must render
 * inside the dashboard guard so `useWorkspaceId` is safe.
 *
 * Loading keeps `isGuest` false so the first paint matches a normal
 * member's — we do not flash the badge or disable controls until the
 * role is known.
 */
export function GuestReadOnlyProvider({ children }: { children: ReactNode }) {
  const wsId = useWorkspaceId();
  const { isGuest, isLoading } = useCanWrite(wsId);
  return (
    <GuestReadOnlyContext.Provider
      value={{ isGuest, isRoleLoading: isLoading }}
    >
      {children}
    </GuestReadOnlyContext.Provider>
  );
}

/**
 * Override the resolved guest flag. Tests use this so they do not have to
 * mock membership queries.
 */
export function GuestReadOnlyScope({
  isGuest,
  children,
}: {
  isGuest: boolean;
  children: ReactNode;
}) {
  return (
    <GuestReadOnlyContext.Provider
      value={{ isGuest, isRoleLoading: false }}
    >
      {children}
    </GuestReadOnlyContext.Provider>
  );
}

export function useGuestReadOnly(): GuestReadOnlyValue {
  return useContext(GuestReadOnlyContext);
}

/**
 * Returns true and toasts the guest copy when the viewer cannot write.
 * Keyboard shortcuts and mutation entry points use this; visible controls
 * should wrap with `WriteAction` instead so the affordance stays in place.
 */
export function useDenyGuestWrite(): () => boolean {
  const { isGuest } = useGuestReadOnly();
  const { t } = useT("layout");
  return useCallback(() => {
    if (!isGuest) return false;
    toast.message(t(($) => $.guest.no_edit));
    return true;
  }, [isGuest, t]);
}

/** Slim page-header chip: "you're a guest, view only". Hidden while loading. */
export function GuestBanner() {
  const { isGuest } = useGuestReadOnly();
  const { t } = useT("layout");
  if (!isGuest) return null;
  return (
    <div
      role="status"
      data-testid="guest-readonly-banner"
      className={cn(
        "flex h-8 shrink-0 items-center gap-1.5 border-b border-warning/30 bg-warning/10 text-caption text-warning",
        PAGE_GUTTER,
      )}
    >
      <Eye aria-hidden="true" className="size-3.5" />
      <span>{t(($) => $.guest.badge)}</span>
    </div>
  );
}

/**
 * Keeps a write control in place but inert for guests. Hover or click
 * explains why — the control is never hidden.
 */
export function WriteAction({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  const { isGuest } = useGuestReadOnly();
  const { t } = useT("layout");
  const [open, setOpen] = useState(false);

  if (!isGuest) return <>{children}</>;

  const message = t(($) => $.guest.no_edit);
  const trigger = (
    <span
      role="button"
      tabIndex={0}
      aria-disabled="true"
      aria-label={message}
      title={message}
      data-testid="write-action"
      className={cn("inline-flex cursor-not-allowed", className)}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        setOpen(true);
      }}
      onPointerDown={(event) => {
        event.preventDefault();
        event.stopPropagation();
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          event.stopPropagation();
          setOpen(true);
        }
      }}
    >
      <span className="pointer-events-none flex w-full opacity-40">
        {children}
      </span>
    </span>
  );

  return (
    <Tooltip open={open} onOpenChange={setOpen}>
      <TooltipTrigger render={trigger} />
      <TooltipContent>{message}</TooltipContent>
    </Tooltip>
  );
}

function GuestPageState({
  testId,
  icon: Icon,
  title,
  description,
  actions,
}: {
  testId: string;
  icon: typeof Inbox;
  title: string;
  description: string;
  actions?: ReactNode;
}) {
  return (
    <div data-testid={testId} className="flex min-h-0 flex-1">
      <Empty className="rounded-none border-0 px-6 py-16">
        <EmptyHeader>
          <EmptyMedia
            variant="icon"
            className="size-12 rounded-full text-muted-foreground [&_svg]:size-6"
          >
            <Icon aria-hidden="true" />
          </EmptyMedia>
          <EmptyTitle>{title}</EmptyTitle>
          <EmptyDescription className="max-w-md">{description}</EmptyDescription>
        </EmptyHeader>
        {actions ? (
          <EmptyContent className="mt-1 flex-row justify-center">
            {actions}
          </EmptyContent>
        ) : null}
      </Empty>
    </div>
  );
}

/** Workspace/module list empty when a guest has had nothing shared with them. */
export function NothingSharedEmpty() {
  const { t } = useT("layout");
  return (
    <GuestPageState
      testId="nothing-shared-empty"
      icon={Inbox}
      title={t(($) => $.guest.nothing_shared_title)}
      description={t(($) => $.guest.nothing_shared_description)}
    />
  );
}

/**
 * Direct URL to a resource the viewer cannot see. Deliberately "not found",
 * never "you don't have permission" — the latter confirms the resource exists.
 */
export function ResourceNotFound({ actions }: { actions?: ReactNode }) {
  const { t } = useT("layout");
  return (
    <GuestPageState
      testId="resource-not-found"
      icon={Search}
      title={t(($) => $.guest.not_found_title)}
      description={t(($) => $.guest.not_found_description)}
      actions={actions}
    />
  );
}
